package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/sources/himalayas"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Himalayas is read only for what changed since the last refresh
// (docs/design.md, "Himalayas"). Each query keeps a stopping point in the
// source's state, and its postings close at their expiry date, or for
// pursued jobs when a lookup says they're gone.

// HimalayasSettings are Himalayas's queries. Its search matches
// descriptions as well as titles, so the filters can't stand in for them.
type HimalayasSettings struct {
	Queries []HimalayasQuery `json:"queries"`
}

// HimalayasQuery is one search of Himalayas: free text, optionally
// narrowed to one country, some seniorities and employment types.
type HimalayasQuery struct {
	Q                string   `json:"q"`
	Country          string   `json:"country,omitempty"`
	Seniority        []string `json:"seniority,omitempty"`
	EmploymentTypes  []string `json:"employment_types,omitempty"`
	ExcludeWorldwide bool     `json:"exclude_worldwide,omitempty"`
}

func (q HimalayasQuery) text() sources.TextQuery {
	return sources.TextQuery{Q: q.Q, Country: q.Country, Seniority: q.Seniority, EmploymentTypes: q.EmploymentTypes, ExcludeWorldwide: q.ExcludeWorldwide}
}

// key names the query in the source's state by what it asks for, so
// reordering or removing queries never moves one's stopping point to
// another.
func (q HimalayasQuery) key() string {
	norm := func(ss []string) []string {
		out := make([]string, 0, len(ss))
		for _, s := range ss {
			out = append(out, strings.ToLower(strings.TrimSpace(s)))
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	k := HimalayasQuery{Q: strings.ToLower(strings.Join(strings.Fields(q.Q), " ")), Country: strings.ToLower(strings.TrimSpace(q.Country)),
		Seniority: norm(q.Seniority), EmploymentTypes: norm(q.EmploymentTypes), ExcludeWorldwide: q.ExcludeWorldwide}
	return string(mustJSON(k))
}

// Himalayas limits. Without a key every caller shares one allowance, so a
// refresh asks little of it; a key gives the account its own.
const (
	HimalayasRequests        = 50
	HimalayasRequestsWithKey = 250
	himalayasFirstRead       = 24 * time.Hour
	// himalayasCacheOverlap reaches back past Himalayas's hour-long edge
	// cache, so a job updated just before a cached page was built is still
	// read next time.
	himalayasCacheOverlap = time.Hour
	maxTextQueryLen       = 200
)

func checkHimalayas(raw json.RawMessage) (any, []string, string) {
	s := HimalayasSettings{Queries: []HimalayasQuery{}}
	if p := decodeSettings(raw, &s); p != nil {
		return nil, p, ""
	}
	if s.Queries == nil {
		s.Queries = []HimalayasQuery{}
	}
	var p []string
	add := func(format string, args ...any) { p = append(p, fmt.Sprintf(format, args...)) }
	if n := len(s.Queries); n > maxQueries {
		add("settings.queries: %d queries is too many; the limit is %d", n, maxQueries)
	}
	for i, q := range s.Queries {
		field := fmt.Sprintf("settings.queries[%d]", i)
		if t := strings.TrimSpace(q.Q); t == "" || len(t) > maxTextQueryLen {
			add("%s.q: must be 1 to %d characters: the words to search for (Himalayas matches them in titles and descriptions)", field, maxTextQueryLen)
		}
		if strings.Contains(q.Country, ",") || len(q.Country) > maxQueryValueLen {
			add("%s.country: one country, as a code (\"US\") or a name; use one query per country", field)
		}
		for _, list := range []struct {
			name   string
			values []string
		}{{"seniority", q.Seniority}, {"employment_types", q.EmploymentTypes}} {
			if len(list.values) > maxQueryValues {
				add("%s.%s: more than %d values", field, list.name, maxQueryValues)
			}
		}
		for _, problem := range himalayas.Check(q.text()) {
			add("%s.%s", field, problem)
		}
	}
	if len(p) > 0 {
		return nil, p, ""
	}
	setup := ""
	if len(s.Queries) == 0 {
		setup = "needs at least one query: Himalayas searches by the words you give it, in titles and descriptions"
	}
	return s, nil, setup
}

// himalayasState is jhh's record of how far each query was read.
type himalayasState struct {
	// Stops are where each query's next read stops, by HimalayasQuery.key.
	Stops map[string]time.Time `json:"stops,omitempty"`
	// Complete says the query's last read reached its stopping point.
	Complete map[string]bool `json:"complete,omitempty"`
	// KeyRefusedAt is when Himalayas last refused the saved key.
	KeyRefusedAt time.Time `json:"key_refused_at,omitzero"`
}

func readHimalayasState(raw json.RawMessage) himalayasState {
	var st himalayasState
	_ = json.Unmarshal(raw, &st) // written by jhh; a state that doesn't decode starts over
	if st.Stops == nil {
		st.Stops = map[string]time.Time{}
	}
	if st.Complete == nil {
		st.Complete = map[string]bool{}
	}
	return st
}

// KeyChanged records that a source's optional key was saved or removed,
// and clears any note that the service refused the old one.
func KeyChanged(ctx context.Context, s *store.Store, by store.Actor, name string, saved bool) error {
	if err := s.KeyChanged(ctx, by, name, saved); err != nil {
		return err
	}
	rows, err := s.SourceRows(ctx)
	if err != nil {
		return err
	}
	row, ok := rows[name]
	if !ok {
		return nil
	}
	st := readHimalayasState(row.State)
	if st.KeyRefusedAt.IsZero() {
		return nil
	}
	st.KeyRefusedAt = time.Time{}
	return s.SetSourceState(ctx, name, mustJSON(st))
}

// keyHolder is a search source that can use an optional key.
type keyHolder interface{ HasKey() bool }

// searchRecent reads each query newest first down to its stopping point,
// within Himalayas's requests for the refresh, shared evenly among the
// queries.
func (r *Refresher) searchRecent(ctx context.Context, sr *searchResult, s sources.RecentSearcher, qs []HimalayasQuery, st *himalayasState) (SearchSummary, *found, error) {
	sum := SearchSummary{Source: s.Name(), Title: sources.SearchTitle(s.Name()), Queries: len(qs)}
	fd := newFound()
	keyed := false
	if k, ok := s.(keyHolder); ok && k.HasKey() {
		keyed = true
	}
	budget := HimalayasRequests
	if keyed {
		budget = HimalayasRequestsWithKey
	}
	if r.MaxRecentRequests > 0 {
		budget = r.MaxRecentRequests
	}
	now := utc.Now()
	used := 0
queries:
	for i, q := range qs {
		label := fmt.Sprintf("himalayas.queries[%d]", i)
		key := q.key()
		stop, had := st.Stops[key]
		if !had {
			stop = now.Add(-himalayasFirstRead)
		}
		share := (budget - used) / (len(qs) - i)
		var newest, oldest time.Time
		reached, failed := false, false
		mine := 0
		for page := 1; !reached; page++ {
			if mine >= share {
				break
			}
			mine++
			used++
			sum.Requests++
			res, err := s.SearchRecent(ctx, q.text(), page)
			if errors.Is(err, sources.ErrKeyRefused) && !sources.Keyless(ctx) {
				// The key no longer works: carry on without it, within the
				// keyless share, and say so.
				ctx = sources.WithoutKey(ctx)
				st.KeyRefusedAt = now
				budget = min(budget, HimalayasRequests)
				share = max(0, min(share, (budget-used+mine)/(len(qs)-i)))
				page--
				continue
			}
			if err != nil {
				if ctx.Err() != nil {
					return sum, fd, ctx.Err()
				}
				r.failed(sr, s, fmt.Errorf("%s: %w", label, err))
				if errors.Is(err, sources.ErrRateLimited) {
					// Not the query's fault: this query and the ones not
					// reached keep their stopping points.
					sum.Stopped = "Himalayas's request limit was reached"
					break queries
				}
				failed = true
				break
			}
			if keyed && !sources.Keyless(ctx) {
				st.KeyRefusedAt = time.Time{}
			}
			fd.add(s, res.Postings)
			var pageOldest time.Time
			for _, p := range res.Postings {
				t := p.UpdatedAt
				if t.After(newest) {
					newest = t
				}
				if pageOldest.IsZero() || t.Before(pageOldest) {
					pageOldest = t
				}
			}
			if !pageOldest.IsZero() {
				oldest = pageOldest
			}
			reached = page >= res.Pages || (!pageOldest.IsZero() && pageOldest.Before(stop))
		}
		if failed {
			sum.Incomplete = append(sum.Incomplete, label)
			continue
		}
		// The next read stops at the newest job seen, less the cache's hour,
		// whether or not this one got all the way down.
		next := stop
		if n := newest.Add(-himalayasCacheOverlap); n.After(next) {
			next = n
		}
		if !reached {
			sum.Incomplete = append(sum.Incomplete, label)
			gap := fmt.Sprintf("%s ran out of requests: jobs last updated between %s and %s were skipped", label,
				utc.Format(stop), utc.Format(oldest))
			if st.Complete[key] {
				gap += "; it finds more new jobs than one refresh can read, so narrow it"
			}
			sum.Gaps = append(sum.Gaps, gap)
		}
		st.Stops[key], st.Complete[key] = next, reached
	}
	// Forget queries that are gone.
	keep := map[string]bool{}
	for _, q := range qs {
		keep[q.key()] = true
	}
	for k := range st.Stops {
		if !keep[k] {
			delete(st.Stops, k)
			delete(st.Complete, k)
		}
	}
	return sum, fd, nil
}

// closeRecent closes a source's postings past their expiry date. While the
// source is on, pursued postings are looked up instead, and closed only
// when the source says they're gone; a lookup that finds one records its
// new expiry date. While it's off, nothing is looked up, and pursued
// postings close at their stored expiry like the rest.
func (r *Refresher) closeRecent(ctx context.Context, sr *searchResult, sum *SearchSummary, s sources.Searcher, on bool, since time.Time) error {
	closed, err := r.Store.CloseExpired(ctx, r.By, s.Name(), !on)
	sr.out.ClosedJobs = append(sr.out.ClosedJobs, closed...)
	if sum != nil {
		sum.Closed += len(closed)
	}
	if err != nil || !on {
		return err
	}
	limit := cmpOr(r.MaxLookups, DefaultMaxLookups)
	refs, err := r.Store.PursuedToCheck(ctx, s.Name(), since, limit-sr.lookups)
	if err != nil || len(refs) == 0 {
		return err
	}
	var gone []string
	for _, ref := range refs {
		if sr.lookups >= limit {
			break
		}
		sr.lookups++
		sum.Lookups++
		p, err := s.Lookup(ctx, ref.ExternalID)
		switch {
		case err == nil:
			if err := r.Store.SeenPosting(ctx, ref.PostingID, p.ExpiresAt); err != nil {
				return err
			}
			continue
		case errors.Is(err, sources.ErrPostingNotFound):
			gone = append(gone, ref.PostingID)
			continue
		case ctx.Err() != nil:
			return ctx.Err()
		}
		r.failed(sr, s, err)
		if errors.Is(err, sources.ErrRateLimited) {
			break
		}
	}
	if len(gone) == 0 {
		return nil
	}
	closed, err = r.Store.ClosePostings(ctx, r.By, gone)
	sr.out.ClosedJobs = append(sr.out.ClosedJobs, closed...)
	sum.Closed += len(closed)
	return err
}

// runHimalayas searches Himalayas when it's on and closes its expired
// postings either way.
func (r *Refresher) runHimalayas(ctx context.Context, sr *searchResult, plan searchPlan, run func(sources.Searcher, bool, func() (SearchSummary, *found, error)) error) error {
	s, ok := r.Searchers["himalayas"].(sources.RecentSearcher)
	if !ok {
		return nil
	}
	since := utc.Now()
	if plan.himalayas == nil {
		return r.closeRecent(ctx, sr, nil, s, false, since)
	}
	st := readHimalayasState(plan.himalayasState)
	var sum *SearchSummary
	err := run(s, true, func() (SearchSummary, *found, error) {
		res, fd, err := r.searchRecent(ctx, sr, s, plan.himalayas.Queries, &st)
		return res, fd, err
	})
	if err != nil {
		return err
	}
	sum = &sr.summaries[len(sr.summaries)-1]
	if err := r.Store.SetSourceState(ctx, s.Name(), mustJSON(st)); err != nil {
		return err
	}
	return r.closeRecent(ctx, sr, sum, s, true, since)
}
