// Package store is jhh's SQLite database: opening it, migrating it, and the
// queries each part of jhh needs, grouped by area.
//
// Several jhh processes (one per agent session, plus the UI) share one
// database file. Every connection uses WAL mode and a busy timeout, and
// every write transaction starts with BEGIN IMMEDIATE, so a process waits
// for its turn to write instead of failing.
//
// All stored times are UTC (docs/decisions/0004-utc-everywhere.md). The store
// rejects any other time instead of converting it, because a local time
// reaching here is a bug in the caller.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/utc"
	"modernc.org/sqlite" // also registers the "sqlite" driver
)

// busyTimeout is how long a connection waits for another writer before
// failing with SQLITE_BUSY.
const busyTimeout = 5 * time.Second

// Options configures Open.
type Options struct {
	// BackupDir receives a copy of the database before migrations run on
	// existing data. Empty disables backups (tests only).
	BackupDir string
	// Now returns the current time. It defaults to utc.Now; tests replace it.
	Now func() time.Time
}

// Store is an open, migrated database.
type Store struct {
	db   *sql.DB
	path string
	now  func() time.Time
}

// Open opens (creating if needed) the database at path and brings its
// schema up to date.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	// journal_mode is deliberately not in the DSN: see enableWAL.
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_txlock=immediate",
		path, busyTimeout.Milliseconds())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	s := &Store{db: db, path: path, now: opts.Now}
	if s.now == nil {
		s.now = utc.Now
	}
	if err := enableWAL(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	ms, err := migrations()
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(ctx, db, ms, opts.BackupDir, s.now()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// sqliteBusy is SQLite's primary result code SQLITE_BUSY.
const sqliteBusy = 5

// enableWAL switches the database to WAL mode. The mode is stored in the
// file, so this only changes anything the first time. It is not a per-
// connection pragma because changing the journal mode needs an exclusive
// lock and ignores busy_timeout: when two processes open a new database at
// the same moment, one would fail at once with SQLITE_BUSY. Instead, retry
// until the other process finishes or busyTimeout passes.
func enableWAL(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(busyTimeout)
	for {
		var mode string
		err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode)
		if err == nil {
			if mode != "wal" {
				return fmt.Errorf("journal mode is %q, want wal", mode)
			}
			return nil
		}
		var se *sqlite.Error
		if !errors.As(err, &se) || se.Code()&0xff != sqliteBusy || time.Now().After(deadline) {
			return fmt.Errorf("enabling WAL: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Path returns the database file's path.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SchemaVersion returns the database's migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	return userVersion(ctx, s.db)
}

// Check runs SQLite's quick integrity check and returns nil if the database
// is healthy.
func (s *Store) Check(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
}

// write runs fn in a write transaction (BEGIN IMMEDIATE) and commits it if
// fn returns nil.
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ErrNotFound is returned when a requested row doesn't exist.
var ErrNotFound = errors.New("not found")

// ErrNotUTC is returned when a caller passes a time that isn't in UTC.
var ErrNotUTC = errors.New("time is not UTC")

// stamp formats the store's current time for a created_at or updated_at column.
func (s *Store) stamp() string { return utc.Format(s.now()) }

// timeText formats a caller-supplied time for storage. The zero time is
// stored as NULL.
func timeText(t time.Time) (sql.NullString, error) {
	if t.IsZero() {
		return sql.NullString{}, nil
	}
	if t.Location() != time.UTC {
		return sql.NullString{}, fmt.Errorf("%w: %s", ErrNotUTC, t)
	}
	return sql.NullString{String: utc.Format(t), Valid: true}, nil
}

// parseTime reads a stored timestamp. NULL becomes the zero time.
func parseTime(s sql.NullString) (time.Time, error) {
	if !s.Valid {
		return time.Time{}, nil
	}
	t, err := time.Parse(utc.Layout, s.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("stored time %q: %w", s.String, err)
	}
	return t, nil
}

// jsonText encodes v for a JSON column.
func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
