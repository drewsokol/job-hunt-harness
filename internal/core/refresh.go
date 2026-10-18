// Package core holds jhh's operations, shared by the MCP tools, the web UI
// and the command line. Each operation is written once here; the front ends
// only translate requests and results.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/prefilter"
	"github.com/drewsokol/job-hunt-harness/internal/registry"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/untrusted"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Default limits. Boards are separate sites, so several can be fetched at
// once; details come from one board, so fewer at a time, and at most
// DefaultMaxDetails per company per refresh (the rest wait for the next).
const (
	DefaultConcurrency       = 8
	DefaultDetailConcurrency = 4
	DefaultMaxDetails        = 250
)

// Refresher fetches company boards, stores what changed and runs the
// prefilter.
//
// For boards that list jobs without descriptions (Greenhouse), the
// prefilter's stage 1 decides which descriptions are fetched: after the
// listing is stored, the company's jobs are judged, and descriptions are
// fetched only for postings of jobs that passed or are waiting for one,
// when the posting is new or the board says it changed. With no filters
// set, no descriptions are fetched.
type Refresher struct {
	Store             *store.Store
	Sources           map[string]sources.Source
	Logger            *slog.Logger
	By                store.Actor
	Concurrency       int
	DetailConcurrency int
	MaxDetails        int
}

// RefreshOptions narrows a refresh.
type RefreshOptions struct {
	// CompanyIDs limits the refresh to these companies. Empty means every
	// enabled company.
	CompanyIDs []int64
}

// RefreshResult summarizes a refresh.
type RefreshResult struct {
	RunID        int64
	Companies    int
	NotModified  int
	Postings     int // listed by the boards that changed
	NewPostings  int
	Details      int // detail requests made
	DetailErrors int // detail requests that failed; retried next time
	Deferred     int // details left for the next refresh by MaxDetails
	NewJobs      []int64
	ClosedJobs   []int64
	RepostedJobs []int64
	Flags        int
	Suspect      []string // company names
	Errors       []store.FetchError
	// Filtered is false when no filters are set, so nothing was judged.
	Filtered    bool
	Passed      []int64 // jobs that moved to unscreened
	FilteredOut []int64 // jobs that moved to filtered_out
	Duration    time.Duration
}

// Refresh fetches the companies' boards concurrently and applies each
// company's result in its own transactions, so an interrupted refresh
// leaves consistent data. A board that fails is recorded and skipped; only
// a database error stops the refresh.
func (r *Refresher) Refresh(ctx context.Context, opts RefreshOptions) (RefreshResult, error) {
	start := utc.Now()
	var res RefreshResult
	companies, err := r.companies(ctx, opts)
	if err != nil {
		return res, err
	}
	filters, version, err := ActiveFilters(ctx, r.Store)
	if err != nil {
		return res, err
	}
	res.Filtered = filters != nil
	if res.RunID, err = r.Store.StartFetchRun(ctx); err != nil {
		return res, err
	}

	var mu sync.Mutex
	moves := map[int64]store.JobStatus{} // each job's last prefilter move
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cmpOr(r.Concurrency, DefaultConcurrency))
	for _, c := range companies {
		g.Go(func() error {
			cr, err := r.refreshCompany(gctx, c, filters, version)
			if err != nil {
				return fmt.Errorf("storing %s: %w", c.Name, err)
			}
			mu.Lock()
			defer mu.Unlock()
			fetch, out := cr.fetch, cr.out
			res.Companies++
			res.Details += cr.details
			res.DetailErrors += cr.detailErrors
			res.Deferred += cr.deferred
			switch {
			case fetch.Err != nil:
				res.Errors = append(res.Errors, store.FetchError{CompanyID: c.ID, Company: c.Name, Error: fetch.Err.Error()})
			case fetch.NotModified:
				res.NotModified++
			}
			res.Postings += len(fetch.Postings)
			res.NewPostings += out.NewPostings
			res.NewJobs = append(res.NewJobs, out.NewJobs...)
			res.ClosedJobs = append(res.ClosedJobs, out.ClosedJobs...)
			res.RepostedJobs = append(res.RepostedJobs, out.RepostedJobs...)
			res.Flags += out.Flags
			if out.Suspect {
				res.Suspect = append(res.Suspect, c.Name)
			}
			for _, pre := range cr.prefilter {
				for _, id := range pre.Passed {
					moves[id] = store.StatusUnscreened
				}
				for _, id := range pre.FilteredOut {
					moves[id] = store.StatusFilteredOut
				}
				for _, id := range pre.Waiting {
					moves[id] = store.StatusNew
				}
			}
			r.logger().Debug("refreshed company", "company_id", c.ID, "postings", len(fetch.Postings),
				"new_jobs", len(out.NewJobs), "closed_jobs", len(out.ClosedJobs), "details", cr.details,
				"not_modified", fetch.NotModified, "error", fetch.Err)
			return nil
		})
	}
	runErr := g.Wait()
	for id, st := range moves {
		switch st {
		case store.StatusUnscreened:
			res.Passed = append(res.Passed, id)
		case store.StatusFilteredOut:
			res.FilteredOut = append(res.FilteredOut, id)
		}
	}
	slices.Sort(res.Passed)
	slices.Sort(res.FilteredOut)
	res.Duration = utc.Now().Sub(start)

	// Record the run even when it stopped early, with a fresh context in
	// case ctx was canceled.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.Store.FinishFetchRun(finishCtx, store.FetchRun{
		ID: res.RunID, Companies: res.Companies, Postings: res.Postings,
		NewJobs: len(res.NewJobs), ClosedJobs: len(res.ClosedJobs), Passed: len(res.Passed), Errors: res.Errors,
	}); err != nil && runErr == nil {
		runErr = err
	}
	r.logger().Info("refresh finished", "run", res.RunID, "companies", res.Companies, "not_modified", res.NotModified,
		"postings", res.Postings, "new_jobs", len(res.NewJobs), "closed_jobs", len(res.ClosedJobs),
		"details", res.Details, "passed", len(res.Passed), "errors", len(res.Errors),
		"duration", res.Duration.Round(time.Millisecond))
	return res, runErr
}

// companyResult is what refreshing one company did.
type companyResult struct {
	fetch                           store.CompanyFetch
	out                             store.FetchOutcome
	prefilter                       []store.PrefilterOutcome // in order
	details, detailErrors, deferred int
}

// refreshCompany fetches one company's board and stores it, runs the
// prefilter on its jobs, then fetches the descriptions stage 1 asked for
// and runs the prefilter again on what they changed. Details are also
// fetched after a 304, for jobs still waiting on one.
func (r *Refresher) refreshCompany(ctx context.Context, c store.Company, filters *prefilter.Compiled, version int64) (companyResult, error) {
	var cr companyResult
	src, board, fetch := r.fetch(ctx, c)
	cr.fetch = fetch
	var err error
	if cr.out, err = r.Store.ApplyFetch(ctx, r.By, fetch); err != nil {
		return cr, err
	}
	if fetch.Err != nil || filters == nil {
		return cr, nil
	}
	judge := func() error {
		out, err := runPrefilter(ctx, r.Store, r.By, filters, version, c.ID)
		cr.prefilter = append(cr.prefilter, out)
		return err
	}
	if err := judge(); err != nil {
		return cr, err
	}
	d, ok := src.(sources.Detailer)
	if !ok {
		return cr, nil
	}
	refs, err := r.Store.PostingsNeedingDetail(ctx, c.ID, c.Source)
	if err != nil || len(refs) == 0 {
		return cr, err
	}
	if limit := cmpOr(r.MaxDetails, DefaultMaxDetails); len(refs) > limit {
		cr.deferred, refs = len(refs)-limit, refs[:limit]
	}
	cr.details = len(refs)
	ps, failed, err := r.details(ctx, c, src, d, board, refs)
	cr.detailErrors = failed
	if err != nil || len(ps) == 0 {
		return cr, err
	}
	out, err := r.Store.ApplyDetails(ctx, r.By, c.ID, ps)
	if err != nil {
		return cr, err
	}
	cr.out.Flags += out.Flags
	return cr, judge()
}

func (r *Refresher) companies(ctx context.Context, opts RefreshOptions) ([]store.Company, error) {
	all, err := r.Store.Companies(ctx)
	if err != nil {
		return nil, err
	}
	want := map[int64]bool{}
	for _, id := range opts.CompanyIDs {
		want[id] = true
	}
	var out []store.Company
	for _, c := range all {
		if len(want) > 0 && !want[c.ID] {
			continue
		}
		if len(want) == 0 && (!c.Enabled || c.Source == "none") {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// fetch reads one company's board and normalizes its postings. Errors are
// returned inside the CompanyFetch, for the store to record.
func (r *Refresher) fetch(ctx context.Context, c store.Company) (sources.Source, sources.Board, store.CompanyFetch) {
	f := store.CompanyFetch{CompanyID: c.ID, Source: c.Source}
	src, ok := r.Sources[c.Source]
	if !ok {
		f.Err = fmt.Errorf("no source named %q", c.Source)
		return nil, sources.Board{}, f
	}
	board, err := sources.ParseBoard(c.SourceConfig)
	if err != nil {
		f.Err = err
		return src, board, f
	}
	list, err := src.List(ctx, board, c.ETag)
	if err != nil {
		f.Err = err
		return src, board, f
	}
	if list.NotModified {
		f.NotModified = true
		return src, board, f
	}
	f.ETag = list.ETag
	for _, p := range list.Postings {
		f.Postings = append(f.Postings, Normalize(src, board, p))
	}
	return src, board, f
}

// details fetches full postings concurrently. A failed request is logged
// and counted, and the posting is tried again next refresh.
func (r *Refresher) details(ctx context.Context, c store.Company, src sources.Source, d sources.Detailer,
	board sources.Board, refs []store.DetailRef) ([]store.PostingInput, int, error) {
	out := make([]*store.PostingInput, len(refs))
	var failed atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cmpOr(r.DetailConcurrency, DefaultDetailConcurrency))
	for i, ref := range refs {
		g.Go(func() error {
			p, err := d.Detail(gctx, board, ref.ExternalID)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				failed.Add(1)
				r.logger().Warn("detail fetch failed", "company_id", c.ID, "posting", ref.PostingID, "error", err)
				return nil
			}
			in := Normalize(src, board, p)
			out[i] = &in
			return nil
		})
	}
	err := g.Wait()
	var ps []store.PostingInput
	for _, p := range out {
		if p != nil {
			ps = append(ps, *p)
		}
	}
	return ps, int(failed.Load()), err
}

// Normalize turns a source's posting into what the store keeps: untrusted
// text cleaned, location and pay parsed, dedupe keys computed, and content
// aimed at AI agents flagged. Detection runs on the raw text, before
// cleaning, so hidden content is still there to find.
func Normalize(src sources.Source, board sources.Board, p sources.Posting) store.PostingInput {
	title := strings.TrimSpace(untrusted.Clean(p.Title))
	locRaw := strings.TrimSpace(untrusted.Clean(p.LocationRaw))
	loc := normalize.ParseLocation(locRaw, title, p.Workplace, p.Country)

	text := p.DescriptionText
	if text == "" && p.DescriptionHTML != "" {
		text = normalize.HTMLToText(p.DescriptionHTML)
	}
	text = untrusted.Clean(text)

	pay := p.Pay
	if len(pay) == 0 {
		pay = normalize.ParsePay(untrusted.Clean(p.PayRaw))
	}
	if len(pay) == 0 && text != "" {
		pay = normalize.ParsePay(text)
	}

	var flags []store.ContentFlag
	for _, raw := range []string{p.Title, p.LocationRaw, p.DescriptionHTML + p.DescriptionText} {
		for _, f := range untrusted.Detect(raw) {
			flags = append(flags, store.ContentFlag{Detector: "rule:" + f.Detector, Kind: f.Kind, Excerpt: f.Excerpt})
		}
	}

	in := store.PostingInput{
		ID:              sources.PostingID(src, board, p.ExternalID),
		Source:          src.Name(),
		ExternalID:      p.ExternalID,
		OpeningKey:      p.OpeningKey,
		CanonicalURL:    dedupe.CanonicalURL(p.URL),
		Title:           title,
		TitleKey:        dedupe.TitleKey(title),
		LocationRaw:     locRaw,
		Workplace:       loc.Workplace,
		Locations:       mustJSON(orEmpty(loc.Places)),
		URL:             p.URL,
		PostedAt:        p.PostedAt,
		SourceUpdatedAt: p.UpdatedAt,
		PayRaw:          untrusted.Clean(p.PayRaw),
		PayRanges:       mustJSON(orEmpty(pay)),
		EmploymentType:  untrusted.Clean(p.EmploymentType),
		Partial:         p.Partial,
		Description:     text,
		DescriptionHTML: p.DescriptionHTML,
		Flags:           flags,
	}
	if text != "" {
		in.Fingerprint = dedupe.Fingerprint(text, strings.Split(locRaw, " | ")...)
	}
	return in
}

// SeedRegistry adds the starter companies the first time it runs and
// returns how many it added. Later calls do nothing, so a company the user
// removes stays removed.
func SeedRegistry(ctx context.Context, s *store.Store, srcs map[string]sources.Source) (int, error) {
	const key = "registry_seeded_at"
	if _, done, err := s.Setting(ctx, key); err != nil || done {
		return 0, err
	}
	entries, err := registry.Starter(func(name string) bool { _, ok := srcs[name]; return ok })
	if err != nil {
		return 0, err
	}
	cs := make([]store.NewCompany, 0, len(entries))
	for _, e := range entries {
		cfg, _ := json.Marshal(sources.Board{Slug: e.Slug})
		cs = append(cs, store.NewCompany{Name: e.Name, Source: e.Source, BoardKey: e.BoardKey(), SourceConfig: cfg, Origin: "registry"})
	}
	n, err := s.AddCompanies(ctx, store.ActorSystem, cs)
	if err != nil {
		return 0, err
	}
	return n, s.SetSetting(ctx, store.ActorSystem, key, utc.Format(utc.Now()))
}

func (r *Refresher) logger() *slog.Logger {
	if r.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Logger
}

func cmpOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only called with plain structs
	}
	return b
}
