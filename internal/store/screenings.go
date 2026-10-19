package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Fits are the screening ratings, best first.
var Fits = []string{"strong", "good", "stretch", "no"}

// NewScreening is one rating to record.
type NewScreening struct {
	JobID  int64
	Fit    string
	Reason string
	Gaps   string
	Flags  []string
}

// ScreeningResult is where a screened job ended up.
type ScreeningResult struct {
	JobID  int64
	Status JobStatus
}

// triaged are the statuses a screening moves a job out of. Jobs the user
// skipped or applied to keep their status; the rating is still recorded.
var triaged = map[JobStatus]bool{StatusNew: true, StatusFilteredOut: true, StatusUnscreened: true, StatusScreenedOut: true, StatusReview: true}

// RecordScreenings stores ratings in one transaction, stamped with the
// profile version they were judged against. A job rated "no" moves to
// screened_out; any other rating moves it to review.
func (s *Store) RecordScreenings(ctx context.Context, by Actor, profileVersion int64, ss []NewScreening) ([]ScreeningResult, error) {
	var out []ScreeningResult
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		for _, sc := range ss {
			var from string
			err := tx.QueryRowContext(ctx, "SELECT status FROM jobs WHERE id = ?", sc.JobID).Scan(&from)
			if errNoRows(err) {
				return fmt.Errorf("job %d: %w", sc.JobID, ErrNotFound)
			}
			if err != nil {
				return err
			}
			flags, err := jsonText(orNone(sc.Flags))
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO screenings (job_id, fit, reason, gaps, flags, profile_version, screened_by, screened_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				sc.JobID, sc.Fit, sc.Reason, sc.Gaps, flags, nullID(profileVersion), by, now); err != nil {
				return err
			}
			to := JobStatus(from)
			if triaged[to] {
				to = StatusReview
				if sc.Fit == "no" {
					to = StatusScreenedOut
				}
			}
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET status = ?, updated_at = ?, updated_by = ? WHERE id = ?",
				to, now, by, sc.JobID); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, now, by, "screened", eventRefs{JobID: sc.JobID},
				map[string]string{"fit": sc.Fit, "from": from, "to": string(to)}); err != nil {
				return err
			}
			out = append(out, ScreeningResult{JobID: sc.JobID, Status: to})
		}
		return nil
	})
	return out, err
}

// ApplicationChange is a change to a job's place in the pipeline; nil and
// empty fields are left alone.
type ApplicationChange struct {
	Status     JobStatus
	AppliedAt  *time.Time
	Notes      *string // replaces the notes
	AppendNote string  // added on a new line
}

// UpdateApplication changes a job's status, applied date or notes in one
// transaction, with one event per change. Moving a job to applied without
// an applied date sets it to now.
func (s *Store) UpdateApplication(ctx context.Context, by Actor, id int64, c ApplicationChange) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var (
			from    string
			applied sql.NullString
			notes   string
		)
		err := tx.QueryRowContext(ctx, "SELECT status, applied_at, notes FROM jobs WHERE id = ?", id).Scan(&from, &applied, &notes)
		if errNoRows(err) {
			return fmt.Errorf("job %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		now := s.stamp()
		ref := eventRefs{JobID: id}
		if c.Status != "" && c.Status != JobStatus(from) {
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET status = ? WHERE id = ?", c.Status, id); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, now, by, "status_changed", ref, map[string]JobStatus{"from": JobStatus(from), "to": c.Status}); err != nil {
				return err
			}
			if c.Status == StatusApplied && c.AppliedAt == nil && !applied.Valid {
				t := s.now()
				c.AppliedAt = &t
			}
		}
		if c.AppliedAt != nil {
			at, err := timeText(*c.AppliedAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET applied_at = ?, applied_unconfirmed = 0 WHERE id = ?", at, id); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, now, by, "applied_at_set", ref, map[string]string{"applied_at": at.String}); err != nil {
				return err
			}
		}
		if c.Notes != nil || c.AppendNote != "" {
			next := notes
			if c.Notes != nil {
				next = *c.Notes
			}
			if c.AppendNote != "" {
				if next != "" {
					next += "\n"
				}
				next += c.AppendNote
			}
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET notes = ? WHERE id = ?", next, id); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, now, by, "notes_changed", ref, nil); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE jobs SET updated_at = ?, updated_by = ? WHERE id = ?", now, by, id)
		return err
	})
}

// ReportContentFlag records text in a job's listing that an agent says is
// addressed to an AI, on the job's primary posting, for the user to review.
// It never hides the job. Reporting the same excerpt twice returns the
// first report.
func (s *Store) ReportContentFlag(ctx context.Context, by Actor, jobID int64, kind, excerpt, note string) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var posting string
		err := tx.QueryRowContext(ctx, `
			SELECT p.id FROM postings p JOIN jobs j ON j.id = p.job_id WHERE j.id = ?
			ORDER BY p.url = j.url DESC, p.closed_at IS NOT NULL, p.first_seen_at LIMIT 1`, jobID).Scan(&posting)
		if errNoRows(err) {
			return fmt.Errorf("job %d has no posting to flag: %w", jobID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		now := s.stamp()
		err = tx.QueryRowContext(ctx, `
			INSERT INTO content_flags (posting_id, detector, kind, excerpt, created_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT DO UPDATE SET kind = kind RETURNING id`, posting, string(by), kind, excerpt, now).Scan(&id)
		if err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "injection_reported", eventRefs{JobID: jobID},
			map[string]any{"flag": id, "kind": kind, "note": note})
	})
	return id, err
}

func orNone(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
