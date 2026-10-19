package store

import (
	"context"
	"database/sql"
	"time"
)

// SetupCounts says how far setup has got.
type SetupCounts struct {
	Companies      int // every company jhh knows
	BoardCompanies int // companies with a board jhh reads
	Memories       int // active
	Inferred       int // active and inferred, so not yet confirmed
	Jobs           int // open
	Screenings     int
	LastRefreshAt  time.Time // zero before the first refresh
}

// SetupCounts counts what exists.
func (s *Store) SetupCounts(ctx context.Context) (SetupCounts, error) {
	var (
		c    SetupCounts
		last sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM companies),
		       (SELECT count(*) FROM companies WHERE source <> 'none' AND enabled),
		       (SELECT count(*) FROM memories WHERE status = 'active'),
		       (SELECT count(*) FROM memories WHERE status = 'active' AND basis = 'inferred'),
		       (SELECT count(*) FROM jobs WHERE closed_at IS NULL),
		       (SELECT count(*) FROM screenings),
		       (SELECT max(finished_at) FROM fetch_runs)`).
		Scan(&c.Companies, &c.BoardCompanies, &c.Memories, &c.Inferred, &c.Jobs, &c.Screenings, &last)
	if err != nil {
		return c, err
	}
	c.LastRefreshAt, err = parseTime(last)
	return c, err
}
