package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// RetentionPeriod is how long a closed job keeps what it doesn't need
// (docs/design.md, "Storage and retention"). It is longer than the repost
// window, so a repost still finds the job it belongs to.
const RetentionPeriod = 90 * 24 * time.Hour

// ClearDescriptions drops the stored descriptions of the jobs' postings,
// for jobs the prefilter filtered out without needing them. Only jobs still
// filtered out are touched, so a job the user acted on keeps its text. The
// fingerprint stays, for dedupe, and the description is fetched again if
// new filters make the job worth judging. It returns how many postings it
// changed.
func (s *Store) ClearDescriptions(ctx context.Context, jobIDs []int64) (int, error) {
	n := 0
	err := s.write(ctx, func(tx *sql.Tx) error {
		for _, id := range jobIDs {
			res, err := tx.ExecContext(ctx, `
				UPDATE postings SET description = NULL, description_html_raw = NULL, detail_fetched_at = NULL
				WHERE job_id = ? AND (description IS NOT NULL OR description_html_raw IS NOT NULL)
				  AND job_id IN (SELECT id FROM jobs WHERE status = 'filtered_out')`, id)
			if err != nil {
				return err
			}
			changed, _ := res.RowsAffected()
			n += int(changed)
		}
		return nil
	})
	return n, err
}

// PruneOutcome is what Prune removed.
type PruneOutcome struct {
	JobsDeleted         int
	CompaniesDeleted    int
	DescriptionsCleared int // postings
}

// Prune applies the retention rules to jobs closed for longer than
// RetentionPeriod: jobs nobody touched are deleted with their postings and
// timeline; jobs that were screened out or skipped keep their screening and
// timeline but lose their descriptions; jobs the user pursued are kept as
// they are. Companies found by search are deleted with their last job.
func (s *Store) Prune(ctx context.Context) (PruneOutcome, error) {
	var out PruneOutcome
	err := s.write(ctx, func(tx *sql.Tx) error {
		cutoff := utc.Format(s.now().Add(-RetentionPeriod))

		rows, err := tx.QueryContext(ctx, `
			SELECT id FROM jobs j
			WHERE closed_at IS NOT NULL AND closed_at < ? AND status IN ('new', 'filtered_out', 'unscreened') AND notes = '' AND applied_elsewhere = 0
			  AND NOT EXISTS (SELECT 1 FROM screenings s WHERE s.job_id = j.id)
			  AND NOT EXISTS (SELECT 1 FROM outreach o WHERE o.job_id = j.id)
			  AND NOT EXISTS (SELECT 1 FROM interviews i WHERE i.job_id = j.id)
			  AND NOT EXISTS (SELECT 1 FROM memories m WHERE m.evidence_job_id = j.id)`, cutoff)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			for _, q := range []string{
				"DELETE FROM content_flags WHERE posting_id IN (SELECT id FROM postings WHERE job_id = ?)",
				"DELETE FROM postings WHERE job_id = ?",
				"DELETE FROM events WHERE job_id = ?",
				"DELETE FROM jobs WHERE id = ?",
			} {
				if _, err := tx.ExecContext(ctx, q, id); err != nil {
					return err
				}
			}
		}
		out.JobsDeleted = len(ids)

		res, err := tx.ExecContext(ctx, `
			UPDATE postings SET description = NULL, description_html_raw = NULL
			WHERE (description IS NOT NULL OR description_html_raw IS NOT NULL)
			  AND job_id IN (SELECT id FROM jobs WHERE closed_at IS NOT NULL AND closed_at < ? AND status IN ('screened_out', 'skipped'))`, cutoff)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		out.DescriptionsCleared = int(n)

		const emptySearchCompanies = `
			SELECT id FROM companies c WHERE origin = 'search'
			  AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.company_id = c.id)
			  AND NOT EXISTS (SELECT 1 FROM postings p WHERE p.company_id = c.id)
			  AND NOT EXISTS (SELECT 1 FROM contacts t WHERE t.company_id = c.id)`
		if _, err := tx.ExecContext(ctx, "DELETE FROM events WHERE company_id IN ("+emptySearchCompanies+")"); err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx, "DELETE FROM companies WHERE id IN ("+emptySearchCompanies+")")
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		out.CompaniesDeleted = int(n)
		return nil
	})
	return out, err
}
