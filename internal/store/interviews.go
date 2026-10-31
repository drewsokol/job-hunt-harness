package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Interview vocabularies.
var (
	InterviewStages   = []string{"recruiter_screen", "hiring_manager", "technical", "system_design", "panel", "product", "final", "other"}
	InterviewMedia    = []string{"video", "phone", "onsite", "other"}
	InterviewStatuses = []string{"scheduled", "completed", "cancelled", "no_show"}
	InterviewOutcomes = []string{"pending", "advanced", "rejected", "offer"}
)

// Interviewer is someone the user meets in an interview.
type Interviewer struct {
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

// Interview is one interview, about a job, an outreach thread or both.
// Joining (a meeting link, id and passcode, or an address) is never
// written to an event. Company and JobTitle can come from a listing.
type Interview struct {
	ID              int64
	JobID           int64
	OutreachID      int64
	Stage           string
	ScheduledAt     time.Time
	DurationMinutes int
	Interviewers    []Interviewer
	Medium          string
	Joining         string
	Status          string
	Outcome         string
	NextStepAt      time.Time
	PrepNotes       string
	DebriefNotes    string
	Recorded        bool
	RecordingTool   string
	UpdatedAt       time.Time
	Company         string
	JobTitle        string
}

// InterviewChange is a change to an interview; nil fields are left alone.
// A zero NextStepAt clears it, and a zero DurationMinutes clears it.
type InterviewChange struct {
	JobID, OutreachID                       *int64
	Stage, Medium, Joining, Status, Outcome *string
	PrepNotes, DebriefNotes, RecordingTool  *string
	ScheduledAt, NextStepAt                 *time.Time
	DurationMinutes                         *int
	Interviewers                            *[]Interviewer
	Recorded                                *bool
}

// InterviewSaved is what saving an interview did. JobStatus is the job's
// status afterwards (empty without a job); Moved says the interview moved
// it to interviewing from From.
type InterviewSaved struct {
	ID        int64
	JobID     int64
	JobStatus JobStatus
	Moved     bool
	From      JobStatus
}

// beforeInterviewing are the job statuses an interview moves on to
// interviewing. A job already interviewing or with an offer stays, and a
// rejected or withdrawn job is the user's to reopen.
var beforeInterviewing = []JobStatus{StatusNew, StatusFilteredOut, StatusUnscreened, StatusScreenedOut, StatusReview, StatusSkipped, StatusApplied}

// AddInterview records an interview and moves its job to interviewing
// when the job hadn't got that far.
func (s *Store) AddInterview(ctx context.Context, by Actor, iv Interview) (InterviewSaved, error) {
	var saved InterviewSaved
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := checkInterviewRefs(ctx, tx, iv.JobID, iv.OutreachID); err != nil {
			return err
		}
		at, err := timeText(iv.ScheduledAt)
		if err != nil {
			return err
		}
		next, err := timeText(iv.NextStepAt)
		if err != nil {
			return err
		}
		people, err := jsonText(noInterviewers(iv.Interviewers))
		if err != nil {
			return err
		}
		now := s.stamp()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO interviews (job_id, outreach_id, stage, scheduled_at, duration_minutes, interviewers, medium, joining, status, outcome,
			                        next_step_at, prep_notes, debrief_notes, recorded, recording_tool, created_at, created_by, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nullID(iv.JobID), nullID(iv.OutreachID), iv.Stage, at, nullInt(iv.DurationMinutes), people, iv.Medium, iv.Joining, iv.Status, iv.Outcome,
			next, iv.PrepNotes, iv.DebriefNotes, iv.Recorded, iv.RecordingTool, now, by, now, by)
		if err != nil {
			return err
		}
		if saved.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		refs := eventRefs{JobID: iv.JobID, OutreachID: iv.OutreachID, InterviewID: saved.ID}
		if err := insertEvent(ctx, tx, now, by, "interview_logged", refs,
			map[string]string{"stage": iv.Stage, "scheduled_at": at.String}); err != nil {
			return err
		}
		return s.interviewMovesJob(ctx, tx, now, by, iv.JobID, saved.ID, &saved)
	})
	return saved, err
}

// UpdateInterview changes an interview. The event names the fields that
// changed, with the old and new stage, time, medium, status and outcome;
// it never holds joining details, notes or interviewers. Linking a job
// moves it as AddInterview does.
func (s *Store) UpdateInterview(ctx context.Context, by Actor, id int64, c InterviewChange) (InterviewSaved, error) {
	saved := InterviewSaved{ID: id}
	err := s.write(ctx, func(tx *sql.Tx) error {
		ivs, err := queryInterviews(ctx, tx, "i.id = ?", []any{id}, "i.id")
		if err != nil {
			return err
		}
		if len(ivs) == 0 {
			return fmt.Errorf("interview %d: %w", id, ErrNotFound)
		}
		old := ivs[0]
		if c.JobID != nil || c.OutreachID != nil {
			if err := checkInterviewRefs(ctx, tx, deref(c.JobID), deref(c.OutreachID)); err != nil {
				return err
			}
		}
		var (
			sets    []string
			args    []any
			changed []string
		)
		data := map[string]any{}
		set := func(field string, v any) {
			sets, args, changed = append(sets, field+" = ?"), append(args, v), append(changed, field)
		}
		// fromTo sets a field that the event may show, old and new.
		fromTo := func(field, from, to string) {
			if from != to {
				set(field, to)
				data[field] = map[string]string{"from": from, "to": to}
			}
		}
		if c.JobID != nil && *c.JobID != old.JobID {
			set("job_id", *c.JobID)
		}
		if c.OutreachID != nil && *c.OutreachID != old.OutreachID {
			set("outreach_id", *c.OutreachID)
		}
		if c.Stage != nil {
			fromTo("stage", old.Stage, *c.Stage)
		}
		if c.Medium != nil {
			fromTo("medium", old.Medium, *c.Medium)
		}
		if c.Status != nil {
			fromTo("status", old.Status, *c.Status)
		}
		if c.Outcome != nil {
			fromTo("outcome", old.Outcome, *c.Outcome)
		}
		if c.ScheduledAt != nil && !c.ScheduledAt.Equal(old.ScheduledAt) {
			at, err := timeText(*c.ScheduledAt)
			if err != nil {
				return err
			}
			set("scheduled_at", at)
			data["scheduled_at"] = map[string]string{"from": utc.Format(old.ScheduledAt), "to": at.String}
		}
		if c.NextStepAt != nil && !c.NextStepAt.Equal(old.NextStepAt) {
			at, err := timeText(*c.NextStepAt)
			if err != nil {
				return err
			}
			set("next_step_at", at)
		}
		if c.DurationMinutes != nil && *c.DurationMinutes != old.DurationMinutes {
			set("duration_minutes", nullInt(*c.DurationMinutes))
		}
		if c.Interviewers != nil && !slices.Equal(*c.Interviewers, old.Interviewers) {
			people, err := jsonText(noInterviewers(*c.Interviewers))
			if err != nil {
				return err
			}
			set("interviewers", people)
		}
		for _, f := range []struct {
			name     string
			now, was *string
		}{{"joining", c.Joining, &old.Joining}, {"prep_notes", c.PrepNotes, &old.PrepNotes},
			{"debrief_notes", c.DebriefNotes, &old.DebriefNotes}, {"recording_tool", c.RecordingTool, &old.RecordingTool}} {
			if f.now != nil && *f.now != *f.was {
				set(f.name, *f.now)
			}
		}
		if c.Recorded != nil && *c.Recorded != old.Recorded {
			set("recorded", *c.Recorded)
		}
		jobID := old.JobID
		if c.JobID != nil {
			jobID = *c.JobID
		}
		if len(sets) > 0 {
			now := s.stamp()
			if _, err := tx.ExecContext(ctx, "UPDATE interviews SET "+strings.Join(sets, ", ")+", updated_at = ?, updated_by = ? WHERE id = ?",
				append(args, now, by, id)...); err != nil {
				return err
			}
			data["changed"] = changed
			outreachID := old.OutreachID
			if c.OutreachID != nil {
				outreachID = *c.OutreachID
			}
			if err := insertEvent(ctx, tx, now, by, "interview_updated", eventRefs{JobID: jobID, OutreachID: outreachID, InterviewID: id}, data); err != nil {
				return err
			}
			if c.JobID != nil && *c.JobID != old.JobID {
				return s.interviewMovesJob(ctx, tx, now, by, jobID, id, &saved)
			}
		}
		return jobStatusOf(ctx, tx, jobID, &saved)
	})
	return saved, err
}

// interviewMovesJob moves a job to interviewing when it hadn't got that
// far, and records the job's status in saved either way.
func (s *Store) interviewMovesJob(ctx context.Context, tx *sql.Tx, now string, by Actor, jobID, interviewID int64, saved *InterviewSaved) error {
	if err := jobStatusOf(ctx, tx, jobID, saved); err != nil || jobID == 0 || !slices.Contains(beforeInterviewing, saved.JobStatus) {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE jobs SET status = ?, updated_at = ?, updated_by = ? WHERE id = ?", StatusInterviewing, now, by, jobID); err != nil {
		return err
	}
	saved.Moved, saved.From, saved.JobStatus = true, saved.JobStatus, StatusInterviewing
	return insertEvent(ctx, tx, now, by, "status_changed", eventRefs{JobID: jobID, InterviewID: interviewID},
		map[string]any{"from": saved.From, "to": StatusInterviewing, "cause": "interview"})
}

func jobStatusOf(ctx context.Context, tx *sql.Tx, jobID int64, saved *InterviewSaved) error {
	saved.JobID = jobID
	if jobID == 0 {
		return nil
	}
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM jobs WHERE id = ?", jobID).Scan(&status); err != nil {
		return err
	}
	saved.JobStatus = JobStatus(status)
	return nil
}

func checkInterviewRefs(ctx context.Context, tx *sql.Tx, jobID, outreachID int64) error {
	var n int
	if jobID != 0 {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE id = ?", jobID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("job %d: %w", jobID, ErrNotFound)
		}
	}
	if outreachID != 0 {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM outreach WHERE id = ?", outreachID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("outreach %d: %w", outreachID, ErrNotFound)
		}
	}
	return nil
}

// InterviewQuery selects interviews; every field set must match.
type InterviewQuery struct {
	ID       int64
	JobID    int64
	Status   string
	From, To time.Time // scheduled_at from From, before To
	// NextStepBy selects completed interviews still pending whose next
	// step was expected by then.
	NextStepBy   time.Time
	SoonestFirst bool // otherwise newest first
	Limit        int  // 0 for no limit
}

// ListInterviews returns interviews.
func (s *Store) ListInterviews(ctx context.Context, q InterviewQuery) ([]Interview, error) {
	where, args := []string{"1"}, []any{}
	add := func(cond string, v any) { where, args = append(where, cond), append(args, v) }
	if q.ID != 0 {
		add("i.id = ?", q.ID)
	}
	if q.JobID != 0 {
		add("i.job_id = ?", q.JobID)
	}
	if q.Status != "" {
		add("i.status = ?", q.Status)
	}
	if !q.From.IsZero() {
		add("i.scheduled_at >= ?", utc.Format(q.From))
	}
	if !q.To.IsZero() {
		add("i.scheduled_at < ?", utc.Format(q.To))
	}
	if !q.NextStepBy.IsZero() {
		add("i.status = 'completed' AND i.outcome = 'pending' AND i.next_step_at IS NOT NULL AND i.next_step_at <= ?", utc.Format(q.NextStepBy))
	}
	order := "i.scheduled_at DESC, i.id DESC"
	if q.SoonestFirst {
		order = "i.scheduled_at, i.id"
	}
	if q.Limit > 0 {
		order += fmt.Sprintf(" LIMIT %d", q.Limit)
	}
	return queryInterviews(ctx, s.db, strings.Join(where, " AND "), args, order)
}

// NextInterviews returns, for each of the jobs that has one, its soonest
// scheduled interview from after on.
func (s *Store) NextInterviews(ctx context.Context, jobIDs []int64, after time.Time) (map[int64]Interview, error) {
	out := map[int64]Interview{}
	if len(jobIDs) == 0 {
		return out, nil
	}
	args := []any{utc.Format(after)}
	for _, id := range jobIDs {
		args = append(args, id)
	}
	ivs, err := queryInterviews(ctx, s.db, "i.status = 'scheduled' AND i.scheduled_at >= ? AND i.job_id IN ("+placeholders(len(jobIDs))+")",
		args, "i.scheduled_at DESC, i.id DESC")
	for _, iv := range ivs { // soonest last, so it wins
		out[iv.JobID] = iv
	}
	return out, err
}

// InterviewCounts counts scheduled interviews from now until before, and
// completed interviews still waiting on an outcome.
func (s *Store) InterviewCounts(ctx context.Context, now, before time.Time) (upcoming, pending int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM interviews WHERE status = 'scheduled' AND scheduled_at >= ? AND scheduled_at < ?),
			(SELECT count(*) FROM interviews WHERE status = 'completed' AND outcome = 'pending')`,
		utc.Format(now), utc.Format(before)).Scan(&upcoming, &pending)
	return upcoming, pending, err
}

type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryInterviews(ctx context.Context, db rowsQuerier, where string, args []any, order string) ([]Interview, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT i.id, coalesce(i.job_id, 0), coalesce(i.outreach_id, 0), i.stage, i.scheduled_at, coalesce(i.duration_minutes, 0), i.interviewers,
		       i.medium, i.joining, i.status, i.outcome, i.next_step_at, i.prep_notes, i.debrief_notes, i.recorded, i.recording_tool, i.updated_at,
		       coalesce(c.name, ct.company_name, ''), coalesce(j.title, '')
		FROM interviews i
		LEFT JOIN jobs j ON j.id = i.job_id
		LEFT JOIN companies c ON c.id = j.company_id
		LEFT JOIN outreach o ON o.id = i.outreach_id
		LEFT JOIN contacts ct ON ct.id = o.contact_id
		WHERE `+where+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interview
	for rows.Next() {
		var (
			iv                Interview
			at, next, updated sql.NullString
			people            string
		)
		if err := rows.Scan(&iv.ID, &iv.JobID, &iv.OutreachID, &iv.Stage, &at, &iv.DurationMinutes, &people,
			&iv.Medium, &iv.Joining, &iv.Status, &iv.Outcome, &next, &iv.PrepNotes, &iv.DebriefNotes, &iv.Recorded, &iv.RecordingTool, &updated,
			&iv.Company, &iv.JobTitle); err != nil {
			return nil, err
		}
		if iv.ScheduledAt, err = parseTime(at); err != nil {
			return nil, err
		}
		if iv.NextStepAt, err = parseTime(next); err != nil {
			return nil, err
		}
		if iv.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(people), &iv.Interviewers); err != nil {
			return nil, fmt.Errorf("interview %d interviewers: %w", iv.ID, err)
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

func noInterviewers(ps []Interviewer) []Interviewer {
	if ps == nil {
		return []Interviewer{}
	}
	return ps
}

func nullInt(n int) sql.NullInt64 { return sql.NullInt64{Int64: int64(n), Valid: n != 0} }

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
