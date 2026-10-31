package mcpserver

import (
	"context"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// outreachOut is one thread. The person, role, note, client and pay are
// the agent's record of what the user said; the company may be a job's,
// which came from a listing, so it is inside untrusted with the job's
// title.
type outreachOut struct {
	ID            int64   `json:"outreach_id"`
	ContactID     int64   `json:"contact_id"`
	Person        string  `json:"person"`
	Role          string  `json:"role,omitempty"`
	Job           int64   `json:"job,omitempty"`
	Direction     string  `json:"direction"`
	Channel       string  `json:"channel"`
	Sent          string  `json:"sent"`
	Status        string  `json:"status"`
	FollowUpAt    string  `json:"follow_up_at,omitempty"`
	Updated       string  `json:"updated_at,omitempty"`
	Note          string  `json:"note,omitempty"`
	Cut           bool    `json:"note_truncated,omitempty"`
	DeclineReason string  `json:"decline_reason,omitempty"`
	DeclineNote   string  `json:"decline_note,omitempty"`
	ClientCompany string  `json:"client_company,omitempty"`
	Pay           *payOut `json:"pay,omitempty"`
	Untrusted     struct {
		Company  string `json:"company,omitempty"`
		JobTitle string `json:"job_title,omitempty"`
	} `json:"untrusted"`
}

func outreach(os []store.Outreach) []outreachOut {
	out := make([]outreachOut, 0, len(os))
	for _, o := range os {
		v := outreachOut{ID: o.ID, ContactID: o.ContactID, Person: o.Person, Role: o.Role, Job: o.JobID, Direction: o.Direction, Channel: o.Channel,
			Sent: timeOrEmpty(o.SentAt), Status: o.Status, FollowUpAt: timeOrEmpty(o.FollowUpAt), Updated: timeOrEmpty(o.UpdatedAt), Note: o.Note,
			DeclineReason: o.DeclineReason, DeclineNote: o.DeclineNote, ClientCompany: o.ClientCompany, Pay: pay(o.Pay)}
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
		Person        string  `json:"person,omitempty" jsonschema:"new thread: who the user is talking to"`
		Direction     string  `json:"direction,omitempty" jsonschema:"inbound (they wrote first) or outbound (the user did); required on a new thread"`
		Channel       string  `json:"channel,omitempty" jsonschema:"new thread: linkedin_inmail, linkedin_message, linkedin_connection, email, job_site, phone, referral, event or other"`
		Company       string  `json:"company,omitempty" jsonschema:"new thread: who the person works for; default: the job's company"`
		JobID         int64   `json:"job_id,omitempty" jsonschema:"the job it's about; a change can set it once the role is known"`
		Role          string  `json:"role,omitempty" jsonschema:"new thread: the person's job title"`
		SentAt        string  `json:"sent_at,omitempty" jsonschema:"new thread: when it started; UTC, RFC 3339; default now"`
		NoFollowUp    bool    `json:"no_follow_up,omitempty" jsonschema:"new thread: no follow-up date"`
		Status        string  `json:"status,omitempty" jsonschema:"received, sent, accepted, replied, scheduled, no_reply, declined or closed"`
		FollowUpAt    string  `json:"follow_up_at,omitempty" jsonschema:"UTC, RFC 3339"`
		ClearFollowUp bool    `json:"clear_follow_up,omitempty" jsonschema:"change: remove the follow-up date"`
		Note          string  `json:"note,omitempty" jsonschema:"a new thread's note, or a line added to it"`
		ClientCompany string  `json:"client_company,omitempty" jsonschema:"an agency recruiter's employer, or undisclosed"`
		PayMin        float64 `json:"pay_min,omitempty" jsonschema:"any pay field replaces the thread's pay"`
		PayMax        float64 `json:"pay_max,omitempty"`
		PayCurrency   string  `json:"pay_currency,omitempty" jsonschema:"ISO code such as USD; needed with an amount"`
		PayPeriod     string  `json:"pay_period,omitempty" jsonschema:"year (the default) or hour"`
		PayNote       string  `json:"pay_note,omitempty" jsonschema:"what numbers can't hold, such as equity or flexibility"`
		DeclineReason string  `json:"decline_reason,omitempty" jsonschema:"with status declined: contract, pay_below_floor, hybrid_or_onsite, wrong_role, level_too_low, no_details, suspicious or other"`
		DeclineNote   string  `json:"decline_note,omitempty" jsonschema:"why, in the user's words"`
		AddRefs       []refIn `json:"add_refs,omitempty" jsonschema:"links to the email threads about this thread"`
		RemoveRefs    []int64 `json:"remove_refs,omitempty" jsonschema:"ids of links to remove"`
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
		refs := refInputs(in.AddRefs)
		if err := core.CheckRefs(refs, in.RemoveRefs); err != nil {
			return nil, err
		}
		p := core.PayInput{Min: in.PayMin, Max: in.PayMax, Currency: in.PayCurrency, Period: in.PayPeriod, Note: in.PayNote}
		if id == 0 && in.Find == nil {
			if in.ClearFollowUp {
				return nil, &core.InputError{Problems: []string{"clear_follow_up: a new thread has no date to clear; pass no_follow_up, or give outreach_id or find to change a thread"}}
			}
			if len(in.RemoveRefs) > 0 {
				return nil, &core.InputError{Problems: []string{"remove_refs: a new thread has no links yet; to change a thread, give outreach_id or find"}}
			}
			l, err := core.LogOutreach(ctx, t.Store, c.by, core.OutreachInput{Person: in.Person, Channel: in.Channel, Company: in.Company,
				Role: in.Role, JobID: in.JobID, Direction: in.Direction, Status: in.Status, SentAt: in.SentAt, FollowUpAt: in.FollowUpAt,
				NoFollowUp: in.NoFollowUp, Note: in.Note, ClientCompany: in.ClientCompany, Pay: p,
				DeclineReason: in.DeclineReason, DeclineNote: in.DeclineNote})
			if err != nil {
				return nil, err
			}
			if err := core.ChangeRefs(ctx, t.Store, c.by, store.RefTarget{OutreachID: l.ID}, refs, nil); err != nil {
				return nil, err
			}
			return saved{l.ID, true, l.ContactID, l.NewContact, timeOrEmpty(l.FollowUpAt)}, nil
		}
		switch {
		case id != 0 && in.Find != nil:
			return nil, &core.InputError{Problems: []string{"outreach_id: give the id or find, not both"}}
		case in.Person != "" || in.Channel != "" || in.Company != "" || in.Role != "" || in.SentAt != "" || in.NoFollowUp:
			return nil, &core.InputError{Problems: []string{"person: a thread's person, channel, company, role and sent_at are set when it's logged; a change takes status, follow_up_at, clear_follow_up, note, direction, job_id, client_company, pay and decline fields (use find to name the thread by person)"}}
		case id == 0:
			var err error
			if id, err = core.FindThread(ctx, t.Store, in.Find.Person, in.Find.Company, in.Find.JobID); err != nil {
				return nil, err
			}
		}
		u := core.OutreachUpdate{Status: in.Status, FollowUpAt: in.FollowUpAt, ClearFollowUp: in.ClearFollowUp, Note: in.Note,
			Direction: in.Direction, JobID: in.JobID, ClientCompany: in.ClientCompany, DeclineReason: in.DeclineReason, DeclineNote: in.DeclineNote}
		if !p.IsZero() {
			u.Pay = &p
		}
		if err := core.CheckRemovals(ctx, t.Store, store.RefTarget{OutreachID: id}, in.RemoveRefs); err != nil {
			return nil, err
		}
		if u != (core.OutreachUpdate{}) || !core.HasRefs(refs, in.RemoveRefs) {
			if err := core.UpdateOutreach(ctx, t.Store, c.by, id, u); err != nil {
				return nil, err
			}
		}
		if err := core.ChangeRefs(ctx, t.Store, c.by, store.RefTarget{OutreachID: id}, refs, in.RemoveRefs); err != nil {
			return nil, err
		}
		return saved{ID: id}, nil
	})

	type listOutreachIn struct {
		OutreachID int64    `json:"outreach_id,omitempty" jsonschema:"one thread, with its whole note and history; give it alone"`
		Status     []string `json:"status,omitempty" jsonschema:"open (all but declined and closed), or any of received, sent, accepted, replied, scheduled, no_reply, declined, closed"`
		Person     string   `json:"person,omitempty" jsonschema:"part of the person's name"`
		Company    string   `json:"company,omitempty" jsonschema:"part of the company's name"`
		JobID      int64    `json:"job_id,omitempty"`
		FollowUp   string   `json:"follow_up,omitempty" jsonschema:"due (waiting, follow-up date come), set or none"`
		Direction  string   `json:"direction,omitempty" jsonschema:"inbound or outbound"`
		Channel    string   `json:"channel,omitempty" jsonschema:"such as linkedin_inmail or email"`
		Decline    string   `json:"decline_reason,omitempty" jsonschema:"declined threads with this reason"`
		Since      string   `json:"since,omitempty" jsonschema:"threads started at or after; UTC, RFC 3339"`
		Until      string   `json:"until,omitempty" jsonschema:"threads started before; UTC, RFC 3339"`
		Limit      int      `json:"limit,omitempty" jsonschema:"default 20, at most 50"`
		Cursor     string   `json:"cursor,omitempty" jsonschema:"next, from the previous page"`
	}
	add(t, s, "list_outreach", reads, func(ctx context.Context, _ call, in listOutreachIn) (any, error) {
		p, err := core.ListOutreach(ctx, t.Store, core.OutreachFilter{ID: in.OutreachID, Statuses: in.Status, Person: in.Person,
			Company: in.Company, JobID: in.JobID, FollowUp: in.FollowUp, Limit: in.Limit, Cursor: in.Cursor,
			Direction: in.Direction, Channel: in.Channel, DeclineReason: in.Decline, Since: in.Since, Until: in.Until})
		if err != nil {
			return nil, err
		}
		if in.OutreachID != 0 {
			history := make([]eventOut, 0, len(p.History))
			for _, e := range p.History {
				history = append(history, event(e))
			}
			refs, err := t.Store.Refs(ctx, store.RefTarget{OutreachID: in.OutreachID})
			if err != nil {
				return nil, err
			}
			return struct {
				outreachOut
				Refs    []refOut   `json:"refs,omitempty"`
				History []eventOut `json:"history"`
			}{outreach(p.Outreach)[0], refsOut(refs), history}, nil
		}
		return struct {
			Total    int           `json:"total"`
			Outreach []outreachOut `json:"outreach"`
			Next     string        `json:"next,omitempty"`
		}{p.Total, cutNotes(outreach(p.Outreach)), p.Next}, nil
	})

	type trackerIn struct {
		View string `json:"view,omitempty" jsonschema:"summary (the default), applied, followups_due, stale or upcoming_interviews"`
	}
	add(t, s, "get_tracker", reads, func(ctx context.Context, _ call, in trackerIn) (any, error) {
		v, err := core.Tracker(ctx, t.Store, in.View)
		if err != nil {
			return nil, err
		}
		if sm := v.Summary; sm != nil {
			return struct {
				Unscreened    int                       `json:"unscreened"`
				ToReview      int                       `json:"to_review"`
				Applications  int                       `json:"applications"`
				AppliedLast7  int                       `json:"applied_last_7_days"`
				Interviewing  int                       `json:"interviewing"`
				Offers        int                       `json:"offers"`
				FollowUpsDue  int                       `json:"follow_ups_due"`
				OpenFlags     int                       `json:"content_flags_to_review"`
				SuspectFeeds  int                       `json:"suspect_feeds"`
				LastRefresh   string                    `json:"last_refresh,omitempty"`
				Outreach      map[string]map[string]int `json:"outreach"`
				InboundLast7  int                       `json:"inbound_last_7_days"`
				InboundLast30 int                       `json:"inbound_last_30_days"`
				Unanswered    int                       `json:"unanswered_inbound"`
				Declined      map[string]int            `json:"declined_by_reason"`
				Soon          int                       `json:"interviews_next_7_days"`
				Pending       int                       `json:"interviews_awaiting_outcome"`
			}{sm.Unscreened, sm.ToReview, sm.Applications, sm.AppliedLast7, sm.Interviewing, sm.Offers,
				sm.FollowUpsDue, sm.OpenFlags, sm.SuspectFeeds, timeOrEmpty(sm.LastRefreshAt), sm.Outreach.ByDirection,
				sm.Outreach.InboundLast7, sm.Outreach.InboundLast30, sm.Outreach.Unanswered, sm.Outreach.DeclinedByReason,
				v.InterviewsSoon, v.InterviewsPending}, nil
		}
		ivs, err := t.withRefs(ctx, interviews(v.Interviews, true))
		if err != nil {
			return nil, err
		}
		if in.View == "upcoming_interviews" {
			return struct {
				Interviews []interviewOut `json:"interviews"`
			}{ivs}, nil
		}
		return struct {
			Jobs          []jobOut       `json:"jobs,omitempty"`
			JobsTotal     int            `json:"jobs_total,omitempty"`
			Outreach      []outreachOut  `json:"outreach,omitempty"`
			OutreachTotal int            `json:"outreach_total,omitempty"`
			Interviews    []interviewOut `json:"interviews,omitempty"`
			Contacts      []contactOut   `json:"contacts_to_re_engage,omitempty"`
			ContactsTotal int            `json:"contacts_to_re_engage_total,omitempty"`
		}{jobs(v.Jobs), v.JobsTotal, outreach(v.Outreach), v.OutreachTotal, ivs, contacts(v.Contacts, true), v.ContactsTotal}, nil
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
			Unanswered          int           `json:"unanswered_inbound"`
			ChangedApplications []jobOut      `json:"applications_closed_or_reposted"`
			OpenFlags           int           `json:"content_flags_to_review"`
			Suspect             []companyRef  `json:"suspect_feeds"`
			Paused              []string      `json:"search_sources_off,omitempty"`
			LastRefresh         string        `json:"last_refresh,omitempty"`
		}{timeOrEmpty(r.Since), timeOrEmpty(r.Now), jobs(r.NewJobs), r.NewJobsTotal, outreach(r.FollowUpsDue), r.FollowUpsTotal, r.ReEngageDue, r.UnansweredInbound,
			jobs(r.ChangedApplications), r.OpenFlags, suspect, r.PausedSearches, timeOrEmpty(r.LastRefreshAt)}, nil
	})
}
