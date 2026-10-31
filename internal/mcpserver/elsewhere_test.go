package mcpserver

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// TestAppliedElsewhere records applications the user made outside jhh
// with add_jobs and a status.
func TestAppliedElsewhere(t *testing.T) {
	src := &board{boards: map[string][]sources.Posting{
		"exampleco": {posting("1", "Senior Backend Engineer")},
	}}
	h := newHarness(t, Deps{Sources: map[string]sources.Source{"fake": src}})
	h.call("set_company", map[string]any{"name": "Exampleco"}, nil)
	var filters map[string]any
	if err := json.Unmarshal([]byte(engineersRemoteInUS), &filters); err != nil {
		t.Fatal(err)
	}
	h.call("set_filters", map[string]any{"filters": filters, "rationale": "Engineers, remote in the US."}, nil)
	h.call("refresh_jobs", map[string]any{}, nil)

	type added struct {
		Jobs []struct {
			Job      int64  `json:"job"`
			Existing bool   `json:"already_in_jhh"`
			ByTitle  bool   `json:"matched_by_title"`
			Status   string `json:"status"`
		} `json:"jobs"`
	}
	type jobView struct {
		Status    string `json:"status"`
		Origin    string `json:"origin"`
		Applied   string `json:"applied"`
		Elsewhere bool   `json:"applied_elsewhere"`
		Timeline  []struct {
			Kind  string `json:"kind"`
			Actor string `json:"actor"`
		} `json:"timeline"`
	}
	get := func(id int64) jobView {
		t.Helper()
		var v jobView
		h.call("get_job", map[string]any{"job_id": id}, &v)
		return v
	}

	var res added
	h.call("add_jobs", map[string]any{"jobs": []any{
		// A sales job the filters would drop, rejected a while ago.
		map[string]any{"company": "Widget Works", "title": "Sr. Sales Manager", "status": "rejected", "applied_at": "2026-09-01T00:00:00Z"},
		// The board posting jhh already has.
		map[string]any{"company": "Exampleco", "url": "https://boards.example/exampleco/1", "status": "interviewing"},
		// No status: found by the agent, as before.
		map[string]any{"company": "Widget Works", "title": "Platform Engineer",
			"description": "Widget Works makes made-up widgets. " + strings.Repeat("Real work on real systems. ", 12)},
	}}, &res)
	if len(res.Jobs) != 3 {
		t.Fatalf("add_jobs: %+v", res)
	}
	if j := res.Jobs[0]; j.Job == 0 || j.Existing || j.ByTitle || j.Status != "rejected" {
		t.Errorf("a new application made elsewhere: %+v", j)
	}
	if j := res.Jobs[1]; !j.Existing || j.ByTitle || j.Status != "interviewing" {
		t.Errorf("a job jhh already had: %+v", j)
	}
	if j := res.Jobs[2]; j.Existing || j.Status != "unscreened" {
		t.Errorf("a job without status: %+v", j)
	}

	v := get(res.Jobs[0].Job)
	if v.Status != "rejected" || v.Origin != "agent" || v.Applied != "2026-09-01T00:00:00.000Z" || !v.Elsewhere {
		t.Errorf("get_job: %+v", v)
	}
	if !slices.ContainsFunc(v.Timeline, func(e struct {
		Kind  string `json:"kind"`
		Actor string `json:"actor"`
	}) bool {
		return e.Kind == "status_changed" && strings.HasPrefix(e.Actor, "agent:")
	}) {
		t.Errorf("no status change in the timeline: %+v", v.Timeline)
	}
	if v := get(res.Jobs[1].Job); v.Status != "interviewing" || !v.Elsewhere || v.Applied == "" {
		t.Errorf("the board job: %+v", v)
	}
	if v := get(res.Jobs[2].Job); v.Elsewhere {
		t.Errorf("a job without status is marked applied elsewhere: %+v", v)
	}

	// The same company and title, spelled a little differently, with no
	// link or description: the same job. Its applied date stays.
	var again added
	h.call("add_jobs", map[string]any{"jobs": []any{
		map[string]any{"company": "Widget Works", "title": "Senior Sales Manager", "status": "withdrawn"},
		map[string]any{"company": "Widget Works", "title": "Sales Director", "status": "applied"},
	}}, &again)
	if j := again.Jobs[0]; j.Job != res.Jobs[0].Job || !j.Existing || !j.ByTitle || j.Status != "withdrawn" {
		t.Errorf("matched by title: %+v", j)
	}
	if v := get(res.Jobs[0].Job); v.Status != "withdrawn" || v.Applied != "2026-09-01T00:00:00.000Z" {
		t.Errorf("after the title match: %+v", v)
	}
	if j := again.Jobs[1]; j.Job == 0 || j.Job == res.Jobs[0].Job || j.Existing || j.ByTitle || j.Status != "applied" {
		t.Errorf("a different title: %+v", j)
	}

	// The prefilter judges them again but never moves them.
	h.call("refresh_jobs", map[string]any{}, nil)
	for id, want := range map[int64]string{res.Jobs[0].Job: "withdrawn", res.Jobs[1].Job: "interviewing", again.Jobs[1].Job: "applied"} {
		if v := get(id); v.Status != want {
			t.Errorf("job %d after a refresh: %s, want %s", id, v.Status, want)
		}
	}

	for _, bad := range []map[string]any{
		{"company": "Widget Works", "title": "Engineer", "status": "screening"},
		{"company": "Widget Works", "title": "Engineer", "applied_at": "2026-09-01T00:00:00Z"},
		{"company": "Widget Works", "title": "Engineer", "status": "applied", "applied_at": "2099-01-01T00:00:00Z"},
		{"company": "Widget Works", "title": "Engineer", "status": "applied", "applied_at": "last week"},
	} {
		if msg := h.fail("add_jobs", map[string]any{"jobs": []any{bad}}); !strings.Contains(msg, "invalid input") {
			t.Errorf("add_jobs(%v): %s", bad, msg)
		}
	}
}
