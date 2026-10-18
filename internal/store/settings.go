package store

import (
	"context"
	"database/sql"
	"errors"
)

// Setting returns a setting's value, and whether it was set.
func (s *Store) Setting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetSetting stores a setting, replacing any previous value.
func (s *Store) SetSetting(ctx context.Context, by Actor, key, value string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO settings (key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			key, value, s.stamp(), by)
		return err
	})
}
