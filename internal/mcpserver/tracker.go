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
	Note       string `json:"note,omitempty"`
	Untrusted  struct {
		Company  string `json:"company,omitempty"`
		JobTitle string `json:"job_title,omitempty"`
	} `json:"untrusted"`
}

func outreach(os []store.Outreach) []outreachOut {
	out := make([]outreachOut, 0, len(os))
	for _, o := range os {
		v := outreachOut{ID: o.ID, ContactID: o.ContactID, Person: o.Person, Role: o.Role, Job: o.JobID, Channel: o.Channel,
			Sent: timeOrEmpty(o.SentAt), Status: o.Status, FollowUpAt: timeOrEmpty(o.FollowUpAt), Note: o.Note}
		v.Untrusted.Company, v.Untrusted.JobTitle = o.Company, o.JobTitle
		out = append(out, v)
	}
	return out
}

func jobs(rs []store.JobRow) []jobOut {
	out := make([]jobOut, 0, len(rs))
	for _, r := range rs {
		out = append(out, job(r, false))
	}
	return out
}

func (t *tools) addTrackerTools(s *mcp.Server) {
	type logIn struct {
		Person     string `json:"person" jsonschema:"who the user wrote to"`
		Channel    string `json:"channel" jsonschema:"such as linkedin, email, phone or in_person"`
		Company    string `json:"company,omitempty" jsonschema:"default: the job's company"`
		JobID      int64  `json:"job_id,omitempty" jsonschema:"the job it's about, if any"`
		Role       string `json:"role,omitempty" jsonschema:"the person's job title"`
		SentAt     string `json:"sent_at,omitempty" jsonschema:"UTC, RFC 3339; default now"`
		FollowUpAt string `json:"follow_up_at,omitempty" jsonschema:"UTC, RFC 3339; default a week after sent_at"`
		NoFollowUp bool   `json:"no_follow_up,omitempty"`
		Note       string `json:"note,omitempty"`
	}
	add(t, s, "log_outreach", writes, func(ctx context.Context, c call, in logIn) (any, error) {
		l, err := core.LogOutreach(ctx, t.Store, c.by, core.OutreachInput{Person: in.Person, Channel: in.Channel, Company: in.Company,
			Role: in.Role, JobID: in.JobID, SentAt: in.SentAt, FollowUpAt: in.FollowUpAt, NoFollowUp: in.NoFollowUp, Note: in.Note})
		return struct {
			ID         int64  `json:"outreach_id"`
			ContactID  int64  `json:"contact_id"`
			NewContact bool   `json:"new_contact"`
			FollowUpAt string `json:"follow_up_at,omitempty"`
		}{l.ID, l.ContactID, l.NewContact, timeOrEmpty(l.FollowUpAt)}, err
	})

	type updateIn struct {
		OutreachID    int64  `json:"outreach_id"`
		Status        string `json:"status,omitempty" jsonschema:"sent, accepted, replied, scheduled, no_reply or closed"`
		FollowUpAt    string `json:"follow_up_at,omitempty" jsonschema:"UTC, RFC 3339"`
		ClearFollowUp bool   `json:"clear_follow_up,omitempty"`
		Note          string `json:"note,omitempty" jsonschema:"added to the thread's note"`
	}
	add(t, s, "update_outreach", writes, func(ctx context.Context, c call, in updateIn) (any, error) {
		return ok(core.UpdateOutreach(ctx, t.Store, c.by, in.OutreachID, core.OutreachUpdate{
			Status: in.Status, FollowUpAt: in.FollowUpAt, ClearFollowUp: in.ClearFollowUp, Note: in.Note}))
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
				Unscreened   int    `json:"unscreened"`
				ToReview     int    `json:"to_review"`
				Applications int    `json:"applications"`
				AppliedLast7 int    `json:"applied_last_7_days"`
				Interviewing int    `json:"interviewing"`
				Offers       int    `json:"offers"`
				FollowUpsDue int    `json:"follow_ups_due"`
				OpenFlags    int    `json:"content_flags_to_review"`
				SuspectFeeds int    `json:"suspect_feeds"`
				LastRefresh  string `json:"last_refresh,omitempty"`
			}{sm.Unscreened, sm.ToReview, sm.Applications, sm.AppliedLast7, sm.Interviewing, sm.Offers,
				sm.FollowUpsDue, sm.OpenFlags, sm.SuspectFeeds, timeOrEmpty(sm.LastRefreshAt)}, nil
		}
		return struct {
			Jobs          []jobOut      `json:"jobs,omitempty"`
			JobsTotal     int           `json:"jobs_total,omitempty"`
			Outreach      []outreachOut `json:"outreach,omitempty"`
			OutreachTotal int           `json:"outreach_total,omitempty"`
		}{jobs(v.Jobs), v.JobsTotal, outreach(v.Outreach), v.OutreachTotal}, nil
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
			ChangedApplications []jobOut      `json:"applications_closed_or_reposted"`
			OpenFlags           int           `json:"content_flags_to_review"`
			Suspect             []companyRef  `json:"suspect_feeds"`
			Paused              []string      `json:"search_sources_off,omitempty"`
			LastRefresh         string        `json:"last_refresh,omitempty"`
		}{timeOrEmpty(r.Since), timeOrEmpty(r.Now), jobs(r.NewJobs), r.NewJobsTotal, outreach(r.FollowUpsDue), r.FollowUpsTotal,
			jobs(r.ChangedApplications), r.OpenFlags, suspect, r.PausedSearches, timeOrEmpty(r.LastRefreshAt)}, nil
	})
}
