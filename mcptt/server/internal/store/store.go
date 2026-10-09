// Package store implements the SQLite persistence layer: schema, users,
// groups, memberships, affiliations, and the audit log.
//
// Types are Postgres-ready: every timestamp column holds RFC3339 UTC text
// (maps to TIMESTAMPTZ), ids are UUID text, the audit id is a rowid alias
// (maps to BIGINT GENERATED ALWAYS AS IDENTITY). Nothing in the queries uses
// SQLite-specific functions, so the Postgres move is a schema translation,
// not a code rewrite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo, race-safe)
)

// Sentinel errors the API layer maps to HTTP statuses.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// Store wraps the SQLite handle. Safe for concurrent use: the connection pool
// is capped at one connection so SQLite's single-writer model never surfaces
// SQLITE_BUSY races — the busy timeout then only covers lock waits on the
// same connection's own transactions.
type Store struct {
	db *sql.DB
}

func Open(path string, busyTimeout time.Duration) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)",
		path, busyTimeout.Milliseconds())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// Single connection: correctness before concurrency for the foundation.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate applies the v1 schema idempotently.
func (s *Store) migrate(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS users (
	id               TEXT PRIMARY KEY,
	username         TEXT NOT NULL UNIQUE,
	password_hash    TEXT NOT NULL,
	display_name     TEXT NOT NULL,
	role             TEXT NOT NULL CHECK (role IN ('dispatcher','supervisor','field')),
	priority         INTEGER NOT NULL CHECK (priority BETWEEN 1 AND 10),
	functional_alias TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL,  -- Postgres: TIMESTAMPTZ
	updated_at       TEXT NOT NULL   -- Postgres: TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS groups (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	created_by  TEXT NOT NULL REFERENCES users(id),
	created_at  TEXT NOT NULL,      -- Postgres: TIMESTAMPTZ
	updated_at  TEXT NOT NULL       -- Postgres: TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS memberships (
	group_id   TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,       -- Postgres: TIMESTAMPTZ
	PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS affiliations (
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	group_id   TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
	state      TEXT NOT NULL CHECK (state IN ('affiliated','deaffiliated')),
	changed_at TEXT NOT NULL,       -- Postgres: TIMESTAMPTZ
	PRIMARY KEY (user_id, group_id)
);

CREATE TABLE IF NOT EXISTS audit_log (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,  -- Postgres: BIGINT GENERATED ALWAYS AS IDENTITY
	actor_id    TEXT REFERENCES users(id),          -- NULL for system actions (seeding)
	action      TEXT NOT NULL,
	target_type TEXT NOT NULL DEFAULT '',
	target_id   TEXT NOT NULL DEFAULT '',
	detail      TEXT NOT NULL DEFAULT '{}',         -- JSON object
	created_at  TEXT NOT NULL                       -- Postgres: TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS emergency_alerts (
	id               TEXT PRIMARY KEY,
	user_id          TEXT NOT NULL REFERENCES users(id),
	call_id          TEXT NOT NULL DEFAULT '',  -- empty: voiceless alert
	kind             TEXT NOT NULL CHECK (kind IN ('emergency','imminent_peril')),
	lat              REAL,                      -- NULL: no client-reported fix
	lon              REAL,                      -- NULL: no client-reported fix
	note             TEXT NOT NULL DEFAULT '',
	status           TEXT NOT NULL CHECK (status IN ('active','acknowledged')),
	acknowledged_by  TEXT REFERENCES users(id),
	acknowledged_at  TEXT,                      -- Postgres: TIMESTAMPTZ
	created_at       TEXT NOT NULL              -- Postgres: TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_memberships_user   ON memberships(user_id);
CREATE INDEX IF NOT EXISTS idx_affiliations_user  ON affiliations(user_id);
CREATE INDEX IF NOT EXISTS idx_affiliations_group ON affiliations(group_id);
CREATE INDEX IF NOT EXISTS idx_audit_created      ON audit_log(created_at);
CREATE INDEX IF NOT EXISTS idx_alerts_status      ON emergency_alerts(status);
CREATE INDEX IF NOT EXISTS idx_alerts_user        ON emergency_alerts(user_id);
`
	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// mapErr normalizes driver constraint failures into sentinel errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	// modernc.org/sqlite surfaces UNIQUE/CHECK/FK violations with these
	// markers in the error text (SQLite result codes 2067 / 787 / 275).
	msg := err.Error()
	for _, marker := range []string{"UNIQUE constraint failed", "FOREIGN key constraint failed", "CHECK constraint failed"} {
		if strings.Contains(msg, marker) {
			return ErrConflict
		}
	}
	return err
}
