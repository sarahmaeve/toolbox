package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sarahmaeve/toolbox/pkg/messagestore"
	"github.com/sarahmaeve/toolbox/pkg/schema"
)

// handlerFixture spins up a bridge Server with a registered taskSchema
// and exposes its raw HTTP handler. Tests that need to assert on status
// codes, response headers, or wire shape that the Client wrapper hides
// go through this instead of fixture().
func handlerFixture(t *testing.T, opts ...func(*ServerConfig)) (http.Handler, *messagestore.Store) {
	t.Helper()
	store, err := messagestore.Open(context.Background(), messagestore.Config{
		DBPath:            filepath.Join(t.TempDir(), "bridge.db"),
		AllowedRoles:      []string{"agent", "orchestrator"},
		MaxActiveSessions: 100,
	})
	require.NoError(t, err)
	sch, err := schema.Parse(json.RawMessage(taskSchemaJSON))
	require.NoError(t, err)
	require.NoError(t, store.RegisterType(messagestore.MessageType{
		Name:   "task.completed",
		Schema: sch,
	}))

	cfg := ServerConfig{
		Store:  store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	srv := NewServer(cfg)
	t.Cleanup(func() { _ = store.Close() })
	return srv.Handler(), store
}

// --- ListSessions ---

func TestHandleListSessions_EmptyReturnsArrayNotNull(t *testing.T) {
	t.Parallel()
	// The handler must coerce a nil result to "[]" so JSON consumers can
	// rely on the response being an array, not null. Regression bait
	// because the store returns nil for an empty result set.
	h, _ := handlerFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, "[]", w.Body.String())
}

func TestHandleListSessions_ReturnsPopulatedList(t *testing.T) {
	t.Parallel()
	h, store := handlerFixture(t)
	ctx := context.Background()
	_, err := store.CreateSession(ctx, "task-a", "")
	require.NoError(t, err)
	_, err = store.CreateSession(ctx, "task-b", "")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got []messagestore.Session
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Len(t, got, 2)
}

// --- GetSession ---

func TestHandleGetSession_Found(t *testing.T) {
	t.Parallel()
	h, store := handlerFixture(t)
	sess, err := store.CreateSession(context.Background(), "t", "")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got messagestore.Session
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, sess.ID, got.ID)
	assert.Equal(t, "t", got.Target)
}

func TestHandleGetSession_NotFoundIs404WithEnvelope(t *testing.T) {
	t.Parallel()
	// 404 is the contract — and the body must be the {"error":"..."}
	// envelope so the Client wrapper can decode the message. A bare
	// 404 with no body would silently break decodeServerError.
	h, _ := handlerFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/no-such-id", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	var env struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	assert.Contains(t, env.Error, "not found")
}

// --- DeleteSession ---

func TestHandleDeleteSession_RemovesSessionAndIsIdempotent(t *testing.T) {
	t.Parallel()
	// Deletion is idempotent at the store layer (no row-count check) —
	// pin that the HTTP layer mirrors that: 204 the first time AND the
	// second time. Also confirm GetSession then returns 404 (no orphan).
	h, store := handlerFixture(t)
	sess, err := store.CreateSession(context.Background(), "t", "")
	require.NoError(t, err)

	delete := func() int {
		req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sess.ID, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	assert.Equal(t, http.StatusNoContent, delete())
	assert.Equal(t, http.StatusNoContent, delete(), "delete must be idempotent")

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code, "session must be gone after delete")
}

func TestHandleDeleteSession_AlsoRemovesMessages(t *testing.T) {
	t.Parallel()
	// The delete is documented to cascade. Pin that, since otherwise a
	// regression would silently leak orphan messages indistinguishable
	// from a fresh deposit.
	h, store := handlerFixture(t)
	ctx := context.Background()
	sess, err := store.CreateSession(ctx, "t", "")
	require.NoError(t, err)
	_, err = store.DepositMessage(ctx, &messagestore.Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"t-1"}`),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sess.ID, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)

	// Re-create the session id won't work (uuid is random), so look up
	// messages by subject across all sessions via the store directly.
	_, err = store.GetLatestMessage(ctx, messagestore.MessageFilter{Type: "task.completed"})
	assert.Error(t, err, "no messages should remain after cascade delete")
}

// --- format=raw shortcut ---

func TestHandleGetLatestMessage_FormatRawReturnsBareContent(t *testing.T) {
	t.Parallel()
	// The raw shortcut bypasses the {"id":..., "content":...} envelope
	// so an LLM client (e.g. Claude Code's WebFetch) sees the deposit's
	// content bytes verbatim. Content-Type stays application/json so the
	// client can still parse the bytes as JSON.
	h, store := handlerFixture(t)
	ctx := context.Background()
	sess, err := store.CreateSession(ctx, "t", "")
	require.NoError(t, err)
	payload := json.RawMessage(`{"task_id":"raw-1","duration":42}`)
	_, err = store.DepositMessage(ctx, &messagestore.Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task.completed",
		Content:   payload,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet,
		"/api/sessions/"+sess.ID+"/messages/latest?format=raw", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
	assert.JSONEq(t, string(payload), w.Body.String(),
		"raw mode must return the content bytes, not the message envelope")
	// Explicitly verify there's no enveloping — a normal response would
	// have "session_id" at the top level.
	assert.NotContains(t, w.Body.String(), "session_id")
}

func TestHandleGetMessages_FormatRawOnlyWhenSingleMatch(t *testing.T) {
	t.Parallel()
	// The raw shortcut on the list endpoint requires exactly one match;
	// with multiple matches it falls back to the standard JSON array.
	// Otherwise concatenated raw payloads would be ambiguous.
	h, store := handlerFixture(t)
	ctx := context.Background()
	sess, err := store.CreateSession(ctx, "t", "")
	require.NoError(t, err)
	for _, id := range []string{"a", "b"} {
		_, err = store.DepositMessage(ctx, &messagestore.Message{
			SessionID: sess.ID,
			Role:      "agent",
			Type:      "task.completed",
			Content:   json.RawMessage(`{"task_id":"` + id + `"}`),
		})
		require.NoError(t, err)
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/sessions/"+sess.ID+"/messages?format=raw", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	// Two matches → must return a JSON array, not raw bytes of one or the
	// concatenation of both.
	var arr []messagestore.Message
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &arr))
	assert.Len(t, arr, 2)
}

// --- MaxBytesReader caps ---

func TestHandleCreateSession_RejectsOversizedBody(t *testing.T) {
	t.Parallel()
	// Default cap is 4 KB on session creation. Set a tighter limit so
	// the test stays fast and concrete; the property under test is that
	// the cap fires with a 4xx rather than panic or half-read.
	h, _ := handlerFixture(t, func(cfg *ServerConfig) {
		cfg.MaxSessionBodyBytes = 256
	})

	huge := strings.Repeat("x", 1024)
	body := bytes.NewBufferString(`{"target":"` + huge + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code < 400 || w.Code >= 500 {
		t.Errorf("oversized create-session body: got %d, want 4xx", w.Code)
	}
}

func TestHandleDepositMessage_RejectsOversizedBody(t *testing.T) {
	t.Parallel()
	h, store := handlerFixture(t, func(cfg *ServerConfig) {
		cfg.MaxMessageBodyBytes = 256
	})
	sess, err := store.CreateSession(context.Background(), "t", "")
	require.NoError(t, err)

	bulk := strings.Repeat("a", 1024)
	body := bytes.NewBufferString(`{"role":"agent","type":"task.completed","content":{"task_id":"` + bulk + `"}}`)
	req := httptest.NewRequest(http.MethodPost,
		"/api/sessions/"+sess.ID+"/messages", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code < 400 || w.Code >= 500 {
		t.Errorf("oversized deposit-message body: got %d, want 4xx", w.Code)
	}
}

// --- buildTLSConfig invariants ---

func TestBuildTLSConfig_EmptyPathFallsBackToSystemPool(t *testing.T) {
	t.Parallel()
	// With no CA anchor we accept whatever the OS trust store offers.
	// Crucial: we still set a minimum TLS version, and we never enable
	// InsecureSkipVerify. Together these mean a misconfigured client
	// (no anchor) fails closed against a self-signed server cert rather
	// than silently trusting any peer.
	cfg, err := buildTLSConfig("")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.False(t, cfg.InsecureSkipVerify,
		"InsecureSkipVerify must never be true — even with no anchor configured")
	assert.GreaterOrEqual(t, cfg.MinVersion, uint16(0x0303),
		"MinVersion must be TLS 1.2 or higher")
}

func TestBuildTLSConfig_MissingFileFallsBackToSystemPool(t *testing.T) {
	t.Parallel()
	// A CAPath pointing at a non-existent file is treated the same as
	// no anchor — this is the day-zero ergonomics path where mkcert is
	// installed on the OS but the project's anchor file hasn't been
	// laid down yet. Important: missing file is NOT an error; bad PEM
	// IS an error (next test). The distinction is intentional.
	cfg, err := buildTLSConfig(filepath.Join(t.TempDir(), "definitely-not-here.pem"))
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.False(t, cfg.InsecureSkipVerify)
	assert.Nil(t, cfg.RootCAs,
		"RootCAs must be nil so crypto/tls falls back to the system root pool")
}

func TestBuildTLSConfig_BadPEMReturnsError(t *testing.T) {
	t.Parallel()
	// A file present but containing no usable CERTIFICATE PEM blocks is
	// a real misconfiguration: the operator pointed at the wrong file
	// or installed a corrupt one. Fall-back to system pool would mask
	// the bug. Fail closed and loud.
	bad := filepath.Join(t.TempDir(), "not-pem.txt")
	require.NoError(t, os.WriteFile(bad, []byte("this is not a PEM file"), 0o600))

	_, err := buildTLSConfig(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usable PEM")
}

func TestBuildTLSConfig_NeverSetsInsecureSkipVerify(t *testing.T) {
	t.Parallel()
	// Belt-and-braces: across every reachable construction path, the
	// returned config must have InsecureSkipVerify == false. Spelled out
	// as its own test because the doc comment in client.go specifically
	// pins this invariant and CLAUDE.md-class regressions on it would
	// silently degrade TLS to "accepts any cert."
	cases := []string{
		"",
		filepath.Join(t.TempDir(), "missing.pem"),
	}
	for _, c := range cases {
		cfg, err := buildTLSConfig(c)
		require.NoError(t, err, "case %q", c)
		assert.False(t, cfg.InsecureSkipVerify, "case %q: InsecureSkipVerify must be false", c)
	}
}
