package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestInterviewPrepPlaybook checks the prep playbook is a prompt and a
// get_playbook name, names the interview it's given, and asks otherwise.
func TestInterviewPrepPlaybook(t *testing.T) {
	h := newHarness(t, Deps{})
	ctx := context.Background()
	got, err := h.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "interview_prep", Arguments: map[string]string{"interview_id": "412"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := got.Messages[0].Content.(*mcp.TextContent).Text; !strings.Contains(text, "interview_id 412") || strings.Contains(text, "Ask the user which one") {
		t.Errorf("interview_prep with an id: %.300s", text)
	}
	if _, err := h.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "interview_prep", Arguments: map[string]string{"interview_id": "next week"}}); err == nil {
		t.Error("an interview_id that isn't a number was accepted")
	}
	var pb struct {
		Text string `json:"text"`
	}
	h.call("get_playbook", map[string]any{"name": "interview_prep"}, &pb)
	if !strings.HasPrefix(pb.Text, "# Prepare for an interview") || !strings.Contains(pb.Text, "Ask the user which one") {
		t.Errorf("get_playbook interview_prep without an id: %.300s", pb.Text)
	}
	h.call("get_playbook", map[string]any{"name": "interview_prep", "interview_id": 7}, &pb)
	if !strings.Contains(pb.Text, "interview_id 7.") {
		t.Errorf("get_playbook interview_prep with an id: %.300s", pb.Text)
	}
	at := func(s string) int {
		t.Helper()
		i := strings.Index(pb.Text, s)
		if i < 0 {
			t.Errorf("interview_prep doesn't say %q", s)
		}
		return i
	}
	// It reads the candidate and the claim limits before drafting anything.
	if find, read, pitch, write := at("`get_job`"), at("`get_context`"), at("The opening pitch"), at("**Write it back.**"); !(find < read && read < pitch && pitch < write) {
		t.Errorf("interview_prep's order: get_job %d, get_context %d, pitch %d, write back %d", find, read, pitch, write)
	}
	for _, s := range []string{
		"`list_outreach`", "`recall` with stories set", "kind practice", "`save_story`", "`save_interview`", "`remember`",
		"Never claim more than the user can stand behind", "claim_limit", "untrusted", "never invent one", "With no web tools",
		"| recruiter_screen |", "| hiring_manager |", "| technical |", "| system_design |", "| panel, product, final |", "| other |",
		"never giving a number the user hasn't agreed to",
		"**Pitch**", "**Stories**", "**Questions to ask**", "**Brush up**", "**Risks**", "**What we know**",
		"only after the user agrees", "status completed", "debrief_notes", "well, mixed, poorly or not_used", "Not feedback",
		"A real gap is a gap", "public role",
	} {
		at(s)
	}
}

// TestStoriesAndPrep goes through the spec's acceptance checks for the
// story bank, interviews' stories, practice memories and interviews to
// prep, with made-up data.
func TestStoriesAndPrep(t *testing.T) {
	h := newHarness(t, Deps{})

	var tools *mcp.ListToolsResult
	tools, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 34 {
		t.Errorf("%d tools; want 34", len(tools.Tools))
	}
	if !strings.Contains(descriptions["save_interview"], "stage included") {
		t.Error("save_interview's description doesn't say stage can be changed")
	}

	// Memories to check stories against.
	type remembered struct {
		ID int64 `json:"id"`
	}
	var limit, gap, archived, practice remembered
	h.call("remember", map[string]any{"content": "Led a team of four at most; never claim eight.", "kind": "claim_limit", "basis": "user_said"}, &limit)
	h.call("remember", map[string]any{"content": "No hands-on Kubernetes operations.", "kind": "gap", "basis": "user_said"}, &gap)
	h.call("remember", map[string]any{"content": "Can't name the payments vendor.", "kind": "claim_limit", "basis": "user_said"}, &archived)
	h.call("update_memory", map[string]any{"id": archived.ID, "archive": "No longer applies."}, nil)

	// Refusals name the field, or the limit.
	base := map[string]any{"title": "Billing queue migration", "summary": "Moved made-up billing jobs to a new queue.",
		"body": "The old queue dropped jobs. I designed the new one and led the move.", "claims": []string{"Led a team of four"}}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{with("title", nil), "title: needed"},
		{with("summary", "   "), "summary: 1 to 600"},
		{with("body", nil), "body: needed"},
		{with("claims", nil), "claims: at least one"},
		{with("claims", []string{}), "claims: at least one"},
		{with("title", strings.Repeat("x", 121)), "title: 1 to 120"},
		{with("body", strings.Repeat("x", 4_001)), "body: 1 to 4000"},
		{with("claims", []string{strings.Repeat("x", 201)}), "claims[0]: 1 to 200"},
		{with("tags", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}), "tags: at most 10"},
		{map[string]any{"story_id": 99, "title": "x"}, "no story 99"},
		{map[string]any{"archive": "wrong"}, "archive: give the story_id"},
	} {
		if msg := h.fail("save_story", c.args); !strings.Contains(msg, c.want) {
			t.Errorf("save_story %v: %s; want %q", c.args, msg, c.want)
		}
	}

	type savedStory struct {
		ID      int64 `json:"story_id"`
		New     bool  `json:"new"`
		Related []struct {
			ID   int64  `json:"id"`
			Kind string `json:"kind"`
		} `json:"related"`
	}
	var billing, incident savedStory
	h.call("save_story", with("tags", []string{"Leadership", "migration"}), &billing)
	if !billing.New || billing.ID == 0 || len(billing.Related) != 1 || billing.Related[0].ID != limit.ID || billing.Related[0].Kind != "claim_limit" {
		t.Errorf("save_story: %+v; want the claim limit %d related, and not the archived one", billing, limit.ID)
	}
	h.call("save_story", map[string]any{"title": "Made-up outage", "summary": "A made-up outage, handled.", "body": "The made-up cache failed; I ran the response.",
		"claims": []string{"Ran the incident response"}, "tags": []string{"incident"}}, &incident)
	if len(incident.Related) != 0 {
		t.Errorf("unrelated claims: %+v", incident.Related)
	}

	type storyList struct {
		Total   int `json:"total"`
		Stories []struct {
			ID     int64    `json:"story_id"`
			Title  string   `json:"title"`
			Claims []string `json:"claims"`
			Tags   []string `json:"tags"`
			Uses   []struct {
				InterviewID int64  `json:"interview_id"`
				Landed      string `json:"landed"`
			} `json:"uses"`
		} `json:"stories"`
	}
	var list storyList
	h.call("recall", map[string]any{"stories": true}, &list)
	if list.Total != 2 || list.Stories[0].ID != billing.ID || !slices.Equal(list.Stories[0].Tags, []string{"leadership", "migration"}) {
		t.Errorf("recall stories: %+v", list)
	}
	h.call("recall", map[string]any{"stories": true, "tag": "incident"}, &list)
	if list.Total != 1 || list.Stories[0].ID != incident.ID {
		t.Errorf("recall stories by tag: %+v", list)
	}
	h.call("recall", map[string]any{"stories": true, "query": "queue"}, &list)
	if list.Total != 1 || list.Stories[0].ID != billing.ID {
		t.Errorf("recall stories by query: %+v", list)
	}
	if msg := h.fail("recall", map[string]any{"stories": true, "kind": "gap"}); !strings.Contains(msg, "kind: memories only") {
		t.Errorf("recall stories with kind: %s", msg)
	}

	// Claims replace the list; the check runs against the new claims.
	var changed savedStory
	h.call("save_story", map[string]any{"story_id": billing.ID, "claims": []string{"Designed the new queue", "Ran it on Kubernetes operations"}}, &changed)
	if changed.New || len(changed.Related) != 1 || changed.Related[0].ID != gap.ID {
		t.Errorf("changing claims: %+v", changed)
	}
	h.call("recall", map[string]any{"stories": true, "query": "queue"}, &list)
	if len(list.Stories) != 1 || !slices.Equal(list.Stories[0].Claims, []string{"Designed the new queue", "Ran it on Kubernetes operations"}) {
		t.Errorf("claims after the change: %+v", list.Stories)
	}

	// An interview about a listing, and one about a thread with no job.
	var added struct {
		Jobs []struct {
			Job int64 `json:"job"`
		} `json:"jobs"`
	}
	h.call("add_jobs", map[string]any{"jobs": []any{map[string]any{"company": "Zzmarker Co", "title": "Backend Engineer zzmarker",
		"description": "Made-up backend work. " + strings.Repeat("Real work on real systems. ", 12)}}}, &added)
	job := added.Jobs[0].Job
	var thread struct {
		ID int64 `json:"outreach_id"`
	}
	h.call("save_outreach", map[string]any{"direction": "inbound", "person": "Riley Recruiter", "company": "Made-Up Talent", "channel": "email"}, &thread)

	type savedIV struct {
		ID int64 `json:"interview_id"`
	}
	var soon, later, prepped, cancelled, screen savedIV
	h.call("save_interview", map[string]any{"job_id": job, "stage": "recruiter_screen", "scheduled_at": daysFromNow(2),
		"stories": []map[string]any{{"story_id": billing.ID}}}, &soon)
	h.call("save_interview", map[string]any{"job_id": job, "stage": "technical", "scheduled_at": daysFromNow(5)}, &later)
	h.call("save_interview", map[string]any{"job_id": job, "stage": "final", "scheduled_at": daysFromNow(1), "prep_notes": "## Pitch\nMade-up pitch."}, &prepped)
	h.call("save_interview", map[string]any{"job_id": job, "stage": "panel", "scheduled_at": daysFromNow(1), "status": "cancelled"}, &cancelled)
	h.call("save_interview", map[string]any{"outreach_id": thread.ID, "stage": "recruiter_screen", "scheduled_at": daysFromNow(10)}, &screen)

	// The stage can be corrected.
	h.call("save_interview", map[string]any{"interview_id": soon.ID, "stage": "hiring_manager"}, nil)

	// Stories: unknown ids and landed values are refused; a new list replaces the old.
	for _, c := range []struct {
		stories []map[string]any
		want    string
	}{
		{[]map[string]any{{"story_id": 999}}, "no story 999"},
		{[]map[string]any{{"story_id": billing.ID, "landed": "great"}}, `landed: "great" must be one of`},
		{[]map[string]any{{"story_id": billing.ID}, {"story_id": billing.ID}}, "listed twice"},
		{[]map[string]any{{"story_id": billing.ID, "note": strings.Repeat("x", 301)}}, "note: up to 300"},
	} {
		if msg := h.fail("save_interview", map[string]any{"interview_id": soon.ID, "stories": c.stories}); !strings.Contains(msg, c.want) {
			t.Errorf("save_interview stories %v: %s; want %q", c.stories, msg, c.want)
		}
	}
	h.call("save_interview", map[string]any{"interview_id": soon.ID, "stories": []map[string]any{
		{"story_id": incident.ID, "landed": "well", "note": "Clear and short."}, {"story_id": billing.ID, "landed": "not_used"}}}, nil)

	type jobView struct {
		Interviews []struct {
			ID      int64  `json:"interview_id"`
			Stage   string `json:"stage"`
			Stories []struct {
				ID     int64  `json:"story_id"`
				Title  string `json:"title"`
				Landed string `json:"landed"`
				Note   string `json:"note"`
			} `json:"stories"`
		} `json:"interviews"`
		Timeline []struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		} `json:"timeline"`
	}
	var jv jobView
	h.call("get_job", map[string]any{"job_id": job}, &jv)
	found := false
	for _, iv := range jv.Interviews {
		if iv.ID != soon.ID {
			continue
		}
		found = true
		if iv.Stage != "hiring_manager" || len(iv.Stories) != 2 || iv.Stories[0].ID != incident.ID || iv.Stories[0].Landed != "well" ||
			iv.Stories[0].Title != "Made-up outage" || iv.Stories[0].Note != "Clear and short." || iv.Stories[1].Landed != "not_used" {
			t.Errorf("get_job's interview: %+v", iv)
		}
	}
	if !found {
		t.Errorf("get_job has no interview %d", soon.ID)
	}
	storiesEvent := false
	for _, e := range jv.Timeline {
		if strings.Contains(string(e.Data), `"stories"`) {
			storiesEvent = true
			if strings.Contains(string(e.Data), "Clear and short") {
				t.Errorf("an event holds a story note: %s", e.Data)
			}
		}
	}
	if !storiesEvent {
		t.Error("no timeline event names the stories")
	}

	// One story whole, with its uses; the listing's text stays in untrusted.
	text, _ := h.raw("recall", map[string]any{"story_id": incident.ID})
	var one struct {
		Body string `json:"body"`
		Uses []struct {
			InterviewID int64  `json:"interview_id"`
			Stage       string `json:"stage"`
			Landed      string `json:"landed"`
			Untrusted   struct {
				Company  string `json:"company"`
				JobTitle string `json:"job_title"`
			} `json:"untrusted"`
		} `json:"uses"`
	}
	if err := json.Unmarshal([]byte(text), &one); err != nil {
		t.Fatal(err)
	}
	if one.Body == "" || len(one.Uses) != 1 || one.Uses[0].InterviewID != soon.ID || one.Uses[0].Landed != "well" ||
		one.Uses[0].Untrusted.Company != "Zzmarker Co" {
		t.Errorf("recall story_id: %s", text)
	}
	if bad := listingTextOutsideEnvelope(t, text); len(bad) > 0 {
		t.Errorf("listing text outside untrusted in recall story_id: %v", bad)
	}
	if text, _ := h.raw("get_job", map[string]any{"job_id": job}); strings.Contains(text, `"stories":[{"story_id":`) {
		if bad := listingTextOutsideEnvelope(t, text); len(bad) > 0 {
			t.Errorf("listing text outside untrusted in get_job: %v", bad)
		}
	}

	// An interview linked only to a thread: prep notes and stories come
	// back from list_outreach, and the use shows the thread's company.
	h.call("save_interview", map[string]any{"interview_id": screen.ID, "prep_notes": "## Pitch\nA made-up pitch.",
		"stories": []map[string]any{{"story_id": billing.ID}}}, nil)
	var th struct {
		Interviews []struct {
			ID        int64  `json:"interview_id"`
			PrepNotes string `json:"prep_notes"`
			Stories   []struct {
				ID     int64  `json:"story_id"`
				Landed string `json:"landed"`
			} `json:"stories"`
		} `json:"interviews"`
	}
	h.call("list_outreach", map[string]any{"outreach_id": thread.ID}, &th)
	if len(th.Interviews) != 1 || th.Interviews[0].ID != screen.ID || !strings.Contains(th.Interviews[0].PrepNotes, "made-up pitch") ||
		len(th.Interviews[0].Stories) != 1 || th.Interviews[0].Stories[0].ID != billing.ID || th.Interviews[0].Stories[0].Landed != "planned" {
		t.Errorf("list_outreach's interviews: %+v", th)
	}
	h.call("recall", map[string]any{"story_id": billing.ID}, &one)
	thread0 := false
	for _, u := range one.Uses {
		if u.InterviewID == screen.ID {
			thread0 = u.Untrusted.Company == "Made-Up Talent"
		}
	}
	if !thread0 {
		t.Errorf("the thread's use: %+v", one.Uses)
	}

	// whats_new lists the interview two days away with no prep notes, and
	// none of the others.
	var news struct {
		ToPrep []struct {
			ID          int64  `json:"interview_id"`
			Stage       string `json:"stage"`
			ScheduledAt string `json:"scheduled_at"`
			Untrusted   struct {
				Company string `json:"company"`
			} `json:"untrusted"`
		} `json:"interviews_to_prep"`
	}
	text, _ = h.raw("whats_new", map[string]any{})
	if err := json.Unmarshal([]byte(text), &news); err != nil {
		t.Fatal(err)
	}
	if len(news.ToPrep) != 1 || news.ToPrep[0].ID != soon.ID || news.ToPrep[0].Stage != "hiring_manager" || news.ToPrep[0].ScheduledAt == "" ||
		news.ToPrep[0].Untrusted.Company != "Zzmarker Co" {
		t.Errorf("interviews_to_prep: %+v (later %d, prepped %d, cancelled %d)", news.ToPrep, later.ID, prepped.ID, cancelled.ID)
	}
	if bad := listingTextOutsideEnvelope(t, text); len(bad) > 0 {
		t.Errorf("listing text outside untrusted in whats_new: %v", bad)
	}

	// Archiving takes a story out of the list, unless asked for.
	h.call("save_story", map[string]any{"story_id": incident.ID, "archive": "Told too often."}, nil)
	h.call("recall", map[string]any{"stories": true}, &list)
	if list.Total != 1 || list.Stories[0].ID != billing.ID {
		t.Errorf("after archiving: %+v", list)
	}
	h.call("recall", map[string]any{"stories": true, "include_archived": true}, &list)
	if list.Total != 2 {
		t.Errorf("with include_archived: %+v", list)
	}
	h.call("get_job", map[string]any{"job_id": job}, &jv)
	for _, iv := range jv.Interviews {
		if iv.ID == soon.ID && len(iv.Stories) != 2 {
			t.Errorf("an archived story's use was unlinked: %+v", iv)
		}
	}

	// Practice memories: saved and found by kind, left out of get_context.
	h.call("remember", map[string]any{"content": "Answers on design trade-offs run long.", "kind": "practice", "basis": "inferred",
		"evidence": "A made-up drill"}, &practice)
	var sc struct {
		Memories map[string][]struct {
			ID int64 `json:"id"`
		} `json:"memories"`
		Practice int    `json:"practice_memories"`
		Note     string `json:"practice_note"`
	}
	h.call("get_context", nil, &sc)
	if _, ok := sc.Memories["practice"]; ok || sc.Practice != 1 || !strings.Contains(sc.Note, "kind practice") || len(sc.Memories["claim_limit"]) != 1 {
		t.Errorf("get_context: %+v", sc)
	}
	var rec struct {
		Memories []struct {
			ID   int64  `json:"id"`
			Kind string `json:"kind"`
		} `json:"memories"`
	}
	h.call("recall", map[string]any{"kind": "practice"}, &rec)
	if len(rec.Memories) != 1 || rec.Memories[0].ID != practice.ID {
		t.Errorf("recall practice: %+v", rec)
	}

	// The story events name who changed what, never the text.
	var kinds []string
	for _, id := range []int64{billing.ID, incident.ID} {
		evs, err := h.s.StoryEvents(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			kinds = append(kinds, e.Kind)
			if e.Actor != "agent:test-agent" || strings.Contains(string(e.Data), "queue") || strings.Contains(string(e.Data), "Told too often") {
				t.Errorf("event %s by %s: %s", e.Kind, e.Actor, e.Data)
			}
		}
	}
	if want := []string{"story_added", "story_changed", "story_added", "story_archived"}; !slices.Equal(kinds, want) {
		t.Errorf("story events %v; want %v", kinds, want)
	}
}
