package mcpserver

import (
	"context"
	"fmt"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// interviewOut is one interview. The interviewers, joining details and
// notes are the agent's record and come back as they were saved; the
// company and job title can come from a listing, so they are inside
// untrusted, and left out where the job is already shown.
type interviewOut struct {
	ID              int64               `json:"interview_id"`
	Job             int64               `json:"job,omitempty"`
	OutreachID      int64               `json:"outreach_id,omitempty"`
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
	Recorded        bool                `json:"recorded,omitempty"`
	RecordingTool   string              `json:"recording_tool,omitempty"`
	Refs            []refOut            `json:"refs,omitempty"`
	Updated         string              `json:"updated_at"`
	Untrusted       *struct {
		Company  string `json:"company,omitempty"`
		JobTitle string `json:"job_title,omitempty"`
	} `json:"untrusted,omitempty"`
}

func interviews(ivs []store.Interview, withJob bool) []interviewOut {
	out := make([]interviewOut, 0, len(ivs))
	for _, iv := range ivs {
		v := interviewOut{ID: iv.ID, Job: iv.JobID, OutreachID: iv.OutreachID, Stage: iv.Stage, ScheduledAt: timeOrEmpty(iv.ScheduledAt),
			DurationMinutes: iv.DurationMinutes, Interviewers: iv.Interviewers, Medium: iv.Medium, Joining: iv.Joining, Status: iv.Status,
			Outcome: iv.Outcome, NextStepAt: timeOrEmpty(iv.NextStepAt), PrepNotes: iv.PrepNotes, DebriefNotes: iv.DebriefNotes,
			Recorded: iv.Recorded, RecordingTool: iv.RecordingTool, Updated: timeOrEmpty(iv.UpdatedAt)}
		if withJob {
			v.Untrusted = &struct {
				Company  string `json:"company,omitempty"`
				JobTitle string `json:"job_title,omitempty"`
			}{iv.Company, iv.JobTitle}
		}
		out = append(out, v)
	}
	return out
}

// withRefs adds each interview's links to email threads.
func (t *tools) withRefs(ctx context.Context, out []interviewOut) ([]interviewOut, error) {
	ids := make([]int64, 0, len(out))
	for _, v := range out {
		ids = append(ids, v.ID)
	}
	refs, err := t.Store.InterviewRefs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if rs := refs[out[i].ID]; len(rs) > 0 {
			out[i].Refs = refsOut(rs)
		}
	}
	return out, nil
}

func (t *tools) addInterviewTools(s *mcp.Server) {
	type saveIn struct {
		InterviewID     int64               `json:"interview_id,omitempty" jsonschema:"an interview to change; leave out to log a new one"`
		JobID           int64               `json:"job_id,omitempty" jsonschema:"the job it's for; a new interview needs job_id, outreach_id or both"`
		OutreachID      int64               `json:"outreach_id,omitempty" jsonschema:"the outreach thread it came from"`
		Stage           string              `json:"stage,omitempty" jsonschema:"new interview: recruiter_screen, hiring_manager, technical, system_design, panel, product, final or other"`
		ScheduledAt     string              `json:"scheduled_at,omitempty" jsonschema:"new interview: when it starts, UTC, RFC 3339; a new time reschedules it"`
		DurationMinutes int                 `json:"duration_minutes,omitempty"`
		Interviewers    []store.Interviewer `json:"interviewers,omitempty" jsonschema:"who the user meets, as name and role; replaces the list"`
		Medium          string              `json:"medium,omitempty" jsonschema:"video, phone, onsite or other (the default)"`
		Joining         string              `json:"joining,omitempty" jsonschema:"how to join: link, meeting id and passcode, or address"`
		Status          string              `json:"status,omitempty" jsonschema:"scheduled (the default), completed, cancelled or no_show"`
		Outcome         string              `json:"outcome,omitempty" jsonschema:"pending (the default), advanced, rejected or offer"`
		NextStepAt      string              `json:"next_step_at,omitempty" jsonschema:"when the user expects to hear back, UTC, RFC 3339"`
		ClearNextStep   bool                `json:"clear_next_step,omitempty" jsonschema:"change: remove next_step_at"`
		PrepNotes       string              `json:"prep_notes,omitempty" jsonschema:"replaces the prep notes"`
		DebriefNotes    string              `json:"debrief_notes,omitempty" jsonschema:"replaces the debrief notes"`
		Recorded        *bool               `json:"recorded,omitempty" jsonschema:"whether the user recorded it"`
		RecordingTool   string              `json:"recording_tool,omitempty"`
		AddRefs         []refIn             `json:"add_refs,omitempty" jsonschema:"links to the email threads about this interview"`
		RemoveRefs      []int64             `json:"remove_refs,omitempty" jsonschema:"ids of links to remove"`
	}
	add(t, s, "save_interview", writes, func(ctx context.Context, c call, in saveIn) (any, error) {
		refs := refInputs(in.AddRefs)
		if err := core.CheckRefs(refs, in.RemoveRefs); err != nil {
			return nil, err
		}
		if in.InterviewID == 0 && len(in.RemoveRefs) > 0 {
			return nil, &core.InputError{Problems: []string{"remove_refs: a new interview has no links yet; to change one, give interview_id"}}
		}
		if err := core.CheckRemovals(ctx, t.Store, store.RefTarget{InterviewID: in.InterviewID}, in.RemoveRefs); err != nil {
			return nil, err
		}
		change := core.InterviewInput{
			JobID: in.JobID, OutreachID: in.OutreachID, Stage: in.Stage, ScheduledAt: in.ScheduledAt, DurationMinutes: in.DurationMinutes,
			Interviewers: in.Interviewers, Medium: in.Medium, Joining: in.Joining, Status: in.Status, Outcome: in.Outcome,
			NextStepAt: in.NextStepAt, ClearNextStep: in.ClearNextStep, PrepNotes: in.PrepNotes, DebriefNotes: in.DebriefNotes,
			Recorded: in.Recorded, RecordingTool: in.RecordingTool}
		// Links alone change nothing else about an interview.
		saved := store.InterviewSaved{ID: in.InterviewID}
		if in.InterviewID == 0 || !change.IsZero() || !core.HasRefs(refs, in.RemoveRefs) {
			var err error
			if saved, err = core.SaveInterview(ctx, t.Store, c.by, in.InterviewID, change); err != nil {
				return nil, err
			}
		}
		if err := core.ChangeRefs(ctx, t.Store, c.by, store.RefTarget{InterviewID: saved.ID}, refs, in.RemoveRefs); err != nil {
			return nil, err
		}
		out := struct {
			ID        int64  `json:"interview_id"`
			New       bool   `json:"new"`
			Job       int64  `json:"job,omitempty"`
			JobStatus string `json:"job_status,omitempty"`
			Moved     string `json:"job_moved_from,omitempty"`
			Note      string `json:"note,omitempty"`
		}{ID: saved.ID, New: in.InterviewID == 0, Job: saved.JobID, JobStatus: string(saved.JobStatus)}
		switch {
		case saved.Moved:
			out.Moved = string(saved.From)
		case saved.JobID != 0 && (saved.JobStatus == store.StatusRejected || saved.JobStatus == store.StatusWithdrawn) &&
			(in.InterviewID == 0 || in.JobID != 0):
			out.Note = fmt.Sprintf("the job is %s, so it stayed there; if the process is open again, change it with update_application", saved.JobStatus)
		}
		return out, nil
	})
}
