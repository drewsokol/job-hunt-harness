package mcpserver

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// TestFindingOutreach checks that a thread can be found again without
// remembering the id save_outreach returned.
func TestFindingOutreach(t *testing.T) {
	src := &board{boards: map[string][]sources.Posting{"exampleco": {posting("1", "Senior Backend Engineer")}}}
	h := newHarness(t, Deps{Sources: map[string]sources.Source{"fake": src}})
	h.call("set_company", map[string]any{"name": "Exampleco"}, nil)
	var filters map[string]any
	if err := json.Unmarshal([]byte(engineersRemoteInUS), &filters); err != nil {
		t.Fatal(err)
	}
	h.call("set_filters", map[string]any{"filters": filters, "rationale": "Engineers, remote in the US."}, nil)
	h.call("refresh_jobs", map[string]any{}, nil)
	var page struct {
		Jobs []struct {
			Job int64 `json:"job"`
		} `json:"jobs"`
	}
	h.call("list_candidates", map[string]any{}, &page)
	if len(page.Jobs) != 1 {
		t.Fatalf("candidates %+v", page)
	}
	job := page.Jobs[0].Job

	type logged struct {
		ID int64 `json:"outreach_id"`
	}
	var aboutJob, general, other logged
	h.call("save_outreach", map[string]any{"person": "Alex Example", "channel": "linkedin", "job_id": job, "sent_at": daysAgo(20)}, &aboutJob)
	h.call("save_outreach", map[string]any{"person": "Alex Example", "channel": "email", "company": "Exampleco", "sent_at": daysAgo(10), "no_follow_up": true}, &general)
	longNote := strings.Repeat("Met at a made-up meetup. ", 20)
	h.call("save_outreach", map[string]any{"person": "Sam Sample", "channel": "email", "company": "Made-Up Labs", "sent_at": daysAgo(5), "note": longNote}, &other)

	type thread struct {
		ID        int64  `json:"outreach_id"`
		Person    string `json:"person"`
		Job       int64  `json:"job"`
		Status    string `json:"status"`
		Updated   string `json:"updated_at"`
		Note      string `json:"note"`
		Cut       bool   `json:"note_truncated"`
		Untrusted struct {
			Company string `json:"company"`
		} `json:"untrusted"`
	}
	type list struct {
		Total    int      `json:"total"`
		Outreach []thread `json:"outreach"`
		Next     string   `json:"next"`
	}
	ids := func(l list) string {
		var out []string
		for _, o := range l.Outreach {
			out = append(out, fmt.Sprint(o.ID))
		}
		return strings.Join(out, ",")
	}
	find := func(args map[string]any) list {
		t.Helper()
		var l list
		h.call("list_outreach", args, &l)
		return l
	}

	all := find(map[string]any{})
	if all.Total != 3 || len(all.Outreach) != 3 || all.Outreach[0].Updated == "" {
		t.Fatalf("every thread: %+v", all)
	}
	text, _ := h.raw("list_outreach", map[string]any{})
	if bad := listingTextOutsideEnvelope(t, text); len(bad) > 0 {
		t.Errorf("list_outreach: listing text outside the envelope at %v", bad)
	}
	if l := find(map[string]any{"person": "alex"}); l.Total != 2 {
		t.Errorf("person alex: %s", ids(l))
	}
	if l := find(map[string]any{"company": "made-up"}); l.Total != 1 || l.Outreach[0].ID != other.ID ||
		!l.Outreach[0].Cut || len([]rune(l.Outreach[0].Note)) != listNoteChars {
		t.Errorf("company made-up: %+v", l)
	}
	if l := find(map[string]any{"job_id": job}); l.Total != 1 || l.Outreach[0].ID != aboutJob.ID {
		t.Errorf("job %d: %s", job, ids(l))
	}
	if l := find(map[string]any{"follow_up": "none"}); l.Total != 1 || l.Outreach[0].ID != general.ID {
		t.Errorf("no follow-up: %s", ids(l))
	}
	if l := find(map[string]any{"follow_up": "due"}); l.Total != 1 || l.Outreach[0].ID != aboutJob.ID {
		t.Errorf("follow-up due: %s", ids(l))
	}
	if l := find(map[string]any{"limit": 2}); len(l.Outreach) != 2 || l.Next != "2" {
		t.Errorf("first page: %+v", l)
	} else if l2 := find(map[string]any{"limit": 2, "cursor": l.Next}); len(l2.Outreach) != 1 || l2.Next != "" {
		t.Errorf("second page: %+v", l2)
	}
	for _, bad := range []map[string]any{
		{"status": []string{"waiting"}},
		{"follow_up": "soon"},
		{"outreach_id": other.ID, "person": "Sam"},
		{"outreach_id": 999},
	} {
		if msg := h.fail("list_outreach", bad); msg == "" {
			t.Errorf("%v: no error", bad)
		}
	}

	// By person: two open threads with Alex, so nothing changes, and the
	// error lists both ids without quoting a company.
	msg := h.fail("save_outreach", map[string]any{"find": map[string]any{"person": "alex example"}, "status": "replied"})
	if !strings.Contains(msg, fmt.Sprint(aboutJob.ID)) || !strings.Contains(msg, fmt.Sprint(general.ID)) || strings.Contains(msg, "Exampleco") {
		t.Errorf("two matches: %s", msg)
	}
	if l := find(map[string]any{"status": []string{"replied"}}); l.Total != 0 {
		t.Errorf("an ambiguous update changed %s", ids(l))
	}
	if msg := h.fail("save_outreach", map[string]any{"find": map[string]any{"person": "Nobody Made-Up"}, "status": "replied"}); !strings.Contains(msg, "no open outreach thread") {
		t.Errorf("no match: %s", msg)
	}
	if msg := h.fail("save_outreach", map[string]any{"find": map[string]any{"person": "Alex"}, "status": "replied"}); !strings.Contains(msg, "no open outreach thread") {
		t.Errorf("part of a name matched: %s", msg)
	}
	if msg := h.fail("save_outreach", map[string]any{"outreach_id": general.ID, "find": map[string]any{"person": "Alex Example"}, "status": "closed"}); !strings.Contains(msg, "not both") {
		t.Errorf("id and person: %s", msg)
	}
	if msg := h.fail("save_outreach", map[string]any{"status": "closed"}); !strings.Contains(msg, "outreach_id or find") {
		t.Errorf("a status without a thread: %s", msg)
	}
	if msg := h.fail("save_outreach", map[string]any{"outreach_id": general.ID, "person": "Someone Else", "status": "closed"}); !strings.Contains(msg, "set when it's logged") {
		t.Errorf("a new thread's fields on a change: %s", msg)
	}

	// The job narrows it to one.
	var updated struct {
		ID int64 `json:"outreach_id"`
	}
	h.call("save_outreach", map[string]any{"find": map[string]any{"person": "ALEX EXAMPLE", "job_id": job}, "status": "replied"}, &updated)
	if updated.ID != aboutJob.ID {
		t.Errorf("updated %d; want %d", updated.ID, aboutJob.ID)
	}
	// Closed threads don't count, so once the other one is closed the
	// person alone is enough; a closed thread is reached only by its id.
	h.call("save_outreach", map[string]any{"outreach_id": general.ID, "status": "closed"}, nil)
	h.call("save_outreach", map[string]any{"find": map[string]any{"person": "Alex Example", "company": "exampleco"}, "status": "scheduled", "note": "A call on Friday."}, &updated)
	if updated.ID != aboutJob.ID {
		t.Errorf("updated %d; want %d", updated.ID, aboutJob.ID)
	}
	if l := find(map[string]any{"status": []string{"open"}}); l.Total != 2 {
		t.Errorf("open threads: %s", ids(l))
	}

	// One thread: its whole note and its history.
	var one struct {
		thread
		History []struct {
			Kind  string `json:"kind"`
			Actor string `json:"actor"`
		} `json:"history"`
	}
	h.call("list_outreach", map[string]any{"outreach_id": aboutJob.ID}, &one)
	var kinds []string
	for _, e := range one.History {
		kinds = append(kinds, e.Kind)
	}
	if one.Status != "scheduled" || one.Note != "A call on Friday." ||
		strings.Join(kinds, ",") != "outreach_logged,outreach_updated,outreach_updated" || one.History[0].Actor != "agent:test-agent" {
		t.Errorf("one thread: %+v", one)
	}
	h.call("list_outreach", map[string]any{"outreach_id": other.ID}, &one)
	if one.Note != strings.TrimSpace(longNote) || one.Cut {
		t.Errorf("one thread's note was cut: %q", one.Note)
	}

	// get_job lists the job's threads, and its timeline names them.
	var detail struct {
		Outreach []struct {
			ID     int64  `json:"outreach_id"`
			Person string `json:"person"`
			Status string `json:"status"`
		} `json:"outreach"`
		Timeline []struct {
			Kind       string `json:"kind"`
			OutreachID int64  `json:"outreach_id"`
		} `json:"timeline"`
	}
	h.call("get_job", map[string]any{"job_id": job}, &detail)
	if len(detail.Outreach) != 1 || detail.Outreach[0].ID != aboutJob.ID || detail.Outreach[0].Status != "scheduled" {
		t.Errorf("get_job outreach: %+v", detail.Outreach)
	}
	named := 0
	for _, e := range detail.Timeline {
		if strings.HasPrefix(e.Kind, "outreach_") {
			if e.OutreachID != aboutJob.ID {
				t.Errorf("timeline event %s names thread %d", e.Kind, e.OutreachID)
			}
			named++
		}
	}
	if named != 3 {
		t.Errorf("%d outreach events in the timeline: %+v", named, detail.Timeline)
	}

	// The summary counts threads by status.
	var summary struct {
		Outreach map[string]int `json:"outreach_by_status"`
	}
	h.call("get_tracker", map[string]any{}, &summary)
	if want := map[string]int{"sent": 1, "accepted": 0, "replied": 0, "scheduled": 1, "no_reply": 0, "closed": 1}; fmt.Sprint(summary.Outreach) != fmt.Sprint(want) {
		t.Errorf("outreach by status %v; want %v", summary.Outreach, want)
	}
}
