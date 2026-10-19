package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
)

// Employer is the company a search result's employer was matched to.
type Employer struct {
	CompanyID int64
	// Covered means jhh already reads this company's board, which has the
	// same jobs with direct links, so its search results are skipped.
	Covered bool
}

// Employers matches employer names from search results to companies by
// their normalized name (dedupe.CompanyKey), adding a company with no
// board (origin "search") for each one jhh doesn't know. The result is
// keyed by the names as given.
func (s *Store) Employers(ctx context.Context, by Actor, names []string) (map[string]Employer, error) {
	out := map[string]Employer{}
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		byKey := map[string]Employer{}
		for _, name := range names {
			if _, done := out[name]; done {
				continue
			}
			key := dedupe.CompanyKey(name)
			if key == "" {
				continue
			}
			if e, ok := byKey[key]; ok {
				out[name] = e
				continue
			}
			var (
				e       Employer
				source  string
				enabled bool
			)
			// A company with an enabled board first, then any other.
			err := tx.QueryRowContext(ctx, `
				SELECT id, source, enabled FROM companies WHERE name_key = ?
				ORDER BY (source <> 'none' AND enabled) DESC, id LIMIT 1`, key).Scan(&e.CompanyID, &source, &enabled)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				res, err := tx.ExecContext(ctx, `
					INSERT INTO companies (name, name_key, source, origin, created_at, created_by, updated_at, updated_by)
					VALUES (?, ?, 'none', 'search', ?, ?, ?, ?)`, name, key, now, by, now, by)
				if err != nil {
					return fmt.Errorf("adding company %q: %w", name, err)
				}
				if e.CompanyID, err = res.LastInsertId(); err != nil {
					return err
				}
				if err := insertEvent(ctx, tx, now, by, "company_added", eventRefs{CompanyID: e.CompanyID},
					map[string]string{"origin": "search"}); err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				e.Covered = source != "none" && enabled
			}
			byKey[key], out[name] = e, e
		}
		return nil
	})
	return out, err
}

// SearchMissing is what CloseSearchMissing found.
type SearchMissing struct {
	ClosedJobs []int64
	// Check are postings of jobs the user is pursuing that no query
	// returned. They are only closed once the source says they're gone.
	Check []DetailRef
}

// CloseSearchMissing closes the open postings found by source that weren't
// in a complete set of search results (seen), except postings of jobs the
// user has acted on, which are returned to be looked up instead.
func (s *Store) CloseSearchMissing(ctx context.Context, by Actor, source string, seen map[string]bool) (SearchMissing, error) {
	var out SearchMissing
	err := s.write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT p.id, p.external_id, p.job_id, j.status FROM postings p JOIN jobs j ON j.id = p.job_id
			WHERE p.source = ? AND p.search = 1 AND p.closed_at IS NULL`, source)
		if err != nil {
			return err
		}
		var closing []string
		for rows.Next() {
			var (
				ref    DetailRef
				jobID  int64
				status string
			)
			if err := rows.Scan(&ref.PostingID, &ref.ExternalID, &jobID, &status); err != nil {
				rows.Close()
				return err
			}
			switch {
			case seen[ref.PostingID]:
			case pursued(JobStatus(status)):
				out.Check = append(out.Check, ref)
			default:
				closing = append(closing, ref.PostingID)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		a := applier{ctx: ctx, tx: tx, now: s.stamp(), at: s.now(), by: by, out: &FetchOutcome{}}
		if err := a.closePostings(closing); err != nil {
			return err
		}
		out.ClosedJobs = a.out.ClosedJobs
		return nil
	})
	return out, err
}

// ClosePostings closes postings a source said are gone, and closes their
// jobs when no posting is left open.
func (s *Store) ClosePostings(ctx context.Context, by Actor, ids []string) ([]int64, error) {
	var closed []int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		a := applier{ctx: ctx, tx: tx, now: s.stamp(), at: s.now(), by: by, out: &FetchOutcome{}}
		err := a.closePostings(ids)
		closed = a.out.ClosedJobs
		return err
	})
	return closed, err
}

func (a *applier) closePostings(ids []string) error {
	for _, id := range ids {
		var jobID sql.NullInt64
		err := a.tx.QueryRowContext(a.ctx, `
			UPDATE postings SET closed_at = ? WHERE id = ? AND closed_at IS NULL RETURNING job_id`, a.now, id).Scan(&jobID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if jobID.Valid {
			a.touched = append(a.touched, jobID.Int64)
		}
	}
	return a.syncTouched()
}

// pursued reports whether the user has taken a job beyond triage.
func pursued(st JobStatus) bool {
	switch st {
	case StatusReview, StatusApplied, StatusInterviewing, StatusOffer, StatusRejected, StatusWithdrawn:
		return true
	}
	return false
}
