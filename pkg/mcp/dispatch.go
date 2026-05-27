// Package mcp: method dispatch.
//
// Supported methods (MCP spec 2025-11-25):
//
//	initialize           — lifecycle handshake (request)
//	notifications/initialized — lifecycle handshake (notification)
//	tools/list           — list registered tools
//	tools/call           — invoke a tool
//	resources/list       — list registered resources
//	resources/read       — read a resource by URI
//
// Unknown methods are rejected with JSON-RPC error code -32601
// (Method not found) per spec.
//
// Resources/read URI matching: static URIs match literally; templated
// URIs (containing "{") match by prefix of the base path before any
// "?" or "{".
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sarahmaeve/toolbox/pkg/schema"
)

// toolsListResult is the result body of a tools/list response.
type toolsListResult struct {
	Tools []toolEntry `json:"tools"`
}

// toolEntry is one tool in the tools/list response.
type toolEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// toolsCallParams are the params of a tools/call request.
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// toolsCallResult is the result body of a tools/call response.
type toolsCallResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError"`
}

// resourcesListResult is the result body of a resources/list response.
type resourcesListResult struct {
	Resources []resourceEntry `json:"resources"`
}

// resourceEntry is one resource in the resources/list response.
type resourceEntry struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// resourcesReadParams are the params of a resources/read request.
type resourcesReadParams struct {
	URI string `json:"uri"`
}

// resourcesReadResult is the result body of a resources/read response.
type resourcesReadResult struct {
	Contents []resourceContent `json:"contents"`
}

// resourceContent is one entry in a resources/read response's contents.
type resourceContent struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType"`
	Text     string `json:"text"`
}

// contentItem is one entry in a tools/call response's content array.
type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// dispatch handles a single JSON-RPC request/notification. For requests
// it returns (result, error); for notifications it returns (nil, nil) —
// no response should be sent.
func (s *Server) dispatch(ctx context.Context, req *rpcRequest) (result any, rpcErr *rpcError) {
	switch req.Method {
	case "initialize":
		return s.dispatchInitialize(req.Params)

	case "notifications/initialized":
		s.shake.handleInitializedNotification()
		return nil, nil

	case "tools/list":
		if !s.shake.isOperational() {
			return nil, &rpcError{Code: codeInvalidRequest, Message: "server not yet initialized"}
		}
		return s.dispatchToolsList()

	case "tools/call":
		if !s.shake.isOperational() {
			return nil, &rpcError{Code: codeInvalidRequest, Message: "server not yet initialized"}
		}
		return s.dispatchToolsCall(ctx, req.Params)

	case "resources/list":
		if !s.shake.isOperational() {
			return nil, &rpcError{Code: codeInvalidRequest, Message: "server not yet initialized"}
		}
		return s.dispatchResourcesList()

	case "resources/read":
		if !s.shake.isOperational() {
			return nil, &rpcError{Code: codeInvalidRequest, Message: "server not yet initialized"}
		}
		return s.dispatchResourcesRead(ctx, req.Params)

	default:
		return nil, &rpcError{
			Code:    codeMethodNotFound,
			Message: fmt.Sprintf("method not found: %s", req.Method),
		}
	}
}

func (s *Server) dispatchInitialize(params json.RawMessage) (any, *rpcError) {
	result, err := s.shake.handleInitialize(params)
	if err != nil {
		return nil, &rpcError{Code: codeInvalidRequest, Message: err.Error()}
	}
	return result, nil
}

func (s *Server) dispatchToolsList() (any, *rpcError) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]toolEntry, 0, len(s.tools))
	for _, t := range s.tools {
		entries = append(entries, toolEntry{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	sortToolEntries(entries)
	return &toolsListResult{Tools: entries}, nil
}

// dispatchToolsCall handles a tools/call request. Protocol-level errors
// (unknown tool, bad params JSON) return an rpcError. Handler-level
// errors are wrapped in the MCP content envelope with isError:true.
func (s *Server) dispatchToolsCall(ctx context.Context, rawParams json.RawMessage) (any, *rpcError) {
	var p toolsCallParams
	if len(rawParams) == 0 {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call: params is required"}
	}
	if err := json.Unmarshal(rawParams, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call: name is required"}
	}

	s.mu.RLock()
	tool, ok := s.tools[p.Name]
	sch := s.toolSchemas[p.Name]
	s.mu.RUnlock()

	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown tool: %s", p.Name)}
	}

	if sch != nil {
		if v := sch.Validate(p.Name, p.Arguments); v != nil {
			return wrapHandlerResponse(violationToResponse(v)), nil
		}
	}

	start := time.Now()
	resp := tool.Handle(ctx, p.Arguments)
	resp.Metadata.ElapsedMs = time.Since(start).Milliseconds()
	resp.Metadata.ServerVersion = s.version

	return wrapHandlerResponse(resp), nil
}

// violationToResponse maps a schema.Violation into the MCP Response
// envelope. The schema package is protocol-agnostic; this is the
// MCP-specific wrapping.
func violationToResponse(v *schema.Violation) *Response {
	details := map[string]any{}
	if v.Field != "" {
		details["field"] = v.Field
	}
	if v.Type != "" {
		details["type"] = v.Type
	}
	if len(v.ValidFields) > 0 {
		details["valid_fields"] = v.ValidFields
	}
	if len(details) == 0 {
		return Err(CodeSchemaViolation, v.Message, nil)
	}
	return Err(CodeSchemaViolation, v.Message, details)
}

// wrapHandlerResponse encodes a *Response as a tools/call result per
// the MCP 2025-11-25 spec: content[0].text = JSON-serialized Response,
// isError = (Response.Status == "error").
func wrapHandlerResponse(resp *Response) *toolsCallResult {
	text, err := json.Marshal(resp)
	if err != nil {
		text = []byte(`{"status":"error","error":{"code":"internal_error","message":"response serialization failed"}}`)
	}
	return &toolsCallResult{
		Content: []contentItem{{Type: "text", Text: string(text)}},
		IsError: resp.Status == "error",
	}
}

func (s *Server) dispatchResourcesList() (any, *rpcError) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]resourceEntry, 0, len(s.resources))
	for _, r := range s.resources {
		entries = append(entries, resourceEntry{
			URI:         r.URIPattern(),
			Name:        resourceName(r.URIPattern()),
			Description: r.Description(),
			MIMEType:    "application/json",
		})
	}
	sortResourceEntries(entries)
	return &resourcesListResult{Resources: entries}, nil
}

func (s *Server) dispatchResourcesRead(ctx context.Context, rawParams json.RawMessage) (any, *rpcError) {
	var p resourcesReadParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid resources/read params: " + err.Error()}
		}
	}
	if p.URI == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "resources/read: uri is required"}
	}

	s.mu.RLock()
	resource := s.matchResource(p.URI)
	s.mu.RUnlock()

	if resource == nil {
		return nil, &rpcError{
			Code:    -32002, // resource not found, per spec §Resources/Error Handling
			Message: fmt.Sprintf("resource not found: %s", p.URI),
			Data:    map[string]string{"uri": p.URI},
		}
	}

	start := time.Now()
	resp := resource.Read(ctx, p.URI)
	resp.Metadata.ElapsedMs = time.Since(start).Milliseconds()
	resp.Metadata.ServerVersion = s.version

	text, err := json.Marshal(resp)
	if err != nil {
		text = []byte(`{"status":"error","error":{"code":"internal_error","message":"response serialization failed"}}`)
	}

	return &resourcesReadResult{
		Contents: []resourceContent{
			{URI: p.URI, MIMEType: "application/json", Text: string(text)},
		},
	}, nil
}

// matchResource finds the resource whose URIPattern matches uri. Exact
// match takes priority. If no exact match, a pattern containing "{" is
// tried as a prefix match against the path-before-"?" of uri.
//
// Static-pattern matching is deliberately strict: the URI must equal
// the pattern exactly OR differ only by a "?"-prefixed query string or
// "#"-prefixed fragment.
func (s *Server) matchResource(uri string) Resource {
	if r, ok := s.resources[uri]; ok {
		return r
	}

	reqBase := uriBase(uri)
	for pattern, r := range s.resources {
		if strings.Contains(pattern, "{") {
			if uriBase(pattern) == reqBase {
				return r
			}
		} else if uri == pattern ||
			strings.HasPrefix(uri, pattern+"?") ||
			strings.HasPrefix(uri, pattern+"#") {
			return r
		}
	}
	return nil
}

// uriBase strips the query string and any RFC 6570 template suffixes
// from a URI, returning the plain path prefix.
func uriBase(uri string) string {
	if i := strings.IndexAny(uri, "?{"); i >= 0 {
		return uri[:i]
	}
	return uri
}

// resourceName derives a short display name from a URI pattern.
func resourceName(pattern string) string {
	base := uriBase(pattern)
	if _, after, ok := strings.Cut(base, "://"); ok {
		return after
	}
	return base
}

func sortToolEntries(entries []toolEntry) {
	slices.SortFunc(entries, func(a, b toolEntry) int {
		return strings.Compare(a.Name, b.Name)
	})
}

func sortResourceEntries(entries []resourceEntry) {
	slices.SortFunc(entries, func(a, b resourceEntry) int {
		return strings.Compare(a.URI, b.URI)
	})
}
