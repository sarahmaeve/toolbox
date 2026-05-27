// Package bridge implements a localhost HTTPS bridge that exposes a
// pkg/messagestore Store as a REST-ish HTTP API. The bridge exists for
// agent frameworks (e.g. Claude Code's WebFetch) whose HTTP clients
// are GET-only or otherwise can't reach an MCP stdio server directly.
//
// Wire shape:
//
//	POST   /api/sessions                            — create session
//	GET    /api/sessions                            — list sessions
//	GET    /api/sessions/{id}                       — get session
//	DELETE /api/sessions/{id}                       — delete session + messages
//	POST   /api/sessions/{id}/messages              — deposit message
//	GET    /api/sessions/{id}/messages              — list messages
//	GET    /api/sessions/{id}/messages/latest       — latest matching message
//
// The bridge binds to 127.0.0.1 only — it is a local development tool,
// not a network service.
//
// TLS: required for agent access via Claude Code's WebFetch (which
// forces HTTPS and rejects self-signed certs). Pair the server with
// pkg/certs to manage the mkcert-issued trust anchor.
package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/sarahmaeve/toolbox/pkg/messagestore"
)

// Request body size caps. Sessions are tiny (target URI + metadata);
// messages may carry larger structured payloads.
const (
	defaultMaxSessionBodyBytes = 4 * 1024         // 4 KB
	defaultMaxMessageBodyBytes = 10 * 1024 * 1024 // 10 MB
)

// ServerConfig configures the bridge HTTP server.
type ServerConfig struct {
	// Store is the backing messagestore.Store the bridge exposes.
	// Required.
	Store *messagestore.Store

	// Logger receives structured info/error events. Defaults to
	// slog.Default() when nil.
	Logger *slog.Logger

	// MaxSessionBodyBytes caps the request body for session-create.
	// Zero applies the default (4 KB).
	MaxSessionBodyBytes int64

	// MaxMessageBodyBytes caps the request body for message-deposit.
	// Zero applies the default (10 MB).
	MaxMessageBodyBytes int64
}

// Server is the bridge HTTP server.
type Server struct {
	store               *messagestore.Store
	mux                 *http.ServeMux
	server              *http.Server
	logger              *slog.Logger
	maxSessionBodyBytes int64
	maxMessageBodyBytes int64
}

// NewServer creates a bridge server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Store == nil {
		panic("bridge: ServerConfig.Store is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	maxSession := cfg.MaxSessionBodyBytes
	if maxSession == 0 {
		maxSession = defaultMaxSessionBodyBytes
	}
	maxMsg := cfg.MaxMessageBodyBytes
	if maxMsg == 0 {
		maxMsg = defaultMaxMessageBodyBytes
	}

	s := &Server{
		store:               cfg.Store,
		mux:                 http.NewServeMux(),
		logger:              logger,
		maxSessionBodyBytes: maxSession,
		maxMessageBodyBytes: maxMsg,
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	s.mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	s.mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/messages", s.handleDepositMessage)
	s.mux.HandleFunc("GET /api/sessions/{id}/messages", s.handleGetMessages)
	s.mux.HandleFunc("GET /api/sessions/{id}/messages/latest", s.handleGetLatestMessage)
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)

	// Cross-session search — same handlers, but SessionID isn't bound
	// to the URL path. Caller passes filter dimensions as query
	// parameters; the store enforces "at least one filter is set."
	s.mux.HandleFunc("GET /api/messages", s.handleSearchMessages)
	s.mux.HandleFunc("GET /api/messages/latest", s.handleSearchLatestMessage)
}

// Handler returns the http.Handler for testing with httptest.
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe starts the HTTP server on the given port, bound to
// 127.0.0.1. If certFile and keyFile are both non-empty, TLS is
// enabled. Shuts down gracefully when ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, port int, certFile, keyFile string) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	s.server = &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}

	// Graceful shutdown on ctx cancel. The shutdown context is rooted
	// at Background — by the time we reach this goroutine the parent
	// ctx has already been cancelled (we waited for <-ctx.Done()), so
	// deriving from it would produce an immediately-dead context and
	// abort the drain.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // see comment above
			s.logger.Error("server shutdown", "error", err)
		}
	}()

	tlsEnabled := certFile != "" && keyFile != ""
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	s.logger.Info("bridge server listening", "addr", addr, "scheme", scheme)

	var err error
	if tlsEnabled {
		err = s.server.ListenAndServeTLS(certFile, keyFile)
	} else {
		err = s.server.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// --- wire types ---

type createSessionRequest struct {
	Target   string `json:"target"`
	Metadata string `json:"metadata,omitempty"`
}

type depositMessageRequest struct {
	Role      string          `json:"role"`
	SenderID  string          `json:"sender_id,omitempty"`
	Type      string          `json:"type"`
	SubjectID string          `json:"subject_id,omitempty"`
	Content   json.RawMessage `json:"content"`
	Metadata  string          `json:"metadata,omitempty"`
}

// --- handlers ---

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxSessionBodyBytes)

	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Target == "" {
		writeError(w, http.StatusBadRequest, "target is required")
		return
	}

	sess, err := s.store.CreateSession(r.Context(), req.Target, req.Metadata)
	if err != nil {
		// CreateSession's session-cap path is the only expected
		// non-internal failure; surface its message.
		s.logger.Error("create session", "error", err)
		writeError(w, http.StatusServiceUnavailable, "%s", err.Error())
		return
	}

	s.logger.Info("session created", "id", sess.ID, "target", sess.Target)
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ListSessions(r.Context())
	if err != nil {
		s.logger.Error("list sessions", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if sessions == nil {
		sessions = []messagestore.Session{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.GetSession(r.Context(), id)
	if err != nil {
		if errors.Is(err, messagestore.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		s.logger.Error("get session", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteSession(r.Context(), id); err != nil {
		s.logger.Error("delete session", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.logger.Info("session deleted", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDepositMessage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxMessageBodyBytes)
	sessionID := r.PathValue("id")

	var req depositMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Role == "" || req.Type == "" {
		writeError(w, http.StatusBadRequest, "role and type are required")
		return
	}
	if len(req.Content) == 0 {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	msg := &messagestore.Message{
		SessionID: sessionID,
		Role:      req.Role,
		SenderID:  req.SenderID,
		Type:      req.Type,
		SubjectID: req.SubjectID,
		Content:   req.Content,
		Metadata:  req.Metadata,
	}
	msg, err := s.store.DepositMessage(r.Context(), msg)
	if err != nil {
		s.logger.Error("deposit message", "session", sessionID, "error", err)
		switch {
		case errors.Is(err, messagestore.ErrSessionNotFound):
			writeError(w, http.StatusBadRequest,
				"session %q not found; create one first", sessionID)
		case errors.Is(err, messagestore.ErrUnknownRole),
			errors.Is(err, messagestore.ErrUnknownType),
			errors.Is(err, messagestore.ErrSchemaViolation),
			errors.Is(err, messagestore.ErrSemanticViolation):
			writeError(w, http.StatusBadRequest, "%s", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	s.logger.Info("message deposited",
		"session", sessionID, "role", req.Role,
		"type", req.Type, "bytes", len(req.Content))
	writeJSON(w, http.StatusCreated, msg)
}

func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	filter := filterFromQuery(r, r.PathValue("id"))
	msgs, err := s.store.GetMessages(r.Context(), filter)
	if err != nil {
		if errors.Is(err, messagestore.ErrFilterRequired) {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
		s.logger.Error("get messages", "session", filter.SessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Raw-content shortcut for single-match queries (typical agent
	// use case via WebFetch). Returns the literal content bytes as
	// text so an LLM doesn't have to unwrap a JSON envelope.
	if r.URL.Query().Get("format") == "raw" && len(msgs) == 1 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(msgs[0].Content) //nolint:errcheck // best-effort
		return
	}

	if msgs == nil {
		msgs = []messagestore.Message{}
	}
	writeJSON(w, http.StatusOK, msgs)
}

func (s *Server) handleGetLatestMessage(w http.ResponseWriter, r *http.Request) {
	filter := filterFromQuery(r, r.PathValue("id"))
	msg, err := s.store.GetLatestMessage(r.Context(), filter)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "no matching message")
			return
		}
		if errors.Is(err, messagestore.ErrFilterRequired) {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
		s.logger.Error("get latest message", "session", filter.SessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if r.URL.Query().Get("format") == "raw" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(msg.Content) //nolint:errcheck // best-effort
		return
	}

	writeJSON(w, http.StatusOK, msg)
}

// handleSearchMessages is the cross-session counterpart to
// handleGetMessages: SessionID isn't bound to the URL, so the store's
// "at least one filter" rule applies. Useful for digest-style memory
// queries — pass subject_id or sender_id to gather messages on a
// topic across every run.
func (s *Server) handleSearchMessages(w http.ResponseWriter, r *http.Request) {
	filter := filterFromQuery(r, "")
	msgs, err := s.store.GetMessages(r.Context(), filter)
	if err != nil {
		if errors.Is(err, messagestore.ErrFilterRequired) {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
		s.logger.Error("search messages", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if msgs == nil {
		msgs = []messagestore.Message{}
	}
	writeJSON(w, http.StatusOK, msgs)
}

// handleSearchLatestMessage is the cross-session counterpart to
// handleGetLatestMessage. Returns the single most-recent message
// matching the supplied filters across all sessions.
func (s *Server) handleSearchLatestMessage(w http.ResponseWriter, r *http.Request) {
	filter := filterFromQuery(r, "")
	msg, err := s.store.GetLatestMessage(r.Context(), filter)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "no matching message")
			return
		}
		if errors.Is(err, messagestore.ErrFilterRequired) {
			writeError(w, http.StatusBadRequest, "%s", err.Error())
			return
		}
		s.logger.Error("search latest message", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, msg)
}

// filterFromQuery assembles a MessageFilter from the request's URL
// query parameters, threading sessionID in from the URL path when
// the per-session endpoint binds it. Empty sessionID is the
// cross-session search mode.
func filterFromQuery(r *http.Request, sessionID string) messagestore.MessageFilter {
	q := r.URL.Query()
	f := messagestore.MessageFilter{
		SessionID: sessionID,
		Role:      q.Get("role"),
		SenderID:  q.Get("sender_id"),
		Type:      q.Get("type"),
		SubjectID: q.Get("subject_id"),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.Limit = n
		}
	}
	return f
}

// --- response helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck,gosec // G104: best-effort response write
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck,gosec // G104: best-effort
}
