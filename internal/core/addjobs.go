package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Limits on jobs an agent adds.
const (
	maxAgentJobs       = 20
	maxAgentURLLen     = 2_000
	maxAgentNameLen    = 200
	maxAgentFieldLen   = 300
	maxAgentDescLen    = 100_000
	agentPostingSource = "agent"
)

// AgentJob is a job an agent found: a link to a posting, or what it read
// about the job. Every field is untrusted.
type AgentJob struct {
	URL            string
	Company        string
	Title          string
	Location       string
	Description    string
	Pay            string
	EmploymentType string
	Notes          string // the agent's own, for the job's notes
	// Status, when set, records an application the user made outside
	// jhh: applied, interviewing, offer, rejected or withdrawn. The job
	// goes straight to it instead of unscreened.
	Status    string
	AppliedAt string // RFC 3339, with Status; default now

	appliedAt time.Time // AppliedAt, parsed
}

// ElsewhereStatuses are the statuses an application made outside jhh can
// be recorded in.
var ElsewhereStatuses = []string{"applied", "interviewing", "offer", "rejected", "withdrawn"}

// AddedJob is what became of one AgentJob.
type AddedJob struct {
	JobID int64
	// Existing means the job was already in jhh; its status is unchanged,
	// unless the job came with a Status.
	Existing bool
	// ByTitle means it matched an existing job by company and title alone,
	// since it came with no link or description.
	ByTitle bool
	// FromBoard means the link was a Greenhouse, Lever or Ashby posting,
	// which jhh read from the board itself instead of the agent's fields.
	FromBoard bool
	Status    store.JobStatus
	Error     string // why it wasn't added; the others still are
}

// boardRef is an AgentJob link that names a posting on a supported board.
type boardRef struct {
	src sources.Source
	ref sources.PostingRef
}

// AddJobs stores jobs an agent found. A link to a posting on a supported
// board is read from the board, so the job is the same as one refresh
// would find; anything else is stored from the agent's fields, as a
// posting with source "agent". Each goes through the dedupe ladder. A new
// job lands in unscreened whatever the filters say, since the agent chose
// it, and the prefilter's decision is recorded for the screener to see.
// Jobs the user adds (by is ActorUser) get origin "manual" and land in
// review: they found the job themselves. A job with a Status is an
// application the user made elsewhere: it goes to that status, new or
// not, and is marked applied elsewhere.
func AddJobs(ctx context.Context, s *store.Store, srcs map[string]sources.Source, by store.Actor, jobs []AgentJob) ([]AddedJob, error) {
	refs, err := checkAgentJobs(srcs, jobs)
	if err != nil {
		return nil, err
	}
	companies, err := s.Companies(ctx)
	if err != nil {
		return nil, err
	}
	origin, companyOrigin, status := "agent", "agent", store.StatusUnscreened
	if by == store.ActorUser {
		origin, companyOrigin, status = "manual", "user", store.StatusReview
	}
	out := make([]AddedJob, len(jobs))
	touched := map[int64]bool{}
	for i, j := range jobs {
		var (
			companyID int64
			in        store.PostingInput
		)
		if j.Status != "" && refs[i] == nil && j.URL == "" && strings.TrimSpace(j.Description) == "" {
			// Nothing to dedupe by but the name and title: an exact match
			// of both is the same job.
			id, err := s.JobWithTitle(ctx, dedupe.CompanyKey(j.Company), dedupe.TitleKey(j.Title))
			if err != nil {
				return out, err
			}
			if id != 0 {
				out[i].JobID, out[i].Existing, out[i].ByTitle = id, true, true
				if err := recordElsewhere(ctx, s, by, id, j); err != nil {
					return out, err
				}
				if err := appendAgentNote(ctx, s, by, id, j.Notes); err != nil {
					return out, err
				}
				continue
			}
		}
		if r := refs[i]; r != nil {
			out[i].FromBoard = true
			key := r.ref.Source + ":" + r.ref.Slug
			for _, c := range companies {
				if c.BoardKey == key {
					companyID = c.ID
				}
			}
			board := sources.Board{Slug: r.ref.Slug}
			p, err := fetchPosting(ctx, r.src, board, r.ref.ExternalID)
			if err != nil {
				out[i].Error = err.Error()
				continue
			}
			in = Normalize(r.src, board, p)
		} else {
			in = agentPosting(j)
		}
		in.Chosen = origin
		if companyID == 0 {
			es, err := s.Employers(ctx, by, companyOrigin, []string{j.Company})
			if err != nil {
				return out, err
			}
			companyID = es[j.Company].CompanyID
		}
		res, err := s.ApplyPostings(ctx, by, companyID, []store.PostingInput{in})
		if err != nil {
			return out, err
		}
		id, err := s.PostingJob(ctx, in.ID)
		if err != nil {
			return out, err
		}
		out[i].JobID, out[i].Existing = id, !slices.Contains(res.NewJobs, id)
		switch {
		case j.Status != "":
			if err := recordElsewhere(ctx, s, by, id, j); err != nil {
				return out, err
			}
		case !out[i].Existing:
			if err := s.SetJobStatus(ctx, by, id, status); err != nil {
				return out, err
			}
		}
		if err := appendAgentNote(ctx, s, by, id, j.Notes); err != nil {
			return out, err
		}
		touched[companyID] = true
	}
	for id := range touched {
		if _, err := RunPrefilter(ctx, s, by, id); err != nil {
			return out, err
		}
	}
	for i := range out {
		if out[i].JobID != 0 {
			j, err := s.Job(ctx, out[i].JobID)
			if err != nil {
				return out, err
			}
			out[i].Status = j.Status
		}
	}
	return out, nil
}

// recordElsewhere moves a job to the status of an application made
// outside jhh, like update_application, and marks it applied elsewhere.
// An applied date the job already has is kept.
func recordElsewhere(ctx context.Context, s *store.Store, by store.Actor, id int64, j AgentJob) error {
	cur, err := s.Job(ctx, id)
	if err != nil {
		return err
	}
	c := store.ApplicationChange{Status: store.JobStatus(j.Status)}
	if cur.AppliedAt.IsZero() {
		c.AppliedAt = &j.appliedAt
	}
	if err := s.UpdateApplication(ctx, by, id, c); err != nil {
		return err
	}
	return s.MarkAppliedElsewhere(ctx, by, id)
}

// appendAgentNote adds the agent's note, if any, to a job's notes.
func appendAgentNote(ctx context.Context, s *store.Store, by store.Actor, id int64, note string) error {
	if note = strings.TrimSpace(note); note == "" {
		return nil
	}
	return s.UpdateApplication(ctx, by, id, store.ApplicationChange{AppendNote: note})
}

// checkAgentJobs checks every job before any is stored, and finds the
// links that name a posting on a supported board.
func checkAgentJobs(srcs map[string]sources.Source, jobs []AgentJob) ([]*boardRef, error) {
	var problems []string
	if len(jobs) == 0 || len(jobs) > maxAgentJobs {
		problems = append(problems, fmt.Sprintf("jobs: send 1 to %d at a time", maxAgentJobs))
	}
	names := make([]string, 0, len(srcs))
	for n := range srcs {
		names = append(names, n)
	}
	slices.Sort(names)
	refs := make([]*boardRef, len(jobs))
	for i := range jobs {
		j := &jobs[i]
		field := fmt.Sprintf("jobs[%d]", i)
		bad := func(name, msg string) { problems = append(problems, field+"."+name+": "+msg) }
		j.URL, j.Company, j.Title = strings.TrimSpace(j.URL), strings.TrimSpace(j.Company), strings.TrimSpace(j.Title)
		if n := utf8.RuneCountInString(j.Company); n == 0 || n > maxAgentNameLen || dedupe.CompanyKey(j.Company) == "" {
			bad("company", fmt.Sprintf("the employer's name, 1 to %d characters", maxAgentNameLen))
		}
		if j.URL != "" {
			u, err := url.Parse(j.URL)
			switch {
			case len(j.URL) > maxAgentURLLen:
				bad("url", fmt.Sprintf("at most %d characters", maxAgentURLLen))
			case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
				bad("url", "an http or https link")
			default:
				for _, n := range names {
					if ref, ok := srcs[n].MatchURL(u); ok && ref.Slug != "" && ref.ExternalID != "" {
						refs[i] = &boardRef{srcs[n], ref}
						break
					}
				}
			}
		}
		if refs[i] == nil && j.Title == "" {
			bad("title", "required unless url is a Greenhouse, Lever or Ashby posting")
		}
		for name, v := range map[string]string{"title": j.Title, "location": j.Location, "pay": j.Pay, "employment_type": j.EmploymentType} {
			if utf8.RuneCountInString(v) > maxAgentFieldLen {
				bad(name, fmt.Sprintf("at most %d characters", maxAgentFieldLen))
			}
		}
		if utf8.RuneCountInString(j.Description) > maxAgentDescLen {
			bad("description", fmt.Sprintf("at most %d characters", maxAgentDescLen))
		}
		if utf8.RuneCountInString(j.Notes) > maxNotesLen {
			bad("notes", fmt.Sprintf("at most %d characters", maxNotesLen))
		}
		switch {
		case j.Status == "" && j.AppliedAt != "":
			bad("applied_at", "goes with status, for an application made elsewhere")
		case j.Status == "":
		case !slices.Contains(ElsewhereStatuses, j.Status):
			bad("status", fmt.Sprintf("%q must be one of %s", j.Status, strings.Join(ElsewhereStatuses, ", ")))
		case j.AppliedAt == "":
			j.appliedAt = utc.Now()
		default:
			t, err := utc.Parse(j.AppliedAt)
			switch {
			case err != nil:
				bad("applied_at", err.Error())
			case t.After(utc.Now().Add(futureAllowance)):
				bad("applied_at", "is in the future")
			default:
				j.appliedAt = t
			}
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, &InputError{Problems: problems}
	}
	return refs, nil
}

// fetchPosting reads one posting from its board: from the detail endpoint
// when the source has one, otherwise from the board's list.
func fetchPosting(ctx context.Context, src sources.Source, b sources.Board, id string) (sources.Posting, error) {
	if d, ok := src.(sources.Detailer); ok {
		p, err := d.Detail(ctx, b, id)
		if err != nil {
			return p, fmt.Errorf("reading the posting from %s: %w", src.Name(), err)
		}
		return p, nil
	}
	res, err := src.List(ctx, b, "")
	if errors.Is(err, sources.ErrBoardNotFound) {
		return sources.Posting{}, fmt.Errorf("%s has no board %q", src.Name(), b.Slug)
	}
	if err != nil {
		return sources.Posting{}, fmt.Errorf("reading the %s board: %w", src.Name(), err)
	}
	for _, p := range res.Postings {
		if p.ExternalID == id {
			return p, nil
		}
	}
	return sources.Posting{}, fmt.Errorf("the %s board doesn't list this posting; it may have closed", src.Name())
}

// agentPosting builds a posting from an agent's fields. Its id comes from
// the link, or from the fields when there is none, so adding the same job
// twice updates one posting.
func agentPosting(j AgentJob) store.PostingInput {
	key := dedupe.CanonicalURL(j.URL)
	if key == "" {
		key = strings.Join([]string{dedupe.CompanyKey(j.Company), dedupe.TitleKey(j.Title), j.Location, j.Description}, "\x00")
	}
	sum := sha256.Sum256([]byte(key))
	id := hex.EncodeToString(sum[:12])
	return normalizePosting(agentPostingSource+":"+id, agentPostingSource, sources.Posting{
		ExternalID: id, Title: j.Title, LocationRaw: j.Location, URL: j.URL,
		PayRaw: j.Pay, EmploymentType: j.EmploymentType, DescriptionText: j.Description,
	})
}
