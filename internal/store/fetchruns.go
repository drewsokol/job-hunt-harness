package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// FetchRun summarizes one refresh.
type FetchRun struct {
	ID         int64
	StartedAt  time.Time
	FinishedAt time.Time
	Companies  int
	Postings   int
	NewJobs    int
	ClosedJobs int
	Errors     []FetchError
}

// FetchError is one company's failure in a refresh.
type FetchError struct {
	CompanyID int64  `json:"company_id"`
	Company   string `json:"company"`
	Error     string `json:"error"`
}

// StartFetchRun records that a refresh began and returns its id.
func (s *Store) StartFetchRun(ctx context.Context) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "INSERT INTO fetch_runs (started_at) VALUES (?)", s.stamp())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// FinishFetchRun records a refresh's totals.
func (s *Store) FinishFetchRun(ctx context.Context, r FetchRun) error {
	errs := r.Errors
	if errs == nil {
		errs = []FetchError{}
	}
	errText, err := jsonText(errs)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE fetch_runs SET finished_at = ?, companies = ?, postings = ?, new_jobs = ?, closed_jobs = ?, errors = ?
			WHERE id = ?`,
			s.stamp(), r.Companies, r.Postings, r.NewJobs, r.ClosedJobs, errText, r.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("fetch run %d: %w", r.ID, ErrNotFound)
		}
		return nil
	})
}

// AddCompanies inserts companies in one transaction, skipping any whose
// board_key is already present, and returns how many were added.
func (s *Store) AddCompanies(ctx context.Context, by Actor, cs []NewCompany) (int, error) {
	added := 0
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		for _, c := range cs {
			cfg := string(c.SourceConfig)
			if cfg == "" {
				cfg = "{}"
			}
			res, err := tx.ExecContext(ctx, `
				INSERT INTO companies (name, source, board_key, source_config, careers_url, origin,
				                       created_at, created_by, updated_at, updated_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (board_key) DO NOTHING`,
				c.Name, c.Source, nullString(c.BoardKey), cfg, nullString(c.CareersURL), c.Origin,
				now, by, now, by)
			if err != nil {
				return fmt.Errorf("adding company %q: %w", c.Name, err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, now, by, "company_added", eventRefs{CompanyID: id}, nil); err != nil {
				return err
			}
			added++
		}
		return nil
	})
	return added, err
}
