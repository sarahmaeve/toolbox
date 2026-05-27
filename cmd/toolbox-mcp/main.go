// Command toolbox-mcp is an MCP-over-stdio server that exposes the
// messagestore primitives as MCP tools. This is the low-friction
// deposit/query path for MCP-capable clients (Claude Code) — no HTTPS
// setup, no TLS trust dance, no per-token API spend. Wire it into
// `.mcp.json` like any other stdio MCP server.
//
// Tools registered:
//
//	create_session       create a new session
//	deposit_message      deposit a typed message (schema-validated)
//	list_sessions        list all sessions
//	get_session          retrieve one session by id
//	get_messages         list messages with optional role/type filters
//	get_latest_message   retrieve the most recent matching message
//
// MessageTypes are loaded from --schemas-dir at startup (filename stem
// becomes the type name). The schemas directory is the same shape as
// toolbox-bridge — the two binaries are interchangeable on this axis.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sarahmaeve/toolbox/pkg/mcp"
	"github.com/sarahmaeve/toolbox/pkg/messagestore"
	"github.com/sarahmaeve/toolbox/pkg/schema"
)

const serverName = "toolbox-mcp"

// version, commit, and buildDate are stamped by the Makefile via
// -ldflags. `go install` skips the stamp and leaves the dev defaults.
// Logged at startup so operators can confirm the running build.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

const (
	instructions = `toolbox-mcp is a typed message bus for agent coordination. ` +
		`Use deposit_message to record typed payloads against a session; ` +
		`use get_messages / get_latest_message to read them back. Sessions ` +
		`group related work (create one with create_session). Every message ` +
		`carries a role (who emitted, controlled vocab from the operator) and ` +
		`a type (what shape; matches a registered MessageType schema). ` +
		`Unknown roles, unknown types, and schema-violating content are ` +
		`rejected with actionable errors — fix the payload and retry.`
)

func main() {
	dbPath := flag.String("db", "~/.toolbox/messages.db", "path to SQLite database")
	schemasDir := flag.String("schemas-dir", "", "directory of JSON Schema files; each <name>.json registers a MessageType named <name>")
	allowedRoles := flag.String("allowed-roles", "", "comma-separated allowed Role values; empty means accept any role")
	maxActive := flag.Int("max-active-sessions", 100, "cap on concurrent active sessions (0 = unlimited)")
	logPath := flag.String("log", "", "redirect log output to this path (default: stderr — fine for MCP since clients read stdout only)")
	flag.Parse()

	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: operator-supplied log path
		if err != nil {
			fmt.Fprintf(os.Stderr, "open log: %v\n", err)
			os.Exit(1)
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
	}

	resolvedDB, err := expandHome(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve db path: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store, err := messagestore.Open(ctx, messagestore.Config{
		DBPath:            resolvedDB,
		AllowedRoles:      splitCSV(*allowedRoles),
		MaxActiveSessions: *maxActive,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close() //nolint:errcheck

	if *schemasDir != "" {
		n, err := loadSchemasDir(store, *schemasDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load schemas: %v\n", err)
			os.Exit(1)
		}
		slog.Info("registered message types", "count", n, "dir", *schemasDir)
	}

	srv := mcp.NewServer(mcp.ServerConfig{
		Name:         serverName,
		Version:      version,
		Instructions: instructions,
	})

	for _, t := range buildTools(store) {
		srv.Register(t)
	}
	for _, t := range buildPDFTools() {
		srv.Register(t)
	}

	slog.Info("toolbox-mcp ready",
		"version", version,
		"commit", commit,
		"built", buildDate,
		"db", resolvedDB,
		"types", len(store.RegisteredTypes()),
		"tools", len(srv.RegisteredToolNames()))

	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}

// --- tools -----------------------------------------------------------------

// buildTools returns the full slate of MCP tools backed by store.
// Each tool's schema is strict-reject (additionalProperties:false) per
// the pkg/mcp Register contract.
func buildTools(store *messagestore.Store) []mcp.Tool {
	return []mcp.Tool{
		&createSessionTool{store: store},
		&depositMessageTool{store: store},
		&listSessionsTool{store: store},
		&getSessionTool{store: store},
		&getMessagesTool{store: store},
		&getLatestMessageTool{store: store},
	}
}

// --- create_session ---

type createSessionTool struct{ store *messagestore.Store }

func (createSessionTool) Name() string { return "create_session" }
func (createSessionTool) Description() string {
	return "Start a new session. Returns the session id; pass it as session_id on subsequent deposit/get calls."
}
func (createSessionTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"target":   {"type": "string"},
			"metadata": {"type": "string"}
		},
		"required": ["target"],
		"additionalProperties": false
	}`)
}
func (t *createSessionTool) Handle(ctx context.Context, input json.RawMessage) *mcp.Response {
	var p struct {
		Target   string `json:"target"`
		Metadata string `json:"metadata"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}
	sess, err := t.store.CreateSession(ctx, p.Target, p.Metadata)
	if err != nil {
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}
	return mcp.OK(sess)
}

// --- deposit_message ---

type depositMessageTool struct{ store *messagestore.Store }

func (depositMessageTool) Name() string { return "deposit_message" }
func (depositMessageTool) Description() string {
	return "Deposit a typed message against a session. Content is validated against the schema registered for the named type; unknown roles, unknown types, and schema-violating content are rejected with actionable errors."
}
func (depositMessageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"session_id": {"type": "string"},
			"role":       {"type": "string"},
			"sender_id":  {"type": "string"},
			"type":       {"type": "string"},
			"subject_id": {"type": "string"},
			"content":    {"type": "object"},
			"metadata":   {"type": "string"}
		},
		"required": ["session_id", "role", "type", "content"],
		"additionalProperties": false
	}`)
}
func (t *depositMessageTool) Handle(ctx context.Context, input json.RawMessage) *mcp.Response {
	var p struct {
		SessionID string          `json:"session_id"`
		Role      string          `json:"role"`
		SenderID  string          `json:"sender_id"`
		Type      string          `json:"type"`
		SubjectID string          `json:"subject_id"`
		Content   json.RawMessage `json:"content"`
		Metadata  string          `json:"metadata"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}
	msg, err := t.store.DepositMessage(ctx, &messagestore.Message{
		SessionID: p.SessionID,
		Role:      p.Role,
		SenderID:  p.SenderID,
		Type:      p.Type,
		SubjectID: p.SubjectID,
		Content:   p.Content,
		Metadata:  p.Metadata,
	})
	if err != nil {
		return mapDepositError(err)
	}
	return mcp.OK(msg)
}

// mapDepositError translates a DepositMessage error into the
// appropriate MCP code. Validation-class errors get
// CodeSchemaViolation / CodeValidationFailed so a self-correcting
// LLM client sees the same code surface it expects from tool-input
// validation; not-found gets CodeNotFound; everything else falls
// through to InternalError.
func mapDepositError(err error) *mcp.Response {
	switch {
	case errors.Is(err, messagestore.ErrSchemaViolation),
		errors.Is(err, messagestore.ErrSemanticViolation):
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	case errors.Is(err, messagestore.ErrUnknownRole),
		errors.Is(err, messagestore.ErrUnknownType):
		return mcp.Err(mcp.CodeValidationFailed, err.Error(), nil)
	case errors.Is(err, messagestore.ErrSessionNotFound):
		return mcp.Err(mcp.CodeNotFound, err.Error(), nil)
	default:
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}
}

// --- list_sessions ---

type listSessionsTool struct{ store *messagestore.Store }

func (listSessionsTool) Name() string { return "list_sessions" }
func (listSessionsTool) Description() string {
	return "List all sessions, most recent first."
}
func (listSessionsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)
}
func (t *listSessionsTool) Handle(ctx context.Context, _ json.RawMessage) *mcp.Response {
	sessions, err := t.store.ListSessions(ctx)
	if err != nil {
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}
	if sessions == nil {
		sessions = []messagestore.Session{}
	}
	return mcp.OK(sessions)
}

// --- get_session ---

type getSessionTool struct{ store *messagestore.Store }

func (getSessionTool) Name() string { return "get_session" }
func (getSessionTool) Description() string {
	return "Retrieve a single session by its id."
}
func (getSessionTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string"}
		},
		"required": ["id"],
		"additionalProperties": false
	}`)
}
func (t *getSessionTool) Handle(ctx context.Context, input json.RawMessage) *mcp.Response {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}
	sess, err := t.store.GetSession(ctx, p.ID)
	if err != nil {
		if errors.Is(err, messagestore.ErrSessionNotFound) {
			return mcp.Err(mcp.CodeNotFound, err.Error(), nil)
		}
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}
	return mcp.OK(sess)
}

// --- get_messages ---

type getMessagesTool struct{ store *messagestore.Store }

func (getMessagesTool) Name() string { return "get_messages" }
func (getMessagesTool) Description() string {
	return "List messages in a session, oldest first, with optional role and type filters."
}
func (getMessagesTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"session_id": {"type": "string"},
			"role":       {"type": "string"},
			"sender_id":  {"type": "string"},
			"type":       {"type": "string"},
			"subject_id": {"type": "string"}
		},
		"required": ["session_id"],
		"additionalProperties": false
	}`)
}
func (t *getMessagesTool) Handle(ctx context.Context, input json.RawMessage) *mcp.Response {
	var p struct {
		SessionID string `json:"session_id"`
		Role      string `json:"role"`
		SenderID  string `json:"sender_id"`
		Type      string `json:"type"`
		SubjectID string `json:"subject_id"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}
	msgs, err := t.store.GetMessages(ctx, messagestore.MessageFilter{
		SessionID: p.SessionID,
		Role:      p.Role,
		SenderID:  p.SenderID,
		Type:      p.Type,
		SubjectID: p.SubjectID,
	})
	if err != nil {
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}
	if msgs == nil {
		msgs = []messagestore.Message{}
	}
	return mcp.OK(msgs)
}

// --- get_latest_message ---

type getLatestMessageTool struct{ store *messagestore.Store }

func (getLatestMessageTool) Name() string { return "get_latest_message" }
func (getLatestMessageTool) Description() string {
	return "Retrieve the most recent message in a session, with optional role and type filters."
}
func (getLatestMessageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"session_id": {"type": "string"},
			"role":       {"type": "string"},
			"sender_id":  {"type": "string"},
			"type":       {"type": "string"},
			"subject_id": {"type": "string"}
		},
		"required": ["session_id"],
		"additionalProperties": false
	}`)
}
func (t *getLatestMessageTool) Handle(ctx context.Context, input json.RawMessage) *mcp.Response {
	var p struct {
		SessionID string `json:"session_id"`
		Role      string `json:"role"`
		SenderID  string `json:"sender_id"`
		Type      string `json:"type"`
		SubjectID string `json:"subject_id"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return mcp.Err(mcp.CodeSchemaViolation, err.Error(), nil)
	}
	msg, err := t.store.GetLatestMessage(ctx, messagestore.MessageFilter{
		SessionID: p.SessionID,
		Role:      p.Role,
		SenderID:  p.SenderID,
		Type:      p.Type,
		SubjectID: p.SubjectID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return mcp.Err(mcp.CodeNotFound, "no matching message", nil)
		}
		return mcp.Err(mcp.CodeInternalError, err.Error(), nil)
	}
	return mcp.OK(msg)
}

// --- helpers ---------------------------------------------------------------

// loadSchemasDir mirrors toolbox-bridge's loader. Filename stem = type
// name; contents = JSON Schema. Strict-reject schemas only.
func loadSchemasDir(store *messagestore.Store, dir string) (int, error) {
	resolved, err := expandHome(dir)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", resolved, err)
	}

	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		typeName := strings.TrimSuffix(name, ".json")
		path := filepath.Join(resolved, name)
		raw, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied schemas dir
		if err != nil {
			return n, fmt.Errorf("read %s: %w", path, err)
		}
		sch, err := schema.Parse(json.RawMessage(raw))
		if err != nil {
			return n, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := store.RegisterType(messagestore.MessageType{
			Name:   typeName,
			Schema: sch,
		}); err != nil {
			return n, fmt.Errorf("register %s: %w", typeName, err)
		}
		n++
	}
	return n, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if p == "~" {
		return home, nil
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// silence unused-import linter when log isn't being redirected.
var _ = io.Discard
