package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
)

func found(id, title string) PostingInput {
	return PostingInput{
		ID: "tm:" + id, Source: "themuse", ExternalID: id, Search: true,
		Title: title, TitleKey: dedupe.TitleKey(title), URL: "https://muse.example/jobs/" + id, Workplace: "remote",
		Description: longDescription,
	}
}

func TestEmployers(t *testing.T) {
	f := newFetchFixture(t)
	ctx := context.Background()
	es, err := f.s.Employers(ctx, ActorSystem, "search", []string{"Exampleco, Inc.", "Made-Up Labs", "made-up labs", "Made-Up Labs"})
	if err != nil {
		t.Fatal(err)
	}
	if e := es["Exampleco, Inc."]; e.CompanyID != f.company || !e.Covered {
		t.Errorf("board company: %+v", e)
	}
	labs := es["Made-Up Labs"]
	if labs.CompanyID == f.company || labs.Covered || es["made-up labs"] != labs {
		t.Errorf("search company: %+v / %+v", labs, es["made-up labs"])
	}
	c, err := f.s.Company(ctx, labs.CompanyID)
	if err != nil || c.Name != "Made-Up Labs" || c.Source != "none" || c.Origin != "search" {
		t.Errorf("%+v, %v", c, err)
	}
	again, err := f.s.Employers(ctx, ActorSystem, "search", []string{"MADE-UP LABS LLC"})
	if err != nil || again["MADE-UP LABS LLC"] != labs {
		t.Errorf("second lookup %+v, %v", again, err)
	}

	// A disabled board doesn't cover its company's search results.
	if _, err := f.s.db.Exec("UPDATE companies SET enabled = 0 WHERE id = ?", f.company); err != nil {
		t.Fatal(err)
	}
	if es, _ := f.s.Employers(ctx, ActorSystem, "search", []string{"Exampleco"}); es["Exampleco"].Covered {
		t.Error("disabled board still covers its results")
	}
}

func TestSearchPostingsClose(t *testing.T) {
	f := newFetchFixture(t)
	ctx := context.Background()
	es, _ := f.s.Employers(ctx, ActorSystem, "search", []string{"Made-Up Labs"})
	labs := es["Made-Up Labs"].CompanyID
	out, err := f.s.ApplyPostings(ctx, ActorSystem, labs, []PostingInput{found("1", "Backend Engineer"), found("2", "Data Engineer")})
	if err != nil || len(out.NewJobs) != 2 {
		t.Fatalf("%+v, %v", out, err)
	}
	backend, data := f.jobOf("tm:1"), f.jobOf("tm:2")
	if j, _ := f.s.Job(ctx, backend); j.Origin != "search" || j.Description != longDescription {
		t.Errorf("job %+v", j)
	}

	// A board's list never closes search postings.
	f.apply(posting("10"))
	if f.jobClosed(backend) {
		t.Fatal("board fetch closed a search posting")
	}

	if err := f.s.SetJobStatus(ctx, ActorUser, data, StatusReview); err != nil {
		t.Fatal(err)
	}
	missing, err := f.s.CloseSearchMissing(ctx, ActorSystem, "themuse", map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.ClosedJobs) != 1 || missing.ClosedJobs[0] != backend || len(missing.Check) != 1 || missing.Check[0].ExternalID != "2" {
		t.Errorf("%+v", missing)
	}
	if f.jobClosed(data) {
		t.Error("pursued job closed without a lookup")
	}
	closed, err := f.s.ClosePostings(ctx, ActorSystem, []string{"tm:2", "tm:2", "tm:404"})
	if err != nil || len(closed) != 1 || closed[0] != data {
		t.Errorf("ClosePostings: %v, %v", closed, err)
	}
}

func TestBoardAddedLaterJoinsSearchJobs(t *testing.T) {
	f := newFetchFixture(t)
	ctx := context.Background()
	es, _ := f.s.Employers(ctx, ActorSystem, "search", []string{"Made-Up Labs"})
	labs := es["Made-Up Labs"].CompanyID
	if _, err := f.s.ApplyPostings(ctx, ActorSystem, labs, []PostingInput{found("1", "Backend Engineer")}); err != nil {
		t.Fatal(err)
	}
	job := f.jobOf("tm:1")

	// Adding the board turns the search company into a board company.
	id, err := f.s.AddCompany(ctx, ActorUser, NewCompany{Name: "Made-Up Labs, Inc.", Source: "lever", BoardKey: "lever:madeup", Origin: "user"})
	if err != nil || id != labs {
		t.Fatalf("AddCompany: %d, %v (want %d)", id, err, labs)
	}
	c, _ := f.s.Company(ctx, labs)
	if c.Source != "lever" || c.Origin != "user" || c.BoardKey != "lever:madeup" {
		t.Errorf("company %+v", c)
	}

	board := posting("77")
	board.ID, board.Source, board.CanonicalURL = "lv:madeup:77", "lever", "https://jobs.example/madeup/77"
	out, err := f.s.ApplyFetch(ctx, ActorSystem, CompanyFetch{CompanyID: labs, Source: "lever", Postings: []PostingInput{board}})
	if err != nil || len(out.NewJobs) != 0 || len(out.LinkedJobs) != 1 || out.LinkedJobs[0] != job {
		t.Errorf("board posting: %+v, %v", out, err)
	}
	events, _ := f.s.JobEvents(ctx, job)
	var rule string
	for _, e := range events {
		if e.Kind == "posting_linked" {
			rule = string(e.Data)
		}
	}
	if rule == "" || !strings.Contains(rule, "search_copy") {
		t.Errorf("link event %q", rule)
	}
}

func TestPrune(t *testing.T) {
	f := newFetchFixture(t)
	ctx := context.Background()
	es, _ := f.s.Employers(ctx, ActorSystem, "search", []string{"Made-Up Labs"})
	labs := es["Made-Up Labs"].CompanyID
	if _, err := f.s.ApplyPostings(ctx, ActorSystem, labs, []PostingInput{found("1", "Backend Engineer")}); err != nil {
		t.Fatal(err)
	}
	untouched := f.jobOf("tm:1")
	f.apply(posting("1", described("fp1")), posting("2", described("fp2"), func(p *PostingInput) {
		p.Title, p.TitleKey = "Data Engineer", "data engineer"
	}), posting("3", described("fp3"), func(p *PostingInput) { p.Title, p.TitleKey = "Site Engineer", "site engineer" }))
	skipped, pursued, noted := f.jobOf("gh:exampleco:1"), f.jobOf("gh:exampleco:2"), f.jobOf("gh:exampleco:3")
	for id, st := range map[int64]JobStatus{skipped: StatusSkipped, pursued: StatusReview} {
		if err := f.s.SetJobStatus(ctx, ActorUser, id, st); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.db.Exec("UPDATE jobs SET notes = 'made-up note' WHERE id = ?", noted); err != nil {
		t.Fatal(err)
	}

	// Everything closes.
	f.apply(posting("9"))
	if _, err := f.s.CloseSearchMissing(ctx, ActorSystem, "themuse", nil); err != nil {
		t.Fatal(err)
	}
	if out, err := f.s.Prune(ctx); err != nil || out != (PruneOutcome{}) {
		t.Fatalf("pruned right after closing: %+v, %v", out, err)
	}

	f.clock = f.clock.Add(RetentionPeriod + time.Hour)
	out, err := f.s.Prune(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out.JobsDeleted != 1 || out.CompaniesDeleted != 1 || out.DescriptionsCleared != 1 {
		t.Errorf("%+v", out)
	}
	if _, err := f.s.Job(ctx, untouched); err == nil {
		t.Error("untouched job kept")
	}
	if _, err := f.s.Company(ctx, labs); err == nil {
		t.Error("empty search company kept")
	}
	for id, want := range map[int64]string{skipped: "", pursued: longDescription, noted: longDescription} {
		j, err := f.s.Job(ctx, id)
		if err != nil || j.Description != want {
			t.Errorf("job %d: description %q, %v", id, j.Description, err)
		}
	}
	if out, _ := f.s.Prune(ctx); out != (PruneOutcome{}) {
		t.Errorf("second prune: %+v", out)
	}
}
