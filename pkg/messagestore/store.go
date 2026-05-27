package messagestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // SQL driver registration

	"github.com/sarahmaeve/toolbox/pkg/schema"
)

// Sentinel errors. Use errors.Is to branch on these.
var (
	// ErrSessionNotFound is returned by DepositMessage when the
	// referenced SessionID does not exist. The FK constraint catches
	// the miss at INSERT time; this method translates it to a domain
	// sentinel so callers don't sniff driver error text.
	ErrSessionNotFound = errors.New("session not found")

	// ErrUnknownType is returned by DepositMessage when Message.Type
	// does not match any RegisterType'd MessageType.
	ErrUnknownType = errors.New("unknown message type")

	// ErrUnknownRole is returned by DepositMessage when Message.Role
	// is not in the configured Config.AllowedRoles set. Not returned
	// if AllowedRoles is empty (the "accept any role" mode).
	ErrUnknownRole = errors.New("unknown role")

	// ErrSchemaViolation is returned when Message.Content fails the
	// MessageType's schema validation. Wraps the schema.Violation.
	ErrSchemaViolation = errors.New("schema violation")

	// ErrSemanticViolation is returned when MessageType.OnIngest
	// rejects Message.Content. Wraps the hook's error.
	ErrSemanticViolation = errors.New("semantic validation failed")

	// ErrTypeAlreadyRegistered is returned by RegisterType when the
	// Name collides with an existing registration.
	ErrTypeAlreadyRegistered = errors.New("message type already registered")

	// ErrFilterRequired is returned by GetMessages / GetLatestMessage
	// when the supplied MessageFilter has no dimension set. The store
	// refuses to scan all messages unconditionally — callers must
	// declare what they're looking for (SessionID for in-session
	// lookups; SubjectID / SenderID / Role / Type for cross-session
	// search).
	ErrFilterRequired = errors.New("MessageFilter requires at least one of SessionID, Role, SenderID, Type, SubjectID")

	// ErrSchemaNotStrict is returned by RegisterType when the type's
	// Schema does not have additionalProperties:false. The store's
	// posture is that unknown fields are an error; permissive schemas
	// are refused at registration.
	ErrSchemaNotStrict = errors.New("schema must set additionalProperties:false")
)

// Config configures a Store at Open.
type Config struct {
	// DBPath is the path to the SQLite database. Created if missing.
	// Required.
	DBPath string

	// AllowedRoles is the controlled vocabulary for Message.Role.
	// Empty means "accept any role" (the store does no role check).
	AllowedRoles []string

	// MaxActiveSessions caps concurrent sessions in "active" status.
	// Zero means unlimited. 100 is a reasonable default for
	// agent-driven workflows.
	MaxActiveSessions int
}

// Store is the typed-message store. Construct via Open.
type Store struct {
	db                *sql.DB
	allowedRoles      map[string]struct{}
	maxActiveSessions int

	typesMu sync.RWMutex
	types   map[string]*MessageType
}

// Open creates or opens a SQLite-backed Store at cfg.DBPath.
// Configures SQLite for safe concurrent use (single connection, WAL,
// busy timeout, FKs on), runs migrations, restricts the DB file to
// 0600 permissions.
//
// ctx is threaded through every SQL operation so cancellation aborts
// in-progress migrations cleanly.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.DBPath == "" {
		return nil, errors.New("messagestore: Config.DBPath is required")
	}

	dir := filepath.Dir(cfg.DBPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	// Atomically create the database file at 0600 BEFORE sql.Open, so
	// there is no window during which the file is world-readable.
	if f, err := os.OpenFile(cfg.DBPath, os.O_CREATE|os.O_RDWR, 0o600); err != nil { //nolint:gosec // G304: caller-supplied DB path
		return nil, fmt.Errorf("create database file: %w", err)
	} else if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close pre-created database file: %w", err)
	}

	// Encode pragmas in the DSN so the modernc.org/sqlite driver applies
	// them on every connection the pool opens — not just the first one.
	// foreign_keys and busy_timeout are connection-local; an explicit
	// PRAGMA issued after sql.Open() applies only to the original conn
	// and is silently lost if the pool replaces it (e.g., on a transient
	// error). journal_mode=WAL is persisted at the database-file level,
	// so encoding it here is belt-and-braces but harmless.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(wal)",
		cfg.DBPath,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := os.Chmod(cfg.DBPath, 0o600); err != nil {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("set database permissions: %w", err)
	}

	// SQLite allows one writer at a time; database/sql's pool would
	// otherwise open multiple connections that serialize awkwardly at
	// the file lock. A single connection avoids the contention.
	db.SetMaxOpenConns(1)

	// Verify WAL was honored — the DSN pragma is fire-and-forget, so we
	// query it back to fail loudly if the platform/build doesn't support it.
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("verify journal_mode: %w", err)
	}
	if journalMode != "wal" {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("WAL mode not honored (got %q); messagestore requires WAL for safe concurrent access", journalMode)
	}

	if err := migrate(ctx, db, cfg.DBPath); err != nil {
		_ = db.Close() //nolint:errcheck
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	roles := make(map[string]struct{}, len(cfg.AllowedRoles))
	for _, r := range cfg.AllowedRoles {
		roles[r] = struct{}{}
	}

	return &Store{
		db:                db,
		allowedRoles:      roles,
		maxActiveSessions: cfg.MaxActiveSessions,
		types:             make(map[string]*MessageType),
	}, nil
}

// Close releases the database connection.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying *sql.DB for tests that need to inspect raw
// state. Not for production use.
func (s *Store) DB() *sql.DB { return s.db }

// RegisterType registers a MessageType. Returns ErrTypeAlreadyRegistered
// if Name collides, ErrSchemaNotStrict if the schema isn't strict-
// reject. Safe to call concurrently with deposits, but typically all
// registrations happen at startup before any traffic.
func (s *Store) RegisterType(t MessageType) error {
	if t.Name == "" {
		return errors.New("MessageType.Name is required")
	}
	if t.Schema == nil {
		return errors.New("MessageType.Schema is required")
	}
	if !t.Schema.StrictReject() {
		return fmt.Errorf("%w: type %q", ErrSchemaNotStrict, t.Name)
	}

	s.typesMu.Lock()
	defer s.typesMu.Unlock()
	if _, exists := s.types[t.Name]; exists {
		return fmt.Errorf("%w: %q", ErrTypeAlreadyRegistered, t.Name)
	}
	tt := t
	s.types[t.Name] = &tt
	return nil
}

// RegisteredTypes returns the sorted names of every registered
// MessageType.
func (s *Store) RegisteredTypes() []string {
	s.typesMu.RLock()
	defer s.typesMu.RUnlock()
	names := make([]string, 0, len(s.types))
	for n := range s.types {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// LookupType returns the registered MessageType with the given name,
// or nil if not registered.
func (s *Store) LookupType(name string) *MessageType {
	s.typesMu.RLock()
	defer s.typesMu.RUnlock()
	return s.types[name]
}

// --- Sessions ---

// CountActiveSessions returns the count of sessions in "active" status.
func (s *Store) CountActiveSessions(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE status = 'active'`,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active sessions: %w", err)
	}
	return count, nil
}

// CreateSession starts a new session for the given target. Returns an
// error if MaxActiveSessions is configured and would be exceeded.
func (s *Store) CreateSession(ctx context.Context, target, metadata string) (*Session, error) {
	if s.maxActiveSessions > 0 {
		count, err := s.CountActiveSessions(ctx)
		if err != nil {
			return nil, err
		}
		if count >= s.maxActiveSessions {
			return nil, fmt.Errorf("active session limit reached (%d); delete old sessions first", s.maxActiveSessions)
		}
	}

	sess := &Session{
		ID:        uuid.New().String(),
		Target:    target,
		Status:    "active",
		CreatedAt: time.Now().UTC(),
		Metadata:  metadata,
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, target, status, created_at, metadata)
		 VALUES (?, ?, ?, ?, ?)`,
		sess.ID, sess.Target, sess.Status,
		sess.CreatedAt.Format(time.RFC3339Nano), nullableString(sess.Metadata),
	)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}

// GetSession retrieves a session by ID. Returns ErrSessionNotFound if
// the session does not exist.
func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, target, status, created_at, metadata
		 FROM sessions WHERE id = ?`, id,
	)
	sess, err := scanSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
		}
		return nil, err
	}
	return sess, nil
}

// UpdateSessionStatus sets the status of a session.
func (s *Store) UpdateSessionStatus(ctx context.Context, id, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET status = ? WHERE id = ?`,
		status, id,
	)
	if err != nil {
		return fmt.Errorf("update session status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return nil
}

// ListSessions returns all sessions, most recent first.
func (s *Store) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, target, status, created_at, metadata
		 FROM sessions ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var sessions []Session
	for rows.Next() {
		var sess Session
		var createdStr string
		var metadata sql.NullString
		if err := rows.Scan(&sess.ID, &sess.Target, &sess.Status, &createdStr, &metadata); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sess.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr)
		if err != nil {
			return nil, fmt.Errorf("list sessions: parse created_at %q: %w", createdStr, err)
		}
		if metadata.Valid {
			sess.Metadata = metadata.String
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// DeleteSession removes a session and all its messages.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id = ?`, id); err != nil {
		return fmt.Errorf("delete messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return tx.Commit()
}

// --- Messages ---

// DepositMessage validates and persists a message. The validation
// pipeline:
//
//  1. Role check: if Config.AllowedRoles is non-empty, msg.Role must
//     be in the set.
//  2. Type lookup: msg.Type must name a registered MessageType.
//  3. Schema validation: msg.Content is checked against the type's
//     schema via pkg/schema.
//  4. Semantic validation: if the type has an OnIngest hook, it runs.
//  5. FK check at INSERT: SessionID must exist; otherwise
//     ErrSessionNotFound.
//
// Returns the persisted Message with ID and CreatedAt populated.
func (s *Store) DepositMessage(ctx context.Context, msg *Message) (*Message, error) {
	if msg == nil {
		return nil, errors.New("nil message")
	}

	if len(s.allowedRoles) > 0 {
		if _, ok := s.allowedRoles[msg.Role]; !ok {
			return nil, fmt.Errorf("%w: %q; allowed roles: [%s]",
				ErrUnknownRole, msg.Role, joinSortedSet(s.allowedRoles))
		}
	}

	mt := s.LookupType(msg.Type)
	if mt == nil {
		return nil, fmt.Errorf("%w: %q; registered types: [%s]",
			ErrUnknownType, msg.Type, strings.Join(s.RegisteredTypes(), ", "))
	}

	if v := mt.Schema.Validate(msg.Type, msg.Content); v != nil {
		return nil, fmt.Errorf("%w: %s", ErrSchemaViolation, v.Message)
	}

	if mt.OnIngest != nil {
		if err := mt.OnIngest(msg.Content); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrSemanticViolation, err)
		}
	}

	msg.CreatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (session_id, role, sender_id, type, subject_id, content, created_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.SessionID, msg.Role, nullableString(msg.SenderID),
		msg.Type, nullableString(msg.SubjectID),
		string(msg.Content),
		msg.CreatedAt.Format(time.RFC3339Nano), nullableString(msg.Metadata),
	)
	if err != nil {
		if isForeignKeyFailure(err) {
			return nil, fmt.Errorf("%w: %q", ErrSessionNotFound, msg.SessionID)
		}
		return nil, fmt.Errorf("deposit message: %w", err)
	}
	id, _ := res.LastInsertId()
	msg.ID = id
	return msg, nil
}

// isForeignKeyFailure reports whether err is SQLite's "FOREIGN KEY
// constraint failed" error. String-matching this particular message is
// stable across SQLite versions and every Go SQLite driver carries it
// verbatim from the SQLite library's sqlite3ErrStr() table.
func isForeignKeyFailure(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// GetMessages retrieves messages matching the filter, oldest first.
// Returns ErrFilterRequired when no filter dimension is set.
func (s *Store) GetMessages(ctx context.Context, f MessageFilter) ([]Message, error) {
	query, args, err := buildMessageQuery(f, "ASC", false)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var msgs []Message
	for rows.Next() {
		msg, err := scanMessageRow(rows)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, msg)
	}
	return msgs, rows.Err()
}

// GetLatestMessage retrieves the most recent message matching the
// filter. Returns sql.ErrNoRows (wrapped) when no message matches;
// ErrFilterRequired when the filter has no dimension set.
func (s *Store) GetLatestMessage(ctx context.Context, f MessageFilter) (*Message, error) {
	query, args, err := buildMessageQuery(f, "DESC", true)
	if err != nil {
		return nil, err
	}

	row := s.db.QueryRowContext(ctx, query, args...)
	msg, err := scanMessageRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("get latest message: %w", sql.ErrNoRows)
		}
		return nil, err
	}
	return &msg, nil
}

// LatestPerSubject returns one Message per distinct SubjectID — the
// most recent for each, by created_at then id (deterministic on
// ties). Rows without a SubjectID (NULL or empty) are excluded; the
// purpose of this method is "give me the current state of every
// topic," and a row that doesn't declare a topic doesn't participate.
//
// The filter follows the same rules as GetMessages: at least one
// dimension must be set, returning ErrFilterRequired otherwise.
// Limit caps the number of subjects returned (0 = unlimited).
//
// Typical use: "what's the current state of every task?" via
// LatestPerSubject(MessageFilter{Type: "task"}).
func (s *Store) LatestPerSubject(ctx context.Context, f MessageFilter) ([]Message, error) {
	if f.SessionID == "" && f.Role == "" && f.SenderID == "" &&
		f.Type == "" && f.SubjectID == "" {
		return nil, ErrFilterRequired
	}

	const cols = `id, session_id, role, sender_id, type, subject_id, content, created_at, metadata`
	// Window-function approach: rank rows by (created_at, id) DESC
	// within each subject_id partition, then keep rank 1. SQLite 3.25+
	// supports this; modernc.org/sqlite ships a newer version.
	//
	// The inner WHERE applies the filter dimensions to constrain the
	// universe of rows the partition runs over. The outer WHERE picks
	// the latest from each partition.
	query := `SELECT ` + cols + ` FROM (
	    SELECT ` + cols + `,
	      ROW_NUMBER() OVER (
	        PARTITION BY subject_id
	        ORDER BY created_at DESC, id DESC
	      ) AS rn
	    FROM messages
	    WHERE subject_id IS NOT NULL AND subject_id != ''`
	var args []any

	if f.SessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, f.SessionID)
	}
	if f.Role != "" {
		query += ` AND role = ?`
		args = append(args, f.Role)
	}
	if f.SenderID != "" {
		query += ` AND sender_id = ?`
		args = append(args, f.SenderID)
	}
	if f.Type != "" {
		query += ` AND type = ?`
		args = append(args, f.Type)
	}
	if f.SubjectID != "" {
		query += ` AND subject_id = ?`
		args = append(args, f.SubjectID)
	}

	query += `
	) WHERE rn = 1
	ORDER BY created_at DESC, id DESC`

	if f.Limit > 0 {
		query += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("latest per subject: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []Message
	for rows.Next() {
		msg, err := scanMessageRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

// buildMessageQuery assembles a SELECT for the messages table from a
// MessageFilter. order is "ASC" or "DESC" applied to (created_at, id);
// limitOne caps the query at one row (overrides f.Limit).
//
// Returns ErrFilterRequired when no filter dimension is set — the
// store refuses to scan all messages unconditionally.
//
// SessionID is optional; when empty, the query runs across all
// sessions (the cross-session-search mode for SubjectID / SenderID
// lookups).
func buildMessageQuery(f MessageFilter, order string, limitOne bool) (string, []any, error) {
	if f.SessionID == "" && f.Role == "" && f.SenderID == "" &&
		f.Type == "" && f.SubjectID == "" {
		return "", nil, ErrFilterRequired
	}

	const cols = `id, session_id, role, sender_id, type, subject_id, content, created_at, metadata`
	// `WHERE 1=1` is a sentinel that lets every filter branch append
	// uniformly with `AND`. SQLite's optimizer drops the constant.
	query := `SELECT ` + cols + ` FROM messages WHERE 1=1`
	var args []any

	if f.SessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, f.SessionID)
	}
	if f.Role != "" {
		query += ` AND role = ?`
		args = append(args, f.Role)
	}
	if f.SenderID != "" {
		query += ` AND sender_id = ?`
		args = append(args, f.SenderID)
	}
	if f.Type != "" {
		query += ` AND type = ?`
		args = append(args, f.Type)
	}
	if f.SubjectID != "" {
		query += ` AND subject_id = ?`
		args = append(args, f.SubjectID)
	}

	query += ` ORDER BY created_at ` + order + `, id ` + order

	switch {
	case limitOne:
		query += ` LIMIT 1`
	case f.Limit > 0:
		// f.Limit is int (not user-string) so embedding via Sprintf
		// is safe from SQL injection. Using a `?` placeholder for
		// LIMIT works on most drivers but not all — keeping this
		// path driver-agnostic.
		query += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}

	return query, args, nil
}

// --- scan helpers ---

func scanSession(row *sql.Row) (*Session, error) {
	var sess Session
	var createdStr string
	var metadata sql.NullString
	if err := row.Scan(&sess.ID, &sess.Target, &sess.Status, &createdStr, &metadata); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}
	var err error
	sess.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr)
	if err != nil {
		return nil, fmt.Errorf("scan session: parse created_at %q: %w", createdStr, err)
	}
	if metadata.Valid {
		sess.Metadata = metadata.String
	}
	return &sess, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanMessageRow(row scannable) (Message, error) {
	var msg Message
	var createdStr string
	var content string
	var senderID, subjectID, metadata sql.NullString
	if err := row.Scan(&msg.ID, &msg.SessionID, &msg.Role, &senderID,
		&msg.Type, &subjectID, &content, &createdStr, &metadata); err != nil {
		return msg, fmt.Errorf("scan message: %w", err)
	}
	msg.Content = json.RawMessage(content)
	var err error
	msg.CreatedAt, err = time.Parse(time.RFC3339Nano, createdStr)
	if err != nil {
		return msg, fmt.Errorf("scan message: parse created_at %q: %w", createdStr, err)
	}
	if senderID.Valid {
		msg.SenderID = senderID.String
	}
	if subjectID.Valid {
		msg.SubjectID = subjectID.String
	}
	if metadata.Valid {
		msg.Metadata = metadata.String
	}
	return msg, nil
}

// nullableString returns the string as an `any` suitable for sql
// parameter binding: empty becomes SQL NULL, non-empty is passed
// verbatim.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// joinSortedSet renders a set of strings as a sorted, comma-separated
// list — used in self-documenting error messages ("allowed roles:
// [agent, orchestrator, user]") so an LLM client learns the
// vocabulary from the rejection.
func joinSortedSet(set map[string]struct{}) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return strings.Join(keys, ", ")
}

// Compile-time check that schema.Violation is in scope (referenced via
// the validation pipeline above through schema.Schema.Validate).
var _ = (*schema.Violation)(nil)
