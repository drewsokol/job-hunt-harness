package web

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// TestPrepShown checks the dashboard marks an interview in the next few
// days with no prep notes until notes are saved, and shows stories with
// their uses, archived ones included, and practice memories.
func TestPrepShown(t *testing.T) {
	f := newFixture(t)
	ids := f.seed("Backend Engineer")
	job := ids["Backend Engineer"]
	ctx := context.Background()
	agent := store.Agent("test-agent")
	str := func(s string) *string { return &s }

	told, err := core.SaveStory(ctx, f.s, agent, 0, core.StoryInput{Title: str("Made-up migration"), Summary: str("Moved a made-up queue."),
		Body: str("The made-up queue dropped jobs; I led the move."), Claims: []string{"Led a team of four"}, Tags: []string{"leadership"}})
	if err != nil {
		t.Fatal(err)
	}
	old, err := core.SaveStory(ctx, f.s, agent, 0, core.StoryInput{Title: str("Old story"), Summary: str("Old."), Body: str("Old."),
		Claims: []string{"Did an old thing"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.SaveStory(ctx, f.s, agent, old.ID, core.StoryInput{Archive: "No longer used."}); err != nil {
		t.Fatal(err)
	}
	stories := []core.StoryLinkInput{{StoryID: told.ID, Landed: "well", Note: "Short and clear."}}
	iv, err := core.SaveInterview(ctx, f.s, agent, 0, core.InterviewInput{JobID: job, Stage: "technical",
		ScheduledAt: time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339), Stories: &stories})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Remember(ctx, f.s, agent, core.RememberInput{Content: "Answers run long.", Kind: "practice", Basis: "inferred"}); err != nil {
		t.Fatal(err)
	}

	next := func() *interviewJSON {
		for _, j := range f.list("applied") {
			if j.ID == job {
				return j.NextInterview
			}
		}
		return nil
	}
	if n := next(); n == nil || !n.NotPrepped {
		t.Fatalf("next interview %+v; want it not prepped", n)
	}
	var d jobDetailJSON
	f.ok("GET", fmt.Sprintf("/api/jobs/%d", job), nil, &d)
	if len(d.Interviews) != 1 || !d.Interviews[0].NotPrepped || len(d.Interviews[0].Stories) != 1 ||
		d.Interviews[0].Stories[0].Title != "Made-up migration" || d.Interviews[0].Stories[0].Landed != "well" {
		t.Errorf("detail interviews %+v", d.Interviews)
	}

	if _, err := core.SaveInterview(ctx, f.s, agent, iv.ID, core.InterviewInput{PrepNotes: "## Pitch\nMade-up."}); err != nil {
		t.Fatal(err)
	}
	if n := next(); n == nil || n.NotPrepped {
		t.Errorf("next interview %+v after prep notes", n)
	}

	var p profileJSON
	f.ok("GET", "/api/profile", nil, &p)
	if len(p.Stories) != 2 || p.Stories[0].Title != "Made-up migration" || len(p.Stories[0].Uses) != 1 ||
		p.Stories[0].Uses[0].Landed != "well" || p.Stories[0].Uses[0].Company == "" || p.Stories[0].Uses[0].Stage != "technical" {
		t.Errorf("stories %+v", p.Stories)
	}
	if p.Stories[1].Archived == "" || p.Stories[1].ArchiveReason != "No longer used." {
		t.Errorf("archived story %+v", p.Stories[1])
	}
	practice := false
	for _, m := range p.Memories {
		practice = practice || m.Kind == "practice"
	}
	if !practice || !strings.Contains(strings.Join(p.Kinds, " "), "practice") {
		t.Errorf("practice memories on the dashboard: %+v, kinds %v", p.Memories, p.Kinds)
	}
}

// TestPageShowsPrep checks the page has what the spec asks it to show:
// the "Not prepped yet" mark with the phrase and a copy button, and the
// stories on the Profile tab behind a "Show archived" toggle.
func TestPageShowsPrep(t *testing.T) {
	js, err := staticFiles.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"Not prepped yet", "help me prepare for my interview with", "navigator.clipboard.writeText", "Show archived", "storiesPanel(p.stories"} {
		if !strings.Contains(string(js), s) {
			t.Errorf("app.js doesn't have %q", s)
		}
	}
}
