package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Limits on what agents read and write.
const (
	DefaultDescriptionChars = 5_000  // what an agent sees of a description by default
	MaxDescriptionChars     = 60_000 // even when it asks for all of it
	defaultPage             = 20
	maxPage                 = 50
	maxPageWithDescriptions = 10
	maxScreenings           = 50
	maxScreeningText        = 600
	maxScreeningFlags       = 10
	maxFlagLen              = 60
	maxNotesLen             = 10_000
	maxExcerptLen           = 500
)

// FlagKinds are the kinds of text addressed to an AI.
var FlagKinds = []string{"canary", "hostile", "hidden_text", "unclear"}

// ListInput selects a page of jobs.
type ListInput struct {
	Status             string
	Fit                string
	Limit              int
	Cursor             string // from the previous page's Next
	IncludeDescription bool
}

// JobPage is a page of jobs. Next is the cursor for the following page, or
// empty on the last one.
type JobPage struct {
	Jobs  []store.JobRow
	Total int
	Next  string
}

// ListJobs returns a page of jobs. Descriptions, when asked for, have
// boilerplate trimmed and are cut to DefaultDescriptionChars.
func ListJobs(ctx context.Context, s *store.Store, in ListInput) (JobPage, error) {
	var problems []string
	status := store.JobStatus(cmpOrString(in.Status, string(store.StatusUnscreened)))
	if !status.Valid() {
		problems = append(problems, fmt.Sprintf("status: %q isn't a job status; use one of new, filtered_out, unscreened, screened_out, review, skipped, applied, interviewing, offer, rejected, withdrawn", in.Status))
	}
	if in.Fit != "" && !slices.Contains(store.Fits, in.Fit) {
		problems = append(problems, fmt.Sprintf("fit: %q must be one of %s", in.Fit, strings.Join(store.Fits, ", ")))
	}
	limit, most := in.Limit, maxPage
	if in.IncludeDescription {
		most = maxPageWithDescriptions
	}
	if limit <= 0 {
		limit = min(defaultPage, most)
	}
	if limit > most {
		problems = append(problems, fmt.Sprintf("limit: at most %d at a time%s", most, map[bool]string{true: " with descriptions", false: ""}[in.IncludeDescription]))
	}
	offset := 0
	if in.Cursor != "" {
		n, err := strconv.Atoi(in.Cursor)
		if err != nil || n < 0 {
			problems = append(problems, "cursor: pass the next value from the previous page unchanged")
		}
		offset = n
	}
	if len(problems) > 0 {
		return JobPage{}, &InputError{Problems: problems}
	}
	rows, total, err := s.ListJobs(ctx, store.JobQuery{Status: status, Fit: in.Fit, Offset: offset, Limit: limit, WithDescription: in.IncludeDescription})
	if err != nil {
		return JobPage{}, err
	}
	for i := range rows {
		rows[i].Description = ForAgent(rows[i].Description, rows[i].Company, DefaultDescriptionChars)
	}
	page := JobPage{Jobs: rows, Total: total}
	if offset+len(rows) < total {
		page.Next = strconv.Itoa(offset + len(rows))
	}
	return page, nil
}

// GetJob returns everything about one job, its description trimmed and
// cut unless full is set.
func GetJob(ctx context.Context, s *store.Store, id int64, full bool) (store.JobDetail, error) {
	d, err := s.GetJobDetail(ctx, id)
	if err != nil {
		return d, inputIfNotFound(err, fmt.Sprintf("job_id: no job %d", id))
	}
	if full {
		d.Description = cut(d.Description, MaxDescriptionChars)
	} else {
		d.Description = ForAgent(d.Description, d.Company, DefaultDescriptionChars)
	}
	return d, nil
}

// ForAgent prepares a description for an agent: boilerplate trimmed (the
// full text is in the UI) and cut to at most limit characters.
func ForAgent(text, company string, limit int) string {
	if text == "" {
		return ""
	}
	return cut(normalize.TrimBoilerplate(text, company), limit)
}

// cutMarker ends a description that was cut short.
const cutMarker = " … [cut; ask for the full description]"

func cut(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit]) + cutMarker
}

// RecordScreenings checks and stores ratings, stamped with the current
// profile version.
func RecordScreenings(ctx context.Context, s *store.Store, by store.Actor, ss []store.NewScreening) ([]store.ScreeningResult, error) {
	var problems []string
	if len(ss) == 0 || len(ss) > maxScreenings {
		problems = append(problems, fmt.Sprintf("screenings: send 1 to %d at a time", maxScreenings))
	}
	seen := map[int64]bool{}
	for i := range ss {
		sc := &ss[i]
		field := fmt.Sprintf("screenings[%d]", i)
		sc.Reason, sc.Gaps = strings.TrimSpace(sc.Reason), strings.TrimSpace(sc.Gaps)
		if seen[sc.JobID] {
			problems = append(problems, fmt.Sprintf("%s.job_id: job %d is rated twice", field, sc.JobID))
		}
		seen[sc.JobID] = true
		if !slices.Contains(store.Fits, sc.Fit) {
			problems = append(problems, fmt.Sprintf("%s.fit: %q must be one of %s", field, sc.Fit, strings.Join(store.Fits, ", ")))
		}
		if n := utf8.RuneCountInString(sc.Reason); n == 0 || n > maxScreeningText {
			problems = append(problems, fmt.Sprintf("%s.reason: at most two sentences, specific to this posting (1 to %d characters)", field, maxScreeningText))
		}
		if utf8.RuneCountInString(sc.Gaps) > maxScreeningText {
			problems = append(problems, fmt.Sprintf("%s.gaps: keep it under %d characters", field, maxScreeningText))
		}
		if len(sc.Flags) > maxScreeningFlags {
			problems = append(problems, fmt.Sprintf("%s.flags: at most %d", field, maxScreeningFlags))
		}
		for j, f := range sc.Flags {
			if n := utf8.RuneCountInString(strings.TrimSpace(f)); n == 0 || n > maxFlagLen {
				problems = append(problems, fmt.Sprintf("%s.flags[%d]: a few words (1 to %d characters)", field, j, maxFlagLen))
			}
		}
	}
	if len(problems) > 0 {
		return nil, &InputError{Problems: problems}
	}
	p, _, err := s.Profile(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.RecordScreenings(ctx, by, p.Version, ss)
	return out, inputIfNotFound(err, "job_id: a rated job doesn't exist; check the numbers")
}

// ApplicationInput is a pipeline change from an agent or person.
type ApplicationInput struct {
	Status     string
	AppliedAt  string // RFC 3339; converted to UTC
	Notes      *string
	AppendNote string
}

// UpdateApplication checks and applies a pipeline change.
func UpdateApplication(ctx context.Context, s *store.Store, by store.Actor, id int64, in ApplicationInput) error {
	var (
		problems []string
		c        = store.ApplicationChange{Notes: in.Notes, AppendNote: strings.TrimSpace(in.AppendNote)}
	)
	if in.Status != "" {
		if c.Status = store.JobStatus(in.Status); !c.Status.Valid() {
			problems = append(problems, fmt.Sprintf("status: %q isn't a job status", in.Status))
		}
	}
	if in.AppliedAt != "" {
		t, err := utc.Parse(in.AppliedAt)
		if err != nil {
			problems = append(problems, "applied_at: "+err.Error())
		} else if t.After(utc.Now().Add(time.Hour)) {
			problems = append(problems, "applied_at: is in the future")
		}
		c.AppliedAt = &t
	}
	if in.Notes != nil && utf8.RuneCountInString(*in.Notes) > maxNotesLen || utf8.RuneCountInString(c.AppendNote) > maxNotesLen {
		problems = append(problems, fmt.Sprintf("notes: keep them under %d characters", maxNotesLen))
	}
	if c.Status == "" && c.AppliedAt == nil && c.Notes == nil && c.AppendNote == "" {
		problems = append(problems, "nothing to change: give a status, applied_at, notes or append_note")
	}
	if len(problems) > 0 {
		return &InputError{Problems: problems}
	}
	return inputIfNotFound(s.UpdateApplication(ctx, by, id, c), fmt.Sprintf("job_id: no job %d", id))
}

// ReportInjection records text in a listing that an agent found addressed
// to an AI. The job is never hidden; the user reviews it.
func ReportInjection(ctx context.Context, s *store.Store, by store.Actor, jobID int64, excerpt, kind, note string) (int64, error) {
	excerpt, note = strings.TrimSpace(excerpt), strings.TrimSpace(note)
	var problems []string
	if !slices.Contains(FlagKinds, kind) {
		problems = append(problems, fmt.Sprintf("kind: %q must be one of %s", kind, strings.Join(FlagKinds, ", ")))
	}
	if n := utf8.RuneCountInString(excerpt); n == 0 || n > maxExcerptLen {
		problems = append(problems, fmt.Sprintf("excerpt: quote the text, 1 to %d characters", maxExcerptLen))
	}
	if utf8.RuneCountInString(note) > maxReasonLen {
		problems = append(problems, fmt.Sprintf("note: keep it under %d characters", maxReasonLen))
	}
	if len(problems) > 0 {
		return 0, &InputError{Problems: problems}
	}
	id, err := s.ReportContentFlag(ctx, by, jobID, kind, excerpt, note)
	return id, inputIfNotFound(err, fmt.Sprintf("job_id: no job %d with a posting", jobID))
}

func cmpOrString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

const maxMerge = 10

// MergeJobs merges jobs that are one opening into the first of ids.
func MergeJobs(ctx context.Context, s *store.Store, by store.Actor, ids []int64) error {
	if len(ids) < 2 || len(ids) > maxMerge {
		return &InputError{Problems: []string{fmt.Sprintf("job_ids: 2 to %d jobs, the one to keep first", maxMerge)}}
	}
	if slices.Contains(ids[1:], ids[0]) || len(slices.Compact(slices.Sorted(slices.Values(ids)))) != len(ids) {
		return &InputError{Problems: []string{"job_ids: a job is listed twice"}}
	}
	return conflictInput(inputIfNotFound(s.MergeJobs(ctx, by, ids[0], ids[1:]), "job_ids: a job doesn't exist, or was already merged"))
}

// SplitJob moves postings out of a job, undoing a merge when they are the
// ones it brought in, and judges the job they move to.
func SplitJob(ctx context.Context, s *store.Store, by store.Actor, id int64, postings []string) (int64, error) {
	if len(postings) == 0 || len(postings) > maxScreenings {
		return 0, &InputError{Problems: []string{fmt.Sprintf("posting_ids: 1 to %d postings, from get_job", maxScreenings)}}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(postings)))) != len(postings) {
		return 0, &InputError{Problems: []string{"posting_ids: a posting is listed twice"}}
	}
	to, err := s.SplitJob(ctx, by, id, postings)
	if err != nil {
		return 0, conflictInput(inputIfNotFound(err, fmt.Sprintf("job_id: no job %d", id)))
	}
	j, err := s.Job(ctx, to)
	if err != nil {
		return to, err
	}
	_, err = RunPrefilter(ctx, s, by, j.CompanyID)
	return to, err
}

// conflictInput turns a store conflict into an input error, whose message
// says what to do instead.
func conflictInput(err error) error {
	if errors.Is(err, store.ErrConflict) {
		return &InputError{Problems: []string{strings.TrimSuffix(err.Error(), ": "+store.ErrConflict.Error())}}
	}
	return err
}
