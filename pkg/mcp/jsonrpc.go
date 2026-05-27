// Package mcp: JSON-RPC 2.0 framing over stdio.
//
// Wire format per MCP spec 2025-11-25 §Transports/stdio: messages are
// newline-delimited JSON (NOT length-prefixed). Each message is a single
// JSON object on one line; embedded newlines are forbidden by the spec.
// We enforce this by using json.Encoder which appends exactly one
// newline per Write call.
//
// Error codes follow JSON-RPC 2.0:
//
//	-32700  Parse error
//	-32600  Invalid Request
//	-32601  Method not found
//	-32602  Invalid params
//	-32603  Internal error
package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// JSON-RPC 2.0 standard error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// rpcVersion is the JSON-RPC version string required in every message.
const rpcVersion = "2.0"

// requestID is a JSON-RPC 2.0 id: string | number | null. We unmarshal
// as json.RawMessage so we can relay it verbatim in responses.
type requestID = json.RawMessage

// rpcRequest is a JSON-RPC 2.0 request or notification received from
// the client. id is nil for notifications (absent means notification).
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      requestID       `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether the request is a notification (no id).
func (r *rpcRequest) isNotification() bool {
	return len(r.ID) == 0
}

// rpcResponse is a JSON-RPC 2.0 result response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      requestID       `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the error object inside a JSON-RPC 2.0 error response.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// codec handles reading and writing newline-delimited JSON-RPC messages
// over an io.Reader/io.Writer pair. It is not safe for concurrent use —
// the caller (server) serializes reads on a single goroutine and writes
// behind a mutex.
type codec struct {
	r   *bufio.Reader
	enc *json.Encoder
}

// newCodec wraps r and w for message-level I/O. The reader buffer is
// sized to maxLineBytes so a single legitimate frame always fits in
// one ReadSlice call; anything larger surfaces as bufio.ErrBufferFull,
// which we recover from rather than terminating on.
func newCodec(r io.Reader, w io.Writer) *codec {
	return &codec{
		r:   bufio.NewReaderSize(r, maxLineBytes),
		enc: json.NewEncoder(w),
	}
}

// maxLineBytes caps a single JSON-RPC frame. MCP frames carry method
// calls, small arguments, and response envelopes — typical inbound
// frames are hundreds of bytes, and the largest tool-call arguments
// for sensibly-bounded schemas are in the low kilobytes. 64 KiB is the
// bufio.Scanner default and sits well above any legitimate frame.
const maxLineBytes = 64 * 1024

// maxDrainBytes caps how much we'll discard to recover from a single
// oversize frame. With a 64 KiB frame limit, 10× gives 640 KiB of
// headroom to walk off the oversize frame and find the next newline.
// Beyond that, the stream is either broken or hostile.
const maxDrainBytes = 10 * maxLineBytes

// readRequest reads one newline-delimited JSON-RPC frame and unmarshals
// it into an rpcRequest.
//
// Returns io.EOF when the underlying reader is closed between frames.
// Returns an *rpcError for parse-level problems that the caller can
// respond to and continue. Returns a plain error for unrecoverable
// transport failures.
func (c *codec) readRequest() (*rpcRequest, error) {
	line, err := c.readLine()
	if err != nil {
		return nil, err
	}
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		// The stdlib json error message can include snippets of the
		// offending input. That's an attacker-controlled-text channel
		// into our response — low severity, but the only such channel.
		// We return a generic message; a client that needs to debug has
		// its own serialized output to look at.
		return nil, &rpcError{Code: codeParseError, Message: "parse error: message is not valid JSON"}
	}
	if req.JSONRPC != rpcVersion {
		return nil, &rpcError{Code: codeInvalidRequest, Message: `jsonrpc must be "2.0"`}
	}
	if req.Method == "" {
		return nil, &rpcError{Code: codeInvalidRequest, Message: "method is required"}
	}
	return &req, nil
}

// readLine reads one newline-terminated frame from the transport, up
// to maxLineBytes. The returned slice does not include the trailing
// newline; its bytes are valid only until the next read call.
func (c *codec) readLine() ([]byte, error) {
	line, err := c.r.ReadSlice('\n')
	switch {
	case err == nil:
		return line[:len(line)-1], nil
	case errors.Is(err, bufio.ErrBufferFull):
		// Frame exceeds maxLineBytes. Drain the rest so the next
		// readLine starts on a fresh frame boundary, then surface a
		// recoverable rpcError.
		if derr := c.drainLine(); derr != nil {
			return nil, derr
		}
		return nil, &rpcError{
			Code:    codeParseError,
			Message: fmt.Sprintf("message exceeds %d-byte limit; discarded", maxLineBytes),
		}
	case errors.Is(err, io.EOF):
		if len(line) > 0 {
			// Stream closed mid-frame. Treat as truncation.
			return nil, &rpcError{
				Code:    codeParseError,
				Message: "unexpected EOF: message not terminated by newline",
			}
		}
		return nil, io.EOF
	default:
		return nil, fmt.Errorf("read json-rpc frame: %w", err)
	}
}

// drainLine reads and discards bytes from the underlying reader until
// it consumes a newline, hits EOF, or crosses maxDrainBytes.
func (c *codec) drainLine() error {
	drained := 0
	for drained < maxDrainBytes {
		chunk, err := c.r.ReadSlice('\n')
		drained += len(chunk)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return nil
		default:
			return fmt.Errorf("drain oversize frame: %w", err)
		}
	}
	return fmt.Errorf("drain cap exceeded: no newline within %d bytes of oversize frame", maxDrainBytes)
}

// writeResult serializes result as a JSON-RPC 2.0 success response.
func (c *codec) writeResult(id requestID, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshaling result: %w", err)
	}
	return c.enc.Encode(rpcResponse{
		JSONRPC: rpcVersion,
		ID:      id,
		Result:  raw,
	})
}

// writeError serializes a JSON-RPC 2.0 error response.
func (c *codec) writeError(id requestID, code int, message string, data any) error {
	return c.enc.Encode(rpcResponse{
		JSONRPC: rpcVersion,
		ID:      id,
		Error:   &rpcError{Code: code, Message: message, Data: data},
	})
}
