// Package mcp: Server struct — the top-level MCP server.
//
// Usage:
//
//	srv := mcp.NewServer(mcp.ServerConfig{
//	    Name:         "my-tool",
//	    Version:      "0.1.0",
//	    Instructions: "...",
//	})
//	srv.Register(myTool)
//	srv.RegisterResource(myResource)
//	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
//	    log.Fatal(err)
//	}
//
// Serve reads newline-delimited JSON-RPC 2.0 messages from r, dispatches
// them, and writes responses to w. It returns when:
//   - The reader signals EOF (client closed stdin — normal shutdown for stdio)
//   - ctx is cancelled (caller-initiated shutdown)
//   - An unrecoverable read/write error occurs
//
// Goroutine model: Serve runs a single read loop on the calling goroutine
// and dispatches each request to a handler goroutine. A WaitGroup tracks
// in-flight handlers so Close / context cancellation can drain them
// before returning. Responses are serialized through a mutex-guarded
// writer to prevent interleaved output.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"sync"

	"github.com/sarahmaeve/toolbox/pkg/schema"
)

// ServerConfig configures a Server at construction.
type ServerConfig struct {
	// Name is the server name returned in the initialize handshake's
	// serverInfo.name. Required.
	Name string

	// Version is the server version returned in the initialize
	// handshake's serverInfo.version and in every
	// Response.Metadata.ServerVersion. Defaults to "0.0.0-unset" when
	// empty.
	Version string

	// Instructions is the server-level orientation text sent in the
	// initialize response. Per MCP 2025-11-25 this is the designated
	// channel for "what this server is for and when to reach for it"
	// — it rides in every session's context window, so prefer brevity.
	// Empty is legal but strongly discouraged for production servers.
	Instructions string
}

// Server is the MCP protocol server. Zero value is not usable; create
// with NewServer.
type Server struct {
	// version is the server version surfaced in Response.Metadata.ServerVersion.
	version string

	mu          sync.RWMutex
	tools       map[string]Tool
	toolSchemas map[string]*schema.Schema // parsed schema per tool
	resources   map[string]Resource

	shake handshake

	// writeMu guards all writes to the codec's underlying writer.
	// Separate from mu so handler goroutines can write without holding
	// the registry read lock.
	writeMu sync.Mutex

	// wg tracks in-flight handler goroutines for graceful drain.
	wg sync.WaitGroup
}

// NewServer creates a ready-to-use Server with the given config.
func NewServer(cfg ServerConfig) *Server {
	version := cfg.Version
	if version == "" {
		version = "0.0.0-unset"
	}
	return &Server{
		version:     version,
		tools:       make(map[string]Tool),
		toolSchemas: make(map[string]*schema.Schema),
		resources:   make(map[string]Resource),
		shake: handshake{
			name:         cfg.Name,
			version:      version,
			instructions: cfg.Instructions,
		},
	}
}

// Version returns the version this Server was constructed with.
func (s *Server) Version() string {
	return s.version
}

// Register adds a Tool to the server's registry. The tool's InputSchema
// is parsed and cached; if parsing fails or the schema doesn't declare
// additionalProperties:false, Register panics — schema validity is a
// programmer error discovered at startup, not a runtime error.
//
// Register is not safe to call after Serve has started.
func (s *Server) Register(t Tool) {
	sch, err := schema.Parse(t.InputSchema())
	if err != nil {
		panic("mcp: invalid InputSchema for tool " + t.Name() + ": " + err.Error())
	}
	if !sch.StrictReject() {
		panic("mcp: tool " + t.Name() +
			" InputSchema must set additionalProperties:false (strict-reject is required by the Tool contract)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[t.Name()] = t
	s.toolSchemas[t.Name()] = sch
}

// RegisterResource adds a Resource to the server's registry. Not safe
// to call after Serve has started.
func (s *Server) RegisterResource(r Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[r.URIPattern()] = r
}

// ClientInfo returns the clientInfo captured during the initialize
// handshake, for use in audit logging. Returns a zero value before the
// handshake completes.
func (s *Server) ClientInfo() ClientInfo {
	return s.shake.ClientInfo()
}

// RegisteredToolNames returns the sorted names of every tool currently
// registered on the server.
func (s *Server) RegisteredToolNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Sorted(maps.Keys(s.tools))
}

// RegisteredResourcePatterns returns the sorted URI patterns of every
// resource currently registered on the server.
func (s *Server) RegisteredResourcePatterns() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Sorted(maps.Keys(s.resources))
}

// Serve reads JSON-RPC messages from r, dispatches them, and writes
// responses to w until r signals EOF or ctx is cancelled.
//
// On ctx cancellation: new incoming requests are rejected; in-flight
// handlers are given a derived context that is also cancelled. Serve
// waits for all in-flight handlers to finish before returning.
//
// Write-error discard policy: response writes go out on the same stdio
// transport the read loop is reading from. If a write fails, the
// connection is dead and no recovery is possible from the JSON-RPC
// layer. The read loop will observe the same condition on its next
// readRequest() call.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	c := newCodec(r, w)

	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return ctx.Err()
		default:
		}

		req, err := c.readRequest()
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.wg.Wait()
				return nil
			}
			if rpcErr, ok := errors.AsType[*rpcError](err); ok {
				s.writeMu.Lock()
				_ = c.writeError(json.RawMessage("null"), rpcErr.Code, rpcErr.Message, nil) //nolint:errcheck // write failure means client hung up
				s.writeMu.Unlock()
				continue
			}
			s.wg.Wait()
			return err
		}

		// Notifications: dispatch synchronously (no response) and continue.
		if req.isNotification() {
			_, _ = s.dispatch(ctx, req) //nolint:errcheck // per JSON-RPC 2.0, notifications get no response
			continue
		}

		// initialize is a lifecycle gate: every subsequent request
		// depends on the state transition it performs. We dispatch it
		// synchronously in the read loop so that by the time we read
		// the next frame, the state is guaranteed visible.
		if req.Method == "initialize" {
			result, rpcErr := s.dispatch(ctx, req)
			s.writeMu.Lock()
			if rpcErr != nil {
				_ = c.writeError(req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data) //nolint:errcheck // write failure means client hung up
			} else if result != nil {
				_ = c.writeResult(req.ID, result) //nolint:errcheck // write failure means client hung up
			}
			s.writeMu.Unlock()
			continue
		}

		s.wg.Add(1)
		go func(req *rpcRequest) {
			defer s.wg.Done()
			result, rpcErr := s.dispatch(ctx, req)

			s.writeMu.Lock()
			defer s.writeMu.Unlock()

			if rpcErr != nil {
				_ = c.writeError(req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data) //nolint:errcheck // write failure means client hung up
				return
			}
			if result != nil {
				_ = c.writeResult(req.ID, result) //nolint:errcheck // write failure means client hung up
			}
		}(req)
	}
}

// Close signals in-flight handlers to stop and waits for them to drain.
// After Close, Serve will return on its next iteration. Close is
// intended for test teardown; in production the ctx cancellation path
// is sufficient.
func (s *Server) Close() {
	s.wg.Wait()
}
