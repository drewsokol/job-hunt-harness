package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// SourceRow is what jhh stores about one source: whether someone turned it
// on or off, its settings, and its last run. A source with no row has its
// defaults.
type SourceRow struct {
	Name string
	// Enabled is nil until someone turns the source on or off.
	Enabled  *bool
	Settings json.RawMessage
	Reason   string // why it last changed, if given
	// LastRunAt is zero when the source never ran.
	LastRunAt    time.Time
	LastPostings int
	LastError    string // may quote the service's reply
	UpdatedAt    time.Time
	UpdatedBy    Actor
}

// SourceRows returns every stored source row by name.
func (s *Store) SourceRows(ctx context.Context) (map[string]SourceRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, enabled, settings, coalesce(reason, ''), last_run_at, coalesce(last_postings, 0), coalesce(last_error, ''),
		       updated_at, updated_by
		FROM sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SourceRow{}
	for rows.Next() {
		var (
			r              SourceRow
			enabled        sql.NullBool
			settings, by   string
			lastRun, updAt sql.NullString
		)
		if err := rows.Scan(&r.Name, &enabled, &settings, &r.Reason, &lastRun, &r.LastPostings, &r.LastError, &updAt, &by); err != nil {
			return nil, err
		}
		if enabled.Valid {
			r.Enabled = &enabled.Bool
		}
		r.Settings, r.UpdatedBy = json.RawMessage(settings), Actor(by)
		if r.LastRunAt, err = parseTime(lastRun); err != nil {
			return nil, err
		}
		if r.UpdatedAt, err = parseTime(updAt); err != nil {
			return nil, err
		}
		out[r.Name] = r
	}
	return out, rows.Err()
}

// SourceChange is a change to a source. A nil field is left as it is.
type SourceChange struct {
	Enabled  *bool
	Settings json.RawMessage
	Reason   string
}

// SetSource changes a source's switch or settings, and records the change
// on the timeline.
func (s *Store) SetSource(ctx context.Context, by Actor, name string, c SourceChange) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		var enabled sql.NullBool
		if c.Enabled != nil {
			enabled = sql.NullBool{Bool: *c.Enabled, Valid: true}
		}
		var settings sql.NullString
		if c.Settings != nil {
			settings = sql.NullString{String: string(c.Settings), Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sources (name, enabled, settings, reason, updated_at, updated_by)
			VALUES (?1, ?2, coalesce(?3, '{}'), nullif(?4, ''), ?5, ?6)
			ON CONFLICT (name) DO UPDATE SET
				enabled = CASE WHEN ?2 IS NULL THEN enabled ELSE ?2 END,
				settings = coalesce(?3, settings),
				reason = nullif(?4, ''), updated_at = ?5, updated_by = ?6`,
			name, enabled, settings, c.Reason, now, by); err != nil {
			return err
		}
		data := map[string]any{"source": name, "settings_changed": c.Settings != nil}
		if c.Enabled != nil {
			data["enabled"] = *c.Enabled
		}
		if c.Reason != "" {
			data["reason"] = c.Reason
		}
		return insertEvent(ctx, tx, now, by, "source_changed", eventRefs{}, data)
	})
}

// SourceRun is what one run of a search source found.
type SourceRun struct {
	At       time.Time
	Postings int
	Error    string // the first error, if any
}

// RecordSourceRun stores a search source's last run. It changes nothing
// anyone chose.
func (s *Store) RecordSourceRun(ctx context.Context, name string, run SourceRun) error {
	at, err := timeText(run.At)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sources (name, last_run_at, last_postings, last_error, updated_at, updated_by)
			VALUES (?1, ?2, ?3, nullif(?4, ''), ?5, ?6)
			ON CONFLICT (name) DO UPDATE SET last_run_at = ?2, last_postings = ?3, last_error = nullif(?4, '')`,
			name, at, run.Postings, run.Error, s.stamp(), ActorSystem)
		return err
	})
}

// BoardCount is how many companies use one board source.
type BoardCount struct {
	Companies int
	Enabled   int // whose board jhh reads
}

// BoardCounts counts the companies on each board source.
func (s *Store) BoardCounts(ctx context.Context) (map[string]BoardCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source, count(*), sum(enabled) FROM companies WHERE source != 'none' GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]BoardCount{}
	for rows.Next() {
		var (
			name string
			c    BoardCount
		)
		if err := rows.Scan(&name, &c.Companies, &c.Enabled); err != nil {
			return nil, err
		}
		out[name] = c
	}
	return out, rows.Err()
}
