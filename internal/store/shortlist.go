package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// The shortlist is the agent's order of the jobs in review to apply to
// first, which the user can adjust on the dashboard. Only jobs in review,
// open and not hidden are shown; a row stays while its job is elsewhere,
// so the job comes back to its place, and nothing that changes a job's
// status touches it.

// shortlistShown is the condition for a shortlisted job to be shown,
// numbered and counted.
const shortlistShown = `j.status = 'review' AND j.closed_at IS NULL AND j.hidden_at IS NULL`

// shortlistFirst starts the order of a list of jobs in review: shortlisted
// jobs first, by rank.
const shortlistFirst = `sl.rank IS NULL, sl.rank, `

// ShortlistPlace is a job's place on the shortlist. Rank counts from 1
// over the jobs shown, whatever gaps the stored ranks have. Reason is the
// agent's or the user's own words, not the listing's.
type ShortlistPlace struct {
	Rank   int
	Reason string
	SetBy  Actor
	SetAt  time.Time
}

// ShortlistEntry is one job to put on the shortlist.
type ShortlistEntry struct {
	JobID  int64
	Reason string
}

// SetShortlist replaces the whole shortlist with entries, in order, in one
// transaction. Every job must be in review, open and not hidden; if any
// isn't, nothing is saved and the error (ErrConflict) names each one.
func (s *Store) SetShortlist(ctx context.Context, by Actor, entries []ShortlistEntry) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var problems []string
		for _, e := range entries {
			p, err := shortlistProblem(ctx, tx, e.JobID)
			if err != nil {
				return err
			}
			if p != "" {
				problems = append(problems, p)
			}
		}
		if len(problems) > 0 {
			return fmt.Errorf("nothing was saved: %s: %w", strings.Join(problems, "; "), ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM shortlist"); err != nil {
			return err
		}
		now := s.stamp()
		for i, e := range entries {
			if _, err := tx.ExecContext(ctx, "INSERT INTO shortlist (job_id, rank, reason, set_at, set_by) VALUES (?, ?, ?, ?, ?)",
				e.JobID, i+1, e.Reason, now, by); err != nil {
				return err
			}
		}
		return insertEvent(ctx, tx, now, by, "shortlist_set", eventRefs{}, map[string]int{"count": len(entries)})
	})
}

// shortlistProblem says why a job can't go on the shortlist, or "" when it
// can. It names jhh's statuses, never the listing.
func shortlistProblem(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	var (
		status         string
		closed, hidden bool
	)
	err := tx.QueryRowContext(ctx, "SELECT status, closed_at IS NOT NULL, hidden_at IS NOT NULL FROM jobs WHERE id = ?", id).
		Scan(&status, &closed, &hidden)
	switch {
	case errNoRows(err):
		return fmt.Sprintf("job %d doesn't exist", id), nil
	case err != nil:
		return "", err
	case hidden:
		return fmt.Sprintf("job %d was merged into another job", id), nil
	case JobStatus(status) != StatusReview:
		return fmt.Sprintf("job %d is %s, not in review", id, status), nil
	case closed:
		return fmt.Sprintf("job %d's posting is closed", id), nil
	}
	return "", nil
}

// shortlistShownRows returns the job ids shown on the shortlist, in order.
func shortlistShownRows(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT sl.job_id FROM shortlist sl JOIN jobs j ON j.id = sl.job_id
		WHERE `+shortlistShown+` ORDER BY sl.rank, sl.job_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MoveOnShortlist swaps a shown job with the one shown above it (up) or
// below it, and records the change.
func (s *Store) MoveOnShortlist(ctx context.Context, by Actor, jobID int64, up bool) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		ids, err := shortlistShownRows(ctx, tx)
		if err != nil {
			return err
		}
		at := -1
		for i, id := range ids {
			if id == jobID {
				at = i
			}
		}
		if at < 0 {
			return fmt.Errorf("job %d isn't on the shortlist: %w", jobID, ErrNotFound)
		}
		to, change := at+1, "moved_down"
		if up {
			to, change = at-1, "moved_up"
		}
		if to < 0 || to >= len(ids) {
			return fmt.Errorf("job %d is already %s on the shortlist: %w", jobID, map[bool]string{true: "first", false: "last"}[up], ErrConflict)
		}
		now := s.stamp()
		var mine, theirs int64
		if err := tx.QueryRowContext(ctx, "SELECT (SELECT rank FROM shortlist WHERE job_id = ?), (SELECT rank FROM shortlist WHERE job_id = ?)",
			jobID, ids[to]).Scan(&mine, &theirs); err != nil {
			return err
		}
		for id, rank := range map[int64]int64{jobID: theirs, ids[to]: mine} {
			if _, err := tx.ExecContext(ctx, "UPDATE shortlist SET rank = ?, set_at = ?, set_by = ? WHERE job_id = ?", rank, now, by, id); err != nil {
				return err
			}
		}
		return insertEvent(ctx, tx, now, by, "shortlist_changed", eventRefs{JobID: jobID},
			map[string]any{"change": change, "from": at + 1, "to": to + 1, "swapped_with": ids[to]})
	})
}

// AddToShortlist puts a job in review after the current last, with reason,
// unless the shortlist already shows most jobs.
func (s *Store) AddToShortlist(ctx context.Context, by Actor, jobID int64, reason string, most int) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		p, err := shortlistProblem(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if p != "" {
			return fmt.Errorf("%s: %w", p, ErrConflict)
		}
		var on bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM shortlist WHERE job_id = ?)", jobID).Scan(&on); err != nil {
			return err
		}
		if on {
			return fmt.Errorf("job %d is already on the shortlist: %w", jobID, ErrConflict)
		}
		ids, err := shortlistShownRows(ctx, tx)
		if err != nil {
			return err
		}
		if len(ids) >= most {
			return fmt.Errorf("the shortlist already holds %d jobs; remove one first: %w", most, ErrConflict)
		}
		now := s.stamp()
		if _, err := tx.ExecContext(ctx, `INSERT INTO shortlist (job_id, rank, reason, set_at, set_by)
			VALUES (?, (SELECT coalesce(max(rank), 0) + 1 FROM shortlist), ?, ?, ?)`, jobID, reason, now, by); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "shortlist_changed", eventRefs{JobID: jobID},
			map[string]any{"change": "added", "to": len(ids) + 1})
	})
}

// RemoveFromShortlist deletes a job's row, and records the change.
func (s *Store) RemoveFromShortlist(ctx context.Context, by Actor, jobID int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM shortlist WHERE job_id = ?", jobID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("job %d isn't on the shortlist: %w", jobID, ErrNotFound)
		}
		return insertEvent(ctx, tx, s.stamp(), by, "shortlist_changed", eventRefs{JobID: jobID},
			map[string]string{"change": "removed"})
	})
}

// ShortlistJobs returns the jobs shown on the shortlist, in order.
func (s *Store) ShortlistJobs(ctx context.Context) ([]JobRow, error) {
	rows, _, err := s.queryJobs(ctx, shortlistShown+" AND sl.job_id IS NOT NULL", nil, "sl.rank, j.id", 0, -1, false)
	return rows, err
}

// ShortlistCounts is what the daily check-in says about the shortlist.
type ShortlistCounts struct {
	Size int // jobs shown
	// Unranked counts the jobs in review, open and not on the shortlist,
	// whose latest screening is strong or good and newer than the last
	// SetShortlist (any, with no shortlist set yet).
	Unranked int
	LastSet  time.Time // zero before the first SetShortlist
}

// ShortlistCounts counts the shortlist's jobs and the good jobs it may be
// missing. The user's changes on the dashboard don't reset LastSet.
func (s *Store) ShortlistCounts(ctx context.Context) (ShortlistCounts, error) {
	var (
		c    ShortlistCounts
		last sql.NullString
	)
	if err := s.db.QueryRowContext(ctx, "SELECT max(at) FROM events WHERE kind = 'shortlist_set'").Scan(&last); err != nil {
		return c, err
	}
	var err error
	if c.LastSet, err = parseTime(last); err != nil {
		return c, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM shortlist sl JOIN jobs j ON j.id = sl.job_id WHERE `+shortlistShown).
		Scan(&c.Size); err != nil {
		return c, err
	}
	err = s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM jobs j JOIN screenings sc ON sc.id = `+latestScreening+`
		WHERE `+shortlistShown+` AND NOT EXISTS (SELECT 1 FROM shortlist sl WHERE sl.job_id = j.id)
		  AND sc.fit IN ('strong', 'good') AND (? IS NULL OR sc.screened_at > ?)`, last, last).Scan(&c.Unranked)
	return c, err
}

// addShortlist fills in the shortlist place of each row that is shown on it.
func (s *Store) addShortlist(ctx context.Context, rs []JobRow) error {
	if len(rs) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sl.job_id, sl.reason, sl.set_by, sl.set_at FROM shortlist sl JOIN jobs j ON j.id = sl.job_id
		WHERE `+shortlistShown+` ORDER BY sl.rank, sl.job_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	places := map[int64]*ShortlistPlace{}
	for rows.Next() {
		var (
			id     int64
			p      = ShortlistPlace{Rank: len(places) + 1}
			by, at string
		)
		if err := rows.Scan(&id, &p.Reason, &by, &at); err != nil {
			return err
		}
		p.SetBy = Actor(by)
		if p.SetAt, err = parseTime(sql.NullString{String: at, Valid: true}); err != nil {
			return err
		}
		places[id] = &p
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range rs {
		rs[i].Shortlist = places[rs[i].ID]
	}
	return nil
}
