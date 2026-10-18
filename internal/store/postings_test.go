package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// fetchFixture is a store with one Greenhouse company and a clock the test
// moves forward.
type fetchFixture struct {
	t       *testing.T
	s       *Store
	clock   time.Time
	company int64
}

func newFetchFixture(t *testing.T) *fetchFixture {
	f := &fetchFixture{t: t, clock: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "jhh.db"), Options{Now: func() time.Time { return f.clock }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	f.s = s
	f.company, err = s.AddCompany(context.Background(), ActorUser, NewCompany{
		Name: "Exampleco", Source: "greenhouse", BoardKey: "greenhouse:exampleco", Origin: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fetchFixture) apply(ps ...PostingInput) FetchOutcome {
	f.t.Helper()
	out, err := f.s.ApplyFetch(context.Background(), ActorSystem, CompanyFetch{
		CompanyID: f.company, Source: "greenhouse", ETag: `"v"`, Postings: ps,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fetchFixture) jobOf(postingID string) int64 {
	f.t.Helper()
	var id int64
	if err := f.s.db.QueryRow("SELECT job_id FROM postings WHERE id = ?", postingID).Scan(&id); err != nil {
		f.t.Fatalf("posting %s: %v", postingID, err)
	}
	return id
}

func (f *fetchFixture) jobClosed(id int64) bool {
	f.t.Helper()
	var closed bool
	if err := f.s.db.QueryRow("SELECT closed_at IS NOT NULL FROM jobs WHERE id = ?", id).Scan(&closed); err != nil {
		f.t.Fatal(err)
	}
	return closed
}

// longDescription is a made-up description long enough to fingerprint.
const longDescription = "Exampleco builds made-up software for tests. You will design services, review code, " +
	"mentor teammates and keep production healthy. We value clear writing, careful testing and kindness. " +
	"This paragraph exists only to be long enough for a fingerprint."

func posting(id string, mods ...func(*PostingInput)) PostingInput {
	p := PostingInput{
		ID: "gh:exampleco:" + id, Source: "greenhouse", ExternalID: id,
		Title: "Backend Engineer", TitleKey: "backend engineer",
		URL: "https://job-boards.greenhouse.io/exampleco/jobs/" + id, CanonicalURL: "https://job-boards.greenhouse.io/exampleco/jobs/" + id,
		Workplace: "unknown",
	}
	for _, m := range mods {
		m(&p)
	}
	return p
}

func opening(key string) func(*PostingInput) { return func(p *PostingInput) { p.OpeningKey = key } }
func located(loc, workplace string) func(*PostingInput) {
	return func(p *PostingInput) {
		p.LocationRaw, p.Workplace = loc, workplace
		p.Locations = fmt.Appendf(nil, `[{"city":%q}]`, loc)
	}
}
func described(fp string) func(*PostingInput) {
	return func(p *PostingInput) {
		p.Description, p.DescriptionHTML, p.Fingerprint = longDescription, "<p>"+longDescription+"</p>", fp
	}
}

func partial(p *PostingInput) { p.Partial = true }

func TestApplyFetchGroupsCityPostingsByOpening(t *testing.T) {
	f := newFetchFixture(t)
	out := f.apply(
		posting("1", opening("900"), located("San Francisco", "onsite")),
		posting("2", opening("900"), located("New York", "remote")),
		posting("3", opening("901")),
	)
	if len(out.NewJobs) != 2 || out.NewPostings != 3 {
		t.Fatalf("outcome %+v, want 2 jobs from 3 postings", out)
	}
	if f.jobOf("gh:exampleco:1") != f.jobOf("gh:exampleco:2") || f.jobOf("gh:exampleco:1") == f.jobOf("gh:exampleco:3") {
		t.Error("postings grouped wrongly")
	}
	var workplace, locations string
	f.s.db.QueryRow("SELECT workplace, locations FROM jobs WHERE id = ?", f.jobOf("gh:exampleco:1")).Scan(&workplace, &locations)
	if workplace != "remote" || locations != `[{"city":"San Francisco"},{"city":"New York"}]` {
		t.Errorf("job workplace %q, locations %s; want the most flexible workplace and every place", workplace, locations)
	}
}

func TestApplyFetchSameTitleDifferentDescriptionStaysSeparate(t *testing.T) {
	f := newFetchFixture(t)
	f.apply(posting("1", described("aaaa")), posting("2", described("bbbb")), posting("3", described("aaaa")))
	if f.jobOf("gh:exampleco:1") == f.jobOf("gh:exampleco:2") {
		t.Error("different descriptions with the same title were merged")
	}
	if f.jobOf("gh:exampleco:1") != f.jobOf("gh:exampleco:3") {
		t.Error("same title and fingerprint were not grouped")
	}
}

func TestApplyFetchMatchesCanonicalURL(t *testing.T) {
	f := newFetchFixture(t)
	f.apply(posting("1"))
	f.apply(posting("1"), posting("2", func(p *PostingInput) { p.CanonicalURL = posting("1").CanonicalURL }))
	if f.jobOf("gh:exampleco:1") != f.jobOf("gh:exampleco:2") {
		t.Error("same canonical URL not grouped")
	}
}

func TestApplyFetchClosesMissingPostingsAndJobs(t *testing.T) {
	f := newFetchFixture(t)
	f.apply(posting("1", opening("900")), posting("2", opening("900")), posting("3", opening("901")))
	job := f.jobOf("gh:exampleco:1")

	f.clock = f.clock.Add(24 * time.Hour)
	out := f.apply(posting("2", opening("900")), posting("3", opening("901")))
	if len(out.ClosedJobs) != 0 || f.jobClosed(job) {
		t.Error("a job closed while one of its postings is still open")
	}
	f.clock = f.clock.Add(24 * time.Hour)
	out = f.apply(posting("3", opening("901")))
	if !slices.Equal(out.ClosedJobs, []int64{job}) || !f.jobClosed(job) {
		t.Errorf("ClosedJobs = %v, want [%d]", out.ClosedJobs, job)
	}
	events, _ := f.s.JobEvents(context.Background(), job)
	if events[len(events)-1].Kind != "job_closed" {
		t.Errorf("last event %q, want job_closed", events[len(events)-1].Kind)
	}
	c, _ := f.s.Company(context.Background(), f.company)
	if c.OpenPostings != 1 || c.ETag != `"v"` || c.Suspect {
		t.Errorf("company %+v", c)
	}
}

func TestApplyFetchRepostWithinWindow(t *testing.T) {
	f := newFetchFixture(t)
	f.apply(posting("1", described("aaaa")), posting("9"))
	job := f.jobOf("gh:exampleco:1")
	f.apply(posting("9"))
	if !f.jobClosed(job) {
		t.Fatal("job not closed")
	}

	f.clock = f.clock.Add(30 * 24 * time.Hour)
	out := f.apply(posting("9"), posting("2", described("aaaa")))
	if f.jobOf("gh:exampleco:2") != job || !slices.Equal(out.RepostedJobs, []int64{job}) || f.jobClosed(job) {
		t.Errorf("repost not attached: outcome %+v", out)
	}
	var reposted bool
	f.s.db.QueryRow("SELECT reposted_at IS NOT NULL FROM jobs WHERE id = ?", job).Scan(&reposted)
	if !reposted {
		t.Error("reposted_at not set")
	}
}

func TestApplyFetchOldRepostIsNewJob(t *testing.T) {
	f := newFetchFixture(t)
	f.apply(posting("1", described("aaaa")), posting("9"))
	job := f.jobOf("gh:exampleco:1")
	f.apply(posting("9"))
	f.clock = f.clock.Add(90 * 24 * time.Hour)
	f.apply(posting("9"), posting("2", described("aaaa")))
	if f.jobOf("gh:exampleco:2") == job {
		t.Error("a posting matching a job closed 90 days ago was attached to it")
	}
}

func TestApplyFetchSuspectFeedClosesNothing(t *testing.T) {
	f := newFetchFixture(t)
	var ps []PostingInput
	for i := range 20 {
		ps = append(ps, posting(fmt.Sprint(i), opening(fmt.Sprint(i))))
	}
	f.apply(ps...)

	out := f.apply(ps[:5]...)
	if !out.Suspect || len(out.ClosedJobs) != 0 {
		t.Errorf("drop from 20 to 5: outcome %+v, want suspect and nothing closed", out)
	}
	c, _ := f.s.Company(context.Background(), f.company)
	if !c.Suspect || c.SuspectReason == "" || c.OpenPostings != 20 {
		t.Errorf("company suspect %v %q, open %d", c.Suspect, c.SuspectReason, c.OpenPostings)
	}

	if out := f.apply(); !out.Suspect {
		t.Error("an empty board is not suspect")
	}

	out = f.apply(ps[:15]...)
	if out.Suspect || len(out.ClosedJobs) != 5 {
		t.Errorf("drop from 20 to 15: outcome %+v, want 5 closed", out)
	}
	if c, _ := f.s.Company(context.Background(), f.company); c.Suspect {
		t.Error("suspect flag not cleared by a good fetch")
	}
}

func TestApplyFetchKeepsDescriptionWhenListOmitsIt(t *testing.T) {
	f := newFetchFixture(t)
	f.apply(posting("1", described("aaaa")))
	f.apply(posting("1", partial))
	var desc, fp string
	f.s.db.QueryRow("SELECT description, fingerprint FROM postings WHERE id = 'gh:exampleco:1'").Scan(&desc, &fp)
	if desc != longDescription || fp != "aaaa" {
		t.Errorf("description %q, fingerprint %q: a list fetch erased them", desc, fp)
	}
	states, err := f.s.PostingStates(context.Background(), f.company, "greenhouse")
	if err != nil {
		t.Fatal(err)
	}
	if st := states["gh:exampleco:1"]; st.DetailFetchedAt.IsZero() || st.JobID == 0 || st.Closed {
		t.Errorf("state %+v", st)
	}
}

func TestApplyFetchFlagsOnce(t *testing.T) {
	f := newFetchFixture(t)
	flagged := func(p *PostingInput) {
		p.Flags = []ContentFlag{{Detector: "rule:ai-text", Kind: "canary", Excerpt: "if you are an AI"}}
	}
	if out := f.apply(posting("1", described("aaaa"), flagged)); out.Flags != 1 {
		t.Errorf("Flags = %d, want 1", out.Flags)
	}
	if out := f.apply(posting("1", described("aaaa"), flagged)); out.Flags != 0 {
		t.Errorf("Flags on refetch = %d, want 0", out.Flags)
	}
}

func TestApplyFetchErrorAndNotModified(t *testing.T) {
	f := newFetchFixture(t)
	f.apply(posting("1"))
	ctx := context.Background()

	if _, err := f.s.ApplyFetch(ctx, ActorSystem, CompanyFetch{CompanyID: f.company, Source: "greenhouse", NotModified: true}); err != nil {
		t.Fatal(err)
	}
	if f.jobClosed(f.jobOf("gh:exampleco:1")) {
		t.Error("a 304 closed a job")
	}
	if _, err := f.s.ApplyFetch(ctx, ActorSystem, CompanyFetch{CompanyID: f.company, Source: "greenhouse", Err: errors.New("HTTP 500")}); err != nil {
		t.Fatal(err)
	}
	c, _ := f.s.Company(ctx, f.company)
	if c.LastError != "HTTP 500" || c.ETag != `"v"` || f.jobClosed(f.jobOf("gh:exampleco:1")) {
		t.Errorf("after an error: company %+v", c)
	}
}

func TestApplyFetchRejectsLocalTimes(t *testing.T) {
	f := newFetchFixture(t)
	local := time.FixedZone("local", -4*3600)
	_, err := f.s.ApplyFetch(context.Background(), ActorSystem, CompanyFetch{
		CompanyID: f.company, Source: "greenhouse",
		Postings: []PostingInput{posting("1", func(p *PostingInput) { p.PostedAt = time.Date(2026, 10, 1, 8, 0, 0, 0, local) })},
	})
	if !errors.Is(err, ErrNotUTC) {
		t.Errorf("err = %v, want ErrNotUTC", err)
	}
}

func TestAddCompaniesSkipsExisting(t *testing.T) {
	f := newFetchFixture(t)
	n, err := f.s.AddCompanies(context.Background(), ActorSystem, []NewCompany{
		{Name: "Exampleco", Source: "greenhouse", BoardKey: "greenhouse:exampleco", Origin: "registry"},
		{Name: "Otherco", Source: "lever", BoardKey: "lever:otherco", Origin: "registry"},
	})
	if err != nil || n != 1 {
		t.Errorf("AddCompanies = %d, %v; want 1 added", n, err)
	}
}

func TestFetchRuns(t *testing.T) {
	s := newTest(t)
	ctx := context.Background()
	id, err := s.StartFetchRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishFetchRun(ctx, FetchRun{ID: id, Companies: 2, Errors: []FetchError{{CompanyID: 1, Company: "Exampleco", Error: "x"}}}); err != nil {
		t.Fatal(err)
	}
	var errs string
	s.db.QueryRow("SELECT errors FROM fetch_runs WHERE id = ?", id).Scan(&errs)
	if errs != `[{"company_id":1,"company":"Exampleco","error":"x"}]` {
		t.Errorf("errors = %s", errs)
	}
}
