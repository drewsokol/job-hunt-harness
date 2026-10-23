package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// FilterVersion is one saved version of the filters.
type FilterVersion struct {
	Version   int64
	Filters   json.RawMessage
	Rationale string
	Effect    json.RawMessage
	Active    bool
	CreatedAt time.Time
	CreatedBy Actor
}

// ActiveFilters returns the active filters, and false when none have been
// set.
func (s *Store) ActiveFilters(ctx context.Context) (FilterVersion, bool, error) {
	var (
		v                   FilterVersion
		filters, effect, by string
		createdAt           sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT version, filters, rationale, effect, created_at, created_by
		FROM filter_versions WHERE active = 1`).
		Scan(&v.Version, &filters, &v.Rationale, &effect, &createdAt, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return FilterVersion{}, false, nil
	}
	if err != nil {
		return FilterVersion{}, false, err
	}
	v.Filters, v.Effect, v.CreatedBy, v.Active = json.RawMessage(filters), json.RawMessage(effect), Actor(by), true
	if v.CreatedAt, err = parseTime(createdAt); err != nil {
		return FilterVersion{}, false, err
	}
	return v, true, nil
}

// NewFilters is a filters version to save.
type NewFilters struct {
	Filters   json.RawMessage
	Rationale string
	Effect    json.RawMessage // nil means {}
}

// SetFilters saves a new filters version, makes it the active one, and
// applies the prefilter decisions made with it, in one transaction.
func (s *Store) SetFilters(ctx context.Context, by Actor, nf NewFilters, ds []PrefilterDecision) (int64, PrefilterOutcome, error) {
	var (
		version int64
		out     PrefilterOutcome
	)
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		if _, err := tx.ExecContext(ctx, "UPDATE filter_versions SET active = 0 WHERE active = 1"); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO filter_versions (filters, rationale, effect, active, created_at, created_by)
			VALUES (?, ?, ?, 1, ?, ?)`,
			string(nf.Filters), nf.Rationale, jsonOr(nf.Effect, "{}"), now, by)
		if err != nil {
			return fmt.Errorf("saving filters: %w", err)
		}
		if version, err = res.LastInsertId(); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, now, by, "filters_set", eventRefs{}, map[string]int64{"version": version}); err != nil {
			return err
		}
		out, err = applyPrefilter(ctx, tx, now, by, version, ds)
		return err
	})
	return version, out, err
}

// PrefilterJob is what the prefilter reads about a job, and the decision
// it has now.
type PrefilterJob struct {
	ID               int64
	CompanyID        int64
	Company          string
	Title            string
	Workplace        string
	Locations        json.RawMessage
	PayRanges        json.RawMessage
	EmploymentType   string
	Description      string
	DescriptionKnown bool
	Closed           bool
	Status           JobStatus
	PrefilterStatus  string // pass | drop | pending; empty before any filters
	Reasons          json.RawMessage
	Score            float64
	FiltersVersion   int64
	// Via names the search source the job came through ("hackernews") when it
	// has no posting from a board.
	Via string
}

// jobDescription is a job's description: its own for jobs from agents and
// people, otherwise the newest open posting's that has one.
const jobDescription = `coalesce(j.description, (
	SELECT p.description FROM postings p WHERE p.job_id = j.id AND p.description IS NOT NULL
	ORDER BY p.closed_at IS NOT NULL, p.first_seen_at DESC, p.id DESC LIMIT 1), '')`

// jobVia is the search source a job came through when it has no posting
// from a board, or ”.
const jobVia = `CASE WHEN EXISTS (SELECT 1 FROM postings p WHERE p.job_id = j.id AND p.search = 0) THEN ''
		            ELSE coalesce((SELECT p.source FROM postings p WHERE p.job_id = j.id AND p.search = 1 LIMIT 1), '') END`

// PrefilterJobs returns every job of the company, or of every company when
// companyID is 0. A feed or search job's description is known once any of
// its postings had one stored; jobs from agents and people carry what they
// were given.
func (s *Store) PrefilterJobs(ctx context.Context, companyID int64) ([]PrefilterJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.id, j.company_id, c.name, j.title, j.workplace, j.locations, j.pay_ranges,
		       coalesce(j.employment_type, ''), `+jobDescription+`,
		       j.origin NOT IN ('feed', 'search') OR EXISTS (SELECT 1 FROM postings p WHERE p.job_id = j.id AND p.detail_fetched_at IS NOT NULL),
		       j.closed_at IS NOT NULL, j.status, coalesce(j.prefilter_status, ''), j.prefilter_reasons,
		       coalesce(j.prefilter_score, 0), coalesce(j.filters_version, 0), `+jobVia+`
		FROM jobs j JOIN companies c ON c.id = j.company_id
		WHERE ? = 0 OR j.company_id = ?
		ORDER BY j.id`, companyID, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PrefilterJob
	for rows.Next() {
		var (
			j                         PrefilterJob
			locs, pay, status, reason string
		)
		if err := rows.Scan(&j.ID, &j.CompanyID, &j.Company, &j.Title, &j.Workplace, &locs, &pay,
			&j.EmploymentType, &j.Description, &j.DescriptionKnown,
			&j.Closed, &status, &j.PrefilterStatus, &reason, &j.Score, &j.FiltersVersion, &j.Via); err != nil {
			return nil, err
		}
		j.Locations, j.PayRanges, j.Status, j.Reasons = json.RawMessage(locs), json.RawMessage(pay), JobStatus(status), json.RawMessage(reason)
		out = append(out, j)
	}
	return out, rows.Err()
}

// PrefilterDecision is the prefilter's result for one job.
type PrefilterDecision struct {
	JobID   int64
	Status  string // pass | drop | pending
	Reasons json.RawMessage
	Score   float64
}

// PrefilterOutcome says which jobs a prefilter run moved.
type PrefilterOutcome struct {
	Changed     int     // jobs whose decision, reasons or score changed
	Passed      []int64 // moved to unscreened
	FilteredOut []int64 // moved to filtered_out
	Waiting     []int64 // moved back to new, waiting for a description
}

// ApplyPrefilter records decisions made with filters version. Jobs that
// haven't been screened move with the decision: pass makes them
// unscreened, drop makes them filtered_out, and pending returns them to
// new. Jobs an agent added, and jobs further along, keep their status; their decision is still
// recorded, so the UI can say a job no longer passes. Unchanged decisions
// write nothing.
func (s *Store) ApplyPrefilter(ctx context.Context, by Actor, version int64, ds []PrefilterDecision) (PrefilterOutcome, error) {
	var out PrefilterOutcome
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = applyPrefilter(ctx, tx, s.stamp(), by, version, ds)
		return err
	})
	return out, err
}

func applyPrefilter(ctx context.Context, tx *sql.Tx, now string, by Actor, version int64, ds []PrefilterDecision) (PrefilterOutcome, error) {
	var out PrefilterOutcome
	for _, d := range ds {
		var (
			status, prev, reasons, origin string
			score                         float64
			prevVersion                   int64
			override                      bool
		)
		err := tx.QueryRowContext(ctx, `
			SELECT status, coalesce(prefilter_status, ''), prefilter_reasons, coalesce(prefilter_score, 0), coalesce(filters_version, 0), origin, prefilter_override
			FROM jobs WHERE id = ?`, d.JobID).Scan(&status, &prev, &reasons, &score, &prevVersion, &origin, &override)
		if errors.Is(err, sql.ErrNoRows) {
			continue // deleted since it was read
		}
		if err != nil {
			return out, err
		}
		newReasons := jsonOr(d.Reasons, "[]")
		to := nextStatus(JobStatus(status), d.Status)
		if origin == "agent" || origin == "manual" || override {
			to = JobStatus(status) // chosen on purpose; the decision is only recorded
		}
		if prev == d.Status && reasons == newReasons && score == d.Score && prevVersion == version && to == JobStatus(status) {
			continue
		}
		out.Changed++
		if _, err := tx.ExecContext(ctx, `
			UPDATE jobs SET prefilter_status = ?, prefilter_reasons = ?, prefilter_score = ?, filters_version = ?, status = ?,
			       updated_at = ?, updated_by = ?
			WHERE id = ?`, d.Status, newReasons, d.Score, version, to, now, by, d.JobID); err != nil {
			return out, err
		}
		if to == JobStatus(status) {
			continue
		}
		switch to {
		case StatusUnscreened:
			out.Passed = append(out.Passed, d.JobID)
		case StatusFilteredOut:
			out.FilteredOut = append(out.FilteredOut, d.JobID)
		case StatusNew:
			out.Waiting = append(out.Waiting, d.JobID)
		}
		if err := insertEvent(ctx, tx, now, by, "status_changed", eventRefs{JobID: d.JobID}, map[string]any{
			"from": status, "to": to, "cause": "prefilter", "filters_version": version,
		}); err != nil {
			return out, err
		}
	}
	return out, nil
}

// nextStatus is where a prefilter decision moves a job. Only jobs nobody
// has screened yet move.
func nextStatus(from JobStatus, decision string) JobStatus {
	switch from {
	case StatusNew, StatusUnscreened, StatusFilteredOut:
	default:
		return from
	}
	switch decision {
	case "pass":
		return StatusUnscreened
	case "drop":
		return StatusFilteredOut
	default:
		return StatusNew
	}
}

// Candidate is a job in a list view.
type Candidate struct {
	ID          int64
	Company     string
	Title       string
	Workplace   string
	Locations   json.RawMessage
	PayRanges   json.RawMessage
	PayRaw      string
	URL         string
	Score       float64
	Reasons     json.RawMessage
	FirstSeenAt time.Time
	// Via names the search source the job came through ("themuse") when it
	// has no posting from a board, so its link goes to that source's page.
	Via string
}

// Candidates returns open jobs with the given status, highest prefilter
// score first, then newest.
func (s *Store) Candidates(ctx context.Context, status JobStatus, limit int) ([]Candidate, error) {
	if !status.Valid() {
		return nil, fmt.Errorf("unknown job status %q", status)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.id, c.name, j.title, j.workplace, j.locations, j.pay_ranges, coalesce(j.pay_raw, ''), coalesce(j.url, ''),
		       coalesce(j.prefilter_score, 0), j.prefilter_reasons, j.first_seen_at, `+jobVia+`
		FROM jobs j JOIN companies c ON c.id = j.company_id
		WHERE j.status = ? AND j.closed_at IS NULL AND j.hidden_at IS NULL
		ORDER BY j.prefilter_score DESC, j.first_seen_at DESC, j.id DESC
		LIMIT ?`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var (
			c                  Candidate
			locs, pay, reasons string
			firstSeen          sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.Company, &c.Title, &c.Workplace, &locs, &pay, &c.PayRaw, &c.URL,
			&c.Score, &reasons, &firstSeen, &c.Via); err != nil {
			return nil, err
		}
		c.Locations, c.PayRanges, c.Reasons = json.RawMessage(locs), json.RawMessage(pay), json.RawMessage(reasons)
		if c.FirstSeenAt, err = parseTime(firstSeen); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StatusCounts returns how many open jobs have each status.
func (s *Store) StatusCounts(ctx context.Context) (map[JobStatus]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, count(*) FROM jobs WHERE closed_at IS NULL AND hidden_at IS NULL GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[JobStatus]int{}
	for rows.Next() {
		var (
			st string
			n  int
		)
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[JobStatus(st)] = n
	}
	return out, rows.Err()
}
