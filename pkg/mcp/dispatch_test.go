package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// panicTool simulates a buggy handler that panics on hostile input.
type panicTool struct{}

func (panicTool) Name() string        { return "panics" }
func (panicTool) Description() string { return "always panics" }
func (panicTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false}`)
}
func (panicTool) Handle(_ context.Context, _ json.RawMessage) *Response {
	panic("handler bug: index out of range")
}

// nilTool violates the documented "never return nil" contract.
type nilTool struct{}

func (nilTool) Name() string        { return "nils" }
func (nilTool) Description() string { return "returns nil" }
func (nilTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false}`)
}
func (nilTool) Handle(_ context.Context, _ json.RawMessage) *Response { return nil }

// decodeEnvelope unpacks the Response envelope from a tools/call result.
func decodeEnvelope(t *testing.T, result any) Response {
	t.Helper()
	tcr, ok := result.(*toolsCallResult)
	require.True(t, ok, "result must be a *toolsCallResult, got %T", result)
	require.NotEmpty(t, tcr.Content)
	var resp Response
	require.NoError(t, json.Unmarshal([]byte(tcr.Content[0].Text), &resp))
	return resp
}

// TestToolsCall_HandlerPanicBecomesErrorResponse: a panic inside a tool
// handler must surface as an internal_error envelope, not kill the
// process — handlers run on goroutines with no other recover boundary,
// and they parse untrusted LLM-supplied arguments.
func TestToolsCall_HandlerPanicBecomesErrorResponse(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{Name: "t", Version: "1.2.3"})
	srv.Register(panicTool{})

	result, rpcErr := srv.dispatchToolsCall(context.Background(),
		json.RawMessage(`{"name":"panics","arguments":{}}`))
	require.Nil(t, rpcErr, "a handler panic is a handler-level error, not a protocol error")

	resp := decodeEnvelope(t, result)
	assert.Equal(t, "error", resp.Status)
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInternalError, resp.Error.Code)
}

// TestToolsCall_NilHandlerResponseBecomesErrorResponse: the Tool
// interface forbids returning nil, but a framework processing untrusted
// input must not let one buggy handler crash the server on the
// metadata-stamping dereference.
func TestToolsCall_NilHandlerResponseBecomesErrorResponse(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{Name: "t", Version: "1.2.3"})
	srv.Register(nilTool{})

	result, rpcErr := srv.dispatchToolsCall(context.Background(),
		json.RawMessage(`{"name":"nils","arguments":{}}`))
	require.Nil(t, rpcErr)

	resp := decodeEnvelope(t, result)
	assert.Equal(t, "error", resp.Status)
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInternalError, resp.Error.Code)
}

// TestToolsCall_SchemaViolationResponseIsStamped: the interfaces.go
// contract says the protocol layer stamps ServerVersion/ElapsedMs at
// emission time — that must include schema-violation envelopes, which
// are emitted without ever reaching the handler.
func TestToolsCall_SchemaViolationResponseIsStamped(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{Name: "t", Version: "1.2.3"})
	srv.Register(echoTool{})

	result, rpcErr := srv.dispatchToolsCall(context.Background(),
		json.RawMessage(`{"name":"echo","arguments":{"bogus_field":1}}`))
	require.Nil(t, rpcErr)

	resp := decodeEnvelope(t, result)
	require.NotNil(t, resp.Error, "unknown field must be a schema violation")
	assert.Equal(t, CodeSchemaViolation, resp.Error.Code)
	assert.Equal(t, "1.2.3", resp.Metadata.ServerVersion,
		"violation envelopes must carry server_version like every other response")
}
