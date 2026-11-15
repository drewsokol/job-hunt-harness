package web

import (
	"context"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// interviewJSON is an interview as the page shows it. Everything in it is
// the agent's record; the dashboard only shows interviews. NotPrepped
// marks a scheduled interview in the next few days with no prep notes.
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
	NotPrepped      bool                `json:"not_prepped,omitempty"`
	Stories         []interviewStory    `json:"stories,omitempty"`
}

// interviewStory is a story linked to an interview, and how it landed.
type interviewStory struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Landed string `json:"landed"`
	Note   string `json:"note,omitempty"`
}

func interviewOf(iv store.Interview, now time.Time) interviewJSON {
	return interviewJSON{ID: iv.ID, Stage: iv.Stage, ScheduledAt: timeText(iv.ScheduledAt), DurationMinutes: iv.DurationMinutes,
		Interviewers: iv.Interviewers, Medium: iv.Medium, Joining: iv.Joining, Status: iv.Status, Outcome: iv.Outcome,
		NextStepAt: timeText(iv.NextStepAt), PrepNotes: iv.PrepNotes, DebriefNotes: iv.DebriefNotes, NotPrepped: core.NeedsPrep(iv, now)}
}

// withNextInterviews sets each job's soonest scheduled interview.
func (s *server) withNextInterviews(ctx context.Context, jobs []jobJSON) error {
	ids := make([]int64, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	now := utc.Now()
	next, err := s.Store.NextInterviews(ctx, ids, now)
	if err != nil {
		return err
	}
	for i := range jobs {
		if iv, ok := next[jobs[i].ID]; ok {
			v := interviewOf(iv, now)
			jobs[i].NextInterview = &v
		}
	}
	return nil
}

// jobInterviews returns a job's interviews, newest first, with their
// stories.
func (s *server) jobInterviews(ctx context.Context, jobID int64) ([]interviewJSON, error) {
	ivs, err := s.Store.ListInterviews(ctx, store.InterviewQuery{JobID: jobID})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(ivs))
	for _, iv := range ivs {
		ids = append(ids, iv.ID)
	}
	stories, err := s.Store.InterviewStories(ctx, ids)
	now := utc.Now()
	out := make([]interviewJSON, 0, len(ivs))
	for _, iv := range ivs {
		v := interviewOf(iv, now)
		for _, x := range stories[iv.ID] {
			v.Stories = append(v.Stories, interviewStory{x.StoryID, x.Title, x.Landed, x.Note})
		}
		out = append(out, v)
	}
	return out, err
}

// storyJSON is a story on the Profile tab. Its text is the agent's; the
// company and job title of each use can come from a listing, and the page
// shows them as text.
type storyJSON struct {
	ID            int64          `json:"id"`
	Title         string         `json:"title"`
	Summary       string         `json:"summary"`
	Body          string         `json:"body"`
	Claims        []string       `json:"claims"`
	Tags          []string       `json:"tags"`
	Archived      string         `json:"archived,omitempty"`
	ArchiveReason string         `json:"archive_reason,omitempty"`
	By            string         `json:"by"`
	At            string         `json:"at"`
	Uses          []storyUseJSON `json:"uses"`
}

type storyUseJSON struct {
	InterviewID int64  `json:"interview_id"`
	Stage       string `json:"stage"`
	At          string `json:"at"`
	Company     string `json:"company,omitempty"`
	JobTitle    string `json:"job_title,omitempty"`
	Landed      string `json:"landed"`
	Note        string `json:"note,omitempty"`
}

// stories returns every story, archived ones included, with its uses.
func (s *server) stories(ctx context.Context) ([]storyJSON, error) {
	sts, _, err := s.Store.Stories(ctx, store.StoryQuery{IncludeArchived: true})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(sts))
	for _, st := range sts {
		ids = append(ids, st.ID)
	}
	uses, err := s.Store.StoryUses(ctx, ids)
	out := make([]storyJSON, 0, len(sts))
	for _, st := range sts {
		v := storyJSON{ID: st.ID, Title: st.Title, Summary: st.Summary, Body: st.Body, Claims: st.Claims, Tags: st.Tags,
			Archived: timeText(st.ArchivedAt), ArchiveReason: st.ArchiveReason, By: string(st.UpdatedBy), At: timeText(st.UpdatedAt),
			Uses: []storyUseJSON{}}
		for _, u := range uses[st.ID] {
			v.Uses = append(v.Uses, storyUseJSON{InterviewID: u.InterviewID, Stage: u.Stage, At: timeText(u.ScheduledAt),
				Company: u.Company, JobTitle: u.JobTitle, Landed: u.Landed, Note: u.Note})
		}
		out = append(out, v)
	}
	return out, err
}
