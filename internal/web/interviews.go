package web

import (
	"context"

	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// interviewJSON is an interview as the page shows it. Everything in it is
// the agent's record; the dashboard only shows interviews.
type interviewJSON struct {
	ID              int64               `json:"id"`
	Stage           string              `json:"stage"`
	ScheduledAt     string              `json:"scheduled_at"`
	DurationMinutes int                 `json:"duration_minutes,omitempty"`
	Interviewers    []store.Interviewer `json:"interviewers,omitempty"`
	Medium          string              `json:"medium"`
	Joining         string              `json:"joining,omitempty"`
	Status          string              `json:"status"`
	Outcome         string              `json:"outcome"`
	NextStepAt      string              `json:"next_step_at,omitempty"`
	PrepNotes       string              `json:"prep_notes,omitempty"`
	DebriefNotes    string              `json:"debrief_notes,omitempty"`
}

func interviewOf(iv store.Interview) interviewJSON {
	return interviewJSON{ID: iv.ID, Stage: iv.Stage, ScheduledAt: timeText(iv.ScheduledAt), DurationMinutes: iv.DurationMinutes,
		Interviewers: iv.Interviewers, Medium: iv.Medium, Joining: iv.Joining, Status: iv.Status, Outcome: iv.Outcome,
		NextStepAt: timeText(iv.NextStepAt), PrepNotes: iv.PrepNotes, DebriefNotes: iv.DebriefNotes}
}

// withNextInterviews sets each job's soonest scheduled interview.
func (s *server) withNextInterviews(ctx context.Context, jobs []jobJSON) error {
	ids := make([]int64, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	next, err := s.Store.NextInterviews(ctx, ids, utc.Now())
	if err != nil {
		return err
	}
	for i := range jobs {
		if iv, ok := next[jobs[i].ID]; ok {
			v := interviewOf(iv)
			jobs[i].NextInterview = &v
		}
	}
	return nil
}

// jobInterviews returns a job's interviews, newest first.
func (s *server) jobInterviews(ctx context.Context, jobID int64) ([]interviewJSON, error) {
	ivs, err := s.Store.ListInterviews(ctx, store.InterviewQuery{JobID: jobID})
	out := make([]interviewJSON, 0, len(ivs))
	for _, iv := range ivs {
		out = append(out, interviewOf(iv))
	}
	return out, err
}
