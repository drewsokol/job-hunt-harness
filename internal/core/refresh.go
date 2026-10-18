// Package core holds jhh's operations, shared by the MCP tools, the web UI
// and the command line. Each operation is written once here; the front ends
// only translate requests and results.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/registry"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/untrusted"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Default concurrency limits. Boards are separate sites, so several can be
// fetched at once; details come from one board, so fewer at a time.
const (
	DefaultConcurrency       = 8
	DefaultDetailConcurrency = 4
)

// Refresher fetches company boards and stores what changed.
type Refresher struct {
	Store   *store.Store
	Sources map[string]sources.Source
	Logger  *slog.Logger
	By      store.Actor
	// WantDetail decides whether a listing that omitted its description
	// (Greenhouse) is worth a detail request. It is only asked about new
	// postings and postings the board says changed. Nil fetches no details.
	WantDetail        func(c store.Company, p sources.Posting) bool
	Concurrency       int
	DetailConcurrency int
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
	NewJobs      []int64
	ClosedJobs   []int64
	RepostedJobs []int64
	Flags        int
	Suspect      []string // company names
	Errors       []store.FetchError
	Duration     time.Duration
}

// Refresh fetches the companies' boards concurrently and applies each
// company's result in its own transaction, so an interrupted refresh leaves
// consistent data. A board that fails is recorded and skipped; only a
// database error stops the refresh.
func (r *Refresher) Refresh(ctx context.Context, opts RefreshOptions) (RefreshResult, error) {
	start := utc.Now()
	var res RefreshResult
	companies, err := r.companies(ctx, opts)
	if err != nil {
		return res, err
	}
	if res.RunID, err = r.Store.StartFetchRun(ctx); err != nil {
		return res, err
	}

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cmpOr(r.Concurrency, DefaultConcurrency))
	for _, c := range companies {
		g.Go(func() error {
			fetch, details := r.fetch(gctx, c)
			out, err := r.Store.ApplyFetch(gctx, r.By, fetch)
			if err != nil {
				return fmt.Errorf("storing %s: %w", c.Name, err)
			}
			mu.Lock()
			defer mu.Unlock()
			res.Companies++
			res.Details += details
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
			r.logger().Debug("refreshed company", "company_id", c.ID, "postings", len(fetch.Postings),
				"new_jobs", len(out.NewJobs), "closed_jobs", len(out.ClosedJobs), "not_modified", fetch.NotModified, "error", fetch.Err)
			return nil
		})
	}
	runErr := g.Wait()
	res.Duration = utc.Now().Sub(start)

	// Record the run even when it stopped early, with a fresh context in
	// case ctx was canceled.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.Store.FinishFetchRun(finishCtx, store.FetchRun{
		ID: res.RunID, Companies: res.Companies, Postings: res.Postings,
		NewJobs: len(res.NewJobs), ClosedJobs: len(res.ClosedJobs), Errors: res.Errors,
	}); err != nil && runErr == nil {
		runErr = err
	}
	r.logger().Info("refresh finished", "run", res.RunID, "companies", res.Companies, "not_modified", res.NotModified,
		"postings", res.Postings, "new_jobs", len(res.NewJobs), "closed_jobs", len(res.ClosedJobs),
		"errors", len(res.Errors), "duration", res.Duration.Round(time.Millisecond))
	return res, runErr
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
func (r *Refresher) fetch(ctx context.Context, c store.Company) (store.CompanyFetch, int) {
	f := store.CompanyFetch{CompanyID: c.ID, Source: c.Source}
	src, ok := r.Sources[c.Source]
	if !ok {
		f.Err = fmt.Errorf("no source named %q", c.Source)
		return f, 0
	}
	board, err := sources.ParseBoard(c.SourceConfig)
	if err != nil {
		f.Err = err
		return f, 0
	}
	list, err := src.List(ctx, board, c.ETag)
	if err != nil {
		f.Err = err
		return f, 0
	}
	if list.NotModified {
		f.NotModified = true
		return f, 0
	}
	f.ETag = list.ETag

	postings := list.Postings
	details := 0
	if d, ok := src.(sources.Detailer); ok && r.WantDetail != nil {
		if postings, details, err = r.fillDetails(ctx, c, src, d, board, postings); err != nil {
			f.Err = err
			return f, details
		}
	}
	for _, p := range postings {
		f.Postings = append(f.Postings, Normalize(src, board, p))
	}
	return f, details
}

// fillDetails replaces partial postings with full ones where WantDetail
// says so and the posting is new or changed since its last detail fetch.
// A failed detail request leaves that posting partial.
func (r *Refresher) fillDetails(ctx context.Context, c store.Company, src sources.Source, d sources.Detailer,
	board sources.Board, ps []sources.Posting) ([]sources.Posting, int, error) {
	states, err := r.Store.PostingStates(ctx, c.ID, c.Source)
	if err != nil {
		return nil, 0, err
	}
	var idx []int
	for i, p := range ps {
		if !p.Partial {
			continue
		}
		st, known := states[sources.PostingID(src, board, p.ExternalID)]
		stale := !known || st.DetailFetchedAt.IsZero() || p.UpdatedAt.After(st.SourceUpdatedAt)
		if stale && r.WantDetail(c, p) {
			idx = append(idx, i)
		}
	}
	out := append([]sources.Posting(nil), ps...)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cmpOr(r.DetailConcurrency, DefaultDetailConcurrency))
	for _, i := range idx {
		g.Go(func() error {
			full, err := d.Detail(gctx, board, ps[i].ExternalID)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				r.logger().Warn("detail fetch failed", "company_id", c.ID, "posting", ps[i].ExternalID, "error", err)
				return nil
			}
			out[i] = full
			return nil
		})
	}
	return out, len(idx), g.Wait()
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
