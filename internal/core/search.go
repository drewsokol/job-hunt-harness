package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/prefilter"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Default search limits: half The Muse's hourly limit of 500 requests, so
// a second refresh in the same hour still works, and a few lookups.
const (
	DefaultMaxSearchRequests = 250
	DefaultMaxLookups        = 25
)

// MaxSearchAge is how old a search result can be and still be stored. The
// Muse keeps some listings for years; a job published longer ago than this
// is most likely filled.
const MaxSearchAge = 90 * 24 * time.Hour

// SearchSummary is what one search source did in a refresh.
type SearchSummary struct {
	Source   string // the searcher's name: "themuse" or "hackernews"
	Title    string // its name for people: "The Muse"
	Queries  int    // The Muse's queries
	Threads  int    // Hacker News threads read
	Requests int
	Postings int // distinct postings found, old ones left out
	Old      int // published more than MaxSearchAge ago, so skipped
	Covered  int // skipped because jhh reads the employer's board
	// Unreadable counts Hacker News ads whose employer couldn't be read.
	Unreadable int
	// Incomplete names the queries that have more pages than they read, or
	// whose reading failed ("themuse.queries[0]", "hackernews"). Until
	// everything is read in full, postings missing from the results aren't
	// closed.
	Incomplete []string
	// Stopped says why searching stopped before the last query, if it did.
	Stopped string
	// Gaps describe what a source read only for what changed (Himalayas)
	// skipped, when a query ran out of requests before its stopping point.
	Gaps    []string
	Closed  int // search postings closed
	Lookups int
}

// Complete reports whether everything was read in full.
func (s SearchSummary) Complete() bool { return len(s.Incomplete) == 0 && s.Stopped == "" }

type searchResult struct {
	summaries []SearchSummary
	lookups   int // across every source, within MaxLookups
	out       store.FetchOutcome
	prefilter []store.PrefilterOutcome
	cleared   int
	errors    []store.FetchError
}

// found collects one source's results before they are stored.
type found struct {
	postings map[string]store.PostingInput
	employer map[string]string // posting id → employer name
	order    []string
	old      map[string]bool
	oldest   time.Time
}

func newFound() *found {
	return &found{postings: map[string]store.PostingInput{}, employer: map[string]string{}, old: map[string]bool{},
		oldest: utc.Now().Add(-MaxSearchAge)}
}

func (f *found) add(s sources.Searcher, ps []sources.Posting) {
	for _, p := range ps {
		in := NormalizeFound(s, p)
		if !p.PostedAt.IsZero() && p.PostedAt.Before(f.oldest) {
			f.old[in.ID] = true
			continue
		}
		if _, dup := f.postings[in.ID]; !dup {
			f.postings[in.ID] = in
			f.employer[in.ID] = p.Company
			f.order = append(f.order, in.ID)
		}
	}
}

// search runs every search source that is on, stores what they found at
// each employer jhh doesn't read a board for, and closes what a source no
// longer returns when it was read in full. A source that is off isn't
// read, so nothing of it is closed either.
func (r *Refresher) search(ctx context.Context, filters *prefilter.Compiled, version int64) (searchResult, error) {
	var sr searchResult
	plan, err := plannedSearches(ctx, r.Store)
	if err != nil {
		return sr, err
	}
	run := func(s sources.Searcher, onlyChanges bool, read func() (SearchSummary, *found, error)) error {
		errs := len(sr.errors)
		sum, fd, err := read()
		if err == nil {
			err = r.storeSearch(ctx, &sr, s, &sum, fd, filters, version, !onlyChanges)
		}
		sr.summaries = append(sr.summaries, sum)
		if err != nil {
			return err
		}
		last := store.SourceRun{At: utc.Now(), Postings: sum.Postings}
		if len(sr.errors) > errs {
			last.Error = sr.errors[errs].Error
		}
		return r.Store.RecordSourceRun(ctx, s.Name(), last)
	}
	if s, ok := r.Searchers["themuse"].(sources.QuerySearcher); ok && plan.muse != nil {
		if err := run(s, false, func() (SearchSummary, *found, error) { return r.searchMuse(ctx, &sr, s, plan.muse.Queries) }); err != nil {
			return sr, err
		}
	}
	if s, ok := r.Searchers["hackernews"].(sources.ThreadSearcher); ok && plan.hackerNews != nil {
		months := plan.hackerNews.Months
		if err := run(s, false, func() (SearchSummary, *found, error) { return r.searchThreads(ctx, &sr, s, months) }); err != nil {
			return sr, err
		}
	}
	if err := r.runHimalayas(ctx, &sr, plan, run); err != nil {
		return sr, err
	}
	return sr, nil
}

func (r *Refresher) failed(sr *searchResult, s sources.Searcher, err error) {
	sr.errors = append(sr.errors, store.FetchError{Company: sources.SearchTitle(s.Name()), Error: err.Error()})
}

// searchMuse reads every page of every query, within the request budget.
func (r *Refresher) searchMuse(ctx context.Context, sr *searchResult, s sources.QuerySearcher, qs []MuseQuery) (SearchSummary, *found, error) {
	sum := SearchSummary{Source: s.Name(), Title: sources.SearchTitle(s.Name()), Queries: len(qs)}
	budget := cmpOr(r.MaxSearchRequests, DefaultMaxSearchRequests)
	fd := newFound()
queries:
	for i, q := range qs {
		label := fmt.Sprintf("themuse.queries[%d]", i)
		for n := 0; q.Pages == 0 || n < q.Pages; n++ {
			if sum.Requests >= budget {
				sum.Stopped = fmt.Sprintf("reached the limit of %d search requests a refresh", budget)
				break queries
			}
			sum.Requests++
			page, err := s.Search(ctx, museQuery(q), n)
			if err != nil {
				if ctx.Err() != nil {
					return sum, fd, ctx.Err()
				}
				r.failed(sr, s, fmt.Errorf("%s: %w", label, err))
				if errors.Is(err, sources.ErrRateLimited) {
					sum.Stopped = "The Muse's hourly request limit was reached"
					break queries
				}
				break // the rest of this query waits for the next refresh
			}
			fd.add(s, page.Postings)
			if n+1 >= page.Pages {
				continue queries
			}
		}
		sum.Incomplete = append(sum.Incomplete, label)
	}
	return sum, fd, nil
}

// searchThreads reads the newest monthly hiring threads in full.
func (r *Refresher) searchThreads(ctx context.Context, sr *searchResult, s sources.ThreadSearcher, months int) (SearchSummary, *found, error) {
	const label = "hackernews"
	sum := SearchSummary{Source: s.Name(), Title: sources.SearchTitle(s.Name())}
	fd := newFound()
	sum.Requests++
	threads, err := s.Threads(ctx, months)
	if err != nil {
		if ctx.Err() != nil {
			return sum, fd, ctx.Err()
		}
		r.failed(sr, s, err)
		sum.Incomplete = append(sum.Incomplete, label)
		return sum, fd, nil
	}
	for _, t := range threads {
		sum.Requests++
		res, err := s.ReadThread(ctx, t.ID)
		if err != nil {
			if ctx.Err() != nil {
				return sum, fd, ctx.Err()
			}
			r.failed(sr, s, err)
			sum.Incomplete = append(sum.Incomplete, label)
			return sum, fd, nil
		}
		sum.Threads++
		sum.Unreadable += res.Unreadable
		fd.add(s, res.Postings)
	}
	return sum, fd, nil
}

// storeSearch stores one source's results under their employers, then,
// with closeMissing, closes what the source no longer returns if it was
// read in full. A source read only for what changed closes by other rules.
func (r *Refresher) storeSearch(ctx context.Context, sr *searchResult, s sources.Searcher, sum *SearchSummary, fd *found,
	filters *prefilter.Compiled, version int64, closeMissing bool) error {
	sum.Postings, sum.Old = len(fd.order), len(fd.old)
	names := make([]string, 0, len(fd.order))
	for _, id := range fd.order {
		names = append(names, fd.employer[id])
	}
	employers, err := r.Store.Employers(ctx, r.By, "search", names)
	if err != nil {
		return err
	}
	byCompany := map[int64][]store.PostingInput{}
	var companies []int64
	seen := map[string]bool{}
	for _, id := range fd.order {
		e, ok := employers[fd.employer[id]]
		if !ok {
			continue // a name with nothing to match on
		}
		if e.Covered {
			sum.Covered++
			continue
		}
		seen[id] = true
		if _, ok := byCompany[e.CompanyID]; !ok {
			companies = append(companies, e.CompanyID)
		}
		byCompany[e.CompanyID] = append(byCompany[e.CompanyID], fd.postings[id])
	}
	for _, id := range companies {
		if err := r.storeFound(ctx, sr, id, s.Name(), byCompany[id], filters, version); err != nil {
			return err
		}
	}

	if !closeMissing || !sum.Complete() {
		return nil
	}
	missing, err := r.Store.CloseSearchMissing(ctx, r.By, s.Name(), seen)
	if err != nil {
		return err
	}
	sr.out.ClosedJobs = append(sr.out.ClosedJobs, missing.ClosedJobs...)
	sum.Closed += len(missing.ClosedJobs)
	return r.lookup(ctx, sr, sum, s, missing.Check)
}

// storeFound stores one employer's search results the way a board listing
// with descriptions is stored: without descriptions, then judged, then with
// the descriptions stage 1 wants, then judged again.
func (r *Refresher) storeFound(ctx context.Context, sr *searchResult, companyID int64, source string,
	ps []store.PostingInput, filters *prefilter.Compiled, version int64) error {
	f := store.CompanyFetch{Postings: ps}
	held := holdDescriptions(&f)
	out, err := r.Store.ApplyPostings(ctx, r.By, companyID, f.Postings)
	if err != nil {
		return err
	}
	merge(&sr.out, out)
	judge := func() error {
		pre, cleared, err := runPrefilter(ctx, r.Store, r.By, filters, version, companyID)
		sr.prefilter = append(sr.prefilter, pre)
		sr.cleared += cleared
		return err
	}
	if err := judge(); err != nil {
		return err
	}
	out, n, err := r.storeHeld(ctx, companyID, source, held)
	sr.out.Flags += out.Flags
	if err != nil || n == 0 {
		return err
	}
	return judge()
}

// lookup asks the source about pursued postings it no longer returned, and
// closes the ones it says are gone.
func (r *Refresher) lookup(ctx context.Context, sr *searchResult, sum *SearchSummary, s sources.Searcher, refs []store.DetailRef) error {
	limit := cmpOr(r.MaxLookups, DefaultMaxLookups)
	var gone []string
refs:
	for _, ref := range refs {
		if sr.lookups >= limit {
			break
		}
		sr.lookups++
		sum.Lookups++
		_, err := s.Lookup(ctx, ref.ExternalID)
		switch {
		case err == nil:
		case errors.Is(err, sources.ErrPostingNotFound):
			gone = append(gone, ref.PostingID)
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			r.failed(sr, s, err)
			if errors.Is(err, sources.ErrRateLimited) {
				break refs
			}
		}
	}
	if len(gone) == 0 {
		return nil
	}
	closed, err := r.Store.ClosePostings(ctx, r.By, gone)
	sr.out.ClosedJobs = append(sr.out.ClosedJobs, closed...)
	sum.Closed += len(closed)
	return err
}

func merge(dst *store.FetchOutcome, src store.FetchOutcome) {
	dst.NewPostings += src.NewPostings
	dst.NewJobs = append(dst.NewJobs, src.NewJobs...)
	dst.LinkedJobs = append(dst.LinkedJobs, src.LinkedJobs...)
	dst.ClosedJobs = append(dst.ClosedJobs, src.ClosedJobs...)
	dst.RepostedJobs = append(dst.RepostedJobs, src.RepostedJobs...)
	dst.Flags += src.Flags
}

// SearchRequests is how many requests every search source made.
func (r RefreshResult) SearchRequests() int {
	n := 0
	for _, s := range r.Searches {
		n += s.Requests
	}
	return n
}

// SearchPostings is how many postings every search source found.
func (r RefreshResult) SearchPostings() int {
	n := 0
	for _, s := range r.Searches {
		n += s.Postings
	}
	return n
}
