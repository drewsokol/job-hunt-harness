package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Interview rules.
const (
	UpcomingInterviews   = 14 * 24 * time.Hour // get_tracker's upcoming_interviews view
	InterviewsSoon       = 7 * 24 * time.Hour  // the summary's count
	maxInterviewers      = 20
	maxInterviewerText   = 200
	maxJoining           = 1_000
	maxInterviewNotes    = 10_000
	maxRecordingTool     = 100
	maxInterviewMinutes  = 24 * 60
	maxInterviewDistance = 2 * 366 * 24 * time.Hour // how far from now a time may be
)

// InterviewInput is an interview, or a change to one, as an agent
// reports it. Empty fields are left alone on a change.
type InterviewInput struct {
	JobID, OutreachID int64
	Stage             string
	ScheduledAt       string // UTC, RFC 3339
	DurationMinutes   int
	Interviewers      []store.Interviewer // nil leaves them alone; empty clears them
	Medium            string
	Joining           string
	Status, Outcome   string
	NextStepAt        string // UTC, RFC 3339
	ClearNextStep     bool
	PrepNotes         string
	DebriefNotes      string
	Recorded          *bool
	RecordingTool     string
}

// SaveInterview logs an interview (id 0) or changes one.
func SaveInterview(ctx context.Context, s *store.Store, by store.Actor, id int64, in InterviewInput) (store.InterviewSaved, error) {
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	in.Stage, in.Medium = strings.ToLower(strings.TrimSpace(in.Stage)), strings.ToLower(strings.TrimSpace(in.Medium))
	in.Status, in.Outcome = strings.ToLower(strings.TrimSpace(in.Status)), strings.ToLower(strings.TrimSpace(in.Outcome))
	in.Joining, in.RecordingTool = strings.TrimSpace(in.Joining), strings.TrimSpace(in.RecordingTool)
	in.PrepNotes, in.DebriefNotes = strings.TrimSpace(in.PrepNotes), strings.TrimSpace(in.DebriefNotes)

	for _, f := range []struct {
		name, v string
		of      []string
	}{{"stage", in.Stage, store.InterviewStages}, {"medium", in.Medium, store.InterviewMedia},
		{"status", in.Status, store.InterviewStatuses}, {"outcome", in.Outcome, store.InterviewOutcomes}} {
		if f.v != "" && !slices.Contains(f.of, f.v) {
			bad("%s: %q must be one of %s", f.name, f.v, strings.Join(f.of, ", "))
		}
	}
	for _, f := range []struct {
		name, v string
		max     int
	}{{"joining", in.Joining, maxJoining}, {"prep_notes", in.PrepNotes, maxInterviewNotes},
		{"debrief_notes", in.DebriefNotes, maxInterviewNotes}, {"recording_tool", in.RecordingTool, maxRecordingTool}} {
		if utf8.RuneCountInString(f.v) > f.max {
			bad("%s: up to %d characters", f.name, f.max)
		}
	}
	if in.DurationMinutes < 0 || in.DurationMinutes > maxInterviewMinutes {
		bad("duration_minutes: 1 to %d", maxInterviewMinutes)
	}
	var people []store.Interviewer
	if in.Interviewers != nil {
		people = []store.Interviewer{}
		if len(in.Interviewers) > maxInterviewers {
			bad("interviewers: at most %d", maxInterviewers)
		}
		for i, p := range in.Interviewers {
			p.Name, p.Role = strings.TrimSpace(p.Name), strings.TrimSpace(p.Role)
			if n := utf8.RuneCountInString(p.Name); n == 0 || n > maxInterviewerText {
				bad("interviewers[%d].name: 1 to %d characters", i, maxInterviewerText)
			}
			if utf8.RuneCountInString(p.Role) > maxInterviewerText {
				bad("interviewers[%d].role: up to %d characters", i, maxInterviewerText)
			}
			people = append(people, p)
		}
	}
	now := utc.Now()
	when := func(name, text string) time.Time {
		if text == "" {
			return time.Time{}
		}
		t, err := utc.Parse(text)
		switch {
		case err != nil:
			bad("%s: %s", name, err)
		case t.Before(now.Add(-maxInterviewDistance)) || t.After(now.Add(maxInterviewDistance)):
			bad("%s: more than two years from now", name)
		}
		return t
	}
	at, next := when("scheduled_at", in.ScheduledAt), when("next_step_at", in.NextStepAt)
	if in.ClearNextStep && in.NextStepAt != "" {
		bad("next_step_at: give a date or clear_next_step, not both")
	}

	if id == 0 {
		if in.Stage == "" {
			bad("stage: needed to log an interview; one of %s", strings.Join(store.InterviewStages, ", "))
		}
		if in.ScheduledAt == "" {
			bad("scheduled_at: needed to log an interview (UTC, RFC 3339)")
		}
		if in.JobID == 0 && in.OutreachID == 0 {
			bad("job_id: an interview needs a job_id, an outreach_id or both")
		}
		if in.ClearNextStep {
			bad("clear_next_step: only for a change, with interview_id")
		}
		if len(problems) > 0 {
			return store.InterviewSaved{}, &InputError{Problems: problems}
		}
		iv := store.Interview{JobID: in.JobID, OutreachID: in.OutreachID, Stage: in.Stage, ScheduledAt: at, DurationMinutes: in.DurationMinutes,
			Interviewers: people, Medium: cmpOrString(in.Medium, "other"), Joining: in.Joining, Status: cmpOrString(in.Status, "scheduled"),
			Outcome: cmpOrString(in.Outcome, "pending"), NextStepAt: next, PrepNotes: in.PrepNotes, DebriefNotes: in.DebriefNotes,
			RecordingTool: in.RecordingTool}
		if in.Recorded != nil {
			iv.Recorded = *in.Recorded
		}
		saved, err := s.AddInterview(ctx, by, iv)
		return saved, refNotFound(err, in.JobID, in.OutreachID)
	}

	c := store.InterviewChange{Recorded: in.Recorded}
	if in.Interviewers != nil {
		c.Interviewers = &people
	}
	str := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	c.Stage, c.Medium, c.Joining, c.Status, c.Outcome = str(in.Stage), str(in.Medium), str(in.Joining), str(in.Status), str(in.Outcome)
	c.PrepNotes, c.DebriefNotes, c.RecordingTool = str(in.PrepNotes), str(in.DebriefNotes), str(in.RecordingTool)
	if in.JobID != 0 {
		c.JobID = &in.JobID
	}
	if in.OutreachID != 0 {
		c.OutreachID = &in.OutreachID
	}
	if in.ScheduledAt != "" {
		c.ScheduledAt = &at
	}
	if in.NextStepAt != "" || in.ClearNextStep {
		c.NextStepAt = &next
	}
	if in.DurationMinutes != 0 {
		c.DurationMinutes = &in.DurationMinutes
	}
	if c == (store.InterviewChange{}) {
		bad("nothing to change: give the fields to change with interview_id")
	}
	if len(problems) > 0 {
		return store.InterviewSaved{}, &InputError{Problems: problems}
	}
	saved, err := s.UpdateInterview(ctx, by, id, c)
	if err != nil && strings.HasPrefix(err.Error(), fmt.Sprintf("interview %d:", id)) {
		return saved, inputIfNotFound(err, fmt.Sprintf("interview_id: no interview %d", id))
	}
	return saved, refNotFound(err, in.JobID, in.OutreachID)
}

// refNotFound turns a missing job or thread into an input error.
func refNotFound(err error, jobID, outreachID int64) error {
	if err == nil {
		return nil
	}
	if strings.HasPrefix(err.Error(), "outreach ") {
		return inputIfNotFound(err, fmt.Sprintf("outreach_id: no outreach %d", outreachID))
	}
	return inputIfNotFound(err, fmt.Sprintf("job_id: no job %d", jobID))
}

// JobInterviews returns a job's interviews, newest first.
func JobInterviews(ctx context.Context, s *store.Store, jobID int64) ([]store.Interview, error) {
	return s.ListInterviews(ctx, store.InterviewQuery{JobID: jobID})
}
