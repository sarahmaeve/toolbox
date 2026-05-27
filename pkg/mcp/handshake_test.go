package mcp

// Race-detection tests for the handshake type. Designed to fail under
// `go test -race ./pkg/mcp` if the mutex coverage regresses.
//
// The race, precisely: handshake.state and handshake.client are
// written from the Serve read loop (initialize + notifications/
// initialized are dispatched synchronously there) and read from
// request-handler goroutines. The mu guard closes the race.

import (
	"runtime"
	"sync/atomic"
	"testing"
)

// TestHandshake_StateAccessIsRaceFree spawns a reader goroutine that
// hammers isOperational() while the main test goroutine transitions
// the lifecycle. Under -race, dropping the mutex would fire the
// detector on handshake.state.
func TestHandshake_StateAccessIsRaceFree(t *testing.T) {
	h := &handshake{name: "test", version: "test"}

	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			_ = h.isOperational()
		}
	}()

	if _, err := h.handleInitialize(nil); err != nil {
		t.Fatalf("handleInitialize: %v", err)
	}
	runtime.Gosched()
	h.handleInitializedNotification()
	runtime.Gosched()

	stop.Store(true)
	<-done
}

// TestHandshake_ClientInfoIsRaceFree covers the parallel race on
// handshake.client.
func TestHandshake_ClientInfoIsRaceFree(t *testing.T) {
	h := &handshake{name: "test", version: "test"}

	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			_ = h.ClientInfo()
		}
	}()

	params := []byte(`{"clientInfo":{"name":"race-client","version":"9.9.9"}}`)
	if _, err := h.handleInitialize(params); err != nil {
		t.Fatalf("handleInitialize: %v", err)
	}
	runtime.Gosched()

	stop.Store(true)
	<-done
}

// TestHandshake_ServerInfoUsesConfigName verifies that the name passed
// to NewServer flows through to the initialize response's
// serverInfo.name, not a hard-coded value.
func TestHandshake_ServerInfoUsesConfigName(t *testing.T) {
	t.Parallel()
	h := &handshake{name: "my-server", version: "1.2.3", instructions: "hello"}
	result, err := h.handleInitialize(nil)
	if err != nil {
		t.Fatalf("handleInitialize: %v", err)
	}
	if result.ServerInfo.Name != "my-server" {
		t.Errorf("ServerInfo.Name = %q, want %q", result.ServerInfo.Name, "my-server")
	}
	if result.ServerInfo.Version != "1.2.3" {
		t.Errorf("ServerInfo.Version = %q, want %q", result.ServerInfo.Version, "1.2.3")
	}
	if result.Instructions != "hello" {
		t.Errorf("Instructions = %q, want %q", result.Instructions, "hello")
	}
}

// TestHandshake_InitializeTwiceFails verifies the one-shot property.
func TestHandshake_InitializeTwiceFails(t *testing.T) {
	t.Parallel()
	h := &handshake{name: "t", version: "t"}
	_, err := h.handleInitialize(nil)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, err = h.handleInitialize(nil)
	if err == nil {
		t.Error("second initialize should fail")
	}
}

// TestHandshake_ClientInfoSourceTag verifies the audit-trail source
// format.
func TestHandshake_ClientInfoSourceTag(t *testing.T) {
	t.Parallel()
	c := ClientInfo{Name: "claude-code", Version: "0.42"}
	if got := c.Source(); got != "mcp/claude-code/0.42" {
		t.Errorf("Source() = %q, want mcp/claude-code/0.42", got)
	}
	zero := ClientInfo{}
	if got := zero.Source(); got != "mcp/unknown/unknown" {
		t.Errorf("zero Source() = %q, want mcp/unknown/unknown", got)
	}
}
