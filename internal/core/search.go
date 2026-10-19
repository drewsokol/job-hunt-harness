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

// SearchSummary is what the searches in the filters did.
type SearchSummary struct {
	Queries  int
	Requests int
	Postings int // distinct postings the queries returned, old ones left out
	Old      int // published more than MaxSearchAge ago, so skipped
	Covered  int // skipped because jhh reads the employer's board
	// Incomplete names the queries that have more pages than they read, or
	// whose reading failed ("search.themuse[0]"). Until every query is read
	// in full, postings missing from the results aren't closed.
	Incomplete []string
	// Stopped says why searching stopped before the last query, if it did.
	Stopped string
	Closed  int // search postings closed
	Lookups int
}

// Complete reports whether every query was read in full.
func (s SearchSummary) Complete() bool { return len(s.Incomplete) == 0 && s.Stopped == "" }

type searchResult struct {
	summary   SearchSummary
	out       store.FetchOutcome
	prefilter []store.PrefilterOutcome
	cleared   int
	errors    []store.FetchError
}

// searchErrorName is the name failed searches are recorded under.
const searchErrorName = "The Muse"

// search runs the filters' queries against The Muse, stores what they found
// at each employer jhh doesn't read a board for, and closes what the
// queries no longer return when they were read in full.
func (r *Refresher) search(ctx context.Context, filters *prefilter.Compiled, version int64) (searchResult, error) {
	var sr searchResult
	f := filters.Filters()
	s := r.Searchers["themuse"]
	if f.Search == nil || len(f.Search.TheMuse) == 0 || s == nil {
		return sr, nil
	}
	qs := f.Search.TheMuse
	sr.summary.Queries = len(qs)
	budget := cmpOr(r.MaxSearchRequests, DefaultMaxSearchRequests)

	found := map[string]store.PostingInput{}
	old := map[string]bool{}
	oldest := utc.Now().Add(-MaxSearchAge)
	var order []string
	employer := map[string]string{} // posting id → employer name
	failed := func(err error) {
		sr.errors = append(sr.errors, store.FetchError{Company: searchErrorName, Error: err.Error()})
	}
queries:
	for i, q := range qs {
		label := fmt.Sprintf("search.themuse[%d]", i)
		for n := 0; q.Pages == 0 || n < q.Pages; n++ {
			if sr.summary.Requests >= budget {
				sr.summary.Stopped = fmt.Sprintf("reached the limit of %d search requests a refresh", budget)
				break queries
			}
			sr.summary.Requests++
			page, err := s.Search(ctx, museQuery(q), n)
			if err != nil {
				if ctx.Err() != nil {
					return sr, ctx.Err()
				}
				failed(fmt.Errorf("%s: %w", label, err))
				if errors.Is(err, sources.ErrRateLimited) {
					sr.summary.Stopped = "The Muse's hourly request limit was reached"
					break queries
				}
				break // the rest of this query waits for the next refresh
			}
			for _, p := range page.Postings {
				in := NormalizeFound(s, p)
				if !p.PostedAt.IsZero() && p.PostedAt.Before(oldest) {
					old[in.ID] = true
					continue
				}
				if _, dup := found[in.ID]; !dup {
					found[in.ID] = in
					employer[in.ID] = p.Company
					order = append(order, in.ID)
				}
			}
			if n+1 >= page.Pages {
				continue queries
			}
		}
		sr.summary.Incomplete = append(sr.summary.Incomplete, label)
	}
	sr.summary.Postings, sr.summary.Old = len(order), len(old)

	names := make([]string, 0, len(order))
	for _, id := range order {
		names = append(names, employer[id])
	}
	employers, err := r.Store.Employers(ctx, r.By, "search", names)
	if err != nil {
		return sr, err
	}
	byCompany := map[int64][]store.PostingInput{}
	var companies []int64
	seen := map[string]bool{}
	for _, id := range order {
		e, ok := employers[employer[id]]
		if !ok {
			continue // a name with nothing to match on
		}
		if e.Covered {
			sr.summary.Covered++
			continue
		}
		seen[id] = true
		if _, ok := byCompany[e.CompanyID]; !ok {
			companies = append(companies, e.CompanyID)
		}
		byCompany[e.CompanyID] = append(byCompany[e.CompanyID], found[id])
	}
	for _, id := range companies {
		if err := r.storeFound(ctx, &sr, id, s.Name(), byCompany[id], filters, version); err != nil {
			return sr, err
		}
	}

	if !sr.summary.Complete() {
		return sr, nil
	}
	missing, err := r.Store.CloseSearchMissing(ctx, r.By, s.Name(), seen)
	if err != nil {
		return sr, err
	}
	sr.out.ClosedJobs = append(sr.out.ClosedJobs, missing.ClosedJobs...)
	sr.summary.Closed += len(missing.ClosedJobs)
	return sr, r.lookup(ctx, &sr, s, missing.Check)
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

// lookup asks the source about pursued postings no query returned, and
// closes the ones it says are gone.
func (r *Refresher) lookup(ctx context.Context, sr *searchResult, s sources.Searcher, refs []store.DetailRef) error {
	limit := cmpOr(r.MaxLookups, DefaultMaxLookups)
	var gone []string
refs:
	for _, ref := range refs {
		if sr.summary.Lookups >= limit {
			break
		}
		sr.summary.Lookups++
		_, err := s.Lookup(ctx, ref.ExternalID)
		switch {
		case err == nil:
		case errors.Is(err, sources.ErrPostingNotFound):
			gone = append(gone, ref.PostingID)
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			sr.errors = append(sr.errors, store.FetchError{Company: searchErrorName, Error: err.Error()})
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
	sr.summary.Closed += len(closed)
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
