package messagestore

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Migration represents a single schema migration with both forward
// (Up) and reverse (Down) SQL. Every migration must be reversible to
// protect against data corruption during upgrades.
type Migration struct {
	Version     int
	Description string
	Up          string
	Down        string
}

// migrations is the ordered list of all schema migrations. Add new
// migrations at the end with the next version number. NEVER modify an
// existing migration — always add a new one.
var migrations = []Migration{
	{
		Version:     1,
		Description: "initial schema: sessions + messages",
		Up:          initialSchema,
		Down:        dropInitialSchema,
	},
	{
		Version:     2,
		Description: "messages.sender_id + messages.subject_id: fine-grained producer ID and external subject reference",
		Up:          migrationV2Up,
		Down:        migrationV2Down,
	},
}

const initialSchema = `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    target     TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    metadata   TEXT
);

CREATE TABLE IF NOT EXISTS messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role       TEXT NOT NULL,
    type       TEXT NOT NULL,
    content    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    metadata   TEXT
);

CREATE INDEX IF NOT EXISTS idx_messages_session_role_type
    ON messages(session_id, role, type);
`

const dropInitialSchema = `
DROP INDEX IF EXISTS idx_messages_session_role_type;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS sessions;
`

// migrationV2Up adds the two finer-grained identifier columns:
//
//   - sender_id: the precise "who produced this" (signatory's
//     analyst_id analog — e.g. "agent.code-reviewer.v2"). Finer than
//     role; lets the same role host multiple discrete producers.
//   - subject_id: the external subject this message concerns (a
//     ticket, file path, URL, conversation topic). Independent of
//     session_id, which groups by run; subject_id groups by topic
//     across runs.
//
// Both nullable for back-compat with v1-shaped rows. A composite index
// on (session_id, sender_id) covers the common "who sent what in this
// session" filter; (subject_id) gets its own index because
// subject-keyed lookups typically span sessions.
const migrationV2Up = `
ALTER TABLE messages ADD COLUMN sender_id TEXT;
ALTER TABLE messages ADD COLUMN subject_id TEXT;
CREATE INDEX IF NOT EXISTS idx_messages_session_sender
    ON messages(session_id, sender_id);
CREATE INDEX IF NOT EXISTS idx_messages_subject
    ON messages(subject_id);
`

const migrationV2Down = `
DROP INDEX IF EXISTS idx_messages_subject;
DROP INDEX IF EXISTS idx_messages_session_sender;
ALTER TABLE messages DROP COLUMN subject_id;
ALTER TABLE messages DROP COLUMN sender_id;
`

// migrate brings the database up to the latest schema version. Each
// pending migration is applied transactionally with an automatic
// backup taken first; a database newer than this code supports is
// refused.
func migrate(ctx context.Context, db *sql.DB, dbPath string) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_version (
			version    INTEGER NOT NULL,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create schema_version table: %w", err)
	}

	currentVersion, err := getCurrentVersion(ctx, db)
	if err != nil {
		return err
	}

	latestVersion := len(migrations)

	if currentVersion > latestVersion {
		return fmt.Errorf(
			"database schema version %d is newer than this code supports (max %d); "+
				"upgrade the application or use the database with a newer version",
			currentVersion, latestVersion)
	}

	for i := currentVersion; i < latestVersion; i++ {
		m := migrations[i]

		if dbPath != "" {
			if err := backupDatabase(ctx, db, dbPath, i); err != nil {
				return fmt.Errorf("backup before migration %d: %w", m.Version, err)
			}
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.Version, err)
		}

		if _, err := tx.ExecContext(ctx, m.Up); err != nil {
			_ = tx.Rollback() //nolint:errcheck // already in error path
			return fmt.Errorf("migration %d (%s) failed: %w", m.Version, m.Description, err)
		}

		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_version (version, applied_at) VALUES (?, ?)",
			m.Version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			_ = tx.Rollback() //nolint:errcheck // already in error path
			return fmt.Errorf("record version %d: %w", m.Version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.Version, err)
		}
	}

	return nil
}

// getCurrentVersion returns the highest applied migration version, or
// 0 if none recorded.
func getCurrentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("get current version: %w", err)
	}
	return version, nil
}

// backupDatabase checkpoints the WAL and copies the database file to
// a timestamped backup with an unguessable random suffix. The random
// suffix serves three purposes:
//
//  1. O_EXCL atomic creation prevents clobbering an existing file.
//  2. Defeats symlink-race attacks on a predictable path.
//  3. Prevents within-second collisions when two backups fire in the
//     same second.
//
// os.CreateTemp opens with O_RDWR|O_CREATE|O_EXCL, mode 0600.
func backupDatabase(ctx context.Context, db *sql.DB, dbPath string, fromVersion int) error {
	if db != nil {
		if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return fmt.Errorf("checkpoint WAL before backup: %w", err)
		}
	}

	src, err := os.Open(dbPath) //nolint:gosec // G304: caller-supplied DB path; backing it up is this function's purpose
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer src.Close() //nolint:errcheck // read-only file

	dir := filepath.Dir(dbPath)
	pattern := fmt.Sprintf("%s.backup-v%d-%s-*",
		filepath.Base(dbPath), fromVersion,
		time.Now().UTC().Format("20060102-150405"))

	dst, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()           //nolint:errcheck
		_ = os.Remove(dst.Name()) //nolint:errcheck
		return fmt.Errorf("copy database to backup: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("close backup: %w", err)
	}
	return nil
}
