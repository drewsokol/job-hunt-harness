package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

func daysAgo(n int) string {
	return time.Now().UTC().Add(-time.Duration(n) * 24 * time.Hour).Format(time.RFC3339)
}

func TestOutreachAndCheckIns(t *testing.T) {
	src := &board{boards: map[string][]sources.Posting{"exampleco": {posting("1", "Senior Backend Engineer"), posting("2", "Data Engineer")}}}
	h := newHarness(t, Deps{Sources: map[string]sources.Source{"fake": src}})
	h.call("set_company", map[string]any{"name": "Exampleco"}, nil)
	var filters map[string]any
	if err := json.Unmarshal([]byte(engineersRemoteInUS), &filters); err != nil {
		t.Fatal(err)
	}
	h.call("set_filters", map[string]any{"filters": filters, "rationale": "Engineers, remote in the US."}, nil)
	h.call("refresh_jobs", map[string]any{}, nil)

	type news struct {
		Since   string `json:"since"`
		Now     string `json:"now"`
		NewJobs []struct {
			Job int64 `json:"job"`
		} `json:"new_jobs"`
		FollowUps []struct {
			ID int64 `json:"outreach_id"`
		} `json:"follow_ups_due"`
		Changed []struct {
			Job    int64  `json:"job"`
			Closed string `json:"closed"`
		} `json:"applications_closed_or_reposted"`
	}
	var first news
	text, _ := h.raw("whats_new", map[string]any{})
	if err := json.Unmarshal([]byte(text), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.NewJobs) != 2 {
		t.Fatalf("first whats_new: %s", text)
	}
	if bad := listingTextOutsideEnvelope(t, text); len(bad) > 0 {
		t.Errorf("whats_new: listing text outside the envelope at %v", bad)
	}
	job := first.NewJobs[0].Job
	var second news
	h.call("whats_new", map[string]any{}, &second)
	if second.Since != first.Now || len(second.NewJobs) != 0 {
		t.Errorf("second whats_new: %+v after %+v", second, first)
	}

	// Outreach: the contact is reused, and the follow-up defaults to a week.
	type logged struct {
		ID         int64  `json:"outreach_id"`
		ContactID  int64  `json:"contact_id"`
		NewContact bool   `json:"new_contact"`
		FollowUpAt string `json:"follow_up_at"`
	}
	var a, b logged
	h.call("save_outreach", map[string]any{"person": "Alex Example", "channel": "LinkedIn", "job_id": job, "role": "Engineering Manager", "sent_at": daysAgo(20)}, &a)
	h.call("save_outreach", map[string]any{"person": "alex example", "channel": "email", "company": "Exampleco", "sent_at": daysAgo(15), "no_follow_up": true}, &b)
	if !a.NewContact || b.NewContact || a.ContactID != b.ContactID || a.FollowUpAt == "" || b.FollowUpAt != "" {
		t.Errorf("save_outreach: %+v then %+v", a, b)
	}
	if msg := h.fail("save_outreach", map[string]any{"person": "Alex Example", "channel": "email", "sent_at": daysAgo(-3)}); !strings.Contains(msg, "in the future") {
		t.Errorf("a message not sent yet: %s", msg)
	}

	h.call("update_application", map[string]any{"job_id": job, "status": "applied", "applied_at": daysAgo(30)}, nil)

	var summary struct {
		Applications int `json:"applications"`
		AppliedLast7 int `json:"applied_last_7_days"`
		FollowUpsDue int `json:"follow_ups_due"`
	}
	h.call("get_tracker", nil, &summary)
	if summary.Applications != 1 || summary.AppliedLast7 != 0 || summary.FollowUpsDue != 1 {
		t.Errorf("summary: %+v", summary)
	}
	type view struct {
		Jobs []struct {
			Job int64 `json:"job"`
		} `json:"jobs"`
		Outreach []struct {
			ID     int64  `json:"outreach_id"`
			Person string `json:"person"`
		} `json:"outreach"`
	}
	var due, stale, applied view
	text, _ = h.raw("get_tracker", map[string]any{"view": "followups_due"})
	if err := json.Unmarshal([]byte(text), &due); err != nil {
		t.Fatal(err)
	}
	if len(due.Outreach) != 1 || due.Outreach[0].ID != a.ID || due.Outreach[0].Person != "Alex Example" {
		t.Errorf("followups_due: %s", text)
	}
	if bad := listingTextOutsideEnvelope(t, text); len(bad) > 0 {
		t.Errorf("followups_due: listing text outside the envelope at %v", bad)
	}
	h.call("get_tracker", map[string]any{"view": "stale"}, &stale)
	if len(stale.Jobs) != 1 || len(stale.Outreach) != 1 || stale.Outreach[0].ID != b.ID {
		t.Errorf("stale: %+v", stale)
	}
	h.call("get_tracker", map[string]any{"view": "applied"}, &applied)
	if len(applied.Jobs) != 1 || applied.Jobs[0].Job != job {
		t.Errorf("applied: %+v", applied)
	}
	if msg := h.fail("get_tracker", map[string]any{"view": "everything"}); !strings.Contains(msg, "view") {
		t.Errorf("unknown view: %s", msg)
	}

	h.call("save_outreach", map[string]any{"outreach_id": a.ID, "status": "replied", "note": "Suggested a call."}, nil)
	due = view{}
	h.call("get_tracker", map[string]any{"view": "followups_due"}, &due)
	if len(due.Outreach) != 0 {
		t.Errorf("a reply leaves a follow-up due: %+v", due)
	}
	if msg := h.fail("save_outreach", map[string]any{"outreach_id": a.ID}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("empty update: %s", msg)
	}
	if msg := h.fail("save_outreach", map[string]any{"outreach_id": a.ID, "status": "ghosted"}); !strings.Contains(msg, "status") {
		t.Errorf("bad status: %s", msg)
	}

	// An application whose posting closes shows up in the next check-in.
	src.mu.Lock()
	var detail struct {
		Postings []struct {
			ID string `json:"id"`
		} `json:"postings"`
	}
	h.call("get_job", map[string]any{"job_id": job}, &detail)
	for _, p := range src.boards["exampleco"] {
		if "fk:exampleco:"+p.ExternalID != detail.Postings[0].ID {
			src.boards["exampleco"] = []sources.Posting{p}
		}
	}
	src.mu.Unlock()
	h.call("refresh_jobs", map[string]any{}, nil)
	var third news
	h.call("whats_new", map[string]any{}, &third)
	if len(third.Changed) != 1 || third.Changed[0].Job != job || third.Changed[0].Closed == "" {
		t.Errorf("closed application: %+v", third)
	}
}
