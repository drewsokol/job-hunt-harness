package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// TestInboundOutreach follows a made-up recruiter's message from arrival,
// through learning the role and pay, to the user declining it.
func TestInboundOutreach(t *testing.T) {
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
		ID         int64  `json:"outreach_id"`
		FollowUpAt string `json:"follow_up_at"`
	}
	var in, out logged
	h.call("save_outreach", map[string]any{"direction": "inbound", "person": "Riley Recruiter", "company": "Made-Up Talent",
		"channel": "linkedin_inmail", "sent_at": daysAgo(3), "client_company": "undisclosed"}, &in)
	if in.FollowUpAt != "" {
		t.Errorf("an inbound thread got a follow-up date: %+v", in)
	}
	h.call("save_outreach", map[string]any{"direction": "outbound", "person": "Pat Placeholder", "channel": "email", "company": "Exampleco",
		"sent_at": daysAgo(40)}, &out)
	if out.FollowUpAt == "" {
		t.Errorf("an outbound thread has no follow-up date: %+v", out)
	}

	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"person": "Nobody Made-Up", "channel": "email"}, "direction"},
		{map[string]any{"direction": "sideways", "person": "Nobody Made-Up", "channel": "email"}, "direction"},
		{map[string]any{"direction": "inbound", "person": "Nobody Made-Up", "channel": "email", "decline_reason": "contract"}, "only with status declined"},
		{map[string]any{"direction": "inbound", "person": "Nobody Made-Up", "channel": "email", "status": "declined", "decline_reason": "bored"}, "decline_reason"},
		{map[string]any{"direction": "inbound", "person": "Nobody Made-Up", "channel": "email", "pay_min": 100000}, "pay_currency"},
		{map[string]any{"direction": "inbound", "person": "Nobody Made-Up", "channel": "email", "pay_min": 200, "pay_max": 100, "pay_currency": "USD"}, "more than pay_max"},
		{map[string]any{"direction": "inbound", "person": "Nobody Made-Up", "channel": "email", "pay_min": 100, "pay_currency": "dollars"}, "three-letter"},
		{map[string]any{"direction": "inbound", "person": "Nobody Made-Up", "channel": "email", "pay_min": 100, "pay_currency": "USD", "pay_period": "week"}, "year or hour"},
		{map[string]any{"direction": "inbound", "person": "Nobody Made-Up", "channel": "email", "pay_currency": "USD"}, "with pay_min or pay_max"},
		{map[string]any{"outreach_id": in.ID, "person": "Someone Else"}, "set when it's logged"},
		{map[string]any{"outreach_id": in.ID, "job_id": 999999}, "no job 999999"},
		{map[string]any{"outreach_id": in.ID, "decline_reason": "contract"}, "only with status declined"},
	} {
		if msg := h.fail("save_outreach", c.args); !strings.Contains(msg, c.want) {
			t.Errorf("save_outreach %v: %s; want %q", c.args, msg, c.want)
		}
	}

	// The recruiter names the client and the role, and quotes pay.
	h.call("save_outreach", map[string]any{"find": map[string]any{"person": "riley recruiter"}, "job_id": job, "client_company": "Exampleco",
		"pay_min": 150000, "pay_max": 180000, "pay_currency": "usd", "pay_note": "plus made-up equity"}, nil)

	type payJSON struct {
		Min      float64 `json:"min"`
		Max      float64 `json:"max"`
		Currency string  `json:"currency"`
		Period   string  `json:"period"`
		Note     string  `json:"note"`
	}
	type thread struct {
		ID            int64    `json:"outreach_id"`
		Job           int64    `json:"job"`
		Direction     string   `json:"direction"`
		Channel       string   `json:"channel"`
		Status        string   `json:"status"`
		FollowUpAt    string   `json:"follow_up_at"`
		DeclineReason string   `json:"decline_reason"`
		DeclineNote   string   `json:"decline_note"`
		ClientCompany string   `json:"client_company"`
		Pay           *payJSON `json:"pay"`
		History       []struct {
			Kind string         `json:"kind"`
			Data map[string]any `json:"data"`
		} `json:"history"`
	}
	var got thread
	text, _ := h.raw("list_outreach", map[string]any{"outreach_id": in.ID})
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Direction != "inbound" || got.Status != "received" || got.FollowUpAt != "" || got.Job != job || got.ClientCompany != "Exampleco" ||
		got.Pay == nil || *got.Pay != (payJSON{150000, 180000, "USD", "year", "plus made-up equity"}) {
		t.Errorf("the thread: %s", text)
	}
	if bad := listingTextOutsideEnvelope(t, text); len(bad) > 0 {
		t.Errorf("listing text outside the envelope at %v", bad)
	}
	if n := len(got.History); n != 2 || got.History[1].Kind != "outreach_updated" || got.History[1].Data["pay"] != true || got.History[1].Data["client_company"] != true {
		t.Errorf("history: %+v", got.History)
	}

	var detail struct {
		Outreach []struct {
			ID        int64    `json:"outreach_id"`
			Direction string   `json:"direction"`
			Pay       *payJSON `json:"pay"`
		} `json:"outreach"`
	}
	h.call("get_job", map[string]any{"job_id": job}, &detail)
	if len(detail.Outreach) != 1 || detail.Outreach[0].Direction != "inbound" || detail.Outreach[0].Pay == nil || detail.Outreach[0].Pay.Max != 180000 {
		t.Errorf("get_job threads: %+v", detail.Outreach)
	}

	type summary struct {
		Outreach   map[string]map[string]int `json:"outreach"`
		Last7      int                       `json:"inbound_last_7_days"`
		Last30     int                       `json:"inbound_last_30_days"`
		Unanswered int                       `json:"unanswered_inbound"`
		Declined   map[string]int            `json:"declined_by_reason"`
	}
	var sm summary
	h.call("get_tracker", nil, &sm)
	if sm.Outreach["inbound"]["received"] != 1 || sm.Outreach["outbound"]["sent"] != 1 || sm.Last7 != 1 || sm.Last30 != 1 || sm.Unanswered != 1 || len(sm.Declined) != 0 {
		t.Errorf("summary: %+v", sm)
	}
	var news struct {
		Unanswered int `json:"unanswered_inbound"`
	}
	h.call("whats_new", nil, &news)
	if news.Unanswered != 1 {
		t.Errorf("whats_new: %+v", news)
	}

	// The user passes; a declined thread is no longer open.
	h.call("save_outreach", map[string]any{"find": map[string]any{"person": "Riley Recruiter"}, "status": "declined",
		"decline_reason": "pay_below_floor", "decline_note": "Below a made-up floor."}, nil)
	if msg := h.fail("save_outreach", map[string]any{"find": map[string]any{"person": "Riley Recruiter"}, "status": "replied"}); !strings.Contains(msg, "no open outreach thread") {
		t.Errorf("a declined thread found by person: %s", msg)
	}
	sm = summary{}
	h.call("get_tracker", nil, &sm)
	if sm.Unanswered != 0 || sm.Declined["pay_below_floor"] != 1 || sm.Outreach["inbound"]["declined"] != 1 {
		t.Errorf("summary after declining: %+v", sm)
	}

	type list struct {
		Total    int      `json:"total"`
		Outreach []thread `json:"outreach"`
	}
	for _, c := range []struct {
		args map[string]any
		want int64
	}{
		{map[string]any{"status": []string{"open"}}, out.ID},
		{map[string]any{"direction": "inbound", "since": daysAgo(7)}, in.ID},
		{map[string]any{"decline_reason": "pay_below_floor"}, in.ID},
		{map[string]any{"direction": "outbound", "until": daysAgo(30)}, out.ID},
		{map[string]any{"channel": "LinkedIn_InMail"}, in.ID},
	} {
		var l list
		h.call("list_outreach", c.args, &l)
		if l.Total != 1 || l.Outreach[0].ID != c.want {
			t.Errorf("list_outreach %v: %+v", c.args, l)
		}
	}
	var none list
	h.call("list_outreach", map[string]any{"direction": "inbound", "since": daysAgo(2)}, &none)
	if none.Total != 0 {
		t.Errorf("since: %+v", none)
	}
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"direction": "both"}, "direction"},
		{map[string]any{"decline_reason": "bored"}, "decline_reason"},
		{map[string]any{"since": "yesterday"}, "since"},
		{map[string]any{"since": daysAgo(1), "until": daysAgo(2)}, "after since"},
		{map[string]any{"outreach_id": in.ID, "direction": "inbound"}, "alone"},
	} {
		if msg := h.fail("list_outreach", c.args); !strings.Contains(msg, c.want) {
			t.Errorf("list_outreach %v: %s; want %q", c.args, msg, c.want)
		}
	}

	// Reopened by id, the thread drops its decline reason.
	h.call("save_outreach", map[string]any{"outreach_id": in.ID, "status": "replied"}, nil)
	got = thread{}
	h.call("list_outreach", map[string]any{"outreach_id": in.ID}, &got)
	if got.Status != "replied" || got.DeclineReason != "" || got.DeclineNote != "Below a made-up floor." {
		t.Errorf("reopened: %+v", got)
	}

	// An older thread learns its direction, and a new one can start declined.
	h.call("save_outreach", map[string]any{"outreach_id": out.ID, "direction": "inbound"}, nil)
	h.call("save_outreach", map[string]any{"direction": "inbound", "person": "Jo Placeholder", "channel": "phone", "status": "declined",
		"decline_reason": "contract"}, nil)
	sm = summary{}
	h.call("get_tracker", nil, &sm)
	if sm.Outreach["outbound"]["sent"] != 0 || sm.Outreach["inbound"]["sent"] != 1 || sm.Declined["contract"] != 1 {
		t.Errorf("summary at the end: %+v", sm)
	}
}
