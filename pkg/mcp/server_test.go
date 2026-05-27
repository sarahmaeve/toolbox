package mcp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echoTool is a minimal Tool implementation used by the server
// integration tests.
type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echoes its input" }
func (echoTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"text": {"type": "string"}
		},
		"required": ["text"],
		"additionalProperties": false
	}`)
}
func (echoTool) Handle(_ context.Context, input json.RawMessage) *Response {
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return Err(CodeSchemaViolation, err.Error(), nil)
	}
	return OK(map[string]string{"echoed": p.Text})
}

// permissiveTool returns a schema without additionalProperties:false to
// exercise the Register panic contract.
type permissiveTool struct{}

func (permissiveTool) Name() string        { return "permissive" }
func (permissiveTool) Description() string { return "" }
func (permissiveTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (permissiveTool) Handle(_ context.Context, _ json.RawMessage) *Response { return OK(nil) }

// TestServer_Register_RejectsPermissiveSchema verifies the strict-reject
// contract: tools without additionalProperties:false must panic at
// Register so the bug is found at startup.
func TestServer_Register_RejectsPermissiveSchema(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{Name: "t", Version: "0.0.1"})
	assert.Panics(t, func() { srv.Register(permissiveTool{}) },
		"Register must panic on a schema without additionalProperties:false")
}

// TestServer_Register_AcceptsStrictSchema verifies that a properly
// strict schema is accepted.
func TestServer_Register_AcceptsStrictSchema(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{Name: "t", Version: "0.0.1"})
	srv.Register(echoTool{})
	assert.Contains(t, srv.RegisteredToolNames(), "echo")
}

// TestServer_FullHandshakeAndToolCall drives the protocol end-to-end:
// initialize → notifications/initialized → tools/list → tools/call.
// This is the integration anchor that proves the server, codec,
// handshake, dispatch, and schema validator all wire together correctly.
func TestServer_FullHandshakeAndToolCall(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{Name: "test-server", Version: "1.2.3", Instructions: "hi"})
	srv.Register(echoTool{})

	in, sin := io.Pipe()
	sout, out := io.Pipe()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(context.Background(), in, out)
	}()

	send := func(line string) {
		_, err := io.WriteString(sin, line+"\n")
		require.NoError(t, err)
	}
	dec := json.NewDecoder(sout)
	type rpcRespLite struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	read := func() rpcRespLite {
		var r rpcRespLite
		require.NoError(t, dec.Decode(&r))
		return r
	}

	// initialize
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"c","version":"v"}}}`)
	r := read()
	assert.Nil(t, r.Error)
	assert.Contains(t, string(r.Result), `"name":"test-server"`)
	assert.Contains(t, string(r.Result), `"version":"1.2.3"`)
	assert.Contains(t, string(r.Result), `"instructions":"hi"`)

	// notifications/initialized — no response
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	// tools/list
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	r = read()
	assert.Nil(t, r.Error)
	assert.Contains(t, string(r.Result), "echo")

	// tools/call — valid input
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello"}}}`)
	r = read()
	assert.Nil(t, r.Error)
	assert.Contains(t, string(r.Result), "echoed")
	assert.Contains(t, string(r.Result), "hello")

	// tools/call — unknown field, strict-reject fires
	send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{"text":"x","bogus":1}}}`)
	r = read()
	assert.Nil(t, r.Error) // protocol layer succeeds; tool layer signals error inside isError
	assert.Contains(t, string(r.Result), `"isError":true`)
	assert.Contains(t, string(r.Result), "bogus")
	assert.Contains(t, string(r.Result), CodeSchemaViolation)

	// tools/call — unknown tool, protocol error
	send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	r = read()
	require.NotNil(t, r.Error)
	assert.Equal(t, codeInvalidParams, r.Error.Code)

	// Cleanly close: closing sin causes EOF on the server's read loop,
	// which returns from Serve.
	require.NoError(t, sin.Close())

	select {
	case err := <-serveErr:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down on EOF")
	}
}

// TestServer_ToolsCallBeforeInitialize verifies the lifecycle gate:
// tool calls before notifications/initialized must be rejected.
func TestServer_ToolsCallBeforeInitialize(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{Name: "t", Version: "0.0.1"})
	srv.Register(echoTool{})

	in, sin := io.Pipe()
	sout, out := io.Pipe()

	go func() { _ = srv.Serve(context.Background(), in, out) }()
	defer sin.Close() //nolint:errcheck

	_, err := io.WriteString(sin, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n")
	require.NoError(t, err)

	dec := json.NewDecoder(sout)
	var r struct {
		Error *rpcError `json:"error"`
	}
	require.NoError(t, dec.Decode(&r))
	require.NotNil(t, r.Error)
	assert.Equal(t, codeInvalidRequest, r.Error.Code)
	assert.Contains(t, r.Error.Message, "not yet initialized")
}

// TestServer_ContextCancellationShutsDown verifies that cancelling the
// context terminates Serve.
func TestServer_ContextCancellationShutsDown(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{Name: "t", Version: "0.0.1"})

	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	defer pw.Close() //nolint:errcheck

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, pr, io.Discard)
	}()

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after ctx cancel")
	}
}

// TestServer_ConcurrentDispatch verifies that the read loop dispatches
// non-initialize requests in goroutines and serializes writes.
func TestServer_ConcurrentDispatch(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{Name: "t", Version: "0.0.1"})
	srv.Register(echoTool{})

	in, sin := io.Pipe()
	sout, out := io.Pipe()

	go func() { _ = srv.Serve(context.Background(), in, out) }()
	defer sin.Close() //nolint:errcheck

	// Handshake.
	_, _ = io.WriteString(sin,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"+
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")

	dec := json.NewDecoder(sout)
	// Drain the initialize response.
	var dummy json.RawMessage
	require.NoError(t, dec.Decode(&dummy))

	// Send N concurrent tool calls.
	const N = 20
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range N {
			_, err := io.WriteString(sin,
				`{"jsonrpc":"2.0","id":`+itoa(i+2)+`,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`+"\n")
			require.NoError(t, err)
		}
	})

	// Collect N responses; under serialized writes each is one
	// complete JSON object.
	seen := make(map[string]int)
	for range N {
		var r struct {
			ID json.RawMessage `json:"id"`
		}
		require.NoError(t, dec.Decode(&r))
		seen[string(r.ID)]++
	}
	wg.Wait()
	assert.Len(t, seen, N, "should see one response per request id")
}

// itoa is a local copy of strconv.Itoa to keep this test file's import
// list short.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestResponse_ConstructorMetadata verifies that OK/Err leave the
// metadata fields zero so the protocol layer can stamp them at emission.
func TestResponse_ConstructorMetadata(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "ok", OK(nil).Status)
	assert.Empty(t, OK(nil).Metadata.ServerVersion)

	e := Err("oops", "boom", nil)
	assert.Equal(t, "error", e.Status)
	assert.NotNil(t, e.Error)
	assert.Equal(t, "oops", e.Error.Code)
}

// silence imports we only need conditionally
var _ = strings.NewReader
