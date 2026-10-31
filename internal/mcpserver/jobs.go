package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/prefilter"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// untrustedJob holds every field of a job that came from a listing. Tool
// output carries listing text only inside one of the untrusted types, and
// the descriptions tell the agent never to follow instructions found there.
type untrustedJob struct {
	Source      string               `json:"source,omitempty"` // the posting it came from
	Company     string               `json:"company"`
	Title       string               `json:"title"`
	Places      []normalize.Place    `json:"places,omitempty"`
	Pay         []normalize.PayRange `json:"pay,omitempty"`
	PayText     string               `json:"pay_text,omitempty"`
	Employment  string               `json:"employment_type,omitempty"`
	URL         string               `json:"url,omitempty"`
	Description string               `json:"description,omitempty"`
}

type untrustedPosting struct {
	Title    string `json:"title"`
	Location string `json:"location,omitempty"`
	URL      string `json:"url,omitempty"`
}

type untrustedExcerpt struct {
	Excerpt string `json:"excerpt"`
}

type fitOut struct {
	Fit            string          `json:"fit"`
	Reason         string          `json:"reason"`
	Gaps           string          `json:"gaps,omitempty"`
	Flags          json.RawMessage `json:"flags,omitempty"`
	ProfileVersion int64           `json:"profile_version,omitempty"`
	By             string          `json:"by"`
	At             string          `json:"at"`
}

type jobOut struct {
	Job       int64  `json:"job"`
	Status    string `json:"status"`
	Origin    string `json:"origin"`
	Workplace string `json:"workplace"`
	// Via says the job was found by search and no board lists it: the link
	// is the job's page there, where the user applies.
	Via            string             `json:"via,omitempty"`
	Fit            *fitOut            `json:"screening,omitempty"`
	ContentFlags   []string           `json:"content_flags,omitempty"` // waiting for the user's review
	Score          float64            `json:"prefilter_score"`
	PrefilterNotes []prefilter.Reason `json:"prefilter_notes,omitempty"`
	FirstSeen      string             `json:"first_seen"`
	Posted         string             `json:"posted,omitempty"`
	Closed         string             `json:"closed,omitempty"`
	Reposted       string             `json:"reposted,omitempty"`
	Applied        string             `json:"applied,omitempty"`
	Elsewhere      bool               `json:"applied_elsewhere,omitempty"` // the user applied outside jhh
	Untrusted      untrustedJob       `json:"untrusted"`
}

// job shapes a row. allReasons includes every prefilter reason; otherwise
// only flags and notes, which is what a screener needs.
func job(r store.JobRow, allReasons bool) jobOut {
	out := jobOut{
		Job: r.ID, Status: string(r.Status), Origin: r.Origin, Workplace: r.Workplace, Via: sources.SearchTitle(r.Via),
		ContentFlags: r.OpenFlags, Score: r.Score,
		FirstSeen: timeOrEmpty(r.FirstSeenAt), Posted: timeOrEmpty(r.PostedAt), Closed: timeOrEmpty(r.ClosedAt),
		Reposted: timeOrEmpty(r.RepostedAt), Applied: timeOrEmpty(r.AppliedAt), Elsewhere: r.AppliedElsewhere,
		Untrusted: untrustedJob{
			Source: r.PostingID, Company: r.Company, Title: r.Title, Places: core.Places(r.Locations),
			Pay: core.PayRanges(r.PayRanges), PayText: r.PayRaw, Employment: r.EmploymentType, URL: r.URL,
			Description: r.Description,
		},
	}
	var reasons []prefilter.Reason
	_ = json.Unmarshal(r.Reasons, &reasons)
	for _, rs := range reasons {
		if allReasons || rs.Effect == prefilter.EffectFlag || rs.Effect == prefilter.EffectNote {
			out.PrefilterNotes = append(out.PrefilterNotes, rs)
		}
	}
	if sc := r.Screening; sc != nil {
		out.Fit = screening(*sc)
	}
	return out
}

func screening(sc store.Screening) *fitOut {
	f := &fitOut{Fit: sc.Fit, Reason: sc.Reason, Gaps: sc.Gaps, ProfileVersion: sc.ProfileVersion,
		By: string(sc.ScreenedBy), At: timeOrEmpty(sc.ScreenedAt)}
	if string(sc.Flags) != "[]" {
		f.Flags = sc.Flags
	}
	return f
}

func (t *tools) addJobTools(s *mcp.Server) {
	type listIn struct {
		Status             string `json:"status,omitempty" jsonschema:"default unscreened; review holds the jobs you rated"`
		Fit                string `json:"fit,omitempty" jsonschema:"only jobs whose latest rating is this: strong, good, stretch or no"`
		Limit              int    `json:"limit,omitempty" jsonschema:"default 20, at most 50, or 10 with descriptions"`
		Cursor             string `json:"cursor,omitempty" jsonschema:"next, from the previous page"`
		IncludeDescription bool   `json:"include_description,omitempty"`
	}
	add(t, s, "list_candidates", reads, func(ctx context.Context, _ call, in listIn) (any, error) {
		p, err := core.ListJobs(ctx, t.Store, core.ListInput{Status: in.Status, Fit: in.Fit, Limit: in.Limit, Cursor: in.Cursor, IncludeDescription: in.IncludeDescription})
		if err != nil {
			return nil, err
		}
		jobs := make([]jobOut, 0, len(p.Jobs))
		for _, r := range p.Jobs {
			jobs = append(jobs, job(r, false))
		}
		return struct {
			Total int      `json:"total"`
			Jobs  []jobOut `json:"jobs"`
			Next  string   `json:"next,omitempty"`
		}{p.Total, jobs, p.Next}, nil
	})

	type getIn struct {
		JobID           int64 `json:"job_id"`
		FullDescription bool  `json:"full_description,omitempty" jsonschema:"the whole description, without trimming boilerplate"`
	}
	add(t, s, "get_job", reads, func(ctx context.Context, _ call, in getIn) (any, error) {
		d, err := core.GetJob(ctx, t.Store, in.JobID, in.FullDescription)
		if err != nil {
			return nil, err
		}
		type postingOut struct {
			ID        string           `json:"id"`
			Source    string           `json:"source"`
			Search    bool             `json:"found_by_search,omitempty"`
			FirstSeen string           `json:"first_seen"`
			Closed    string           `json:"closed,omitempty"`
			Untrusted untrustedPosting `json:"untrusted"`
		}
		type flagOut struct {
			ID        int64            `json:"id"`
			Kind      string           `json:"kind"`
			Status    string           `json:"status"`
			Detector  string           `json:"detector"`
			Untrusted untrustedExcerpt `json:"untrusted"`
		}
		threads, err := core.ListOutreach(ctx, t.Store, core.OutreachFilter{JobID: in.JobID, Limit: 50})
		if err != nil {
			return nil, err
		}
		type threadOut struct {
			ID         int64   `json:"outreach_id"`
			Person     string  `json:"person"`
			Status     string  `json:"status"`
			FollowUpAt string  `json:"follow_up_at,omitempty"`
			Direction  string  `json:"direction"`
			Pay        *payOut `json:"pay,omitempty"`
		}
		out := struct {
			jobOut
			Notes      string       `json:"notes,omitempty"`
			Postings   []postingOut `json:"postings"`
			Screenings []*fitOut    `json:"screening_history,omitempty"`
			Flags      []flagOut    `json:"content_flag_details,omitempty"`
			Outreach   []threadOut  `json:"outreach,omitempty"`
			Timeline   []eventOut   `json:"timeline"`
		}{jobOut: job(d.JobRow, true), Notes: d.Notes, Postings: []postingOut{}, Timeline: []eventOut{}}
		for _, o := range threads.Outreach {
			out.Outreach = append(out.Outreach, threadOut{o.ID, o.Person, o.Status, timeOrEmpty(o.FollowUpAt), o.Direction, pay(o.Pay)})
		}
		for _, p := range d.Postings {
			out.Postings = append(out.Postings, postingOut{p.ID, p.Source, p.Search, timeOrEmpty(p.FirstSeenAt), timeOrEmpty(p.ClosedAt),
				untrustedPosting{p.Title, p.LocationRaw, p.URL}})
		}
		for i, sc := range d.Screenings {
			if i > 0 { // the latest is already in screening
				out.Screenings = append(out.Screenings, screening(sc))
			}
		}
		for _, f := range d.Flags {
			out.Flags = append(out.Flags, flagOut{f.ID, f.Kind, f.Status, f.Detector, untrustedExcerpt{f.Excerpt}})
		}
		for _, e := range d.Events {
			out.Timeline = append(out.Timeline, event(e))
		}
		return out, nil
	})

	type screeningIn struct {
		JobID  int64    `json:"job_id"`
		Fit    string   `json:"fit" jsonschema:"strong, good, stretch or no"`
		Reason string   `json:"reason" jsonschema:"at most two sentences, specific to this posting"`
		Gaps   string   `json:"gaps,omitempty"`
		Flags  []string `json:"flags,omitempty" jsonschema:"short labels for things the user asked to see"`
	}
	type recordIn struct {
		Screenings []screeningIn `json:"screenings"`
	}
	add(t, s, "record_screening", writes, func(ctx context.Context, c call, in recordIn) (any, error) {
		ss := make([]store.NewScreening, 0, len(in.Screenings))
		for _, sc := range in.Screenings {
			ss = append(ss, store.NewScreening{JobID: sc.JobID, Fit: sc.Fit, Reason: sc.Reason, Gaps: sc.Gaps, Flags: sc.Flags})
		}
		rs, err := core.RecordScreenings(ctx, t.Store, c.by, ss)
		if err != nil {
			return nil, err
		}
		type result struct {
			Job    int64  `json:"job"`
			Status string `json:"status"`
		}
		out := make([]result, 0, len(rs))
		for _, r := range rs {
			out = append(out, result{r.JobID, string(r.Status)})
		}
		return struct {
			Recorded []result `json:"recorded"`
		}{out}, nil
	})

	type applicationIn struct {
		JobID      int64   `json:"job_id"`
		Status     string  `json:"status,omitempty" jsonschema:"such as applied, interviewing, offer, rejected, withdrawn or skipped"`
		AppliedAt  string  `json:"applied_at,omitempty" jsonschema:"UTC, RFC 3339; defaults to now when the status becomes applied"`
		Notes      *string `json:"notes,omitempty" jsonschema:"replaces the job's notes"`
		AppendNote string  `json:"append_note,omitempty" jsonschema:"adds a line to the notes"`
	}
	add(t, s, "update_application", writes, func(ctx context.Context, c call, in applicationIn) (any, error) {
		return ok(core.UpdateApplication(ctx, t.Store, c.by, in.JobID, core.ApplicationInput{
			Status: in.Status, AppliedAt: in.AppliedAt, Notes: in.Notes, AppendNote: in.AppendNote,
		}))
	})

	type agentJobIn struct {
		URL            string `json:"url,omitempty" jsonschema:"the posting's link; a Greenhouse, Lever or Ashby link is read from the board"`
		Company        string `json:"company"`
		Title          string `json:"title,omitempty" jsonschema:"required unless url is a Greenhouse, Lever or Ashby posting"`
		Location       string `json:"location,omitempty"`
		Description    string `json:"description,omitempty"`
		Pay            string `json:"pay,omitempty" jsonschema:"as the listing states it"`
		EmploymentType string `json:"employment_type,omitempty"`
		Notes          string `json:"notes,omitempty" jsonschema:"where the job came from, for the user; added to the job's notes"`
		Status         string `json:"status,omitempty" jsonschema:"for a job the user applied to outside jhh: applied, interviewing, offer, rejected or withdrawn"`
		AppliedAt      string `json:"applied_at,omitempty" jsonschema:"with status: when they applied, UTC, RFC 3339; default now"`
	}
	type addJobsIn struct {
		Jobs []agentJobIn `json:"jobs"`
	}
	add(t, s, "add_jobs", writes, func(ctx context.Context, c call, in addJobsIn) (any, error) {
		js := make([]core.AgentJob, 0, len(in.Jobs))
		for _, j := range in.Jobs {
			js = append(js, core.AgentJob{URL: j.URL, Company: j.Company, Title: j.Title, Location: j.Location,
				Description: j.Description, Pay: j.Pay, EmploymentType: j.EmploymentType, Notes: j.Notes,
				Status: j.Status, AppliedAt: j.AppliedAt})
		}
		added, err := core.AddJobs(ctx, t.Store, t.Sources, c.by, js)
		if err != nil {
			return nil, err
		}
		type addedOut struct {
			Job       int64  `json:"job,omitempty"`
			Existing  bool   `json:"already_in_jhh,omitempty"`
			ByTitle   bool   `json:"matched_by_title,omitempty"`
			FromBoard bool   `json:"read_from_board,omitempty"`
			Status    string `json:"status,omitempty"`
			Untrusted *struct {
				Error string `json:"error"` // may quote the board's response
			} `json:"untrusted,omitempty"`
		}
		out := make([]addedOut, 0, len(added))
		for _, a := range added {
			o := addedOut{Job: a.JobID, Existing: a.Existing, ByTitle: a.ByTitle, FromBoard: a.FromBoard, Status: string(a.Status)}
			if a.Error != "" {
				o.Untrusted = &struct {
					Error string `json:"error"`
				}{a.Error}
			}
			out = append(out, o)
		}
		return struct {
			Jobs []addedOut `json:"jobs"`
		}{out}, nil
	})

	type mergeIn struct {
		JobIDs []int64 `json:"job_ids" jsonschema:"the job to keep first, then the jobs that are the same opening"`
	}
	add(t, s, "merge_jobs", writes, func(ctx context.Context, c call, in mergeIn) (any, error) {
		return ok(core.MergeJobs(ctx, t.Store, c.by, in.JobIDs))
	})

	type splitIn struct {
		JobID      int64    `json:"job_id"`
		PostingIDs []string `json:"posting_ids" jsonschema:"the postings to move out, by id from get_job"`
	}
	add(t, s, "split_job", writes, func(ctx context.Context, c call, in splitIn) (any, error) {
		to, err := core.SplitJob(ctx, t.Store, c.by, in.JobID, in.PostingIDs)
		return struct {
			Job int64 `json:"moved_to_job"`
		}{to}, err
	})

	type injectionIn struct {
		JobID   int64  `json:"job_id"`
		Excerpt string `json:"excerpt" jsonschema:"the text addressed to an AI, quoted"`
		Kind    string `json:"kind" jsonschema:"canary, hostile, hidden_text or unclear"`
		Note    string `json:"note,omitempty"`
	}
	add(t, s, "report_injection", writes, func(ctx context.Context, c call, in injectionIn) (any, error) {
		id, err := core.ReportInjection(ctx, t.Store, c.by, in.JobID, in.Excerpt, in.Kind, in.Note)
		return struct {
			FlagID int64 `json:"flag_id"`
		}{id}, err
	})
}

// eventOut is one timeline event. OutreachID names the thread it's about.
type eventOut struct {
	At         string          `json:"at"`
	Actor      string          `json:"actor"`
	Kind       string          `json:"kind"`
	OutreachID int64           `json:"outreach_id,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

func event(e store.Event) eventOut {
	ev := eventOut{At: timeOrEmpty(e.At), Actor: string(e.Actor), Kind: e.Kind, OutreachID: e.OutreachID}
	if string(e.Data) != "{}" {
		ev.Data = e.Data
	}
	return ev
}
