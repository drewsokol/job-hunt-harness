package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// JobRow is a job as lists show it. Title, Company, Locations, PayRaw,
// PayRanges, EmploymentType, URL and Description come from listings and
// are untrusted.
type JobRow struct {
	ID             int64
	CompanyID      int64
	Company        string
	Title          string
	Status         JobStatus
	Origin         string
	Workplace      string
	Locations      json.RawMessage
	PayRaw         string
	PayRanges      json.RawMessage
	EmploymentType string
	URL            string
	PostingID      string // the posting the URL belongs to, when there is one
	Via            string // the search source, when no board lists the job
	Score          float64
	Reasons        json.RawMessage
	FirstSeenAt    time.Time
	PostedAt       time.Time
	ClosedAt       time.Time
	RepostedAt     time.Time
	AppliedAt      time.Time
	Description    string // only when asked for
	Screening      *Screening
	OpenFlags      []string // kinds of content flags waiting for review
}

// Screening is an agent's fit rating of a job.
type Screening struct {
	ID             int64
	JobID          int64
	Fit            string // strong | good | stretch | no
	Reason         string
	Gaps           string
	Flags          json.RawMessage
	ProfileVersion int64
	ScreenedBy     Actor
	ScreenedAt     time.Time
}

// JobQuery selects jobs for ListJobs.
type JobQuery struct {
	Status          JobStatus
	Fit             string // the latest screening's fit; empty for any
	Offset, Limit   int
	WithDescription bool
}

// listOpenOnly are the statuses whose closed jobs are left out of lists:
// nothing can be done with a closed posting nobody applied to.
var listOpenOnly = map[JobStatus]bool{StatusNew: true, StatusFilteredOut: true, StatusUnscreened: true, StatusReview: true}

const latestScreening = `(SELECT s.id FROM screenings s WHERE s.job_id = j.id ORDER BY s.screened_at DESC, s.id DESC LIMIT 1)`

// ListJobs returns a page of jobs with a status and the total that match.
// Screened jobs come best fit first; the rest by prefilter score, newest
// first.
func (s *Store) ListJobs(ctx context.Context, q JobQuery) ([]JobRow, int, error) {
	if !q.Status.Valid() {
		return nil, 0, fmt.Errorf("unknown job status %q", q.Status)
	}
	where := []string{"j.status = ?", "j.hidden_at IS NULL"}
	args := []any{q.Status}
	if listOpenOnly[q.Status] {
		where = append(where, "j.closed_at IS NULL")
	}
	if q.Fit != "" {
		where = append(where, "sc.fit = ?")
		args = append(args, q.Fit)
	}
	return s.queryJobs(ctx, strings.Join(where, " AND "), args,
		`CASE sc.fit WHEN 'strong' THEN 0 WHEN 'good' THEN 1 WHEN 'stretch' THEN 2 WHEN 'no' THEN 3 ELSE 4 END,
		 j.prefilter_score DESC, j.first_seen_at DESC, j.id DESC`, q.Offset, q.Limit, q.WithDescription)
}

// queryJobs returns the jobs matching where (over jobs j, companies c and
// the latest screening sc), in order, and how many match in all.
func (s *Store) queryJobs(ctx context.Context, where string, args []any, order string, offset, limit int, withDescription bool) ([]JobRow, int, error) {
	from := `FROM jobs j JOIN companies c ON c.id = j.company_id LEFT JOIN screenings sc ON sc.id = ` + latestScreening + `
		WHERE ` + where
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) "+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	desc := "''"
	if withDescription {
		desc = jobDescription
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobRowColumns+`, `+desc+` `+from+`
		ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	out, err := scanJobRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, s.addOpenFlags(ctx, out)
}

// JobDetail is everything about one job.
type JobDetail struct {
	JobRow
	Notes      string
	Postings   []PostingView
	Screenings []Screening // newest first
	Flags      []ContentFlagView
	Events     []Event // the most recent, oldest first
}

// PostingView is one posting of a job. Title, LocationRaw and URL are
// untrusted.
type PostingView struct {
	ID          string
	Source      string
	Search      bool
	Title       string
	LocationRaw string
	URL         string
	FirstSeenAt time.Time
	ClosedAt    time.Time
}

// ContentFlagView is a finding about text in a posting addressed to an AI.
// Excerpt is untrusted.
type ContentFlagView struct {
	ID        int64
	PostingID string
	Detector  string
	Kind      string
	Excerpt   string
	Status    string // open | kept | hidden
}

// maxEvents is how much of a job's timeline JobDetail returns.
const maxEvents = 30

// GetJobDetail returns one job with its postings, screenings, content flags
// and recent timeline.
func (s *Store) GetJobDetail(ctx context.Context, id int64) (JobDetail, error) {
	var d JobDetail
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobRowColumns+`, `+jobDescription+`
		FROM jobs j JOIN companies c ON c.id = j.company_id LEFT JOIN screenings sc ON sc.id = `+latestScreening+`
		WHERE j.id = ?`, id)
	if err != nil {
		return d, err
	}
	rs, err := scanJobRows(rows)
	if err != nil {
		return d, err
	}
	if len(rs) == 0 {
		return d, fmt.Errorf("job %d: %w", id, ErrNotFound)
	}
	d.JobRow = rs[0]
	if err := s.db.QueryRowContext(ctx, "SELECT notes FROM jobs WHERE id = ?", id).Scan(&d.Notes); err != nil {
		return d, err
	}
	if d.Postings, err = s.jobPostings(ctx, id); err != nil {
		return d, err
	}
	if d.Screenings, err = s.screenings(ctx, id); err != nil {
		return d, err
	}
	if d.Flags, err = s.jobFlags(ctx, id); err != nil {
		return d, err
	}
	events, err := s.JobEvents(ctx, id)
	if err != nil {
		return d, err
	}
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	d.Events = events
	for _, f := range d.Flags {
		if f.Status == "open" {
			d.OpenFlags = append(d.OpenFlags, f.Kind)
		}
	}
	return d, nil
}

const jobRowColumns = `j.id, j.company_id, c.name, j.title, j.status, j.origin, j.workplace, j.locations,
	coalesce(j.pay_raw, ''), j.pay_ranges, coalesce(j.employment_type, ''), coalesce(j.url, ''),
	coalesce((SELECT p.id FROM postings p WHERE p.job_id = j.id AND p.url = j.url ORDER BY p.closed_at IS NOT NULL LIMIT 1), ''),
	CASE WHEN EXISTS (SELECT 1 FROM postings p WHERE p.job_id = j.id AND p.search = 0) THEN ''
	     ELSE coalesce((SELECT p.source FROM postings p WHERE p.job_id = j.id AND p.search = 1 LIMIT 1), '') END,
	coalesce(j.prefilter_score, 0), j.prefilter_reasons, j.first_seen_at, j.posted_at, j.closed_at, j.reposted_at, j.applied_at,
	sc.id, sc.fit, sc.reason, sc.gaps, sc.flags, sc.profile_version, sc.screened_by, sc.screened_at`

func scanJobRows(rows *sql.Rows) ([]JobRow, error) {
	defer rows.Close()
	var out []JobRow
	for rows.Next() {
		var (
			r                                      JobRow
			status, locs, pay, reasons             string
			first, posted, closed, repost, applied sql.NullString
			scID, scVersion                        sql.NullInt64
			fit, reason, gaps, flags, by, at       sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.CompanyID, &r.Company, &r.Title, &status, &r.Origin, &r.Workplace, &locs,
			&r.PayRaw, &pay, &r.EmploymentType, &r.URL, &r.PostingID, &r.Via,
			&r.Score, &reasons, &first, &posted, &closed, &repost, &applied,
			&scID, &fit, &reason, &gaps, &flags, &scVersion, &by, &at, &r.Description); err != nil {
			return nil, err
		}
		r.Status = JobStatus(status)
		r.Locations, r.PayRanges, r.Reasons = json.RawMessage(locs), json.RawMessage(pay), json.RawMessage(reasons)
		var err error
		for _, t := range []struct {
			dst *time.Time
			src sql.NullString
		}{{&r.FirstSeenAt, first}, {&r.PostedAt, posted}, {&r.ClosedAt, closed}, {&r.RepostedAt, repost}, {&r.AppliedAt, applied}} {
			if *t.dst, err = parseTime(t.src); err != nil {
				return nil, err
			}
		}
		if scID.Valid {
			sc := Screening{ID: scID.Int64, JobID: r.ID, Fit: fit.String, Reason: reason.String, Gaps: gaps.String,
				Flags: json.RawMessage(flags.String), ProfileVersion: scVersion.Int64, ScreenedBy: Actor(by.String)}
			if sc.ScreenedAt, err = parseTime(at); err != nil {
				return nil, err
			}
			r.Screening = &sc
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// addOpenFlags fills in the kinds of open content flags for each row.
func (s *Store) addOpenFlags(ctx context.Context, rs []JobRow) error {
	if len(rs) == 0 {
		return nil
	}
	idx := map[int64]int{}
	ids := make([]any, len(rs))
	for i, r := range rs {
		idx[r.ID], ids[i] = i, r.ID
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT p.job_id, f.kind FROM content_flags f JOIN postings p ON p.id = f.posting_id
		WHERE f.status = 'open' AND p.job_id IN (`+placeholders(len(ids))+`) ORDER BY f.kind`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   int64
			kind string
		)
		if err := rows.Scan(&id, &kind); err != nil {
			return err
		}
		rs[idx[id]].OpenFlags = append(rs[idx[id]].OpenFlags, kind)
	}
	return rows.Err()
}

func (s *Store) jobPostings(ctx context.Context, jobID int64) ([]PostingView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source, search, title, coalesce(location_raw, ''), coalesce(url, ''), first_seen_at, closed_at
		FROM postings WHERE job_id = ? ORDER BY closed_at IS NOT NULL, first_seen_at, id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PostingView
	for rows.Next() {
		var (
			p             PostingView
			first, closed sql.NullString
		)
		if err := rows.Scan(&p.ID, &p.Source, &p.Search, &p.Title, &p.LocationRaw, &p.URL, &first, &closed); err != nil {
			return nil, err
		}
		if p.FirstSeenAt, err = parseTime(first); err != nil {
			return nil, err
		}
		if p.ClosedAt, err = parseTime(closed); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) screenings(ctx context.Context, jobID int64) ([]Screening, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, fit, reason, gaps, flags, coalesce(profile_version, 0), screened_by, screened_at
		FROM screenings WHERE job_id = ? ORDER BY screened_at DESC, id DESC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Screening
	for rows.Next() {
		var (
			sc        = Screening{JobID: jobID}
			flags, by string
			at        sql.NullString
		)
		if err := rows.Scan(&sc.ID, &sc.Fit, &sc.Reason, &sc.Gaps, &flags, &sc.ProfileVersion, &by, &at); err != nil {
			return nil, err
		}
		sc.Flags, sc.ScreenedBy = json.RawMessage(flags), Actor(by)
		if sc.ScreenedAt, err = parseTime(at); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) jobFlags(ctx context.Context, jobID int64) ([]ContentFlagView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.posting_id, f.detector, f.kind, f.excerpt, f.status
		FROM content_flags f JOIN postings p ON p.id = f.posting_id WHERE p.job_id = ? ORDER BY f.id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContentFlagView
	for rows.Next() {
		var f ContentFlagView
		if err := rows.Scan(&f.ID, &f.PostingID, &f.Detector, &f.Kind, &f.Excerpt, &f.Status); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// errNoRows reports whether err means a lookup found nothing.
func errNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
