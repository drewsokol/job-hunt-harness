package mcpserver

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// addTestJob adds a made-up job through add_jobs and returns its number.
func addTestJob(h *harness) int64 {
	h.t.Helper()
	var res struct {
		Jobs []struct {
			Job int64 `json:"job"`
		} `json:"jobs"`
	}
	h.call("add_jobs", map[string]any{"jobs": []any{map[string]any{
		"company": "Made-Up Labs", "title": "Platform Engineer", "location": "Remote - US",
		"description": "Made-Up Labs builds made-up tools. " + strings.Repeat("Real work on real systems. ", 12),
	}}}, &res)
	if len(res.Jobs) != 1 {
		h.t.Fatalf("add_jobs: %+v", res)
	}
	return res.Jobs[0].Job
}

type handledReply struct {
	Handled []struct {
		MessageID string `json:"message_id"`
		Action    string `json:"action"`
		Job       int64  `json:"job"`
		Outreach  int64  `json:"outreach_id"`
		At        string `json:"handled_at"`
		By        string `json:"handled_by"`
	} `json:"handled"`
	Marked         int `json:"marked"`
	AlreadyHandled []struct {
		MessageID string `json:"message_id"`
		Action    string `json:"action"`
	} `json:"already_handled"`
	CheckedThrough string `json:"checked_through"`
}

func TestHandledMessages(t *testing.T) {
	h := newHarness(t, Deps{})
	job := addTestJob(h)
	var thread struct {
		ID int64 `json:"outreach_id"`
	}
	h.call("save_outreach", map[string]any{"direction": "outbound", "person": "Sam Sample", "channel": "email", "company": "Made-Up Labs"}, &thread)

	var r handledReply
	h.call("handled_messages", map[string]any{"system": "gmail", "check": []string{"m1", "m2"}}, &r)
	if len(r.Handled) != 0 || r.Marked != 0 || r.CheckedThrough != "" {
		t.Fatalf("fresh check: %+v", r)
	}

	through := utc.Format(utc.Now().Add(-time.Hour).Truncate(time.Second))
	r = handledReply{}
	h.call("handled_messages", map[string]any{"system": "gmail", "checked_through": through, "mark": []any{
		map[string]any{"message_id": "m1", "action": "marked the job rejected", "job_id": job},
		map[string]any{"message_id": "m2", "action": "logged a reply", "outreach_id": thread.ID},
	}}, &r)
	if r.Marked != 2 || len(r.AlreadyHandled) != 0 || r.CheckedThrough != through {
		t.Fatalf("mark: %+v", r)
	}

	// The first mark is kept; a second one is reported, not applied.
	r = handledReply{}
	h.call("handled_messages", map[string]any{"system": "gmail", "mark": []any{
		map[string]any{"message_id": "m1", "action": "something else"},
		map[string]any{"message_id": "m3", "action": "nothing to do"},
	}, "check": []string{"m1", "m2", "m4"}}, &r)
	if r.Marked != 1 || len(r.AlreadyHandled) != 1 || r.AlreadyHandled[0].MessageID != "m1" || r.AlreadyHandled[0].Action != "marked the job rejected" {
		t.Fatalf("second mark: %+v", r)
	}
	if r.CheckedThrough != through {
		t.Errorf("checked_through = %q, want the stored %q on every call", r.CheckedThrough, through)
	}
	got := map[string]string{}
	for _, m := range r.Handled {
		got[m.MessageID] = fmt.Sprintf("%s|%d|%d|%s", m.Action, m.Job, m.Outreach, m.By)
		if m.At == "" {
			t.Errorf("%s has no handled_at", m.MessageID)
		}
	}
	want := map[string]string{
		"m1": fmt.Sprintf("marked the job rejected|%d|0|agent:test-agent", job),
		"m2": fmt.Sprintf("logged a reply|0|%d|agent:test-agent", thread.ID),
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("handled = %v, want %v", got, want)
	}

	// Systems are kept apart.
	r = handledReply{}
	h.call("handled_messages", map[string]any{"system": "outlook", "check": []string{"m1"}}, &r)
	if len(r.Handled) != 0 || r.CheckedThrough != "" {
		t.Errorf("outlook sees gmail's record: %+v", r)
	}

	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprint("x", i)
	}
	for name, args := range map[string]map[string]any{
		"system casing":   {"system": "Gmail"},
		"101 ids":         {"system": "gmail", "check": many},
		"id with a space": {"system": "gmail", "check": []string{"a b"}},
		"empty action":    {"system": "gmail", "mark": []any{map[string]any{"message_id": "m9", "action": " "}}},
		"long action":     {"system": "gmail", "mark": []any{map[string]any{"message_id": "m9", "action": strings.Repeat("a", 201)}}},
		"two-line action": {"system": "gmail", "mark": []any{map[string]any{"message_id": "m9", "action": "a\nb"}}},
		"listed twice":    {"system": "gmail", "mark": []any{map[string]any{"message_id": "m9", "action": "a"}, map[string]any{"message_id": "m9", "action": "b"}}},
		"no such job":     {"system": "gmail", "mark": []any{map[string]any{"message_id": "m9", "action": "a", "job_id": 9999}}},
		"future":          {"system": "gmail", "checked_through": utc.Format(utc.Now().Add(48 * time.Hour))},
		"not a time":      {"system": "gmail", "checked_through": "yesterday"},
	} {
		if msg := h.fail("handled_messages", args); !strings.Contains(msg, "invalid input") {
			t.Errorf("%s: %s", name, msg)
		}
	}
	r = handledReply{}
	h.call("handled_messages", map[string]any{"system": "gmail", "check": []string{"m9"}}, &r)
	if len(r.Handled) != 0 {
		t.Errorf("a refused call marked m9: %+v", r)
	}
}

type refsReply struct {
	Refs []struct {
		ID     int64  `json:"id"`
		System string `json:"system"`
		Ref    string `json:"ref"`
		URL    string `json:"url"`
	} `json:"refs"`
}

func TestEmailRefs(t *testing.T) {
	h := newHarness(t, Deps{})
	job := addTestJob(h)

	// Refs alone on update_application need no other change.
	h.call("update_application", map[string]any{"job_id": job, "add_refs": []any{
		map[string]any{"system": "gmail", "ref": "thread-1", "url": "https://mail.example/thread-1"},
		map[string]any{"system": "gmail", "ref": "thread-2"},
	}}, nil)
	h.call("update_application", map[string]any{"job_id": job, "add_refs": []any{map[string]any{"system": "gmail", "ref": "thread-1", "url": "https://mail.example/thread-1"}}}, nil)
	var j refsReply
	h.call("get_job", map[string]any{"job_id": job}, &j)
	if len(j.Refs) != 2 || j.Refs[0].URL != "https://mail.example/thread-1" || j.Refs[1].Ref != "thread-2" || j.Refs[1].URL != "" {
		t.Fatalf("get_job refs: %+v", j)
	}
	h.call("update_application", map[string]any{"job_id": job, "status": "applied", "remove_refs": []int64{j.Refs[1].ID}}, nil)
	j = refsReply{}
	h.call("get_job", map[string]any{"job_id": job}, &j)
	if len(j.Refs) != 1 || j.Refs[0].Ref != "thread-1" {
		t.Errorf("after remove: %+v", j)
	}

	// Outreach: added on a new thread, changed later, shown in its one-thread view.
	var thread struct {
		ID int64 `json:"outreach_id"`
	}
	h.call("save_outreach", map[string]any{"direction": "outbound", "person": "Sam Sample", "channel": "email", "job_id": job,
		"add_refs": []any{map[string]any{"system": "outlook", "ref": "conv-9", "url": "https://mail.example/conv-9"}}}, &thread)
	var one refsReply
	h.call("list_outreach", map[string]any{"outreach_id": thread.ID}, &one)
	if len(one.Refs) != 1 || one.Refs[0].Ref != "conv-9" {
		t.Fatalf("one thread refs: %+v", one)
	}
	h.call("save_outreach", map[string]any{"outreach_id": thread.ID, "remove_refs": []int64{one.Refs[0].ID}}, nil)
	one = refsReply{}
	h.call("list_outreach", map[string]any{"outreach_id": thread.ID}, &one)
	if len(one.Refs) != 0 {
		t.Errorf("after remove: %+v", one)
	}
	// The job's link is not the thread's.
	if msg := h.fail("save_outreach", map[string]any{"outreach_id": thread.ID, "remove_refs": []int64{j.Refs[0].ID}}); !strings.Contains(msg, "remove_refs") {
		t.Errorf("removing another target's link: %s", msg)
	}

	for name, ref := range map[string]map[string]any{
		"http":       {"system": "gmail", "ref": "t", "url": "http://mail.example/t"},
		"script":     {"system": "gmail", "ref": "t", "url": "javascript:alert(1)"},
		"userinfo":   {"system": "gmail", "ref": "t", "url": "https://user:pw@mail.example/t"},
		"no host":    {"system": "gmail", "ref": "t", "url": "https:///t"},
		"no ref":     {"system": "gmail", "ref": ""},
		"bad system": {"system": "G Mail", "ref": "t"},
	} {
		if msg := h.fail("update_application", map[string]any{"job_id": job, "add_refs": []any{ref}}); !strings.Contains(msg, "invalid input") {
			t.Errorf("%s: %s", name, msg)
		}
	}
	// A bad link stops the whole call: the status is not changed.
	h.fail("update_application", map[string]any{"job_id": job, "status": "rejected", "add_refs": []any{map[string]any{"system": "gmail", "ref": "t", "url": "http://x.example"}}})
	var status struct {
		Status string `json:"status"`
	}
	h.call("get_job", map[string]any{"job_id": job}, &status)
	if status.Status != "applied" {
		t.Errorf("status = %q after a refused call, want applied", status.Status)
	}
	if msg := h.fail("save_outreach", map[string]any{"direction": "outbound", "person": "Sam Sample", "channel": "email", "remove_refs": []int64{1}}); !strings.Contains(msg, "remove_refs") {
		t.Errorf("remove_refs on a new thread: %s", msg)
	}

	// A link that isn't the target's stops the whole call before anything
	// is saved, so a retry doesn't repeat the note or the status event.
	one = refsReply{}
	h.call("save_outreach", map[string]any{"outreach_id": thread.ID, "add_refs": []any{map[string]any{"system": "outlook", "ref": "conv-10"}}}, nil)
	h.call("list_outreach", map[string]any{"outreach_id": thread.ID}, &one)
	if msg := h.fail("update_application", map[string]any{"job_id": job, "status": "rejected", "append_note": "Made-up note.", "remove_refs": []int64{one.Refs[0].ID}}); !strings.Contains(msg, "remove_refs") {
		t.Errorf("removing the thread's link from the job: %s", msg)
	}
	var after struct {
		Status string `json:"status"`
		Notes  string `json:"notes"`
	}
	h.call("get_job", map[string]any{"job_id": job}, &after)
	if after.Status != "applied" || strings.Contains(after.Notes, "Made-up note.") {
		t.Errorf("a refused call saved: %+v", after)
	}
	if msg := h.fail("save_outreach", map[string]any{"outreach_id": thread.ID, "status": "replied", "note": "Made-up reply.", "remove_refs": []int64{j.Refs[0].ID}}); !strings.Contains(msg, "remove_refs") {
		t.Errorf("removing the job's link from the thread: %s", msg)
	}
	var th struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	h.call("list_outreach", map[string]any{"outreach_id": thread.ID}, &th)
	if th.Status == "replied" || strings.Contains(th.Note, "Made-up reply.") {
		t.Errorf("a refused call changed the thread: %+v", th)
	}
}
