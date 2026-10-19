package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ProfileVersion is one saved version of profile.md.
type ProfileVersion struct {
	Version   int64
	Markdown  string
	Summary   string // what changed, in a line
	CreatedAt time.Time
	CreatedBy Actor
}

// Profile returns the current profile, and false when none was saved yet.
func (s *Store) Profile(ctx context.Context) (ProfileVersion, bool, error) {
	vs, err := s.ProfileHistory(ctx, 1)
	if err != nil || len(vs) == 0 {
		return ProfileVersion{}, false, err
	}
	return vs[0], true, nil
}

// ProfileHistory returns up to limit versions, newest first.
func (s *Store) ProfileHistory(ctx context.Context, limit int) ([]ProfileVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT version, markdown, change_summary, created_at, created_by
		FROM profile_versions ORDER BY version DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProfileVersion
	for rows.Next() {
		var (
			v       ProfileVersion
			created sql.NullString
			by      string
		)
		if err := rows.Scan(&v.Version, &v.Markdown, &v.Summary, &created, &by); err != nil {
			return nil, err
		}
		if v.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		v.CreatedBy = Actor(by)
		out = append(out, v)
	}
	return out, rows.Err()
}

// SaveProfile stores a new version of the profile and returns its number.
func (s *Store) SaveProfile(ctx context.Context, by Actor, markdown, summary string) (int64, error) {
	var version int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO profile_versions (markdown, change_summary, created_at, created_by) VALUES (?, ?, ?, ?)`,
			markdown, summary, now, by)
		if err != nil {
			return err
		}
		if version, err = res.LastInsertId(); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "profile_saved", eventRefs{}, map[string]any{"version": version})
	})
	return version, err
}

// ResumeVersion is one saved version of the resume text.
type ResumeVersion struct {
	Version   int64
	Text      string
	Source    string // pasted | file
	CreatedAt time.Time
	CreatedBy Actor
}

// Resume returns the current resume text, and false when none was saved.
func (s *Store) Resume(ctx context.Context) (ResumeVersion, bool, error) {
	var (
		v       ResumeVersion
		created sql.NullString
		by      string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT version, text, source, created_at, created_by FROM resume_versions ORDER BY version DESC LIMIT 1`).
		Scan(&v.Version, &v.Text, &v.Source, &created, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return ResumeVersion{}, false, nil
	}
	if err != nil {
		return ResumeVersion{}, false, err
	}
	if v.CreatedAt, err = parseTime(created); err != nil {
		return ResumeVersion{}, false, err
	}
	v.CreatedBy = Actor(by)
	return v, true, nil
}

// SaveResume stores a new version of the resume text.
func (s *Store) SaveResume(ctx context.Context, by Actor, text, source string) (int64, error) {
	if source != "pasted" && source != "file" {
		return 0, fmt.Errorf("resume source %q must be pasted or file", source)
	}
	var version int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO resume_versions (text, source, created_at, created_by) VALUES (?, ?, ?, ?)`, text, source, now, by)
		if err != nil {
			return err
		}
		if version, err = res.LastInsertId(); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "resume_saved", eventRefs{}, map[string]any{"version": version, "source": source})
	})
	return version, err
}
