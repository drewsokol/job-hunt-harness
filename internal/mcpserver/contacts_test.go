package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// TestContacts checks save_contact and list_contacts: a contact is reused
// by name and company, tags are replaced whole, the filters and paging
// work, one contact comes with its threads and jobs, and contacts due to
// get back in touch with show up in the check-ins.
func TestContacts(t *testing.T) {
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
	h.call("save_outreach", map[string]any{"direction": "outbound", "person": "Alex Example", "company": "Exampleco", "channel": "email", "job_id": job,
		"sent_at": daysAgo(3), "no_follow_up": true}, nil)

	type saved struct {
		ID  int64 `json:"contact_id"`
		New bool  `json:"new"`
	}
	// The thread made the contact; saving the same name and company finds it.
	var alex saved
	h.call("save_contact", map[string]any{"name": "Alex Example", "company": "exampleco", "warmth": "warm", "org_type": "employer",
		"relationship": "hiring_manager", "email": "alex@example.com", "tags": []string{"Platform", "platform", "sre"},
		"re_engage_after": daysAgo(1)}, &alex)
	if alex.ID == 0 || alex.New {
		t.Fatalf("the thread's contact wasn't reused: %+v", alex)
	}
	var sam saved
	h.call("save_contact", map[string]any{"name": "Sam Sample", "company": "Made-Up Agency", "org_type": "agency", "warmth": "cold",
		"relationship": "recruiter", "tags": []string{"recruiting"}, "re_engage_after": time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)}, &sam)
	if !sam.New || sam.ID == alex.ID {
		t.Fatalf("a new contact: %+v", sam)
	}

	type contactRow struct {
		ID           int64    `json:"contact_id"`
		Name         string   `json:"name"`
		Warmth       string   `json:"warmth"`
		Tags         []string `json:"tags"`
		Threads      int      `json:"threads"`
		LastThreadAt string   `json:"last_thread_at"`
		Untrusted    struct {
			Company string `json:"company"`
		} `json:"untrusted"`
	}
	type list struct {
		Total    int          `json:"total"`
		Contacts []contactRow `json:"contacts"`
		Next     string       `json:"next"`
	}
	find := func(args map[string]any) list {
		t.Helper()
		var l list
		h.call("list_contacts", args, &l)
		return l
	}

	all := find(map[string]any{})
	if all.Total != 2 || all.Contacts[0].ID != alex.ID || all.Contacts[0].Threads != 1 || all.Contacts[0].LastThreadAt == "" ||
		all.Contacts[0].Untrusted.Company != "Exampleco" || strings.Join(all.Contacts[0].Tags, ",") != "platform,sre" {
		t.Fatalf("every contact, in touch most recently first: %+v", all)
	}

	// Tags are replaced as a whole list.
	h.call("save_contact", map[string]any{"contact_id": alex.ID, "tags": []string{"referral"}}, nil)
	if l := find(map[string]any{"tag": "sre"}); l.Total != 0 {
		t.Errorf("an old tag still matches: %+v", l)
	}
	for _, c := range []struct {
		args map[string]any
		want int64
	}{
		{map[string]any{"tag": "Referral"}, alex.ID},
		{map[string]any{"name": "sam"}, sam.ID},
		{map[string]any{"company": "made-up"}, sam.ID},
		{map[string]any{"org_type": "agency"}, sam.ID},
		{map[string]any{"warmth": "warm"}, alex.ID},
		{map[string]any{"relationship": "hiring"}, alex.ID},
		{map[string]any{"re_engage": "due"}, alex.ID},
		{map[string]any{"since": daysAgo(10)}, alex.ID},
	} {
		if l := find(c.args); l.Total != 1 || l.Contacts[0].ID != c.want {
			t.Errorf("%v: %+v", c.args, l)
		}
	}
	if l := find(map[string]any{"re_engage": "set"}); l.Total != 2 {
		t.Errorf("re_engage set: %+v", l)
	}
	if l := find(map[string]any{"until": daysAgo(10)}); l.Total != 0 {
		t.Errorf("until: %+v", l)
	}
	if l := find(map[string]any{"limit": 1}); len(l.Contacts) != 1 || l.Next != "1" {
		t.Errorf("first page: %+v", l)
	} else if l2 := find(map[string]any{"limit": 1, "cursor": l.Next}); len(l2.Contacts) != 1 || l2.Contacts[0].ID != sam.ID || l2.Next != "" {
		t.Errorf("second page: %+v", l2)
	}

	// One contact: its threads and the jobs they're about, with listing
	// text inside the envelope.
	text, _ := h.raw("list_contacts", map[string]any{"contact_id": alex.ID})
	if bad := listingTextOutsideEnvelope(t, text); len(bad) > 0 {
		t.Errorf("list_contacts: listing text outside the envelope at %v", bad)
	}
	var one struct {
		contactRow
		Email    string `json:"email"`
		Outreach []struct {
			ID int64 `json:"outreach_id"`
		} `json:"outreach"`
		Jobs []struct {
			Job       int64 `json:"job"`
			Untrusted struct {
				Title string `json:"title"`
			} `json:"untrusted"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(text), &one); err != nil {
		t.Fatal(err)
	}
	if one.Name != "Alex Example" || one.Email != "alex@example.com" || len(one.Outreach) != 1 || len(one.Jobs) != 1 ||
		one.Jobs[0].Job != job || !strings.Contains(one.Jobs[0].Untrusted.Title, "Backend") {
		t.Errorf("one contact: %+v", one)
	}

	// Check-ins: a contact whose date has come is due, after the threads.
	var due struct {
		Contacts []contactRow `json:"contacts_to_re_engage"`
		Total    int          `json:"contacts_to_re_engage_total"`
	}
	h.call("get_tracker", map[string]any{"view": "followups_due"}, &due)
	if due.Total != 1 || len(due.Contacts) != 1 || due.Contacts[0].ID != alex.ID {
		t.Errorf("followups_due: %+v", due)
	}
	var news struct {
		ReEngage int `json:"contacts_to_re_engage"`
	}
	h.call("whats_new", map[string]any{}, &news)
	if news.ReEngage != 1 {
		t.Errorf("whats_new: %+v", news)
	}
	// Clearing the date takes them off the list.
	h.call("save_contact", map[string]any{"contact_id": alex.ID, "re_engage_after": ""}, nil)
	if l := find(map[string]any{"re_engage": "due"}); l.Total != 0 {
		t.Errorf("a cleared date is still due: %+v", l)
	}

	// Events name the fields changed, never their values.
	events, err := h.s.ContactEvents(context.Background(), alex.ID)
	if err != nil {
		t.Fatal(err)
	}
	var updates int
	for _, e := range events {
		if e.Kind != "contact_updated" {
			continue
		}
		updates++
		data := string(e.Data)
		for _, v := range []string{"alex@example.com", "warm", "referral", "hiring_manager"} {
			if strings.Contains(data, `"`+v+`"`) {
				t.Errorf("event %s carries the value %q: %s", e.Kind, v, data)
			}
		}
		if !strings.Contains(data, `"fields"`) {
			t.Errorf("event %s doesn't name the fields: %s", e.Kind, data)
		}
	}
	if updates != 3 {
		t.Errorf("%d contact_updated events, want 3", updates)
	}

	for _, bad := range []struct {
		tool string
		args map[string]any
	}{
		{"save_contact", map[string]any{"company": "Exampleco"}},
		{"save_contact", map[string]any{"contact_id": 999, "warmth": "warm"}},
		{"save_contact", map[string]any{"contact_id": alex.ID}},
		{"save_contact", map[string]any{"contact_id": alex.ID, "name": ""}},
		{"save_contact", map[string]any{"contact_id": alex.ID, "warmth": "hot"}},
		{"save_contact", map[string]any{"contact_id": alex.ID, "org_type": "startup"}},
		{"save_contact", map[string]any{"contact_id": alex.ID, "tags": []string{"two words"}}},
		{"save_contact", map[string]any{"contact_id": alex.ID, "profile_url": "javascript:alert(1)"}},
		{"save_contact", map[string]any{"contact_id": alex.ID, "re_engage_after": "next week"}},
		{"list_contacts", map[string]any{"contact_id": alex.ID, "name": "Alex"}},
		{"list_contacts", map[string]any{"contact_id": 999}},
		{"list_contacts", map[string]any{"re_engage": "soon"}},
		{"list_contacts", map[string]any{"warmth": "hot"}},
		{"list_contacts", map[string]any{"limit": 51}},
	} {
		if msg := h.fail(bad.tool, bad.args); msg == "" {
			t.Errorf("%s %v: no error", bad.tool, bad.args)
		}
	}
}
