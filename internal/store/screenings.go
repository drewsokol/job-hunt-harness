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
	Status         JobStatus
	AppliedAt      *time.Time
	ClearAppliedAt bool    // undoes a "mark applied"
	Notes          *string // replaces the notes
	AppendNote     string  // added on a new line
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
			if c.Status == StatusApplied && c.AppliedAt == nil && !c.ClearAppliedAt && !applied.Valid {
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
		} else if c.ClearAppliedAt && applied.Valid {
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET applied_at = NULL, applied_unconfirmed = 0 WHERE id = ?", id); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, now, by, "applied_at_cleared", ref, nil); err != nil {
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

// Content flag reviews.
const (
	FlagKeep   = "keep"   // the user read it and the job stays
	FlagHide   = "hide"   // the job goes to skipped
	FlagReopen = "reopen" // undoes either
)

// ReviewContentFlag records the user's decision on a content flag and
// returns the flagged job. Keeping can reclassify the flag as kind (for
// example canary). Hiding skips the job if nobody has acted on it yet.
func (s *Store) ReviewContentFlag(ctx context.Context, by Actor, flagID int64, action, kind string) (int64, error) {
	var jobID int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var (
			oldKind string
			status  string
		)
		err := tx.QueryRowContext(ctx, `
			SELECT p.job_id, f.kind, j.status FROM content_flags f JOIN postings p ON p.id = f.posting_id JOIN jobs j ON j.id = p.job_id
			WHERE f.id = ?`, flagID).Scan(&jobID, &oldKind, &status)
		if errNoRows(err) {
			return fmt.Errorf("content flag %d: %w", flagID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		now := s.stamp()
		data := map[string]any{"flag": flagID, "action": action}
		switch action {
		case FlagKeep:
			if kind == "" {
				kind = oldKind
			}
			_, err = tx.ExecContext(ctx, "UPDATE content_flags SET status = 'kept', kind = ?, reviewed_at = ?, reviewed_by = ? WHERE id = ?",
				kind, now, by, flagID)
			if kind != oldKind {
				data["kind"] = kind
			}
		case FlagHide:
			_, err = tx.ExecContext(ctx, "UPDATE content_flags SET status = 'hidden', reviewed_at = ?, reviewed_by = ? WHERE id = ?",
				now, by, flagID)
			if err == nil && triaged[JobStatus(status)] {
				if _, err = tx.ExecContext(ctx, "UPDATE jobs SET status = ?, updated_at = ?, updated_by = ? WHERE id = ?",
					StatusSkipped, now, by, jobID); err == nil {
					err = insertEvent(ctx, tx, now, by, "status_changed", eventRefs{JobID: jobID},
						map[string]any{"from": status, "to": StatusSkipped, "cause": "content_flag"})
				}
			}
		case FlagReopen:
			_, err = tx.ExecContext(ctx, "UPDATE content_flags SET status = 'open', reviewed_at = NULL, reviewed_by = NULL WHERE id = ?", flagID)
		default:
			return fmt.Errorf("unknown flag review %q", action)
		}
		if err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "content_flag_reviewed", eventRefs{JobID: jobID}, data)
	})
	return jobID, err
}

// SetPrefilterOverride sends a job the prefilter dropped to screening
// anyway (on), or undoes that (off). While it is on, the prefilter records
// its decisions on the job without moving it.
func (s *Store) SetPrefilterOverride(ctx context.Context, by Actor, id int64, on bool) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var status, decision string
		err := tx.QueryRowContext(ctx, "SELECT status, coalesce(prefilter_status, '') FROM jobs WHERE id = ?", id).Scan(&status, &decision)
		if errNoRows(err) {
			return fmt.Errorf("job %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		from, to := JobStatus(status), JobStatus(status)
		switch {
		case on && from != StatusFilteredOut:
			return fmt.Errorf("job %d is %s, not filtered out: %w", id, status, ErrConflict)
		case on:
			to = StatusUnscreened
		case from == StatusUnscreened && decision == "drop":
			to = StatusFilteredOut
		}
		now := s.stamp()
		if _, err := tx.ExecContext(ctx, "UPDATE jobs SET prefilter_override = ?, status = ?, updated_at = ?, updated_by = ? WHERE id = ?",
			on, to, now, by, id); err != nil {
			return err
		}
		if to == from {
			return nil
		}
		return insertEvent(ctx, tx, now, by, "status_changed", eventRefs{JobID: id},
			map[string]any{"from": from, "to": to, "cause": "prefilter_override"})
	})
}

// JobWithTitle returns the newest job, not hidden, at a company with this
// name key and with this title key, or 0. It matches an application made
// elsewhere that came with no link or description to dedupe by: exact
// keys only, never a near match.
func (s *Store) JobWithTitle(ctx context.Context, companyKey, titleKey string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT j.id FROM jobs j JOIN companies c ON c.id = j.company_id
		WHERE c.name_key = ? AND j.title_key = ? AND j.hidden_at IS NULL
		ORDER BY j.id DESC LIMIT 1`, companyKey, titleKey).Scan(&id)
	if errNoRows(err) {
		return 0, nil
	}
	return id, err
}

// MarkAppliedElsewhere records that the user applied to a job outside jhh.
func (s *Store) MarkAppliedElsewhere(ctx context.Context, by Actor, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE jobs SET applied_elsewhere = 1, updated_at = ?, updated_by = ? WHERE id = ? AND applied_elsewhere = 0",
			s.stamp(), by, id)
		return err
	})
}
