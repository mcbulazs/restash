// Package store keeps restash's records in SQLite: accounts, sessions,
// invites, PCs and backup results. It never holds anything that reveals a
// person's files; keys are stored only in locked form.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a record doesn't exist (or has expired).
var ErrNotFound = errors.New("not found")

// Store wraps the database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (and if needed creates or upgrades) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite writes one at a time anyway; avoids busy errors
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrations run in order; each runs once. Never edit a released one.
var migrations = []string{
	`CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE TABLE users (
		id                   TEXT PRIMARY KEY,
		email                TEXT NOT NULL UNIQUE COLLATE NOCASE,
		name                 TEXT NOT NULL,
		is_admin             INTEGER NOT NULL DEFAULT 0,
		kdf_salt             TEXT NOT NULL,
		kdf_iterations       INTEGER NOT NULL,
		auth_hash            TEXT NOT NULL,
		account_key          TEXT NOT NULL,
		recovery_auth_hash   TEXT NOT NULL,
		recovery_account_key TEXT NOT NULL,
		created_at           INTEGER NOT NULL
	);
	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE invites (
		token      TEXT PRIMARY KEY,
		email      TEXT NOT NULL,
		admin      INTEGER NOT NULL DEFAULT 0,
		created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE join_codes (
		code_hash  TEXT PRIMARY KEY,
		user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		pc_id      TEXT
	);
	CREATE TABLE pcs (
		id            TEXT PRIMARY KEY,
		user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name          TEXT NOT NULL,
		os            TEXT NOT NULL,
		arch          TEXT NOT NULL,
		agent_version TEXT NOT NULL DEFAULT '',
		token_hash    TEXT NOT NULL,
		pc_key        TEXT,
		created_at    INTEGER NOT NULL,
		last_seen     INTEGER
	);
	CREATE INDEX pcs_user ON pcs(user_id);
	CREATE TABLE backups (
		id          INTEGER PRIMARY KEY,
		pc_id       TEXT NOT NULL REFERENCES pcs(id) ON DELETE CASCADE,
		entry       TEXT NOT NULL,
		started_at  INTEGER NOT NULL,
		finished_at INTEGER NOT NULL,
		ok          INTEGER NOT NULL,
		files       INTEGER NOT NULL DEFAULT 0,
		bytes       INTEGER NOT NULL DEFAULT 0,
		added       INTEGER NOT NULL DEFAULT 0,
		snapshot    TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX backups_pc_time ON backups(pc_id, finished_at);`,

	`ALTER TABLE pcs ADD COLUMN timezone TEXT NOT NULL DEFAULT '';
	ALTER TABLE backups ADD COLUMN detail TEXT NOT NULL DEFAULT '';
	CREATE TABLE entries (
		id         TEXT PRIMARY KEY,
		pc_id      TEXT NOT NULL REFERENCES pcs(id) ON DELETE CASCADE,
		kind       TEXT NOT NULL,
		minute     INTEGER NOT NULL DEFAULT 0,
		weekday    INTEGER NOT NULL DEFAULT 0,
		config     TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX entries_pc ON entries(pc_id);`,

	`ALTER TABLE users ADD COLUMN ntfy_topic TEXT NOT NULL DEFAULT '';
	ALTER TABLE users ADD COLUMN notify_fail INTEGER NOT NULL DEFAULT 1;
	ALTER TABLE users ADD COLUMN notify_quiet INTEGER NOT NULL DEFAULT 1;
	ALTER TABLE users ADD COLUMN notify_login INTEGER NOT NULL DEFAULT 1;
	ALTER TABLE users ADD COLUMN notify_weekly INTEGER NOT NULL DEFAULT 0;
	CREATE TABLE events (
		id      INTEGER PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		pc_id   TEXT,
		kind    TEXT NOT NULL,
		at      INTEGER NOT NULL,
		info    TEXT NOT NULL DEFAULT '{}',
		secret  TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX events_user_at ON events(user_id, at);
	CREATE TABLE devices (
		user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		device     TEXT NOT NULL,
		first_seen INTEGER NOT NULL,
		last_seen  INTEGER NOT NULL,
		PRIMARY KEY (user_id, device)
	);
	CREATE TABLE alerts (
		key        TEXT PRIMARY KEY,
		user_id    TEXT NOT NULL,
		started_at INTEGER NOT NULL
	);`,

	// Storage limit per person, set by the admin; 0 means none.
	`ALTER TABLE users ADD COLUMN storage_limit INTEGER NOT NULL DEFAULT 0;`,
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Secret returns a random value stored under name, creating it on first use.
func (s *Store) Secret(ctx context.Context, name string) ([]byte, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		b := make([]byte, 32)
		rand.Read(b)
		v = hex.EncodeToString(b)
		if _, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO settings(key, value) VALUES(?, ?)", name, v); err != nil {
			return nil, err
		}
		return s.Secret(ctx, name)
	}
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(v)
}

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// newID makes a random lowercase id, safe to use as a folder name.
func newID() string {
	b := make([]byte, 10)
	rand.Read(b)
	return idEncoding.EncodeToString(b)
}

// NewToken makes a random secret for sessions, agents and invite links.
func NewToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "=")
}

// hashToken is how secrets are stored, so a leaked database can't be used to
// log in.
func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func unix(t time.Time) int64 { return t.Unix() }

func fromUnix(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(n.Int64, 0)
	return &t
}
