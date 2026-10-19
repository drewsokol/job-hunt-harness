package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// ErrConflict means a change can't be made as asked; the message says why.
var ErrConflict = errors.New("conflict")

// MergeJobs corrects dedupe that missed: the postings of every job in from
// move to into, and those jobs are hidden. Their status, screenings and
// notes stay with them, so SplitJob can restore one; their notes are also
// added to into's. A job the user acted on (skipped, applied or later) can
// only be the one merged into.
func (s *Store) MergeJobs(ctx context.Context, by Actor, into int64, from []int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var out FetchOutcome
		a := applier{ctx: ctx, tx: tx, now: s.stamp(), at: s.now(), by: by, out: &out}
		if _, _, err := visibleJob(ctx, tx, into); err != nil {
			return err
		}
		for _, id := range from {
			status, notes, err := visibleJob(ctx, tx, id)
			if err != nil {
				return err
			}
			if !triaged[status] {
				return fmt.Errorf("job %d is %s; merge it the other way, into job %d: %w", id, status, id, ErrConflict)
			}
			postings, err := jobPostingIDs(ctx, tx, id)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE postings SET job_id = ? WHERE job_id = ?", into, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET hidden_at = ?, updated_at = ?, updated_by = ? WHERE id = ?",
				a.now, a.now, by, id); err != nil {
				return err
			}
			if notes != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE jobs SET notes = notes || CASE WHEN notes = '' THEN '' ELSE char(10) END || ? WHERE id = ?`,
					notes, into); err != nil {
					return err
				}
			}
			if err := insertEvent(ctx, tx, a.now, by, "job_merged", eventRefs{JobID: id}, mergeData{Into: into, Postings: postings}); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, a.now, by, "jobs_merged_in", eventRefs{JobID: into}, mergeData{From: id, Postings: postings}); err != nil {
				return err
			}
		}
		return a.syncJob(into)
	})
}

type mergeData struct {
	Into     int64    `json:"into,omitempty"`
	From     int64    `json:"from,omitempty"`
	Postings []string `json:"postings"`
}

// SplitJob corrects dedupe that went too far: the postings move out of the
// job. When they are exactly the postings a merge brought in, the merged
// job comes back as it was; otherwise they become a new job, which starts
// as new for the prefilter to judge. It returns the job they moved to.
func (s *Store) SplitJob(ctx context.Context, by Actor, id int64, postings []string) (int64, error) {
	var to int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var out FetchOutcome
		a := applier{ctx: ctx, tx: tx, now: s.stamp(), at: s.now(), by: by, out: &out}
		if _, _, err := visibleJob(ctx, tx, id); err != nil {
			return err
		}
		has, err := jobPostingIDs(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, p := range postings {
			if !slices.Contains(has, p) {
				return fmt.Errorf("posting %s isn't part of job %d: %w", p, id, ErrConflict)
			}
		}
		if len(postings) >= len(has) {
			return fmt.Errorf("a job keeps at least one posting: %w", ErrConflict)
		}
		want := slices.Sorted(slices.Values(postings))

		to, err = mergedJob(ctx, tx, id, want)
		if err != nil {
			return err
		}
		kind := "job_unmerged"
		if to != 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE jobs SET hidden_at = NULL, updated_at = ?, updated_by = ? WHERE id = ?", a.now, by, to); err != nil {
				return err
			}
		} else {
			kind = "job_split"
			var title, titleKey string
			var companyID int64
			if err := tx.QueryRowContext(ctx, "SELECT company_id, title, title_key FROM postings WHERE id = ?", want[0]).Scan(&companyID, &title, &titleKey); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO jobs (company_id, title, title_key, first_seen_at, origin, created_at, created_by, updated_at, updated_by)
				SELECT ?, ?, ?, min(first_seen_at), (SELECT origin FROM jobs WHERE id = ?), ?, ?, ?, ?
				FROM postings WHERE id IN (`+placeholders(len(want))+`)`,
				append([]any{companyID, title, titleKey, id, a.now, by, a.now, by}, anys(want)...)...)
			if err != nil {
				return err
			}
			if to, err = res.LastInsertId(); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE postings SET job_id = ? WHERE id IN ("+placeholders(len(want))+")",
			append([]any{to}, anys(want)...)...); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, a.now, by, kind, eventRefs{JobID: id}, map[string]any{"to": to, "postings": want}); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, a.now, by, "job_split_off", eventRefs{JobID: to}, map[string]any{"from": id, "postings": want}); err != nil {
			return err
		}
		if err := a.syncJob(id); err != nil {
			return err
		}
		return a.syncJob(to)
	})
	return to, err
}

// mergedJob returns the hidden job whose merge into id brought exactly
// these postings, or 0.
func mergedJob(ctx context.Context, tx *sql.Tx, id int64, postings []string) (int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT e.job_id, e.data FROM events e JOIN jobs j ON j.id = e.job_id
		WHERE e.kind = 'job_merged' AND json_extract(e.data, '$.into') = ? AND j.hidden_at IS NOT NULL
		ORDER BY e.id DESC`, id)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			job  int64
			data string
			d    mergeData
		)
		if err := rows.Scan(&job, &data); err != nil {
			return 0, err
		}
		if json.Unmarshal([]byte(data), &d) == nil && slices.Equal(slices.Sorted(slices.Values(d.Postings)), postings) {
			return job, nil
		}
	}
	return 0, rows.Err()
}

// visibleJob returns a job's status and notes, or ErrNotFound when it
// doesn't exist or was merged into another.
func visibleJob(ctx context.Context, tx *sql.Tx, id int64) (JobStatus, string, error) {
	var (
		status, notes string
		hidden        bool
	)
	err := tx.QueryRowContext(ctx, "SELECT status, notes, hidden_at IS NOT NULL FROM jobs WHERE id = ?", id).Scan(&status, &notes, &hidden)
	if errors.Is(err, sql.ErrNoRows) || err == nil && hidden {
		return "", "", fmt.Errorf("job %d: %w", id, ErrNotFound)
	}
	return JobStatus(status), notes, err
}

func jobPostingIDs(ctx context.Context, tx *sql.Tx, id int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM postings WHERE job_id = ? ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		ids = append(ids, p)
	}
	return ids, rows.Err()
}

func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
