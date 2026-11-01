package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

type shortlisted struct {
	Total int `json:"total"`
	Jobs  []struct {
		Job       int64 `json:"job"`
		Shortlist *struct {
			Rank   int    `json:"rank"`
			Reason string `json:"reason"`
			SetBy  string `json:"set_by"`
		} `json:"shortlist"`
	} `json:"jobs"`
}

// order returns the jobs in review in list_candidates' order, and the
// shortlisted ones' places as "job:rank".
func reviewOrder(h *harness) (all []int64, places []string) {
	h.t.Helper()
	var page shortlisted
	text, isErr := h.raw("list_candidates", map[string]any{"status": "review"})
	if isErr {
		h.t.Fatalf("list_candidates: %s", text)
	}
	if bad := listingTextOutsideEnvelope(h.t, text); len(bad) > 0 {
		h.t.Errorf("list_candidates: listing text outside the envelope at %v", bad)
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		h.t.Fatal(err)
	}
	for _, j := range page.Jobs {
		all = append(all, j.Job)
		if j.Shortlist != nil {
			places = append(places, fmt.Sprintf("%d:%d", j.Job, j.Shortlist.Rank))
		}
	}
	return all, places
}

func shortlistNewsOf(h *harness) shortlistNews {
	h.t.Helper()
	var r struct {
		Shortlist shortlistNews `json:"shortlist"`
	}
	h.call("whats_new", map[string]any{}, &r)
	return r.Shortlist
}

func TestShortlist(t *testing.T) {
	ps := []sources.Posting{posting("1", "Platform Engineer"), posting("2", "Backend Engineer"), posting("3", "Data Engineer"), posting("4", "Frontend Engineer")}
	src := &board{boards: map[string][]sources.Posting{"exampleco": ps}}
	h := newHarness(t, Deps{Sources: map[string]sources.Source{"fake": src}})
	ctx := context.Background()
	h.call("set_company", map[string]any{"name": "Exampleco"}, nil)
	var filters map[string]any
	// A title keyword gives Platform Engineer the highest prefilter score;
	// the body keyword lets the rest through.
	if err := json.Unmarshal([]byte(`{"schema_version": 1, "titles": {"require_any": ["engineer"]}, "location": {"remote_in": ["US"]},
		"keywords": {"title_any": ["platform"], "body_any": ["systems"]}}`), &filters); err != nil {
		t.Fatal(err)
	}
	h.call("set_filters", map[string]any{"filters": filters, "rationale": "Engineers, remote in the US."}, nil)
	h.call("refresh_jobs", map[string]any{}, nil)
	ids := map[string]int64{}
	for _, p := range ps {
		id, err := h.s.PostingJob(ctx, "fk:exampleco:"+p.ExternalID)
		if err != nil {
			t.Fatal(err)
		}
		ids[strings.Fields(p.Title)[0]] = id
	}
	a, b, c, d := ids["Platform"], ids["Backend"], ids["Data"], ids["Frontend"]
	var rate []map[string]any
	for _, id := range []int64{a, b, c, d} {
		rate = append(rate, map[string]any{"job_id": id, "fit": "good", "reason": "Matches the profile."})
	}
	h.call("record_screening", map[string]any{"screenings": rate}, nil)

	if all, places := reviewOrder(h); len(all) != 4 || all[0] != a || len(places) != 0 {
		t.Fatalf("before a shortlist: %v %v; want job %d (the highest score) first", all, places, a)
	}
	if n := shortlistNewsOf(h); n.Size != 0 || n.Unranked != 4 {
		t.Errorf("whats_new with no shortlist: %+v; want size 0 and all 4 good jobs", n)
	}

	// The agent sets C, A, B; they come first, in that order, with their
	// reasons, and the rest follow.
	text, isErr := h.raw("set_shortlist", map[string]any{"jobs": []map[string]any{
		{"job_id": c, "reason": "Pay fits, closing soon"}, {"job_id": a, "reason": " Principal level "}, {"job_id": b, "reason": "Strong team"}}})
	if isErr {
		t.Fatalf("set_shortlist: %s", text)
	}
	if bad := listingTextOutsideEnvelope(t, text); len(bad) > 0 {
		t.Errorf("set_shortlist: listing text outside the envelope at %v", bad)
	}
	var set struct {
		Shortlist []struct {
			Rank      int    `json:"rank"`
			Job       int64  `json:"job"`
			Reason    string `json:"reason"`
			Untrusted struct {
				Company string `json:"company"`
				Title   string `json:"title"`
			} `json:"untrusted"`
		} `json:"shortlist"`
	}
	if err := json.Unmarshal([]byte(text), &set); err != nil {
		t.Fatal(err)
	}
	if len(set.Shortlist) != 3 || set.Shortlist[0].Job != c || set.Shortlist[1].Rank != 2 || set.Shortlist[1].Reason != "Principal level" ||
		set.Shortlist[2].Job != b || set.Shortlist[0].Untrusted.Company != "Exampleco" || !strings.HasPrefix(set.Shortlist[0].Untrusted.Title, "Data Engineer") {
		t.Errorf("set_shortlist: %s", text)
	}
	want := []int64{c, a, b, d}
	all, places := reviewOrder(h)
	if !slices.Equal(all, want) || !slices.Equal(places, []string{fmt.Sprintf("%d:1", c), fmt.Sprintf("%d:2", a), fmt.Sprintf("%d:3", b)}) {
		t.Errorf("after set_shortlist: %v %v; want %v", all, places, want)
	}
	var page shortlisted
	h.call("list_candidates", map[string]any{"status": "review", "limit": 1}, &page)
	if p := page.Jobs[0].Shortlist; p == nil || p.Reason != "Pay fits, closing soon" || p.SetBy != "agent" {
		t.Errorf("the first shortlisted job: %+v", p)
	}
	if n := shortlistNewsOf(h); n.Size != 3 || n.Unranked != 0 {
		t.Errorf("whats_new after set_shortlist: %+v; want size 3 and none screened since", n)
	}

	// A new session sees the same order.
	s2, err := store.Open(ctx, h.s.Path(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })
	if all, _ := reviewOrder(newHarness(t, Deps{Store: s2})); !slices.Equal(all, want) {
		t.Errorf("a new session: %v; want %v", all, want)
	}

	// Re-screening D after set_shortlist counts it as unranked; the user's
	// moves on the dashboard don't reset that.
	time.Sleep(5 * time.Millisecond)
	h.call("record_screening", map[string]any{"screenings": []map[string]any{{"job_id": d, "fit": "strong", "reason": "Better than it looked."}}}, nil)
	if n := shortlistNewsOf(h); n.Unranked != 1 {
		t.Errorf("whats_new after a new screening: %+v; want 1 unranked", n)
	}
	if err := core.ChangeShortlist(ctx, h.s, store.ActorUser, b, "up"); err != nil {
		t.Fatal(err)
	}
	if n := shortlistNewsOf(h); n.Size != 3 || n.Unranked != 1 {
		t.Errorf("whats_new after the user moved a job: %+v", n)
	}
	h.call("list_candidates", map[string]any{"status": "review", "limit": 3}, &page)
	if page.Jobs[1].Job != b || page.Jobs[1].Shortlist.SetBy != "user" || page.Jobs[2].Job != a || page.Jobs[2].Shortlist.SetBy != "user" {
		t.Errorf("after the user moved B up: %+v", page.Jobs)
	}
	if err := core.ChangeShortlist(ctx, h.s, store.ActorUser, b, "down"); err != nil {
		t.Fatal(err)
	}

	// Marking A applied takes it off the shown list; undoing that puts it
	// back in its place.
	h.call("update_application", map[string]any{"job_id": a, "status": "applied"}, nil)
	if _, places := reviewOrder(h); !slices.Equal(places, []string{fmt.Sprintf("%d:1", c), fmt.Sprintf("%d:2", b)}) {
		t.Errorf("with A applied: %v", places)
	}
	if n := shortlistNewsOf(h); n.Size != 2 {
		t.Errorf("whats_new with A applied: %+v; want size 2", n)
	}
	h.call("update_application", map[string]any{"job_id": a, "status": "review"}, nil)
	if _, places = reviewOrder(h); !slices.Equal(places, []string{fmt.Sprintf("%d:1", c), fmt.Sprintf("%d:2", a), fmt.Sprintf("%d:3", b)}) {
		t.Errorf("after undoing applied: %v", places)
	}

	// A closed posting isn't shown; seen again, it's back in its place.
	src.mu.Lock()
	src.boards["exampleco"] = []sources.Posting{ps[0], ps[1], ps[3]}
	src.mu.Unlock()
	h.call("refresh_jobs", map[string]any{}, nil)
	if _, places = reviewOrder(h); !slices.Equal(places, []string{fmt.Sprintf("%d:1", a), fmt.Sprintf("%d:2", b)}) {
		t.Errorf("with C's posting closed: %v", places)
	}
	src.mu.Lock()
	src.boards["exampleco"] = ps
	src.mu.Unlock()
	h.call("refresh_jobs", map[string]any{}, nil)
	if _, places = reviewOrder(h); !slices.Equal(places, []string{fmt.Sprintf("%d:1", c), fmt.Sprintf("%d:2", a), fmt.Sprintf("%d:3", b)}) {
		t.Errorf("with C's posting back: %v", places)
	}

	// Re-screening: no takes B off the shown list; good keeps C's place.
	h.call("record_screening", map[string]any{"screenings": []map[string]any{
		{"job_id": b, "fit": "no", "reason": "Wrong role after all."}, {"job_id": c, "fit": "good", "reason": "Still a match."}}}, nil)
	if _, places = reviewOrder(h); !slices.Equal(places, []string{fmt.Sprintf("%d:1", c), fmt.Sprintf("%d:2", a)}) {
		t.Errorf("after re-screening: %v", places)
	}

	// A job out of review is refused by number, and nothing is saved.
	msg := h.fail("set_shortlist", map[string]any{"jobs": []map[string]any{{"job_id": d, "reason": "Top pick"}, {"job_id": b, "reason": "Back up"}, {"job_id": 9999, "reason": "Gone"}}})
	if !strings.Contains(msg, fmt.Sprintf("job %d is screened_out, not in review", b)) || !strings.Contains(msg, "job 9999 doesn't exist") || strings.Contains(msg, fmt.Sprintf("job %d ", d)) {
		t.Errorf("set_shortlist with jobs out of review: %s", msg)
	}
	if _, places = reviewOrder(h); !slices.Equal(places, []string{fmt.Sprintf("%d:1", c), fmt.Sprintf("%d:2", a)}) {
		t.Errorf("a refused set_shortlist changed the list: %v", places)
	}
	var many []map[string]any
	for i := range 31 {
		many = append(many, map[string]any{"job_id": i + 1, "reason": "Fits"})
	}
	if msg := h.fail("set_shortlist", map[string]any{"jobs": many}); !strings.Contains(msg, "at most 30") {
		t.Errorf("31 jobs: %s", msg)
	}
	for _, bad := range []map[string]any{
		{"job_id": c, "reason": " "},
		{"job_id": c, "reason": strings.Repeat("x", 121)},
	} {
		if msg := h.fail("set_shortlist", map[string]any{"jobs": []map[string]any{bad}}); !strings.Contains(msg, "reason") {
			t.Errorf("set_shortlist with %v: %s", bad, msg)
		}
	}
	if msg := h.fail("set_shortlist", map[string]any{"jobs": []map[string]any{{"job_id": c, "reason": "One"}, {"job_id": c, "reason": "Two"}}}); !strings.Contains(msg, "twice") {
		t.Errorf("a job listed twice: %s", msg)
	}

	// An empty list clears it.
	h.call("set_shortlist", map[string]any{"jobs": []any{}}, &set)
	if len(set.Shortlist) != 0 {
		t.Errorf("clearing: %+v", set)
	}
	if _, places = reviewOrder(h); len(places) != 0 {
		t.Errorf("after clearing: %v", places)
	}
	if n := shortlistNewsOf(h); n.Size != 0 || n.Unranked != 0 {
		t.Errorf("whats_new after clearing: %+v; want nothing screened since", n)
	}
}
