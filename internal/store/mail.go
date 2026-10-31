package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// HandledMessage is an email the agent already handled. jhh keeps the
// message's id and the agent's one-line note of what it did, never the
// message itself.
type HandledMessage struct {
	System, MessageID, Action string
	JobID, OutreachID         int64
	InterviewID               int64
	HandledAt                 time.Time
	HandledBy                 Actor
}

// NewHandled is a message to mark handled.
type NewHandled struct {
	MessageID, Action string
	JobID, OutreachID int64
}

// HandledMessages returns which of ids, in system, were already handled.
func (s *Store) HandledMessages(ctx context.Context, system string, ids []string) ([]HandledMessage, error) {
	return handledMessages(ctx, s.db, system, ids)
}

func handledMessages(ctx context.Context, q rowsQuerier, system string, ids []string) ([]HandledMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{system}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `
		SELECT system, message_id, action, coalesce(job_id, 0), coalesce(outreach_id, 0), coalesce(interview_id, 0), handled_at, handled_by
		FROM handled_messages WHERE system = ? AND message_id IN (`+placeholders(len(ids))+`) ORDER BY handled_at, message_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HandledMessage
	for rows.Next() {
		var (
			m       HandledMessage
			at      sql.NullString
			handler string
		)
		if err := rows.Scan(&m.System, &m.MessageID, &m.Action, &m.JobID, &m.OutreachID, &m.InterviewID, &at, &handler); err != nil {
			return nil, err
		}
		if m.HandledAt, err = parseTime(at); err != nil {
			return nil, err
		}
		m.HandledBy = Actor(handler)
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkHandled records messages as handled. A message already marked keeps
// its first record; those are returned in existing.
func (s *Store) MarkHandled(ctx context.Context, by Actor, system string, ms []NewHandled) (added int, existing []HandledMessage, err error) {
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.MessageID)
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		added, existing = 0, nil
		for _, m := range ms {
			if err := targetExists(ctx, tx, RefTarget{JobID: m.JobID, OutreachID: m.OutreachID}); err != nil {
				return err
			}
		}
		var err error
		if existing, err = handledMessages(ctx, tx, system, ids); err != nil {
			return err
		}
		now := s.stamp()
		for _, m := range ms {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO handled_messages (system, message_id, action, job_id, outreach_id, handled_at, handled_by)
				VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
				system, m.MessageID, m.Action, nullID(m.JobID), nullID(m.OutreachID), now, by)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added++
			}
		}
		return nil
	})
	return added, existing, err
}

// MailCheck returns how far the agent has read system, or zero.
func (s *Store) MailCheck(ctx context.Context, system string) (time.Time, error) {
	var at sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT checked_through FROM mail_checks WHERE system = ?", system).Scan(&at)
	if errNoRows(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return parseTime(at)
}

// SetMailCheck records how far the agent has read system.
func (s *Store) SetMailCheck(ctx context.Context, by Actor, system string, through time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		_, err := tx.ExecContext(ctx, `
			INSERT INTO mail_checks (system, checked_through, updated_at, updated_by) VALUES (?, ?, ?, ?)
			ON CONFLICT (system) DO UPDATE SET checked_through = excluded.checked_through,
				updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			system, utc.Format(through), now, by)
		return err
	})
}

// RefTarget is what a link to an email thread belongs to: one of a job
// (its application) or an outreach thread.
type RefTarget struct {
	JobID, OutreachID int64
}

// Ref is a link to an email thread.
type Ref struct {
	ID               int64
	System, Ref, URL string
}

// NewRef is a link to add.
type NewRef struct {
	System, Ref, URL string
}

// Refs returns a target's links, oldest first.
func (s *Store) Refs(ctx context.Context, t RefTarget) ([]Ref, error) {
	where, id := "job_id = ?", t.JobID
	if t.OutreachID != 0 {
		where, id = "outreach_id = ?", t.OutreachID
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id, system, ref, coalesce(url, '') FROM external_refs WHERE "+where+" ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ref
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.ID, &r.System, &r.Ref, &r.URL); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ChangeRefs adds and removes a target's links. A link it already has is
// not added twice; a link to remove must be the target's.
func (s *Store) ChangeRefs(ctx context.Context, by Actor, t RefTarget, add []NewRef, remove []int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := targetExists(ctx, tx, t); err != nil {
			return err
		}
		now := s.stamp()
		removed := 0
		for _, id := range remove {
			res, err := tx.ExecContext(ctx, "DELETE FROM external_refs WHERE id = ? AND coalesce(job_id, 0) = ? AND coalesce(outreach_id, 0) = ?",
				id, t.JobID, t.OutreachID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("remove_refs: no link %d here: %w", id, ErrNotFound)
			}
			removed++
		}
		added := 0
		for _, r := range add {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO external_refs (job_id, outreach_id, system, ref, url, created_at, created_by)
				VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
				nullID(t.JobID), nullID(t.OutreachID), r.System, r.Ref, nullString(r.URL), now, by)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added++
			}
		}
		if added == 0 && removed == 0 {
			return nil
		}
		refs := eventRefs{JobID: t.JobID, OutreachID: t.OutreachID}
		if t.OutreachID != 0 {
			var job sql.NullInt64
			if err := tx.QueryRowContext(ctx, "SELECT job_id FROM outreach WHERE id = ?", t.OutreachID).Scan(&job); err != nil {
				return err
			}
			refs.JobID = job.Int64
		}
		// Counts only: the links themselves are in external_refs.
		return insertEvent(ctx, tx, now, by, "refs_changed", refs, map[string]int{"added": added, "removed": removed})
	})
}

// RefsBelong checks that each link id is one of the job's or thread's.
func (s *Store) RefsBelong(ctx context.Context, t RefTarget, ids []int64) error {
	for _, id := range ids {
		var one int
		err := s.db.QueryRowContext(ctx, "SELECT 1 FROM external_refs WHERE id = ? AND coalesce(job_id, 0) = ? AND coalesce(outreach_id, 0) = ?",
			id, t.JobID, t.OutreachID).Scan(&one)
		if errNoRows(err) {
			return fmt.Errorf("remove_refs: no link %d here: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// targetExists checks that the job and thread named exist.
func targetExists(ctx context.Context, tx *sql.Tx, t RefTarget) error {
	for _, c := range []struct {
		id          int64
		table, name string
	}{{t.JobID, "jobs", "job"}, {t.OutreachID, "outreach", "outreach"}} {
		if c.id == 0 {
			continue
		}
		var one int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM "+c.table+" WHERE id = ?", c.id).Scan(&one)
		if errNoRows(err) {
			return fmt.Errorf("%s_id: no %s %d: %w", c.name, c.name, c.id, ErrNotFound)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
