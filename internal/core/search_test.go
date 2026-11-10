package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// The search tests run on fake Hacker News threads: what they check, from
// storing results under employers to closing and lookups, is the same for
// every search source read in full.

// searchFilters is engineersWritingSoftware with Hacker News reading one
// month.
const searchFilters = `{"schema_version": 1, "titles": {"require_any": ["engineer"]}, "keywords": {"body_any": ["software"]},
	"search": {"hackernews": {"months": 1}}}`

// newSearchFixture serves ps as the ads of one thread.
func newSearchFixture(t *testing.T, ps ...sources.Posting) (*refreshFixture, *fakeThreads) {
	f := newRefreshFixture(t, "exampleco")
	f.src.set("exampleco", fakePosting("1", "Backend Engineer"))
	s := &fakeThreads{threads: []sources.Thread{{ID: "901"}}, ads: map[string][]sources.Posting{"901": ps}, gone: map[string]bool{}}
	f.r.Searchers = map[string]sources.Searcher{"hackernews": s}
	return f, s
}

// jobsByTitle returns every job by title.
func (f *refreshFixture) jobsByTitle() map[string]store.PrefilterJob {
	f.t.Helper()
	js, err := f.s.PrefilterJobs(context.Background(), 0)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]store.PrefilterJob{}
	for _, j := range js {
		out[j.Title] = j
	}
	return out
}

func TestRefreshSearches(t *testing.T) {
	f, s := newSearchFixture(t,
		hnAd("1", "Made-Up Labs, Inc.", "Platform Engineer"),
		hnAd("2", "made-up labs", "Office Manager"),
		hnAd("3", "Exampleco", "Backend Engineer"), // jhh reads Exampleco's board
	)
	f.setFilters(searchFilters)
	res := f.refresh()
	sum := searchSummary(res)
	if sum.Requests != 2 || sum.Postings != 3 || sum.Covered != 1 || !sum.Complete() || len(res.Errors) != 0 {
		t.Fatalf("search summary %+v, errors %v", sum, res.Errors)
	}
	jobs := f.jobsByTitle()
	eng, mgr := jobs["Platform Engineer"], jobs["Office Manager"]
	if eng.Company != "Made-Up Labs, Inc." || mgr.CompanyID != eng.CompanyID {
		t.Errorf("employers: %+v / %+v", eng, mgr)
	}
	if eng.PrefilterStatus != "pass" || !strings.Contains(eng.Description, "made-up software") {
		t.Errorf("engineer: status %q, description %q", eng.PrefilterStatus, eng.Description)
	}
	// The manager was dropped by its title, so its description wasn't kept.
	if mgr.PrefilterStatus != "drop" || mgr.Description != "" {
		t.Errorf("manager: status %q, description %q", mgr.PrefilterStatus, mgr.Description)
	}
	if len(jobs) != 3 {
		t.Errorf("%d jobs, want 3 (the covered result is skipped)", len(jobs))
	}
	c, err := f.s.Company(context.Background(), eng.CompanyID)
	if err != nil || c.Source != "none" || c.Origin != "search" {
		t.Errorf("search company %+v, %v", c, err)
	}
	cs, _ := f.s.Candidates(context.Background(), store.StatusUnscreened, 10)
	via := map[string]string{}
	for _, c := range cs {
		via[c.Title] = c.Via
	}
	if via["Platform Engineer"] != "hackernews" || via["Backend Engineer"] != "" {
		t.Errorf("via: %v", via)
	}

	// The next refresh changes nothing; then the manager's posting goes
	// away and its job closes.
	if res = f.refresh(); len(res.NewJobs) != 0 || len(res.ClosedJobs) != 0 {
		t.Errorf("unchanged search: %+v", res)
	}
	s.ads["901"] = slices.Delete(s.ads["901"], 1, 2)
	res = f.refresh()
	if len(res.ClosedJobs) != 1 || res.ClosedJobs[0] != mgr.ID || searchSummary(res).Closed != 1 {
		t.Errorf("closing: %+v", res)
	}
}

func TestRefreshSearchLooksUpPursuedJobs(t *testing.T) {
	f, s := newSearchFixture(t,
		hnAd("1", "Made-Up Labs", "Platform Engineer"),
		hnAd("2", "Made-Up Labs", "Data Engineer"),
	)
	f.setFilters(searchFilters)
	f.refresh()
	jobs := f.jobsByTitle()
	ctx := context.Background()
	for _, title := range []string{"Platform Engineer", "Data Engineer"} {
		if err := f.s.SetJobStatus(ctx, store.ActorUser, jobs[title].ID, store.StatusReview); err != nil {
			t.Fatal(err)
		}
	}

	// Both drop out of the results; only the one the source says is gone
	// closes.
	s.ads["901"] = nil
	s.gone["2"] = true
	res := f.refresh()
	if s.lookups != 2 || searchSummary(res).Lookups != 2 || len(res.ClosedJobs) != 1 || res.ClosedJobs[0] != jobs["Data Engineer"].ID {
		t.Errorf("%+v; %d lookups", res, s.lookups)
	}
	if j, _ := f.s.Job(ctx, jobs["Platform Engineer"].ID); !j.ClosedAt.IsZero() {
		t.Errorf("pursued job closed without the source saying so")
	}
}

// The board often words a job differently from the search result. The
// title still joins the board's posting to the job search found.
func TestRefreshSearchAttachesBoardPostingWithOtherText(t *testing.T) {
	f, _ := newSearchFixture(t, hnAd("1", "Made-Up Labs", "Platform Engineer"))
	f.setFilters(searchFilters)
	f.refresh()
	found := f.jobsByTitle()["Platform Engineer"]

	if _, err := f.s.AddCompany(context.Background(), store.ActorUser, store.NewCompany{
		Name: "Made-Up Labs", Source: "fake", BoardKey: "fake:madeup", SourceConfig: []byte(`{"slug":"madeup"}`), Origin: "user",
	}); err != nil {
		t.Fatal(err)
	}
	p := fakePosting("77", "Platform Engineer")
	p.DescriptionHTML = "<p>Made-Up Labs runs imaginary platforms. " + strings.Repeat("Different words on the board. ", 12) + "</p>"
	f.src.complete = true // the board lists descriptions, so the posting has a fingerprint of its own
	f.src.set("madeup", p)
	if res := f.refresh(); len(res.NewJobs) != 0 {
		t.Errorf("new jobs %v; the board's posting should join the job search found", res.NewJobs)
	}
	if js := f.jobsByTitle(); js["Platform Engineer"].ID != found.ID {
		t.Errorf("jobs %+v", js)
	}
}

func TestRefreshSearchAttachesBoardPostingsLater(t *testing.T) {
	f, _ := newSearchFixture(t, hnAd("1", "Made-Up Labs", "Platform Engineer"))
	f.setFilters(searchFilters)
	f.refresh()
	found := f.jobsByTitle()["Platform Engineer"]

	// The employer's board is added: its posting joins the job search
	// found, and the board wins from then on.
	if _, err := f.s.AddCompany(context.Background(), store.ActorUser, store.NewCompany{
		Name: "Made-Up Labs", Source: "fake", BoardKey: "fake:madeup", SourceConfig: []byte(`{"slug":"madeup"}`), Origin: "user",
	}); err != nil {
		t.Fatal(err)
	}
	f.src.set("madeup", fakePosting("77", "Platform Engineer"))
	res := f.refresh()
	if len(res.NewJobs) != 0 || searchSummary(res).Covered != 1 {
		t.Errorf("%+v", res)
	}
	if js := f.jobsByTitle(); len(js) != 2 || js["Platform Engineer"].ID != found.ID {
		t.Errorf("jobs %+v", js)
	}
	cs, _ := f.s.Candidates(context.Background(), store.StatusUnscreened, 10)
	for _, c := range cs {
		if c.ID == found.ID && c.Via != "" {
			t.Errorf("job with a board posting still via %q", c.Via)
		}
	}
}

// listOnly is a fake board without a detail endpoint, like Lever and
// Ashby, whose listings include descriptions.
type listOnly struct{ f *fakeSource }

func (l listOnly) Name() string   { return "fake" }
func (l listOnly) Prefix() string { return "fk" }
func (l listOnly) List(ctx context.Context, b sources.Board, etag string) (sources.ListResult, error) {
	return l.f.List(ctx, b, etag)
}
func (l listOnly) Probe(ctx context.Context, slug string) (int, error) { return l.f.Probe(ctx, slug) }
func (l listOnly) MatchURL(u *url.URL) (sources.PostingRef, bool)      { return l.f.MatchURL(u) }

func TestRefreshKeepsDescriptionsOnlyWhereNeeded(t *testing.T) {
	f := newRefreshFixture(t, "exampleco")
	f.src.complete = true
	f.r.Sources["fake"] = listOnly{f.src}
	f.src.set("exampleco", fakePosting("1", "Backend Engineer"), fakePosting("2", "Data Engineer"), fakePosting("9", "Sales Manager"))

	// Without filters nothing is wanted, so no description is stored.
	f.refresh()
	for title, j := range f.jobsByTitle() {
		if j.Description != "" || j.DescriptionKnown {
			t.Errorf("%s: description stored before filters", title)
		}
	}

	// Filters make the engineers wait for descriptions. The board hasn't
	// changed, but it is read again in full to get them.
	f.setFilters(engineersWritingSoftware)
	res := f.refresh()
	if res.NotModified != 0 || len(res.Passed) != 2 {
		t.Fatalf("after setting filters: %+v", res)
	}
	jobs := f.jobsByTitle()
	if jobs["Backend Engineer"].Description == "" || jobs["Sales Manager"].Description != "" {
		t.Errorf("descriptions: %+v", jobs)
	}
	if res = f.refresh(); res.NotModified != 1 {
		t.Errorf("read in full again with nothing waiting: %+v", res)
	}

	// New filters drop the data engineer by title: its description goes.
	f.setFilters(`{"schema_version": 1, "titles": {"require_any": ["engineer"], "exclude_any": ["data"]}, "keywords": {"body_any": ["software"]}}`)
	jobs = f.jobsByTitle()
	if jobs["Data Engineer"].Description != "" || jobs["Backend Engineer"].Description == "" {
		t.Errorf("after narrowing: data %q, backend %q", jobs["Data Engineer"].Description, jobs["Backend Engineer"].Description)
	}
}

func TestRefreshClearsDescriptionsOfStageOneDrops(t *testing.T) {
	f := newRefreshFixture(t, "exampleco")
	f.setFilters(`{"schema_version": 1, "titles": {"require_any": ["engineer"]}, "keywords": {"body_any": ["software"]},
		"location": {"remote_in": ["US"]}}`)
	f.src.set("exampleco", fakePosting("1", "Backend Engineer"), fakePosting("2", "Data Engineer"))
	f.refresh()

	// The board moves a job outside the filters' locations.
	moved := fakePosting("2", "Data Engineer")
	moved.LocationRaw = "Remote - UK"
	f.src.set("exampleco", fakePosting("1", "Backend Engineer"), moved)
	res := f.refresh()
	if res.DescriptionsCleared != 1 || len(res.FilteredOut) != 1 {
		t.Errorf("%+v", res)
	}
	if j := f.jobsByTitle()["Data Engineer"]; j.Description != "" || j.DescriptionKnown {
		t.Errorf("dropped job kept its description: %+v", j)
	}
}

func TestNormalizeFound(t *testing.T) {
	s := &fakeThreads{}
	p := hnAd("42", "Made-Up Labs", "Platform Engineer")
	p.LocationRaw = "Remote | Springfield, IL"
	in := NormalizeFound(s, p)
	if in.ID != "hn:42" || in.Source != "hackernews" || !in.Search || in.DescriptionHTML != "" || in.Description == "" {
		t.Errorf("%+v", in)
	}
	if !strings.Contains(string(in.Locations), "Springfield") {
		t.Errorf("locations %s", in.Locations)
	}
}

// The Muse is retired: it can't be set up or turned on, it isn't read, and
// it is listed, with why, only where it was set up.
func TestRetiredMuse(t *testing.T) {
	ctx := context.Background()
	f := newRefreshFixture(t)
	on := true
	for _, u := range []SourceUpdate{{Enabled: &on}, {Settings: json.RawMessage(`{"queries": [{"categories": ["Software Engineering"]}]}`)}} {
		_, err := SetSource(ctx, f.s, store.ActorUser, "TheMuse", u)
		var in *InputError
		if !errors.As(err, &in) || !strings.Contains(in.Error(), "The Muse is retired") || !strings.Contains(in.Error(), "hackernews, himalayas") {
			t.Errorf("SetSource(themuse, %+v) = %v", u, err)
		}
	}
	for _, info := range mustListSources(t, f.s) {
		if info.Name == "themuse" {
			t.Errorf("listed where it was never set up: %+v", info)
		}
	}

	// An install that had it set up, the way migration 0003 or set_source
	// left it, sees it as retired, with its last run.
	if err := f.s.SetSource(ctx, store.ActorUser, "themuse", store.SourceChange{Enabled: &on, Settings: json.RawMessage(`{"queries":[{"categories":["Software Engineering"]}]}`)}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RecordSourceRun(ctx, "themuse", store.SourceRun{At: time.Now().UTC(), Error: "the muse: board not found"}); err != nil {
		t.Fatal(err)
	}
	m := sourceByName(t, f.s, "themuse")
	if m.Status != SourceRetired || !strings.Contains(m.Retired, "removed its public jobs API") || m.Settings != nil || m.LastRunAt.IsZero() {
		t.Errorf("%+v", m)
	}
	ss := mustListSources(t, f.s)
	if SearchesOn(ss) != 1 || len(PausedSearches(ss)) != 0 {
		t.Errorf("a retired source counts as on or paused: %+v", ss)
	}
	if p, err := plannedSearches(ctx, f.s); err != nil || p.hackerNews == nil {
		t.Errorf("plan %+v, %v", p, err)
	}
	if !slices.Contains(RetiredTitles(), "The Muse") {
		t.Errorf("retired titles %v", RetiredTitles())
	}
}

func mustListSources(t *testing.T, s *store.Store) []SourceInfo {
	t.Helper()
	ss, err := ListSources(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

// A search source whose API is gone or blocks jhh says so, with the HTTP
// status, rather than naming a missing board.
func TestSearchFailureSaysTheSourceIsUnavailable(t *testing.T) {
	f, s := newSearchFixture(t, hnAd("1", "Made-Up Labs", "Platform Engineer"))
	f.setFilters(searchFilters)
	for _, tt := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("hacker news: %w", sources.ErrBoardNotFound), "Hacker News is unavailable: its API answered HTTP 404"},
		// As the source returns it: the status stays in the chain, the URL out of the text.
		{sources.StatusOnly("hacker news", &sources.StatusError{URL: "https://hn.example/items/901", Code: 403}),
			"Hacker News refused jhh's requests, so it may be blocking them (hacker news: HTTP 403)"},
		{errors.New("hacker news: HTTP 500"), "hacker news: HTTP 500"},
	} {
		s.fail = tt.err
		res := f.refresh()
		if len(res.Errors) != 1 || !strings.HasPrefix(res.Errors[0].Error, tt.want) || strings.Contains(res.Errors[0].Error, "board not found") ||
			strings.Contains(res.Errors[0].Error, "hn.example") {
			t.Errorf("%v: errors %+v, want %q", tt.err, res.Errors, tt.want)
		}
	}
}

func TestRefreshSearchSkipsOldListings(t *testing.T) {
	var ps []sources.Posting
	for i := range 3 {
		ps = append(ps, hnAd(fmt.Sprint(i+1), "Made-Up Labs", fmt.Sprintf("Engineer %d", i+1)))
	}
	ps[2].PostedAt = time.Now().UTC().Add(-MaxSearchAge - 24*time.Hour)
	f, _ := newSearchFixture(t, ps...)
	f.setFilters(searchFilters)
	res := f.refresh()
	if sum := searchSummary(res); !sum.Complete() || sum.Postings != 2 || sum.Old != 1 {
		t.Errorf("%+v", sum)
	}
	if _, ok := f.jobsByTitle()["Engineer 3"]; ok {
		t.Error("old listing stored")
	}
}

// searchSummary returns Hacker News's summary from a refresh.
func searchSummary(res RefreshResult) SearchSummary {
	for _, s := range res.Searches {
		if s.Source == "hackernews" {
			return s
		}
	}
	return SearchSummary{}
}
