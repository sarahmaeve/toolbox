// Package mcp implements a generic MCP server: the JSON-RPC 2.0 over
// stdio protocol layer, the uniform response envelope, and the handler
// registries for tools and resources.
//
// External surface: Serve(ctx, in, out) dispatches MCP messages to
// handlers registered via Register(Tool) and RegisterResource(Resource).
// Handlers implement the small interfaces below.
//
// Protocol target: MCP spec 2025-11-25
// (https://modelcontextprotocol.io/specification/2025-11-25).
// Every handler returns the same Response shape so the protocol layer
// can emit a uniform tools/call or resources/read response with no
// handler-specific serialization.
//
// This package was extracted from signatory's internal/mcp; it is
// protocol- and domain-agnostic. Schema validation lives in the sibling
// pkg/schema package so the same machinery can be reused outside MCP.
package mcp

import (
	"context"
	"encoding/json"
)

// Tool handles a tools/call method per MCP.
type Tool interface {
	// Name returns the tool's canonical name. Must be unique across
	// all registered tools.
	Name() string

	// Description returns a human-readable description for the
	// tools/list output. Keep it one sentence.
	Description() string

	// InputSchema returns the JSON Schema for the tool's input. The
	// protocol layer uses this for tools/list and for strict-reject
	// validation of incoming tools/call inputs. Schemas MUST set
	// additionalProperties:false — the package's posture is that
	// unknown fields are an error, not a warning.
	InputSchema() json.RawMessage

	// Handle invokes the tool. The input is the raw JSON from the
	// MCP request's params.arguments. Handlers should decode into
	// their own typed struct and return Err(CodeSchemaViolation, ...)
	// on decode failure.
	//
	// The returned Response is wrapped by the protocol layer into a
	// tools/call response with content[].text carrying the envelope.
	// Handlers never return a nil *Response — use OK, Err, or one of
	// the constructors below.
	Handle(ctx context.Context, input json.RawMessage) *Response
}

// Resource handles a resources/read method per MCP.
type Resource interface {
	// URIPattern returns the URI this resource is registered under.
	// Static resources use a literal URI like "myproto://posture".
	// Templated resources use an RFC 6570 fragment like
	// "myproto://analyses{?target}" — the protocol layer matches the
	// incoming URI against the pattern and passes the full URI
	// (including query) to Read.
	URIPattern() string

	// Description for resources/list.
	Description() string

	// Read returns the resource representation. uri is the full
	// requested URI, including any query parameters. Handlers that
	// consume query parameters parse them from uri themselves.
	Read(ctx context.Context, uri string) *Response
}

// Response is the uniform tool/resource response envelope. Every tool
// and resource returns this shape; the protocol layer serializes it as
// content[].text in the MCP response.
type Response struct {
	Status   string           `json:"status"`
	Data     any              `json:"data,omitempty"`
	Error    *ResponseError   `json:"error,omitempty"`
	Metadata ResponseMetadata `json:"metadata"`
}

// ResponseError is the error block inside a Response when Status is
// "error".
type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// ResponseMetadata carries always-present operational fields plus
// optional flags (cache_hit, requires_confirm).
type ResponseMetadata struct {
	ServerVersion   string `json:"server_version"`
	ElapsedMs       int64  `json:"elapsed_ms"`
	CacheHit        bool   `json:"cache_hit,omitempty"`
	RequiresConfirm bool   `json:"requires_confirm,omitempty"`
}

// OK returns a successful Response with the given data.
//
// Metadata.ServerVersion and Metadata.ElapsedMs are left zero — the
// protocol layer stamps them at emission time, immediately before
// serializing to the MCP wire format.
func OK(data any) *Response {
	return &Response{
		Status: "ok",
		Data:   data,
	}
}

// Err returns an error Response with the given code and message.
// details may be nil; if non-nil, it must be JSON-serializable.
func Err(code, message string, details any) *Response {
	return &Response{
		Status: "error",
		Error:  &ResponseError{Code: code, Message: message, Details: details},
	}
}

// WithCacheHit is a chain-style helper: r.WithCacheHit(true) sets the
// flag.
func (r *Response) WithCacheHit(v bool) *Response {
	r.Metadata.CacheHit = v
	return r
}

// WithRequiresConfirm sets the confirmation-required metadata flag
// (used by write tools that return a preview + confirm_token).
func (r *Response) WithRequiresConfirm(v bool) *Response {
	r.Metadata.RequiresConfirm = v
	return r
}

// Error codes used in Response.Error.Code when Status is "error".
// Named Code* rather than Err* to avoid collision with Go's error-value
// naming convention.
const (
	CodeSchemaViolation             = "schema_violation"
	CodeNotFound                    = "not_found"
	CodeCacheMissRequiresRefresh    = "cache_miss_requires_refresh"
	CodeUnsafeOperationNeedsConfirm = "unsafe_operation_needs_confirm"
	CodeInvalidConfirmToken         = "invalid_confirm_token"
	CodeDispatchRequested           = "dispatch_requested"
	CodeValidationFailed            = "validation_failed"
	CodeInternalError               = "internal_error"
)
