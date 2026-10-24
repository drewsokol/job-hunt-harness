package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// fakeRecent returns its postings newest first by UpdatedAt, 2 to a page.
// Every query gets the same results.
type fakeRecent struct {
	mu       sync.Mutex
	postings []sources.Posting
	pages    []int // pages asked for, in order
	keyed    bool  // a key is saved
	refuse   bool  // the saved key is refused
	keyless  []bool
	limitAt  int // the request number (from 1) that gets a 429; 0 for none
	gone     map[string]bool
	expires  time.Time // what a lookup that finds a job says about its expiry
	lookups  []string
}

func (s *fakeRecent) Name() string   { return "himalayas" }
func (s *fakeRecent) Prefix() string { return "hm" }
func (s *fakeRecent) HasKey() bool   { return s.keyed }

func (s *fakeRecent) SearchRecent(ctx context.Context, _ sources.TextQuery, page int) (sources.SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages = append(s.pages, page)
	s.keyless = append(s.keyless, sources.Keyless(ctx))
	if s.limitAt > 0 && len(s.pages) >= s.limitAt {
		return sources.SearchResult{}, fmt.Errorf("himalayas: %w", sources.ErrRateLimited)
	}
	if s.keyed && s.refuse && !sources.Keyless(ctx) {
		return sources.SearchResult{}, fmt.Errorf("himalayas: %w", sources.ErrKeyRefused)
	}
	ps := slices.Clone(s.postings)
	slices.SortStableFunc(ps, func(a, b sources.Posting) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	const size = 2
	lo := min((page-1)*size, len(ps))
	hi := min(lo+size, len(ps))
	return sources.SearchResult{Postings: ps[lo:hi], Pages: (len(ps) + size - 1) / size}, nil
}

func (s *fakeRecent) Lookup(_ context.Context, id string) (sources.Posting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups = append(s.lookups, id)
	if s.gone[id] {
		return sources.Posting{}, sources.ErrPostingNotFound
	}
	return sources.Posting{ExternalID: id, ExpiresAt: s.expires}, nil
}

func (s *fakeRecent) asked() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pages
	s.pages = nil
	return out
}

// recentPosting is a job last updated ago before now, listed for 60 days.
func recentPosting(id, title string, ago time.Duration) sources.Posting {
	p := fakePosting(id, title)
	p.ExternalID = "made-up-co-" + id + "/" + strings.ToLower(strings.ReplaceAll(title, " ", "-"))
	p.Company = "Made-Up Co " + id
	p.URL = "https://himalayas.example/companies/" + p.ExternalID
	p.UpdatedAt = utc.Now().Add(-ago).Truncate(time.Second)
	p.PostedAt = p.UpdatedAt
	p.ExpiresAt = p.UpdatedAt.Add(60 * 24 * time.Hour)
	return p
}

const himalayasQuery = `{"queries": [{"q": "platform engineer", "country": "US"}]}`

func newHimalayasFixture(t *testing.T, ps ...sources.Posting) (*refreshFixture, *fakeRecent) {
	f := newRefreshFixture(t)
	s := &fakeRecent{postings: ps, gone: map[string]bool{}}
	f.r.Searchers = map[string]sources.Searcher{"himalayas": s}
	f.setFilters(engineersWritingSoftware)
	f.setSource("himalayas", SourceUpdate{Settings: json.RawMessage(himalayasQuery)})
	return f, s
}

func himalayasSummary(res RefreshResult) SearchSummary {
	for _, s := range res.Searches {
		if s.Source == "himalayas" {
			return s
		}
	}
	return SearchSummary{}
}

func himalayasStateOf(t *testing.T, s *store.Store) himalayasState {
	t.Helper()
	rows, err := s.SourceRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return readHimalayasState(rows["himalayas"].State)
}

func onlyStop(t *testing.T, st himalayasState) time.Time {
	t.Helper()
	if len(st.Stops) != 1 {
		t.Fatalf("stops %v; want one", st.Stops)
	}
	for _, at := range st.Stops {
		return at
	}
	return time.Time{}
}

func TestHimalayasReadsOnlyWhatChanged(t *testing.T) {
	f, s := newHimalayasFixture(t,
		recentPosting("1", "Platform Engineer", 1*time.Hour),
		recentPosting("2", "Backend Engineer", 3*time.Hour),
		recentPosting("3", "Data Engineer", 20*time.Hour),
		recentPosting("4", "Old Engineer", 30*time.Hour),
		recentPosting("5", "Older Engineer", 40*time.Hour),
		recentPosting("6", "Oldest Engineer", 50*time.Hour),
	)
	// A first read goes back one day: page 2 holds a job from 30 hours ago,
	// so it stops there.
	res := f.refresh()
	if got := s.asked(); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("first read asked for pages %v; want 1, 2", got)
	}
	if sum := himalayasSummary(res); sum.Postings != 4 || len(sum.Gaps) != 0 || !sum.Complete() {
		t.Errorf("first read %+v", sum)
	}
	newest := s.postings[0].UpdatedAt
	if stop := onlyStop(t, himalayasStateOf(t, f.s)); !stop.Equal(newest.Add(-time.Hour)) {
		t.Errorf("stopping point %s; want the newest job less an hour, %s", stop, newest.Add(-time.Hour))
	}

	// A new job: the stopping point is an hour before the newest job of the
	// last read, so page 2 (3 hours ago) reaches it.
	s.postings = append(s.postings, recentPosting("7", "Staff Engineer", 0))
	f.refresh()
	if got := s.asked(); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("second read asked for pages %v; want 1, 2", got)
	}
	if j := f.jobsByTitle()["Staff Engineer"]; j.ID == 0 {
		t.Error("the new job wasn't stored")
	}
	// Nothing missing is closed: a read of what changed can't tell.
	if j := f.jobsByTitle()["Data Engineer"]; j.Closed {
		t.Error("a job the read didn't reach was closed")
	}
}

func TestHimalayasQueryThatRunsOutCatchesUp(t *testing.T) {
	var ps []sources.Posting
	for i := range 10 {
		ps = append(ps, recentPosting(fmt.Sprint(i+1), fmt.Sprintf("Engineer %d", i+1), time.Duration(i+1)*time.Hour))
	}
	f, s := newHimalayasFixture(t, ps...)
	f.r.MaxRecentRequests = 2
	sum := himalayasSummary(f.refresh())
	if got := s.asked(); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("asked for pages %v; want 1, 2", got)
	}
	if len(sum.Gaps) != 1 || !strings.Contains(sum.Gaps[0], "ran out of requests") || strings.Contains(sum.Gaps[0], "narrow it") {
		t.Errorf("gaps %q; want one, without calling a first read too broad", sum.Gaps)
	}
	st := himalayasStateOf(t, f.s)
	if stop := onlyStop(t, st); !stop.Equal(ps[0].UpdatedAt.Add(-time.Hour)) {
		t.Errorf("stopping point %s; it should still come from the newest job seen", stop)
	}

	// The next refresh reads only what's new (and the hour before), and
	// reaches its stopping point.
	s.postings = append(s.postings, recentPosting("11", "Staff Engineer", 0))
	sum = himalayasSummary(f.refresh())
	if got := s.asked(); !slices.Equal(got, []int{1, 2}) || len(sum.Gaps) != 0 {
		t.Errorf("after running out: pages %v, gaps %q; want pages 1, 2 and no gap", got, sum.Gaps)
	}

	// Running out after a complete read means the query is too broad.
	for i := range 6 {
		s.postings = append(s.postings, recentPosting(fmt.Sprint(20+i), fmt.Sprintf("New Engineer %d", i), -time.Duration(i+1)*time.Minute))
	}
	sum = himalayasSummary(f.refresh())
	if len(sum.Gaps) != 1 || !strings.Contains(sum.Gaps[0], "narrow it") {
		t.Errorf("gaps %q; want the query called too broad", sum.Gaps)
	}
}

func TestHimalayasRateLimitKeepsStoppingPoints(t *testing.T) {
	f, s := newHimalayasFixture(t, recentPosting("1", "Platform Engineer", time.Hour), recentPosting("2", "Data Engineer", 2*time.Hour))
	f.refresh()
	before := onlyStop(t, himalayasStateOf(t, f.s))
	s.asked()
	s.postings = append(s.postings, recentPosting("3", "Staff Engineer", 0))
	s.limitAt = 1
	sum := himalayasSummary(f.refresh())
	if sum.Stopped == "" || len(sum.Gaps) != 0 {
		t.Errorf("summary %+v; want stopped, with no gap", sum)
	}
	if after := onlyStop(t, himalayasStateOf(t, f.s)); !after.Equal(before) {
		t.Errorf("stopping point moved from %s to %s on a 429", before, after)
	}
}

func TestHimalayasRefusedKeyCarriesOnWithout(t *testing.T) {
	f, s := newHimalayasFixture(t, recentPosting("1", "Platform Engineer", time.Hour))
	s.keyed, s.refuse = true, true
	res := f.refresh()
	if sum := himalayasSummary(res); sum.Postings != 1 {
		t.Errorf("summary %+v; the search should go on without the key", sum)
	}
	if !slices.Equal(s.keyless, []bool{false, true}) {
		t.Errorf("keyless per request %v; want the key, then none", s.keyless)
	}
	info := sourceByName(t, f.s, "himalayas")
	if !info.KeyRefused || info.Status != SourceOn {
		t.Errorf("source %+v; want on, with the key marked refused", info)
	}
	// A key that works again clears the mark.
	s.refuse = false
	f.refresh()
	if sourceByName(t, f.s, "himalayas").KeyRefused {
		t.Error("the key is still marked refused after it worked")
	}
}

func TestHimalayasClosesAtExpiry(t *testing.T) {
	ctx := context.Background()
	expired := recentPosting("1", "Platform Engineer", time.Hour)
	expired.ExpiresAt = utc.Now().Add(-time.Minute)
	pursued := recentPosting("2", "Backend Engineer", 2*time.Hour)
	pursued.ExpiresAt = utc.Now().Add(2 * time.Second).Truncate(time.Second)
	f, s := newHimalayasFixture(t, expired, pursued)
	f.refresh()
	jobs := f.jobsByTitle()
	if !jobs["Platform Engineer"].Closed || jobs["Backend Engineer"].Closed {
		t.Errorf("closed: expired %v, not yet expired %v", jobs["Platform Engineer"].Closed, jobs["Backend Engineer"].Closed)
	}
	if err := f.s.SetJobStatus(ctx, store.ActorUser, jobs["Backend Engineer"].ID, store.StatusReview); err != nil {
		t.Fatal(err)
	}
	// Past its stored expiry, and not in this refresh's read, so it's
	// looked up: still listed, with a later expiry, so it stays open.
	time.Sleep(3 * time.Second)
	s.postings = s.postings[:1]
	s.expires = utc.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	f.refresh()
	if j := f.jobsByTitle()["Backend Engineer"]; j.Closed || len(s.lookups) != 1 {
		t.Errorf("pursued job closed %v after %v lookups; want open after one", j.Closed, s.lookups)
	}
	// Gone from Himalayas: the lookup closes it.
	s.gone[pursued.ExternalID] = true
	f.refresh()
	if j := f.jobsByTitle()["Backend Engineer"]; !j.Closed {
		t.Error("a pursued job Himalayas says is gone stayed open")
	}
	if st := f.status(jobs["Backend Engineer"].ID); st != store.StatusReview {
		t.Errorf("closing changed the job's status to %s", st)
	}
}

func TestHimalayasPausedStillClosesExpired(t *testing.T) {
	ctx := context.Background()
	soon := recentPosting("1", "Platform Engineer", time.Hour)
	soon.ExpiresAt = utc.Now().Add(2 * time.Second).Truncate(time.Second)
	f, s := newHimalayasFixture(t, soon)
	f.refresh()
	j := f.jobsByTitle()["Platform Engineer"]
	if j.Closed {
		t.Fatal("closed before it expired")
	}
	if err := f.s.SetJobStatus(ctx, store.ActorUser, j.ID, store.StatusReview); err != nil {
		t.Fatal(err)
	}
	off := false
	f.setSource("himalayas", SourceUpdate{Enabled: &off})
	time.Sleep(3 * time.Second)
	s.asked()
	f.refresh()
	if got := s.asked(); len(got) != 0 || len(s.lookups) != 0 {
		t.Errorf("a paused source was read: pages %v, lookups %v", got, s.lookups)
	}
	if !f.jobsByTitle()["Platform Engineer"].Closed {
		t.Error("an expired job of a paused source stayed open")
	}
}

func TestHimalayasStopsFollowTheQuery(t *testing.T) {
	a := HimalayasQuery{Q: "Platform  Engineer", Country: "US", Seniority: []string{"Senior", "Manager"}}
	b := HimalayasQuery{Q: "platform engineer", Country: "us", Seniority: []string{"Manager", "Senior"}}
	if a.key() != b.key() {
		t.Errorf("the same query, written differently, has two keys:\n%s\n%s", a.key(), b.key())
	}
	if c := (HimalayasQuery{Q: "platform engineer", Country: "CA"}); c.key() == a.key() {
		t.Error("different queries share a key")
	}
}

func TestSetSourceChecksHimalayas(t *testing.T) {
	f := newRefreshFixture(t)
	ctx := context.Background()
	for _, tt := range []struct{ settings, want string }{
		{`{"queries": [{"country": "US"}]}`, "settings.queries[0].q: must be 1 to"},
		{`{"queries": [{"q": "engineer", "country": "US,CA"}]}`, "use one query per country"},
		{`{"queries": [{"q": "engineer", "seniority": ["senior"]}]}`, `did you mean "Senior"`},
		{`{"queries": [{"q": "engineer"}], "api_key": "x"}`, `entered only by the user, on the dashboard's Sources tab`},
		{`{"queries": [{"q": "engineer"}], "keywords": "x"}`, `unknown field "keywords"`},
	} {
		_, err := SetSource(ctx, f.s, store.ActorUser, "himalayas", SourceUpdate{Settings: json.RawMessage(tt.settings)})
		var in *InputError
		if !errors.As(err, &in) || !strings.Contains(in.Error(), tt.want) {
			t.Errorf("%s: %v; want %q", tt.settings, err, tt.want)
		}
	}
	info, err := SetSource(ctx, f.s, store.ActorUser, "himalayas", SourceUpdate{Settings: json.RawMessage(himalayasQuery)})
	if err != nil || info.Status != SourceOn || !info.TakesKey {
		t.Errorf("%+v, %v", info, err)
	}
}
