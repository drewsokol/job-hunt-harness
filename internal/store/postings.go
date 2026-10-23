package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// PostingInput is one posting as refresh read it, already normalized. Text
// fields are untrusted.
type PostingInput struct {
	ID              string // "gh:exampleco:123"
	Source          string
	ExternalID      string
	OpeningKey      string
	CanonicalURL    string
	Fingerprint     string // empty when the description is unknown or too short
	Title           string
	TitleKey        string
	LocationRaw     string
	Workplace       string          // remote | hybrid | onsite | unknown
	Locations       json.RawMessage // []normalize.Place
	URL             string
	PostedAt        time.Time
	SourceUpdatedAt time.Time
	PayRaw          string
	PayRanges       json.RawMessage // []normalize.PayRange
	EmploymentType  string
	// Partial means the listing left out the description (Greenhouse
	// lists): the stored description and pay are kept, and the posting
	// still needs a detail fetch. A complete posting replaces them, even
	// with nothing.
	Partial bool
	// Search means a search source found the posting: its job is created
	// with origin "search", and it isn't closed by a board's list.
	Search bool
	// Chosen is "agent" when an agent added the posting (add_jobs) and
	// "manual" when the user did (the dashboard). Its job is created with
	// that origin, which the prefilter never moves.
	Chosen          string
	Description     string
	DescriptionHTML string // stored only when Flags isn't empty
	Flags           []ContentFlag
}

// ContentFlag is one finding from the untrusted-content rules.
type ContentFlag struct {
	Detector string // "rule:<name>" or "agent:<name>"
	Kind     string // canary | hostile | hidden_text | unclear
	Excerpt  string
}

// PostingState is what refresh needs to know about a stored posting to
// decide whether to fetch its details again.
type PostingState struct {
	JobID           int64
	SourceUpdatedAt time.Time
	DetailFetchedAt time.Time
	Closed          bool
}

// PostingStates returns the stored state of every posting a company has
// from source, by posting id.
func (s *Store) PostingStates(ctx context.Context, companyID int64, source string) (map[string]PostingState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, job_id, source_updated_at, detail_fetched_at, closed_at
		FROM postings WHERE company_id = ? AND source = ?`, companyID, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PostingState{}
	for rows.Next() {
		var (
			id                      string
			jobID                   sql.NullInt64
			updated, detail, closed sql.NullString
			st                      PostingState
		)
		if err := rows.Scan(&id, &jobID, &updated, &detail, &closed); err != nil {
			return nil, err
		}
		st.JobID, st.Closed = jobID.Int64, closed.Valid
		if st.SourceUpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		if st.DetailFetchedAt, err = parseTime(detail); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// CompanyFetch is the result of reading one company's board.
type CompanyFetch struct {
	CompanyID int64
	Source    string
	// Err is set when the fetch failed. Nothing is closed and the stored
	// ETag is kept.
	Err error
	// NotModified means the board hasn't changed since the stored ETag.
	NotModified bool
	ETag        string
	// Postings is every open posting on the board.
	Postings []PostingInput
}

// FetchOutcome is what ApplyFetch changed.
type FetchOutcome struct {
	NewPostings  int
	NewJobs      []int64
	LinkedJobs   []int64 // existing jobs that gained a posting
	ClosedJobs   []int64
	RepostedJobs []int64
	Flags        int
	Suspect      bool
	SuspectWhy   string
}

// Thresholds for the suspect-feed guard (docs/design.md, "Refresh").
const (
	suspectMinPrevious = 10
	repostWindow       = 60 * 24 * time.Hour
)

// ApplyFetch stores one company's fetch result in a single transaction:
// postings are upserted and matched to jobs by the dedupe ladder, postings
// missing from a good fetch are closed (unless the feed looks suspect), jobs
// are updated from their postings, and the company's fetch status is
// recorded.
func (s *Store) ApplyFetch(ctx context.Context, by Actor, f CompanyFetch) (FetchOutcome, error) {
	var out FetchOutcome
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		a := applier{ctx: ctx, tx: tx, now: now, at: s.now(), by: by, out: &out}
		return a.apply(f)
	})
	return out, err
}

type applier struct {
	ctx     context.Context
	tx      *sql.Tx
	now     string
	at      time.Time
	by      Actor
	out     *FetchOutcome
	touched []int64 // jobs whose derived fields need updating
}

func (a *applier) apply(f CompanyFetch) error {
	if f.Err != nil {
		_, err := a.tx.ExecContext(a.ctx, `
			UPDATE companies SET last_fetch_at = ?, last_fetch_ok = 0, last_error = ? WHERE id = ?`,
			a.now, f.Err.Error(), f.CompanyID)
		return err
	}
	if f.NotModified {
		_, err := a.tx.ExecContext(a.ctx, `
			UPDATE companies SET last_fetch_at = ?, last_fetch_ok = 1, last_error = NULL WHERE id = ?`,
			a.now, f.CompanyID)
		return err
	}

	var prevOpen int
	if err := a.tx.QueryRowContext(a.ctx, "SELECT open_postings FROM companies WHERE id = ?", f.CompanyID).Scan(&prevOpen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("company %d: %w", f.CompanyID, ErrNotFound)
		}
		return err
	}
	switch n := len(f.Postings); {
	case n == 0:
		a.out.Suspect, a.out.SuspectWhy = true, "the board listed no postings"
	case prevOpen >= suspectMinPrevious && n < prevOpen/2:
		a.out.Suspect, a.out.SuspectWhy = true, fmt.Sprintf("the board listed %d postings, down from %d", n, prevOpen)
	}

	seen := map[string]bool{}
	for _, p := range f.Postings {
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		if err := a.upsert(f.CompanyID, p); err != nil {
			return fmt.Errorf("posting %s: %w", p.ID, err)
		}
	}
	if !a.out.Suspect {
		if err := a.closeMissing(f.CompanyID, f.Source, seen); err != nil {
			return err
		}
	}
	if err := a.syncTouched(); err != nil {
		return err
	}

	var open int
	if err := a.tx.QueryRowContext(a.ctx, `
		SELECT count(*) FROM postings WHERE company_id = ? AND source = ? AND closed_at IS NULL`,
		f.CompanyID, f.Source).Scan(&open); err != nil {
		return err
	}
	_, err := a.tx.ExecContext(a.ctx, `
		UPDATE companies SET etag = ?, last_fetch_at = ?, last_fetch_ok = 1, last_error = NULL,
		       open_postings = ?, suspect = ?, suspect_reason = ?
		WHERE id = ?`,
		nullString(f.ETag), a.now, open, a.out.Suspect, nullString(a.out.SuspectWhy), f.CompanyID)
	return err
}

func (a *applier) syncTouched() error {
	slices.Sort(a.touched)
	for _, id := range slices.Compact(a.touched) {
		if err := a.syncJob(id); err != nil {
			return fmt.Errorf("job %d: %w", id, err)
		}
	}
	return nil
}

// ApplyPostings stores postings without treating them as a board's whole
// list: descriptions read from a board's detail endpoint or held back from
// a listing, and postings found by search. Only those postings and their
// jobs change; nothing is closed and the company's fetch status is left
// alone.
func (s *Store) ApplyPostings(ctx context.Context, by Actor, companyID int64, ps []PostingInput) (FetchOutcome, error) {
	var out FetchOutcome
	err := s.write(ctx, func(tx *sql.Tx) error {
		a := applier{ctx: ctx, tx: tx, now: s.stamp(), at: s.now(), by: by, out: &out}
		for _, p := range ps {
			if err := a.upsert(companyID, p); err != nil {
				return fmt.Errorf("posting %s: %w", p.ID, err)
			}
		}
		return a.syncTouched()
	})
	return out, err
}

// DetailRef names a posting whose description should be fetched.
type DetailRef struct {
	PostingID  string
	ExternalID string
}

// PostingsWantingDescription returns the ids of the company's open postings
// from source whose descriptions are worth storing: their jobs passed stage
// 1, wait on stage 2, or have been acted on.
func (s *Store) PostingsWantingDescription(ctx context.Context, companyID int64, source string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id FROM postings p JOIN jobs j ON j.id = p.job_id
		WHERE p.company_id = ? AND p.source = ? AND p.closed_at IS NULL
		  AND (j.prefilter_status IN ('pass', 'pending') OR j.status NOT IN ('new', 'filtered_out'))`, companyID, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// PostingsNeedingDetail returns the company's open postings from source
// whose jobs passed stage 1 (status pass or pending) and whose description
// was never fetched or is older than the board's last update.
func (s *Store) PostingsNeedingDetail(ctx context.Context, companyID int64, source string) ([]DetailRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.external_id FROM postings p JOIN jobs j ON j.id = p.job_id
		WHERE p.company_id = ? AND p.source = ? AND p.closed_at IS NULL
		  AND j.prefilter_status IN ('pass', 'pending')
		  AND (p.detail_fetched_at IS NULL OR p.source_updated_at > p.detail_fetched_at)
		ORDER BY p.id`, companyID, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DetailRef
	for rows.Next() {
		var r DetailRef
		if err := rows.Scan(&r.PostingID, &r.ExternalID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// upsert stores a posting and makes sure it belongs to a job.
func (a *applier) upsert(companyID int64, p PostingInput) error {
	postedAt, err := timeText(p.PostedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeText(p.SourceUpdatedAt)
	if err != nil {
		return err
	}
	locations, pay := jsonOr(p.Locations, "[]"), jsonOr(p.PayRanges, "[]")
	workplace := p.Workplace
	if workplace == "" {
		workplace = "unknown"
	}
	hasDetail := !p.Partial

	var jobID sql.NullInt64
	err = a.tx.QueryRowContext(a.ctx, "SELECT job_id FROM postings WHERE id = ?", p.ID).Scan(&jobID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		a.out.NewPostings++
		if _, err := a.tx.ExecContext(a.ctx, `
			INSERT INTO postings (id, company_id, source, external_id, opening_key, canonical_url, fingerprint,
			                      title, title_key, location_raw, workplace, locations, url, posted_at, source_updated_at,
			                      pay_raw, pay_ranges, employment_type, description, description_html_raw, search, detail_fetched_at,
			                      first_seen_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, companyID, p.Source, p.ExternalID, nullString(p.OpeningKey), nullString(p.CanonicalURL), nullString(p.Fingerprint),
			p.Title, p.TitleKey, nullString(p.LocationRaw), workplace, locations, nullString(p.URL), postedAt, updatedAt,
			nullString(p.PayRaw), pay, nullString(p.EmploymentType), nullString(p.Description), nullString(p.DescriptionHTML), p.Search,
			a.detailStamp(hasDetail), a.now, a.now); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		// Description columns are only replaced when this fetch read them.
		if _, err := a.tx.ExecContext(a.ctx, `
			UPDATE postings SET opening_key = ?, canonical_url = ?, title = ?, title_key = ?, location_raw = ?,
			       workplace = ?, locations = ?, url = ?, posted_at = ?, source_updated_at = ?, pay_raw = ?,
			       pay_ranges = CASE WHEN ? = '[]' AND NOT ? THEN pay_ranges ELSE ? END,
			       employment_type = ?, last_seen_at = ?, closed_at = NULL,
			       fingerprint          = CASE WHEN ? THEN ? ELSE fingerprint END,
			       description          = CASE WHEN ? THEN ? ELSE description END,
			       description_html_raw = CASE WHEN ? THEN ? ELSE description_html_raw END,
			       detail_fetched_at    = CASE WHEN ? THEN ? ELSE detail_fetched_at END
			WHERE id = ?`,
			nullString(p.OpeningKey), nullString(p.CanonicalURL), p.Title, p.TitleKey, nullString(p.LocationRaw),
			workplace, locations, nullString(p.URL), postedAt, updatedAt, nullString(p.PayRaw),
			pay, hasDetail, pay,
			nullString(p.EmploymentType), a.now,
			hasDetail, nullString(p.Fingerprint),
			hasDetail, nullString(p.Description),
			hasDetail, nullString(p.DescriptionHTML),
			hasDetail, a.now,
			p.ID); err != nil {
			return err
		}
	}

	for _, fl := range p.Flags {
		res, err := a.tx.ExecContext(a.ctx, `
			INSERT INTO content_flags (posting_id, detector, kind, excerpt, created_at)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			p.ID, fl.Detector, fl.Kind, fl.Excerpt, a.now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			a.out.Flags++
		}
	}

	if jobID.Valid {
		a.touched = append(a.touched, jobID.Int64)
		return nil
	}
	return a.assignJob(companyID, p)
}

func (a *applier) detailStamp(has bool) sql.NullString {
	return sql.NullString{String: a.now, Valid: has}
}

// assignJob links a posting that has no job yet to one, following the
// dedupe ladder (docs/decisions/0003-dedupe-identity-ladder.md), or creates
// a new job.
func (a *applier) assignJob(companyID int64, p PostingInput) error {
	jobID, rule, err := a.match(companyID, p)
	if err != nil {
		return err
	}
	if jobID == 0 {
		origin := "feed"
		switch {
		case p.Chosen != "":
			origin = p.Chosen
		case p.Search:
			origin = "search"
		}
		res, err := a.tx.ExecContext(a.ctx, `
			INSERT INTO jobs (company_id, title, title_key, first_seen_at, origin, created_at, created_by, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			companyID, p.Title, p.TitleKey, a.now, origin, a.now, a.by, a.now, a.by)
		if err != nil {
			return err
		}
		if jobID, err = res.LastInsertId(); err != nil {
			return err
		}
		a.out.NewJobs = append(a.out.NewJobs, jobID)
		if err := insertEvent(a.ctx, a.tx, a.now, a.by, "job_added", eventRefs{JobID: jobID, CompanyID: companyID},
			map[string]string{"origin": origin, "posting": p.ID}); err != nil {
			return err
		}
	} else {
		a.out.LinkedJobs = append(a.out.LinkedJobs, jobID)
		if err := insertEvent(a.ctx, a.tx, a.now, a.by, "posting_linked", eventRefs{JobID: jobID, CompanyID: companyID},
			map[string]string{"posting": p.ID, "rule": rule}); err != nil {
			return err
		}
	}
	if _, err := a.tx.ExecContext(a.ctx, "UPDATE postings SET job_id = ? WHERE id = ?", jobID, p.ID); err != nil {
		return err
	}
	a.touched = append(a.touched, jobID)
	return nil
}

// match finds the job a new posting belongs to, strongest signal first. It
// returns 0 when the posting is a new opening.
func (a *applier) match(companyID int64, p PostingInput) (int64, string, error) {
	one := func(query string, args ...any) (int64, error) {
		var id int64
		err := a.tx.QueryRowContext(a.ctx, query, args...).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return id, err
	}
	if p.OpeningKey != "" {
		id, err := one(`
			SELECT job_id FROM postings
			WHERE company_id = ? AND source = ? AND opening_key = ? AND job_id IS NOT NULL AND id <> ?
			ORDER BY first_seen_at LIMIT 1`, companyID, p.Source, p.OpeningKey, p.ID)
		if err != nil || id != 0 {
			return id, "opening", err
		}
	}
	if p.CanonicalURL != "" {
		id, err := one(`
			SELECT job_id FROM postings
			WHERE canonical_url = ? AND job_id IS NOT NULL AND id <> ?
			ORDER BY first_seen_at LIMIT 1`, p.CanonicalURL, p.ID)
		if err != nil || id != 0 {
			return id, "url", err
		}
	}
	// A search ad may have no title (a Hacker News ad naming no role); its
	// text alone still finds the same ad posted again.
	if p.Fingerprint != "" && (p.TitleKey != "" || p.Search) {
		// An open job first; otherwise the most recently closed one, which
		// makes this posting a repost if it closed recently enough.
		var (
			id     int64
			closed sql.NullString
		)
		err := a.tx.QueryRowContext(a.ctx, `
			SELECT j.id, j.closed_at FROM postings p JOIN jobs j ON j.id = p.job_id
			WHERE p.company_id = ? AND p.title_key = ? AND p.fingerprint = ? AND p.id <> ?
			ORDER BY j.closed_at IS NOT NULL, j.closed_at DESC LIMIT 1`,
			companyID, p.TitleKey, p.Fingerprint, p.ID).Scan(&id, &closed)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", nil
		}
		if err != nil {
			return 0, "", err
		}
		if !closed.Valid {
			return id, "fingerprint", nil
		}
		closedAt, err := parseTime(closed)
		if err != nil {
			return 0, "", err
		}
		if a.at.Sub(closedAt) <= repostWindow {
			return id, "repost", nil
		}
	}
	if !p.Search && p.TitleKey != "" {
		// A board posting of a job that search found first: the same title
		// at the same company, on an open job with only search postings.
		// The board's copy joins that job, so nothing tracked is lost.
		id, err := a.searchCopy(companyID, p.TitleKey)
		if err != nil || id != 0 {
			return id, "search_copy", err
		}
	}
	return 0, "", nil
}

// searchCopy finds the open search job at the company whose title has key,
// or, for a source whose titles name several roles (a Hacker News ad), one
// of whose roles has it.
func (a *applier) searchCopy(companyID int64, key string) (int64, error) {
	rows, err := a.tx.QueryContext(a.ctx, `
		SELECT j.id, j.title, j.title_key,
		       coalesce((SELECT p.source FROM postings p WHERE p.job_id = j.id LIMIT 1), '')
		FROM jobs j
		WHERE j.company_id = ? AND j.origin = 'search' AND j.closed_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM postings p WHERE p.job_id = j.id AND p.search = 0)
		ORDER BY j.id`, companyID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id                      int64
			title, titleKey, source string
		)
		if err := rows.Scan(&id, &title, &titleKey, &source); err != nil {
			return 0, err
		}
		if titleKey == key {
			return id, nil
		}
		if sources.TitlesNameRoles(source) {
			for _, role := range strings.Split(title, normalize.RoleSeparator) {
				if dedupe.TitleKey(role) == key {
					return id, nil
				}
			}
		}
	}
	return 0, rows.Err()
}

// closeMissing closes the company's open postings from source that the
// fetch didn't list.
func (a *applier) closeMissing(companyID int64, source string, seen map[string]bool) error {
	rows, err := a.tx.QueryContext(a.ctx, `
		SELECT id, job_id FROM postings WHERE company_id = ? AND source = ? AND closed_at IS NULL`, companyID, source)
	if err != nil {
		return err
	}
	type gone struct {
		id    string
		jobID sql.NullInt64
	}
	var missing []gone
	for rows.Next() {
		var g gone
		if err := rows.Scan(&g.id, &g.jobID); err != nil {
			rows.Close()
			return err
		}
		if !seen[g.id] {
			missing = append(missing, g)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, g := range missing {
		if _, err := a.tx.ExecContext(a.ctx, "UPDATE postings SET closed_at = ? WHERE id = ?", a.now, g.id); err != nil {
			return err
		}
		if g.jobID.Valid {
			a.touched = append(a.touched, g.jobID.Int64)
		}
	}
	return nil
}

// workplaceRank orders workplaces from most to least flexible; a job with
// a remote posting and an onsite one can be done remotely.
var workplaceRank = map[string]int{"remote": 0, "hybrid": 1, "onsite": 2, "unknown": 3}

// syncJob recomputes a job's listing fields from its postings: places and
// pay are the union over open postings (all postings once the job has
// closed), and the job closes when its last posting closes and reopens,
// marked reposted, when one comes back. The description isn't copied: a
// feed job's description is read from its newest posting (jobDescription).
func (a *applier) syncJob(jobID int64) error {
	rows, err := a.tx.QueryContext(a.ctx, `
		SELECT workplace, locations, pay_raw, pay_ranges, url, posted_at, employment_type, closed_at IS NULL
		FROM postings WHERE job_id = ?
		ORDER BY first_seen_at, id`, jobID)
	if err != nil {
		return err
	}
	type row struct {
		workplace, locations, payRanges string
		payRaw, url, postedAt, emp      sql.NullString
		open                            bool
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.workplace, &r.locations, &r.payRaw, &r.payRanges, &r.url, &r.postedAt, &r.emp, &r.open); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(all) == 0 {
		return nil
	}

	use := all
	var open []row
	for _, r := range all {
		if r.open {
			open = append(open, r)
		}
	}
	if len(open) > 0 {
		use = open
	}
	var (
		workplace       = "unknown"
		locations, pays jsonSet
		payRaws         []string
		url             sql.NullString
		postedAt, emp   sql.NullString
	)
	for _, r := range use {
		if workplaceRank[r.workplace] < workplaceRank[workplace] {
			workplace = r.workplace
		}
		if err := locations.addArray(r.locations); err != nil {
			return err
		}
		if err := pays.addArray(r.payRanges); err != nil {
			return err
		}
		if r.payRaw.Valid && !slices.Contains(payRaws, r.payRaw.String) {
			payRaws = append(payRaws, r.payRaw.String)
		}
		if !url.Valid {
			url = r.url
		}
		if !emp.Valid && r.emp.String != "" {
			emp = r.emp
		}
		if r.postedAt.Valid && (!postedAt.Valid || r.postedAt.String < postedAt.String) {
			postedAt = r.postedAt
		}
	}

	var wasClosed bool
	if err := a.tx.QueryRowContext(a.ctx, "SELECT closed_at IS NOT NULL FROM jobs WHERE id = ?", jobID).Scan(&wasClosed); err != nil {
		return err
	}
	isClosed := len(open) == 0
	if _, err := a.tx.ExecContext(a.ctx, `
		UPDATE jobs SET workplace = ?, locations = ?, pay_raw = ?, pay_ranges = ?, url = ?, posted_at = ?, employment_type = ?,
		       closed_at   = CASE WHEN ? THEN coalesce(closed_at, ?) ELSE NULL END,
		       reposted_at = CASE WHEN ? THEN ? ELSE reposted_at END,
		       updated_at = ?, updated_by = ?
		WHERE id = ?`,
		workplace, locations.json(), nullString(strings.Join(payRaws, "; ")), pays.json(), url, postedAt, emp,
		isClosed, a.now,
		wasClosed && !isClosed, a.now,
		a.now, a.by, jobID); err != nil {
		return err
	}
	switch {
	case isClosed && !wasClosed:
		a.out.ClosedJobs = append(a.out.ClosedJobs, jobID)
		return insertEvent(a.ctx, a.tx, a.now, a.by, "job_closed", eventRefs{JobID: jobID}, nil)
	case wasClosed && !isClosed:
		a.out.RepostedJobs = append(a.out.RepostedJobs, jobID)
		return insertEvent(a.ctx, a.tx, a.now, a.by, "job_reposted", eventRefs{JobID: jobID}, nil)
	}
	return nil
}

// jsonSet collects distinct JSON values from arrays, keeping first-seen order.
type jsonSet struct {
	items []json.RawMessage
	seen  map[string]bool
}

func (s *jsonSet) addArray(text string) error {
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		return err
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	for _, v := range arr {
		if k := string(v); !s.seen[k] {
			s.seen[k] = true
			s.items = append(s.items, v)
		}
	}
	return nil
}

func (s *jsonSet) json() string {
	if len(s.items) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(s.items)
	return string(b)
}

// jsonOr returns raw as text, or def when it's empty.
func jsonOr(raw json.RawMessage, def string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return def
	}
	return string(raw)
}

// PostingJob returns the job a posting belongs to.
func (s *Store) PostingJob(ctx context.Context, postingID string) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT job_id FROM postings WHERE id = ?", postingID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !id.Valid {
		return 0, fmt.Errorf("posting %s: %w", postingID, ErrNotFound)
	}
	return id.Int64, err
}
