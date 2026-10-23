package core

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// fakeThreads serves made-up hiring threads, newest first.
type fakeThreads struct {
	mu      sync.Mutex
	threads []sources.Thread
	ads     map[string][]sources.Posting // by thread id
	fail    error                        // returned by ReadThread
	gone    map[string]bool
	reads   int
}

func (s *fakeThreads) Name() string   { return "hackernews" }
func (s *fakeThreads) Prefix() string { return "hn" }

func (s *fakeThreads) Threads(_ context.Context, n int) ([]sources.Thread, error) {
	return s.threads[:min(n, len(s.threads))], nil
}

func (s *fakeThreads) ReadThread(_ context.Context, id string) (sources.ThreadResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.fail != nil {
		return sources.ThreadResult{}, s.fail
	}
	return sources.ThreadResult{Postings: slices.Clone(s.ads[id]), Unreadable: 1}, nil
}

func (s *fakeThreads) Lookup(_ context.Context, id string) (sources.Posting, error) {
	if s.gone[id] {
		return sources.Posting{}, sources.ErrPostingNotFound
	}
	return sources.Posting{ExternalID: id}, nil
}

func hnAd(id, company, title string) sources.Posting {
	p := fakePosting(id, title)
	p.Company = company
	p.URL = "https://news.ycombinator.com/item?id=" + id
	return p
}

func TestRefreshHackerNews(t *testing.T) {
	f := newRefreshFixture(t, "exampleco")
	f.src.set("exampleco", fakePosting("1", "Backend Engineer"))
	hn := &fakeThreads{
		threads: []sources.Thread{{ID: "901"}, {ID: "801"}},
		ads: map[string][]sources.Posting{
			"901": {
				hnAd("911", "Made-Up Labs", "Engineering Manager · Platform Engineer"),
				hnAd("912", "Fictional Freight", ""), // names no role
				hnAd("913", "Sample Co", "Office Manager"),
				hnAd("914", "Exampleco", "Backend Engineer"), // jhh reads Exampleco's board
			},
			"801": {hnAd("811", "Older Co", "Site Engineer")},
		},
		gone: map[string]bool{},
	}
	f.r.Searchers = map[string]sources.Searcher{"hackernews": hn}
	f.setFilters(`{"schema_version": 1, "titles": {"require_any": ["engineer"], "exclude_any": ["manager"]}, "search": {"hackernews": {"months": 1}}}`)

	res := f.refresh()
	if len(res.Searches) != 1 {
		t.Fatalf("searches %+v", res.Searches)
	}
	sum := res.Searches[0]
	if sum.Source != "hackernews" || sum.Threads != 1 || sum.Requests != 2 || sum.Postings != 4 || sum.Covered != 1 ||
		sum.Unreadable != 1 || !sum.Complete() || len(res.Errors) != 0 {
		t.Fatalf("summary %+v, errors %v", sum, res.Errors)
	}
	jobs := f.jobsByTitle()
	if len(jobs) != 4 {
		t.Errorf("%d jobs, want 4: the board's, and three ads (the covered one is skipped, last month's isn't read)", len(jobs))
	}
	// One fitting role is enough; an ad naming no role isn't dropped for it.
	for title, want := range map[string]string{
		"Engineering Manager · Platform Engineer": "pass", "": "pass", "Office Manager": "drop",
	} {
		if got := jobs[title].PrefilterStatus; got != want {
			t.Errorf("%q: prefilter %q, want %q", title, got, want)
		}
	}
	cs, _ := f.s.Candidates(context.Background(), store.StatusUnscreened, 10)
	for _, c := range cs {
		if c.Title == "" && c.Via != "hackernews" {
			t.Errorf("via %q", c.Via)
		}
	}

	// An ad that's gone from the thread closes its job.
	freight := jobs[""]
	hn.ads["901"] = slices.Delete(hn.ads["901"], 1, 2)
	res = f.refresh()
	if len(res.ClosedJobs) != 1 || res.ClosedJobs[0] != freight.ID || res.Searches[0].Closed != 1 {
		t.Errorf("closing: %+v", res.Searches[0])
	}

	// A thread that can't be read closes nothing.
	hn.ads["901"] = nil
	hn.fail = errors.New("hacker news: HTTP 500")
	res = f.refresh()
	if res.Searches[0].Complete() || len(res.ClosedJobs) != 0 || len(res.Errors) != 1 || res.Errors[0].Company != "Hacker News" {
		t.Errorf("failed read: %+v, closed %v, errors %v", res.Searches[0], res.ClosedJobs, res.Errors)
	}
}

func TestHackerNewsDedupe(t *testing.T) {
	f := newRefreshFixture(t, "exampleco")
	hn := &fakeThreads{
		threads: []sources.Thread{{ID: "901"}, {ID: "801"}},
		ads: map[string][]sources.Posting{
			// The same ads, posted two months running.
			"901": {hnAd("911", "Fictional Freight", ""), hnAd("912", "Made-Up Labs", "Engineering Manager · Platform Engineer")},
			"801": {hnAd("811", "Fictional Freight", ""), hnAd("812", "Made-Up Labs", "Engineering Manager · Platform Engineer")},
		},
		gone: map[string]bool{},
	}
	f.r.Searchers = map[string]sources.Searcher{"hackernews": hn}
	f.setFilters(`{"schema_version": 1, "titles": {"require_any": ["engineer"], "exclude_any": ["manager"]}, "search": {"hackernews": {}}}`)
	f.refresh()
	if all, _ := f.s.PrefilterJobs(context.Background(), 0); len(all) != 2 {
		t.Fatalf("%d jobs, want one for each ad, even the one naming no role", len(all))
	}
	jobs := f.jobsByTitle()
	found := jobs["Engineering Manager · Platform Engineer"]
	if found.PrefilterStatus != "pass" {
		t.Fatalf("prefilter %q for the ad, want pass: one of its roles fits", found.PrefilterStatus)
	}

	// The employer's board is added: its posting for one of the ad's roles
	// joins the job the ad made.
	if _, err := f.s.AddCompany(context.Background(), store.ActorUser, store.NewCompany{
		Name: "Made-Up Labs", Source: "fake", BoardKey: "fake:madeup", SourceConfig: []byte(`{"slug":"madeup"}`), Origin: "user",
	}); err != nil {
		t.Fatal(err)
	}
	f.src.set("madeup", fakePosting("77", "Platform Engineer"))
	if res := f.refresh(); len(res.NewJobs) != 0 {
		t.Errorf("new jobs %v; the board's posting should join the ad's job", res.NewJobs)
	}
	// The board confirmed which role this is: the job takes its title, and
	// the ad's other role no longer counts against it.
	js := f.jobsByTitle()
	if len(js) != 2 || js["Platform Engineer"].ID != found.ID {
		t.Fatalf("jobs %+v", js)
	}
	if got := js["Platform Engineer"].PrefilterStatus; got != "pass" {
		t.Errorf("prefilter %q after the board posting joined, want pass", got)
	}
}

func TestLookupLimitIsSharedBySources(t *testing.T) {
	f, muse := newSearchFixture(t, musePosting(1, "Made-Up Labs", "Platform Engineer"))
	hn := &fakeThreads{
		threads: []sources.Thread{{ID: "901"}},
		ads:     map[string][]sources.Posting{"901": {hnAd("911", "Fictional Freight", "Data Engineer")}},
		gone:    map[string]bool{},
	}
	f.r.Searchers["hackernews"] = hn
	f.r.MaxLookups = 1
	f.setFilters(`{"schema_version": 1, "titles": {"require_any": ["engineer"]}, "keywords": {"body_any": ["software"]},
		"search": {"themuse": [{"categories": ["Software Engineering"]}], "hackernews": {"months": 1}}}`)
	f.refresh()
	jobs := f.jobsByTitle()
	ctx := context.Background()
	for _, title := range []string{"Platform Engineer", "Data Engineer"} {
		if err := f.s.SetJobStatus(ctx, store.ActorUser, jobs[title].ID, store.StatusReview); err != nil {
			t.Fatal(err)
		}
	}

	// Both pursued jobs drop out of their source; one lookup in all.
	muse.postings, hn.ads["901"] = nil, nil
	res := f.refresh()
	n := 0
	for _, s := range res.Searches {
		n += s.Lookups
	}
	if n != 1 {
		t.Errorf("%d lookups across sources, want the limit of 1: %+v", n, res.Searches)
	}
}
