package core

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/prefilter"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// fakeSearcher returns its postings 2 to a page. Every query gets the
// same results.
type fakeSearcher struct {
	mu       sync.Mutex
	postings []sources.Posting
	fail     error // returned by every search
	gone     map[string]bool
	searches int
	lookups  int
}

func (s *fakeSearcher) Name() string                 { return "themuse" }
func (s *fakeSearcher) Prefix() string               { return "tm" }
func (s *fakeSearcher) Check(sources.Query) []string { return nil }

func (s *fakeSearcher) Search(_ context.Context, _ sources.Query, page int) (sources.SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searches++
	if s.fail != nil {
		return sources.SearchResult{}, s.fail
	}
	const size = 2
	pages := (len(s.postings) + size - 1) / size
	lo := min(page*size, len(s.postings))
	hi := min(lo+size, len(s.postings))
	return sources.SearchResult{Postings: slices.Clone(s.postings[lo:hi]), Pages: pages}, nil
}

func (s *fakeSearcher) Lookup(_ context.Context, id string) (sources.Posting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	if s.gone[id] {
		return sources.Posting{}, sources.ErrPostingNotFound
	}
	return sources.Posting{ExternalID: id}, nil
}

func musePosting(id int, company, title string) sources.Posting {
	p := fakePosting(strconv.Itoa(id), title)
	p.Company = company
	p.URL = "https://muse.example/jobs/" + p.ExternalID
	p.UpdatedAt = p.PostedAt
	return p
}

// searchFilters is engineersWritingSoftware with a Muse query of pages.
func searchFilters(pages int) string {
	return fmt.Sprintf(`{"schema_version": 1, "titles": {"require_any": ["engineer"]}, "keywords": {"body_any": ["software"]},
		"search": {"themuse": [{"categories": ["Software Engineering"], "pages": %d}]}}`, pages)
}

func newSearchFixture(t *testing.T, ps ...sources.Posting) (*refreshFixture, *fakeSearcher) {
	f := newRefreshFixture(t, "exampleco")
	f.src.set("exampleco", fakePosting("1", "Backend Engineer"))
	s := &fakeSearcher{postings: ps, gone: map[string]bool{}}
	f.r.Searchers = map[string]sources.Searcher{"themuse": s}
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
		musePosting(1, "Made-Up Labs, Inc.", "Platform Engineer"),
		musePosting(2, "made-up labs", "Office Manager"),
		musePosting(3, "Exampleco", "Backend Engineer"), // jhh reads Exampleco's board
	)
	f.setFilters(searchFilters(10))
	res := f.refresh()
	sum := muse(res)
	if sum.Queries != 1 || sum.Requests != 2 || sum.Postings != 3 || sum.Covered != 1 || !sum.Complete() || len(res.Errors) != 0 {
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
	if via["Platform Engineer"] != "themuse" || via["Backend Engineer"] != "" {
		t.Errorf("via: %v", via)
	}

	// The next refresh changes nothing; then the manager's posting goes
	// away and its job closes.
	if res = f.refresh(); len(res.NewJobs) != 0 || len(res.ClosedJobs) != 0 {
		t.Errorf("unchanged search: %+v", res)
	}
	s.postings = slices.Delete(s.postings, 1, 2)
	res = f.refresh()
	if len(res.ClosedJobs) != 1 || res.ClosedJobs[0] != mgr.ID || muse(res).Closed != 1 {
		t.Errorf("closing: %+v", res)
	}

}

func TestRefreshSearchOnlyClosesAfterCompleteReads(t *testing.T) {
	f, s := newSearchFixture(t,
		musePosting(1, "Made-Up Labs", "Platform Engineer"),
		musePosting(2, "Made-Up Labs", "Data Engineer"),
		musePosting(3, "Other Co", "Site Engineer"),
	)
	f.setFilters(searchFilters(1)) // 2 pages of results, 1 read
	res := f.refresh()
	if muse(res).Complete() || !slices.Equal(muse(res).Incomplete, []string{"search.themuse[0]"}) || muse(res).Postings != 2 {
		t.Fatalf("summary %+v", muse(res))
	}
	s.postings = []sources.Posting{
		musePosting(3, "Other Co", "Site Engineer"),
		musePosting(4, "Other Co", "Field Engineer"),
		musePosting(5, "Other Co", "Test Engineer"),
	}
	if res = f.refresh(); len(res.ClosedJobs) != 0 || muse(res).Complete() {
		t.Errorf("closed after an incomplete read: %+v", res)
	}

	// A rate limit stops searching and closes nothing either.
	f.setFilters(searchFilters(10))
	s.fail = fmt.Errorf("the muse: %w", sources.ErrRateLimited)
	res = f.refresh()
	if muse(res).Stopped == "" || len(res.ClosedJobs) != 0 || s.searches != 3 {
		t.Errorf("rate limited: %+v, %d searches", muse(res), s.searches)
	}
	if len(res.Errors) != 1 || res.Errors[0].Company != "The Muse" {
		t.Errorf("errors %+v", res.Errors)
	}
}

func TestRefreshSearchLimitsRequests(t *testing.T) {
	f, s := newSearchFixture(t,
		musePosting(1, "Made-Up Labs", "Platform Engineer"),
		musePosting(2, "Made-Up Labs", "Data Engineer"),
		musePosting(3, "Other Co", "Site Engineer"),
	)
	f.r.MaxSearchRequests = 1
	f.setFilters(searchFilters(10))
	if res := f.refresh(); muse(res).Requests != 1 || s.searches != 1 || !strings.Contains(muse(res).Stopped, "limit of 1") {
		t.Errorf("%+v", muse(res))
	}
}

func TestRefreshSearchLooksUpPursuedJobs(t *testing.T) {
	f, s := newSearchFixture(t,
		musePosting(1, "Made-Up Labs", "Platform Engineer"),
		musePosting(2, "Made-Up Labs", "Data Engineer"),
	)
	f.setFilters(searchFilters(10))
	f.refresh()
	jobs := f.jobsByTitle()
	ctx := context.Background()
	for _, title := range []string{"Platform Engineer", "Data Engineer"} {
		if err := f.s.SetJobStatus(ctx, store.ActorUser, jobs[title].ID, store.StatusReview); err != nil {
			t.Fatal(err)
		}
	}

	// Both drop out of the results; only the one The Muse says is gone
	// closes.
	s.postings = nil
	s.gone["2"] = true
	res := f.refresh()
	if s.lookups != 2 || muse(res).Lookups != 2 || len(res.ClosedJobs) != 1 || res.ClosedJobs[0] != jobs["Data Engineer"].ID {
		t.Errorf("%+v; %d lookups", res, s.lookups)
	}
	if j, _ := f.s.Job(ctx, jobs["Platform Engineer"].ID); !j.ClosedAt.IsZero() {
		t.Errorf("pursued job closed without the source saying so")
	}
}

func TestRefreshSearchAttachesBoardPostingsLater(t *testing.T) {
	f, _ := newSearchFixture(t, musePosting(1, "Made-Up Labs", "Platform Engineer"))
	f.setFilters(searchFilters(10))
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
	if len(res.NewJobs) != 0 || muse(res).Covered != 1 {
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
	s := &fakeSearcher{}
	p := musePosting(42, "Made-Up Labs", "Platform Engineer")
	p.LocationRaw = "Flexible / Remote | Columbus, OH"
	in := NormalizeFound(s, p)
	if in.ID != "tm:42" || in.Source != "themuse" || !in.Search || in.DescriptionHTML != "" || in.Description == "" {
		t.Errorf("%+v", in)
	}
	if !strings.Contains(string(in.Locations), "Columbus") {
		t.Errorf("locations %s", in.Locations)
	}
}

func TestSetFiltersChecksMuseNames(t *testing.T) {
	f := newRefreshFixture(t)
	fl, err := prefilter.Parse([]byte(`{"schema_version": 1, "search": {"themuse": [{"categories": ["software engineering", "Robotics"]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = SetFilters(context.Background(), f.s, store.ActorUser, fl, "test")
	var ve *prefilter.ValidationError
	if !errors.As(err, &ve) || len(ve.Problems) != 2 || !strings.Contains(ve.Problems[0], `search.themuse[0].categories[0]: "software engineering"`) ||
		!strings.Contains(ve.Problems[0], `did you mean "Software Engineering"?`) {
		t.Errorf("%v", err)
	}
	if _, ok, _ := f.s.ActiveFilters(context.Background()); ok {
		t.Error("invalid filters were saved")
	}
}

func TestRefreshSearchReadsEveryPageAndSkipsOldListings(t *testing.T) {
	var ps []sources.Posting
	for i := range 7 {
		ps = append(ps, musePosting(i+1, "Made-Up Labs", fmt.Sprintf("Engineer %d", i+1)))
	}
	ps[6].PostedAt = time.Now().UTC().Add(-MaxSearchAge - 24*time.Hour)
	f, s := newSearchFixture(t, ps...)
	f.setFilters(searchFilters(0)) // no pages cap
	res := f.refresh()
	if s.searches != 4 || !muse(res).Complete() || muse(res).Postings != 6 || muse(res).Old != 1 {
		t.Errorf("%+v after %d searches", muse(res), s.searches)
	}
	if _, ok := f.jobsByTitle()["Engineer 7"]; ok {
		t.Error("old listing stored")
	}
}

// muse returns The Muse's summary from a refresh.
func muse(res RefreshResult) SearchSummary {
	for _, s := range res.Searches {
		if s.Source == "themuse" {
			return s
		}
	}
	return SearchSummary{}
}
