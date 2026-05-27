// Package mcp: initialize/initialized lifecycle.
//
// The MCP lifecycle requires:
//  1. Client sends initialize (request with id).
//  2. Server responds with its capabilities and serverInfo.
//  3. Client sends notifications/initialized (notification, no id).
//  4. Server is now in operational state.
//
// Any requests (other than initialize) received before the initialized
// notification is seen are rejected per spec.
//
// clientInfo (name + version) from the initialize request is recorded
// for callers that want to stamp an audit trail.
package mcp

import (
	"encoding/json"
	"fmt"
	"sync"
)

// protocolVersion is the MCP spec version this server implements.
const protocolVersion = "2025-11-25"

// initializeParams mirrors the params of the initialize request. We
// only decode the fields we need; extra capabilities are ignored.
type initializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    clientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
}

// clientCapabilities holds the subset of client capability flags we
// care about.
type clientCapabilities struct {
	Tools     *struct{} `json:"tools,omitempty"`
	Resources *struct{} `json:"resources,omitempty"`
}

// ClientInfo carries the name and version from initialize.clientInfo.
// Exposed so callers can stamp audit records with the connecting
// client's identity.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Source returns the audit-trail source tag "mcp/<name>/<version>" for
// this client. Returns "mcp/unknown/unknown" when called before the
// handshake completes.
func (c ClientInfo) Source() string {
	name := c.Name
	if name == "" {
		name = "unknown"
	}
	ver := c.Version
	if ver == "" {
		ver = "unknown"
	}
	return fmt.Sprintf("mcp/%s/%s", name, ver)
}

// initializeResult is the result body of the server's initialize
// response. Instructions carries server-level orientation that the
// client may surface to the model — per MCP 2025-11-25 it is the
// designated channel for "what this server is for and when to reach
// for it." Empty is legal; non-empty is strongly recommended because
// it's pushed to the model on every session start.
type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      serverInfoBody     `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

// serverCapabilities declares what the server supports.
type serverCapabilities struct {
	Tools     *struct{} `json:"tools,omitempty"`
	Resources *struct{} `json:"resources,omitempty"`
}

// serverInfoBody is the serverInfo block in the initialize response.
type serverInfoBody struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// lifecycleState tracks where we are in the MCP lifecycle.
type lifecycleState int

const (
	statePreInit     lifecycleState = iota // before any initialize request
	stateInitialized                       // after initialize response sent, before notifications/initialized
	stateOperational                       // after notifications/initialized received
)

// handshake manages the MCP initialize/initialized lifecycle. It is
// owned by the Server; its mutating methods run in the Serve read loop,
// while its observers (isOperational, ClientInfo) run in spawned
// handler goroutines. The mu guard closes the resulting race on
// state + client.
//
// name, version, instructions are set once at construction and never
// mutated, so they are outside the lock's coverage.
type handshake struct {
	mu     sync.RWMutex
	state  lifecycleState
	client ClientInfo

	// name is the server name announced in the initialize response's
	// serverInfo.name. Immutable after construction.
	name string
	// version is the server version announced in the initialize
	// response's serverInfo.version. Immutable after construction.
	version string
	// instructions is the server-level orientation text sent in the
	// initialize response. Empty is legal. Immutable after construction.
	instructions string
}

// handleInitialize processes an initialize request. Returns the result
// to send back. May only be called once; a second call returns an error.
func (h *handshake) handleInitialize(params json.RawMessage) (*initializeResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.state != statePreInit {
		return nil, fmt.Errorf("initialize already called")
	}

	var p initializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid initialize params: %w", err)
		}
	}

	h.client = p.ClientInfo

	h.state = stateInitialized

	empty := struct{}{}
	return &initializeResult{
		ProtocolVersion: protocolVersion,
		Capabilities: serverCapabilities{
			Tools:     &empty,
			Resources: &empty,
		},
		ServerInfo: serverInfoBody{
			Name:    h.name,
			Version: h.version,
		},
		Instructions: h.instructions,
	}, nil
}

// handleInitializedNotification processes the notifications/initialized
// notification and transitions state to operational. Per the MCP
// lifecycle, if it arrives outside the expected window (before
// initialize, or after already-operational) the server ignores it.
func (h *handshake) handleInitializedNotification() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state != stateInitialized {
		return
	}
	h.state = stateOperational
}

// isOperational reports whether the lifecycle is past the handshake
// and ready for tool/resource calls.
func (h *handshake) isOperational() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.state == stateOperational
}

// ClientInfo returns the clientInfo captured during the initialize
// handshake. Safe to call at any time; returns zero value before
// initialize is processed.
func (h *handshake) ClientInfo() ClientInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.client
}
