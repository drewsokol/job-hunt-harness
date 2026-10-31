package core

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Tracker rules.
const (
	DefaultFollowUp   = 7 * 24 * time.Hour  // after a message, when to follow up
	StaleApplication  = 21 * 24 * time.Hour // applied with no news for this long
	QuietOutreach     = 14 * 24 * time.Hour // a message with no reply and no follow-up date
	defaultWhatsNew   = 7 * 24 * time.Hour  // the first whats_new looks back this far
	whatsNewSetting   = "whats_new_at"
	trackerPage       = 50
	whatsNewPage      = 20
	maxChannelLen     = 40
	maxOutreachName   = 200
	maxOutreachNote   = 2_000
	futureAllowance   = time.Hour
	maxFollowUpFuture = 366 * 24 * time.Hour
	outreachPage      = 20
	maxOutreachPage   = 50
)

// OutreachInput is a message the user sent, as an agent reports it.
type OutreachInput struct {
	Person, Channel, Company, Role string
	JobID                          int64
	SentAt                         string // RFC 3339; default now
	FollowUpAt                     string // RFC 3339; default DefaultFollowUp after SentAt
	NoFollowUp                     bool
	Note                           string
}

// LoggedOutreach is what LogOutreach stored.
type LoggedOutreach struct {
	ID, ContactID int64
	NewContact    bool
	FollowUpAt    time.Time
}

// LogOutreach checks and records a message.
func LogOutreach(ctx context.Context, s *store.Store, by store.Actor, in OutreachInput) (LoggedOutreach, error) {
	var problems []string
	o := store.NewOutreach{
		Person: strings.TrimSpace(in.Person), Role: strings.TrimSpace(in.Role), Company: strings.TrimSpace(in.Company),
		Channel: strings.ToLower(strings.TrimSpace(in.Channel)), JobID: in.JobID, Note: strings.TrimSpace(in.Note),
	}
	for _, f := range []struct {
		name, v string
		max     int
		need    bool
	}{{"person", o.Person, maxOutreachName, true}, {"channel", o.Channel, maxChannelLen, true},
		{"role", o.Role, maxOutreachName, false}, {"company", o.Company, maxOutreachName, false}, {"note", o.Note, maxOutreachNote, false}} {
		switch n := utf8.RuneCountInString(f.v); {
		case f.need && (n == 0 || n > f.max):
			problems = append(problems, fmt.Sprintf("%s: 1 to %d characters", f.name, f.max))
		case n > f.max:
			problems = append(problems, fmt.Sprintf("%s: up to %d characters", f.name, f.max))
		}
	}
	now := utc.Now()
	o.SentAt = now
	if in.SentAt != "" {
		t, err := utc.Parse(in.SentAt)
		switch {
		case err != nil:
			problems = append(problems, "sent_at: "+err.Error())
		case t.After(now.Add(futureAllowance)):
			problems = append(problems, "sent_at: is in the future; log a message once it is sent")
		default:
			o.SentAt = t
		}
	}
	switch {
	case in.NoFollowUp && in.FollowUpAt != "":
		problems = append(problems, "follow_up_at: give a date or no_follow_up, not both")
	case in.NoFollowUp:
	case in.FollowUpAt != "":
		problems = append(problems, followUp(in.FollowUpAt, o.SentAt, &o.FollowUpAt)...)
	default:
		o.FollowUpAt = o.SentAt.Add(DefaultFollowUp)
	}
	if len(problems) > 0 {
		return LoggedOutreach{}, &InputError{Problems: problems}
	}
	id, contact, isNew, err := s.LogOutreach(ctx, by, o)
	if err != nil {
		return LoggedOutreach{}, inputIfNotFound(err, fmt.Sprintf("job_id: no job %d", in.JobID))
	}
	return LoggedOutreach{ID: id, ContactID: contact, NewContact: isNew, FollowUpAt: o.FollowUpAt}, nil
}

// followUp parses a follow-up date, which must be after the message and
// within a year.
func followUp(text string, after time.Time, dst *time.Time) []string {
	t, err := utc.Parse(text)
	switch {
	case err != nil:
		return []string{"follow_up_at: " + err.Error()}
	case !t.After(after):
		return []string{"follow_up_at: must be after the message was sent"}
	case t.After(utc.Now().Add(maxFollowUpFuture)):
		return []string{"follow_up_at: more than a year away"}
	}
	*dst = t
	return nil
}

// OutreachUpdate is a change to a thread, as an agent reports it.
type OutreachUpdate struct {
	Status        string
	FollowUpAt    string // RFC 3339
	ClearFollowUp bool
	Note          string // added to the thread's note
}

// UpdateOutreach checks and records a change to a thread.
func UpdateOutreach(ctx context.Context, s *store.Store, by store.Actor, id int64, in OutreachUpdate) error {
	var problems []string
	c := store.OutreachChange{Status: in.Status, ClearFollowUp: in.ClearFollowUp, AppendNote: strings.TrimSpace(in.Note)}
	if c.Status != "" && !slices.Contains(store.OutreachStatuses, c.Status) {
		problems = append(problems, fmt.Sprintf("status: %q must be one of %s", c.Status, strings.Join(store.OutreachStatuses, ", ")))
	}
	switch {
	case in.ClearFollowUp && in.FollowUpAt != "":
		problems = append(problems, "follow_up_at: give a date or clear_follow_up, not both")
	case in.FollowUpAt != "":
		problems = append(problems, followUp(in.FollowUpAt, utc.Now().Add(-futureAllowance), &c.FollowUpAt)...)
	}
	if utf8.RuneCountInString(c.AppendNote) > maxOutreachNote {
		problems = append(problems, fmt.Sprintf("note: up to %d characters", maxOutreachNote))
	}
	if c.Status == "" && c.FollowUpAt.IsZero() && !c.ClearFollowUp && c.AppendNote == "" {
		problems = append(problems, "nothing to change: give a status, follow_up_at, clear_follow_up or note")
	}
	if len(problems) > 0 {
		return &InputError{Problems: problems}
	}
	return inputIfNotFound(s.UpdateOutreach(ctx, by, id, c), fmt.Sprintf("outreach_id: no outreach %d", id))
}

// OutreachFilter selects outreach threads, as an agent asks for them.
// Every field given must match.
type OutreachFilter struct {
	ID       int64    // one thread, with its history; nothing else may be set
	Statuses []string // outreach statuses, or "open" for all but closed
	Person   string   // part of the person's name
	Company  string   // part of the company's name
	JobID    int64
	FollowUp string // due, set or none
	Limit    int
	Cursor   string // from the previous page's Next
}

// OutreachFollowUps are the follow_up filters ListOutreach takes.
var OutreachFollowUps = []string{"due", "set", "none"}

// OutreachPage is a page of threads. Next is the cursor for the following
// page, or empty on the last one. History is set only for one thread
// asked for by id: every event about it, oldest first.
type OutreachPage struct {
	Outreach []store.Outreach
	Total    int
	Next     string
	History  []store.Event
}

// ListOutreach finds outreach threads, most recently changed first, so an
// agent can find a thread's id again.
func ListOutreach(ctx context.Context, s *store.Store, in OutreachFilter) (OutreachPage, error) {
	var problems []string
	q := store.OutreachQuery{ID: in.ID, Person: strings.TrimSpace(in.Person), Company: strings.TrimSpace(in.Company),
		JobID: in.JobID, ChangedFirst: true}
	if in.ID != 0 && (len(in.Statuses) > 0 || q.Person != "" || q.Company != "" || in.JobID != 0 || in.FollowUp != "" || in.Cursor != "") {
		problems = append(problems, "outreach_id: give it alone for one thread, or leave it out to filter")
	}
	for _, st := range in.Statuses {
		switch {
		case st == "open":
			for _, o := range store.OutreachStatuses {
				if o != "closed" && !slices.Contains(q.Statuses, o) {
					q.Statuses = append(q.Statuses, o)
				}
			}
		case slices.Contains(store.OutreachStatuses, st):
			if !slices.Contains(q.Statuses, st) {
				q.Statuses = append(q.Statuses, st)
			}
		default:
			problems = append(problems, fmt.Sprintf("status: %q must be open or one of %s", st, strings.Join(store.OutreachStatuses, ", ")))
		}
	}
	switch in.FollowUp {
	case "":
	case "due":
		q.DueBy = utc.Now()
	case "set":
		q.FollowUpSet = true
	case "none":
		q.FollowUpNone = true
	default:
		problems = append(problems, fmt.Sprintf("follow_up: %q must be one of %s", in.FollowUp, strings.Join(OutreachFollowUps, ", ")))
	}
	q.Limit = in.Limit
	if q.Limit <= 0 {
		q.Limit = outreachPage
	}
	if q.Limit > maxOutreachPage {
		problems = append(problems, fmt.Sprintf("limit: at most %d at a time", maxOutreachPage))
	}
	if in.Cursor != "" {
		n, err := strconv.Atoi(in.Cursor)
		if err != nil || n < 0 {
			problems = append(problems, "cursor: pass the next value from the previous page unchanged")
		}
		q.Offset = n
	}
	if len(problems) > 0 {
		return OutreachPage{}, &InputError{Problems: problems}
	}
	os, total, err := s.ListOutreach(ctx, q)
	if err != nil {
		return OutreachPage{}, err
	}
	page := OutreachPage{Outreach: os, Total: total}
	if in.ID != 0 {
		if total == 0 {
			return page, &InputError{Problems: []string{fmt.Sprintf("outreach_id: no outreach %d", in.ID)}}
		}
		page.History, err = s.OutreachEvents(ctx, in.ID)
		return page, err
	}
	if q.Offset+len(os) < total {
		page.Next = strconv.Itoa(q.Offset + len(os))
	}
	return page, nil
}

// FindThread returns the one open thread with the person named exactly
// (in any case), narrowed by company and job when given. It never guesses:
// when none or several match, the error says so and lists the candidates'
// ids, so the agent can pick one. The candidates' companies are left out
// of the error, since they can come from a listing.
func FindThread(ctx context.Context, s *store.Store, person, company string, jobID int64) (int64, error) {
	person, company = strings.TrimSpace(person), strings.TrimSpace(company)
	if person == "" {
		return 0, &InputError{Problems: []string{"outreach_id: give the thread's id, or the person it's with (list_outreach finds ids)"}}
	}
	os, err := s.OpenThreads(ctx, person, company, jobID)
	if err != nil {
		return 0, err
	}
	switch len(os) {
	case 1:
		return os[0].ID, nil
	case 0:
		return 0, &InputError{Problems: []string{"person: no open outreach thread matches; check the name with list_outreach, and reopen a closed thread by its outreach_id"}}
	}
	ids := make([]string, 0, len(os))
	for _, o := range os {
		c := fmt.Sprintf("%d (sent %s", o.ID, o.SentAt.Format("2006-01-02"))
		if o.JobID != 0 {
			c += fmt.Sprintf(", job %d", o.JobID)
		}
		ids = append(ids, c+")")
	}
	return 0, &InputError{Problems: []string{fmt.Sprintf("person: %d open threads match, so nothing was changed; pass outreach_id, one of %s (list_outreach shows them)",
		len(os), strings.Join(ids, ", "))}}
}

// TrackerViews are the views get_tracker offers.
var TrackerViews = []string{"summary", "applied", "followups_due", "stale"}

// TrackerView is one view of the tracker; only its view's fields are set.
type TrackerView struct {
	Summary       *store.TrackerSummary
	Jobs          []store.JobRow
	JobsTotal     int
	Outreach      []store.Outreach
	OutreachTotal int
}

// Tracker returns a view of the applications and outreach.
func Tracker(ctx context.Context, s *store.Store, view string) (TrackerView, error) {
	var (
		v   TrackerView
		err error
	)
	now := utc.Now()
	switch cmpOrString(view, "summary") {
	case "summary":
		var t store.TrackerSummary
		t, err = s.Tracker(ctx, now)
		v.Summary = &t
	case "applied":
		v.Jobs, v.JobsTotal, err = s.Applications(ctx, 0, trackerPage)
	case "followups_due":
		v.Outreach, v.OutreachTotal, err = s.ListOutreach(ctx, store.OutreachQuery{DueBy: now, Limit: trackerPage})
	case "stale":
		v.Jobs, v.JobsTotal, err = s.StaleApplications(ctx, now.Add(-StaleApplication), 0, trackerPage)
		if err == nil {
			v.Outreach, v.OutreachTotal, err = s.ListOutreach(ctx, store.OutreachQuery{QuietSince: now.Add(-QuietOutreach), Limit: trackerPage})
		}
	default:
		return v, &InputError{Problems: []string{fmt.Sprintf("view: %q must be one of %s", view, strings.Join(TrackerViews, ", "))}}
	}
	return v, err
}

// WhatsNewReport is the daily check-in.
type WhatsNewReport struct {
	Since, Now          time.Time
	NewJobs             []store.JobRow // waiting for screening or review
	NewJobsTotal        int
	FollowUpsDue        []store.Outreach
	FollowUpsTotal      int
	ChangedApplications []store.JobRow // closed or reposted
	OpenFlags           int
	Suspect             []store.Company
	// PausedSearches are the search sources someone turned off, so their
	// jobs aren't being refreshed.
	PausedSearches []string
	LastRefreshAt  time.Time
}

// WhatsNew reports what changed since then, or since the last call when
// since is empty, and remembers this call.
func WhatsNew(ctx context.Context, s *store.Store, by store.Actor, since string) (WhatsNewReport, error) {
	r := WhatsNewReport{Now: utc.Now()}
	if since != "" {
		t, err := utc.Parse(since)
		if err != nil {
			return r, &InputError{Problems: []string{"since: " + err.Error()}}
		}
		r.Since = t
	} else if last, ok, err := s.Setting(ctx, whatsNewSetting); err != nil {
		return r, err
	} else if t, perr := utc.Parse(last); ok && perr == nil {
		r.Since = t
	} else {
		r.Since = r.Now.Add(-defaultWhatsNew)
	}
	var err error
	if r.NewJobs, r.NewJobsTotal, err = s.NewJobs(ctx, r.Since, whatsNewPage); err != nil {
		return r, err
	}
	if r.FollowUpsDue, r.FollowUpsTotal, err = s.ListOutreach(ctx, store.OutreachQuery{DueBy: r.Now, Limit: whatsNewPage}); err != nil {
		return r, err
	}
	if r.ChangedApplications, _, err = s.ChangedApplications(ctx, r.Since, trackerPage); err != nil {
		return r, err
	}
	t, err := s.Tracker(ctx, r.Now)
	if err != nil {
		return r, err
	}
	r.OpenFlags, r.LastRefreshAt = t.OpenFlags, t.LastRefreshAt
	if r.Suspect, err = s.SuspectCompanies(ctx); err != nil {
		return r, err
	}
	ss, err := ListSources(ctx, s, nil)
	if err != nil {
		return r, err
	}
	r.PausedSearches = PausedSearches(ss)
	return r, s.SetSetting(ctx, by, whatsNewSetting, utc.Format(r.Now))
}
