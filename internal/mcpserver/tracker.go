package mcpserver

import (
	"context"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// outreachOut is one message. The person, role and note are the user's;
// the company may be a job's, which came from a listing, so it is inside
// untrusted with the job's title.
type outreachOut struct {
	ID         int64  `json:"outreach_id"`
	ContactID  int64  `json:"contact_id"`
	Person     string `json:"person"`
	Role       string `json:"role,omitempty"`
	Job        int64  `json:"job,omitempty"`
	Channel    string `json:"channel"`
	Sent       string `json:"sent"`
	Status     string `json:"status"`
	FollowUpAt string `json:"follow_up_at,omitempty"`
	Updated    string `json:"updated_at,omitempty"`
	Note       string `json:"note,omitempty"`
	Cut        bool   `json:"note_truncated,omitempty"`
	Untrusted  struct {
		Company  string `json:"company,omitempty"`
		JobTitle string `json:"job_title,omitempty"`
	} `json:"untrusted"`
}

func outreach(os []store.Outreach) []outreachOut {
	out := make([]outreachOut, 0, len(os))
	for _, o := range os {
		v := outreachOut{ID: o.ID, ContactID: o.ContactID, Person: o.Person, Role: o.Role, Job: o.JobID, Channel: o.Channel,
			Sent: timeOrEmpty(o.SentAt), Status: o.Status, FollowUpAt: timeOrEmpty(o.FollowUpAt), Updated: timeOrEmpty(o.UpdatedAt), Note: o.Note}
		v.Untrusted.Company, v.Untrusted.JobTitle = o.Company, o.JobTitle
		out = append(out, v)
	}
	return out
}

// listNoteChars is how much of each note list_outreach shows.
const listNoteChars = 200

// cutNotes shortens each thread's note to listNoteChars.
func cutNotes(os []outreachOut) []outreachOut {
	for i := range os {
		if r := []rune(os[i].Note); len(r) > listNoteChars {
			os[i].Note, os[i].Cut = string(r[:listNoteChars]), true
		}
	}
	return os
}

func jobs(rs []store.JobRow) []jobOut {
	out := make([]jobOut, 0, len(rs))
	for _, r := range rs {
		out = append(out, job(r, false))
	}
	return out
}

func (t *tools) addTrackerTools(s *mcp.Server) {
	type findIn struct {
		Person  string `json:"person" jsonschema:"the person's name, exactly (any case)"`
		Company string `json:"company,omitempty" jsonschema:"narrows the match"`
		JobID   int64  `json:"job_id,omitempty" jsonschema:"narrows the match"`
	}
	type saveIn struct {
		OutreachID    int64   `json:"outreach_id,omitempty" jsonschema:"a thread to change"`
		Find          *findIn `json:"find,omitempty" jsonschema:"instead of outreach_id: the one open thread with this person"`
		Person        string  `json:"person,omitempty" jsonschema:"new thread: who the user wrote to"`
		Channel       string  `json:"channel,omitempty" jsonschema:"new thread: such as linkedin, email, phone or in_person"`
		Company       string  `json:"company,omitempty" jsonschema:"new thread; default: the job's company"`
		JobID         int64   `json:"job_id,omitempty" jsonschema:"new thread: the job it's about, if any"`
		Role          string  `json:"role,omitempty" jsonschema:"new thread: the person's job title"`
		SentAt        string  `json:"sent_at,omitempty" jsonschema:"new thread: UTC, RFC 3339; default now"`
		NoFollowUp    bool    `json:"no_follow_up,omitempty" jsonschema:"new thread: no follow-up date"`
		Status        string  `json:"status,omitempty" jsonschema:"change: sent, accepted, replied, scheduled, no_reply or closed"`
		FollowUpAt    string  `json:"follow_up_at,omitempty" jsonschema:"UTC, RFC 3339; a new thread's default is a week after sent_at"`
		ClearFollowUp bool    `json:"clear_follow_up,omitempty" jsonschema:"change: remove the follow-up date"`
		Note          string  `json:"note,omitempty" jsonschema:"a new thread's note, or a line added to it"`
	}
	add(t, s, "save_outreach", writes, func(ctx context.Context, c call, in saveIn) (any, error) {
		type saved struct {
			ID         int64  `json:"outreach_id"`
			New        bool   `json:"new"`
			ContactID  int64  `json:"contact_id,omitempty"`
			NewContact bool   `json:"new_contact,omitempty"`
			FollowUpAt string `json:"follow_up_at,omitempty"`
		}
		id := in.OutreachID
		if id == 0 && in.Find == nil {
			if in.Status != "" || in.ClearFollowUp {
				return nil, &core.InputError{Problems: []string{"status: a new thread starts as sent; to change a thread, give outreach_id or find"}}
			}
			l, err := core.LogOutreach(ctx, t.Store, c.by, core.OutreachInput{Person: in.Person, Channel: in.Channel, Company: in.Company,
				Role: in.Role, JobID: in.JobID, SentAt: in.SentAt, FollowUpAt: in.FollowUpAt, NoFollowUp: in.NoFollowUp, Note: in.Note})
			return saved{l.ID, true, l.ContactID, l.NewContact, timeOrEmpty(l.FollowUpAt)}, err
		}
		switch {
		case id != 0 && in.Find != nil:
			return nil, &core.InputError{Problems: []string{"outreach_id: give the id or find, not both"}}
		case in.Person != "" || in.Channel != "" || in.Company != "" || in.JobID != 0 || in.Role != "" || in.SentAt != "" || in.NoFollowUp:
			return nil, &core.InputError{Problems: []string{"person: a thread's person, channel, company, job, role and sent_at are set when it's logged; a change takes status, follow_up_at, clear_follow_up and note (use find to name the thread by person)"}}
		case id == 0:
			var err error
			if id, err = core.FindThread(ctx, t.Store, in.Find.Person, in.Find.Company, in.Find.JobID); err != nil {
				return nil, err
			}
		}
		if err := core.UpdateOutreach(ctx, t.Store, c.by, id, core.OutreachUpdate{
			Status: in.Status, FollowUpAt: in.FollowUpAt, ClearFollowUp: in.ClearFollowUp, Note: in.Note}); err != nil {
			return nil, err
		}
		return saved{ID: id}, nil
	})

	type listOutreachIn struct {
		OutreachID int64    `json:"outreach_id,omitempty" jsonschema:"one thread, with its whole note and history; give it alone"`
		Status     []string `json:"status,omitempty" jsonschema:"open (all but closed), or any of sent, accepted, replied, scheduled, no_reply, closed"`
		Person     string   `json:"person,omitempty" jsonschema:"part of the person's name"`
		Company    string   `json:"company,omitempty" jsonschema:"part of the company's name"`
		JobID      int64    `json:"job_id,omitempty"`
		FollowUp   string   `json:"follow_up,omitempty" jsonschema:"due (waiting, follow-up date come), set or none"`
		Limit      int      `json:"limit,omitempty" jsonschema:"default 20, at most 50"`
		Cursor     string   `json:"cursor,omitempty" jsonschema:"next, from the previous page"`
	}
	add(t, s, "list_outreach", reads, func(ctx context.Context, _ call, in listOutreachIn) (any, error) {
		p, err := core.ListOutreach(ctx, t.Store, core.OutreachFilter{ID: in.OutreachID, Statuses: in.Status, Person: in.Person,
			Company: in.Company, JobID: in.JobID, FollowUp: in.FollowUp, Limit: in.Limit, Cursor: in.Cursor})
		if err != nil {
			return nil, err
		}
		if in.OutreachID != 0 {
			history := make([]eventOut, 0, len(p.History))
			for _, e := range p.History {
				history = append(history, event(e))
			}
			return struct {
				outreachOut
				History []eventOut `json:"history"`
			}{outreach(p.Outreach)[0], history}, nil
		}
		return struct {
			Total    int           `json:"total"`
			Outreach []outreachOut `json:"outreach"`
			Next     string        `json:"next,omitempty"`
		}{p.Total, cutNotes(outreach(p.Outreach)), p.Next}, nil
	})

	type trackerIn struct {
		View string `json:"view,omitempty" jsonschema:"summary (the default), applied, followups_due or stale"`
	}
	add(t, s, "get_tracker", reads, func(ctx context.Context, _ call, in trackerIn) (any, error) {
		v, err := core.Tracker(ctx, t.Store, in.View)
		if err != nil {
			return nil, err
		}
		if sm := v.Summary; sm != nil {
			return struct {
				Unscreened   int            `json:"unscreened"`
				ToReview     int            `json:"to_review"`
				Applications int            `json:"applications"`
				AppliedLast7 int            `json:"applied_last_7_days"`
				Interviewing int            `json:"interviewing"`
				Offers       int            `json:"offers"`
				FollowUpsDue int            `json:"follow_ups_due"`
				OpenFlags    int            `json:"content_flags_to_review"`
				SuspectFeeds int            `json:"suspect_feeds"`
				LastRefresh  string         `json:"last_refresh,omitempty"`
				Outreach     map[string]int `json:"outreach_by_status"`
			}{sm.Unscreened, sm.ToReview, sm.Applications, sm.AppliedLast7, sm.Interviewing, sm.Offers,
				sm.FollowUpsDue, sm.OpenFlags, sm.SuspectFeeds, timeOrEmpty(sm.LastRefreshAt), sm.OutreachByStatus}, nil
		}
		return struct {
			Jobs          []jobOut      `json:"jobs,omitempty"`
			JobsTotal     int           `json:"jobs_total,omitempty"`
			Outreach      []outreachOut `json:"outreach,omitempty"`
			OutreachTotal int           `json:"outreach_total,omitempty"`
			Contacts      []contactOut  `json:"contacts_to_re_engage,omitempty"`
			ContactsTotal int           `json:"contacts_to_re_engage_total,omitempty"`
		}{jobs(v.Jobs), v.JobsTotal, outreach(v.Outreach), v.OutreachTotal, contacts(v.Contacts, true), v.ContactsTotal}, nil
	})

	type whatsNewIn struct {
		Since string `json:"since,omitempty" jsonschema:"UTC, RFC 3339; default: the last call"`
	}
	add(t, s, "whats_new", writes, func(ctx context.Context, c call, in whatsNewIn) (any, error) {
		r, err := core.WhatsNew(ctx, t.Store, c.by, in.Since)
		if err != nil {
			return nil, err
		}
		suspect := make([]companyRef, 0, len(r.Suspect))
		for _, co := range r.Suspect {
			suspect = append(suspect, companyName(co.ID, co.Name, co.Origin))
		}
		return struct {
			Since               string        `json:"since"`
			Now                 string        `json:"now"`
			NewJobs             []jobOut      `json:"new_jobs"`
			NewJobsTotal        int           `json:"new_jobs_total"`
			FollowUpsDue        []outreachOut `json:"follow_ups_due"`
			FollowUpsTotal      int           `json:"follow_ups_due_total"`
			ReEngageDue         int           `json:"contacts_to_re_engage"`
			ChangedApplications []jobOut      `json:"applications_closed_or_reposted"`
			OpenFlags           int           `json:"content_flags_to_review"`
			Suspect             []companyRef  `json:"suspect_feeds"`
			Paused              []string      `json:"search_sources_off,omitempty"`
			LastRefresh         string        `json:"last_refresh,omitempty"`
		}{timeOrEmpty(r.Since), timeOrEmpty(r.Now), jobs(r.NewJobs), r.NewJobsTotal, outreach(r.FollowUpsDue), r.FollowUpsTotal, r.ReEngageDue,
			jobs(r.ChangedApplications), r.OpenFlags, suspect, r.PausedSearches, timeOrEmpty(r.LastRefreshAt)}, nil
	})
}
