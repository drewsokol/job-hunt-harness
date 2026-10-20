package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Kinds of dashboard token.
const (
	TokenLogin   = "login"
	TokenSession = "session"
)

// AddUIToken stores the hash of a dashboard token that is good for ttl,
// and drops expired ones.
func (s *Store) AddUIToken(ctx context.Context, kind, hash string, ttl time.Duration) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		now := s.now()
		if _, err := tx.ExecContext(ctx, "DELETE FROM ui_tokens WHERE expires_at < ?", utc.Format(now)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO ui_tokens (hash, kind, created_at, expires_at) VALUES (?, ?, ?, ?)",
			hash, kind, utc.Format(now), utc.Format(now.Add(ttl)))
		return err
	})
}

// UseLoginToken reports whether hash is an unexpired login token, and
// removes it: each one works once.
func (s *Store) UseLoginToken(ctx context.Context, hash string) (bool, error) {
	var ok bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM ui_tokens WHERE hash = ? AND kind = ? AND expires_at >= ?",
			hash, TokenLogin, s.stamp())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		ok = n == 1
		return err
	})
	return ok, err
}

// ValidSession reports whether hash is an unexpired session.
func (s *Store) ValidSession(ctx context.Context, hash string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM ui_tokens WHERE hash = ? AND kind = ? AND expires_at >= ?",
		hash, TokenSession, s.stamp()).Scan(&n)
	return n == 1, err
}

// Watch calls changed whenever another connection commits to the
// database, including other jhh processes, checking every interval until
// ctx ends. It holds one connection of its own for SQLite's data_version,
// which only counts other connections' commits.
func (s *Store) Watch(ctx context.Context, every time.Duration, changed func()) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	version := func() (int64, error) {
		var v int64
		err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&v)
		return v, err
	}
	last, err := version()
	if err != nil {
		return fmt.Errorf("reading data_version: %w", err)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		v, err := version()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("reading data_version: %w", err)
		}
		if v != last {
			last = v
			changed()
		}
	}
}
