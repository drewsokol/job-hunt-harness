package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

func daysFromNow(n int) string {
	return time.Now().UTC().Add(time.Duration(n) * 24 * time.Hour).Truncate(time.Minute).Format(time.RFC3339)
}

// TestInterviews logs, reschedules and finishes made-up interviews, and
// checks what they do to their jobs, the tracker and the timeline.
func TestInterviews(t *testing.T) {
	src := &board{boards: map[string][]sources.Posting{"exampleco": {
		posting("1", "Senior Backend Engineer"), posting("2", "Data Engineer"), posting("3", "Platform Engineer")}}}
	var logs bytes.Buffer
	h := newHarness(t, Deps{Sources: map[string]sources.Source{"fake": src},
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
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
	if len(page.Jobs) != 3 {
		t.Fatalf("candidates %+v", page)
	}
	jobA, jobB, jobC := page.Jobs[0].Job, page.Jobs[1].Job, page.Jobs[2].Job

	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"job_id": jobA, "scheduled_at": daysFromNow(2)}, "stage: needed"},
		{map[string]any{"job_id": jobA, "stage": "technical"}, "scheduled_at: needed"},
		{map[string]any{"stage": "technical", "scheduled_at": daysFromNow(2)}, "job_id: an interview needs"},
		{map[string]any{"job_id": jobA, "stage": "lunch", "scheduled_at": daysFromNow(2)}, `stage: "lunch" must be one of`},
		{map[string]any{"job_id": jobA, "stage": "technical", "scheduled_at": "tomorrow"}, "scheduled_at:"},
		{map[string]any{"job_id": 999, "stage": "technical", "scheduled_at": daysFromNow(2)}, "no job 999"},
		{map[string]any{"outreach_id": 999, "stage": "technical", "scheduled_at": daysFromNow(2)}, "no outreach 999"},
		{map[string]any{"interview_id": 999, "status": "completed"}, "no interview 999"},
	} {
		if msg := h.fail("save_interview", c.args); !strings.Contains(msg, c.want) {
			t.Errorf("save_interview %v: %s; want %q", c.args, msg, c.want)
		}
	}

	type saved struct {
		ID        int64  `json:"interview_id"`
		New       bool   `json:"new"`
		Job       int64  `json:"job"`
		JobStatus string `json:"job_status"`
		Moved     string `json:"job_moved_from"`
		Note      string `json:"note"`
	}
	const passcode = "zzpasscode-4821"
	joining := "https://meet.example/made-up-room passcode " + passcode
	var first, second, rejected, phone, done saved
	h.call("save_interview", map[string]any{"job_id": jobA, "stage": "technical", "scheduled_at": daysFromNow(2), "duration_minutes": 60,
		"medium": "video", "joining": joining, "interviewers": []map[string]string{{"name": "Robin Example", "role": "Staff Engineer"}},
		"prep_notes": "Review the made-up system " + passcode}, &first)
	if !first.New || first.Job != jobA || first.JobStatus != "interviewing" || first.Moved != "unscreened" || first.Note != "" {
		t.Errorf("first interview: %+v", first)
	}
	h.call("save_interview", map[string]any{"job_id": jobA, "stage": "final", "scheduled_at": daysFromNow(5)}, &second)
	if second.JobStatus != "interviewing" || second.Moved != "" || second.Note != "" {
		t.Errorf("second interview on the same job: %+v", second)
	}

	h.call("update_application", map[string]any{"job_id": jobB, "status": "rejected"}, nil)
	h.call("save_interview", map[string]any{"job_id": jobB, "stage": "recruiter_screen", "scheduled_at": daysFromNow(1)}, &rejected)
	if rejected.JobStatus != "rejected" || rejected.Moved != "" || !strings.Contains(rejected.Note, "rejected") {
		t.Errorf("interview on a rejected job: %+v", rejected)
	}

	var thread struct {
		ID int64 `json:"outreach_id"`
	}
	h.call("save_outreach", map[string]any{"person": "Sam Sample", "channel": "email", "company": "Made-Up Labs"}, &thread)
	h.call("save_interview", map[string]any{"outreach_id": thread.ID, "stage": "recruiter_screen", "scheduled_at": daysFromNow(4), "medium": "phone"}, &phone)
	if phone.Job != 0 || phone.JobStatus != "" {
		t.Errorf("interview from a thread: %+v", phone)
	}

	h.call("save_interview", map[string]any{"job_id": jobC, "stage": "hiring_manager", "scheduled_at": daysAgo(3), "status": "completed",
		"next_step_at": daysAgo(1)}, &done)
	if done.Moved != "unscreened" {
		t.Errorf("a past interview logged: %+v", done)
	}

	// Reschedule, then finish one with an outcome, which leaves the job alone.
	var moved saved
	h.call("save_interview", map[string]any{"interview_id": first.ID, "scheduled_at": daysFromNow(3), "joining": joining + " (new link)"}, &moved)
	if moved.New || moved.ID != first.ID || moved.JobStatus != "interviewing" {
		t.Errorf("reschedule: %+v", moved)
	}
	h.call("save_interview", map[string]any{"interview_id": second.ID, "status": "completed", "outcome": "rejected"}, nil)
	if msg := h.fail("save_interview", map[string]any{"interview_id": second.ID}); !strings.Contains(msg, "nothing to change") {
		t.Errorf("empty change: %s", msg)
	}

	type interview struct {
		ID          int64  `json:"interview_id"`
		Job         int64  `json:"job"`
		Stage       string `json:"stage"`
		ScheduledAt string `json:"scheduled_at"`
		Joining     string `json:"joining"`
		Status      string `json:"status"`
		Outcome     string `json:"outcome"`
		Untrusted   *struct {
			Company string `json:"company"`
		} `json:"untrusted"`
	}
	var job struct {
		Status     string      `json:"status"`
		Interviews []interview `json:"interviews"`
		Timeline   []eventOut  `json:"timeline"`
	}
	h.call("get_job", map[string]any{"job_id": jobA}, &job)
	if job.Status != "interviewing" {
		t.Errorf("an outcome changed the job: %s", job.Status)
	}
	if len(job.Interviews) != 2 || job.Interviews[0].ID != second.ID || job.Interviews[1].Joining != joining+" (new link)" ||
		job.Interviews[0].Outcome != "rejected" || job.Interviews[0].Untrusted != nil {
		t.Errorf("get_job interviews: %+v", job.Interviews)
	}
	var reschedule struct {
		Changed     []string `json:"changed"`
		ScheduledAt struct {
			From, To time.Time
		} `json:"scheduled_at"`
	}
	kinds := map[string]int{}
	for _, e := range job.Timeline {
		kinds[e.Kind]++
		if e.Kind == "status_changed" && e.InterviewID != 0 {
			kinds["moved by interview"]++
		}
		if strings.Contains(string(e.Data), passcode) || strings.Contains(string(e.Data), "Review the made-up") {
			t.Errorf("event %s holds joining or notes: %s", e.Kind, e.Data)
		}
		if e.Kind == "interview_updated" && e.InterviewID == first.ID {
			if err := json.Unmarshal(e.Data, &reschedule); err != nil {
				t.Fatal(err)
			}
		}
	}
	if kinds["interview_logged"] != 2 || kinds["interview_updated"] != 2 || kinds["moved by interview"] != 1 {
		t.Errorf("timeline kinds %v", kinds)
	}
	if want, _ := time.Parse(time.RFC3339, daysFromNow(3)); strings.Join(reschedule.Changed, ",") != "scheduled_at,joining" ||
		!reschedule.ScheduledAt.To.Equal(want) || !reschedule.ScheduledAt.From.Equal(want.Add(-24*time.Hour)) {
		t.Errorf("reschedule event %+v", reschedule)
	}

	var upcoming struct {
		Interviews []interview `json:"interviews"`
	}
	text, _ := h.raw("get_tracker", map[string]any{"view": "upcoming_interviews"})
	if bad := listingTextOutsideEnvelope(t, text); len(bad) > 0 {
		t.Errorf("upcoming_interviews: listing text outside the envelope at %v", bad)
	}
	h.call("get_tracker", map[string]any{"view": "upcoming_interviews"}, &upcoming)
	var order []int64
	for _, iv := range upcoming.Interviews {
		order = append(order, iv.ID)
	}
	if want := []int64{rejected.ID, first.ID, phone.ID}; len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Errorf("upcoming order %v; want %v", order, want)
	}
	if up := upcoming.Interviews[1]; up.Joining == "" || up.Untrusted == nil || !strings.Contains(up.Untrusted.Company, "Exampleco") {
		t.Errorf("upcoming interview: %+v", up)
	}

	var due struct {
		Interviews []interview `json:"interviews"`
	}
	h.call("get_tracker", map[string]any{"view": "followups_due"}, &due)
	if len(due.Interviews) != 1 || due.Interviews[0].ID != done.ID {
		t.Errorf("followups_due interviews: %+v", due.Interviews)
	}

	var summary struct {
		Soon    int `json:"interviews_next_7_days"`
		Pending int `json:"interviews_awaiting_outcome"`
		Busy    int `json:"interviewing"`
	}
	h.call("get_tracker", map[string]any{}, &summary)
	if summary.Soon != 3 || summary.Pending != 1 || summary.Busy != 2 {
		t.Errorf("summary %+v", summary)
	}

	events, err := h.s.InterviewEvents(context.Background(), first.ID)
	if err != nil || len(events) != 3 { // logged, job moved, rescheduled
		t.Errorf("interview events %d, %v", len(events), err)
	}
	if strings.Contains(logs.String(), passcode) {
		t.Error("the joining text was logged")
	}
}
