package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type noInput struct{}

type profileOut struct {
	Version   int64  `json:"version"`
	Markdown  string `json:"markdown"`
	UpdatedBy string `json:"updated_by"`
	UpdatedAt string `json:"updated_at"`
}

type resumeOut struct {
	Version int64  `json:"version"`
	Source  string `json:"source"`
	Text    string `json:"text"`
}

type memoryOut struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind,omitempty"`
	Content     string `json:"content"`
	Basis       string `json:"basis"`
	Evidence    string `json:"evidence,omitempty"`
	FromJob     int64  `json:"from_job,omitempty"`
	Status      string `json:"status,omitempty"` // only when it isn't active
	Supersedes  int64  `json:"supersedes,omitempty"`
	CreatedBy   string `json:"created_by"`
	LastChanged string `json:"last_changed"`
}

func memory(m store.Memory, withKind bool) memoryOut {
	out := memoryOut{
		ID: m.ID, Content: m.Content, Basis: m.Basis, Evidence: m.Evidence, FromJob: m.EvidenceJobID,
		Supersedes: m.SupersedesID, CreatedBy: string(m.CreatedBy), LastChanged: timeOrEmpty(m.UpdatedAt),
	}
	if withKind {
		out.Kind = m.Kind
	}
	if m.Status != "active" {
		out.Status = m.Status
	}
	return out
}

type filtersOut struct {
	Version   int64           `json:"version"`
	Rationale string          `json:"rationale"`
	SetBy     string          `json:"set_by"`
	Filters   json.RawMessage `json:"filters"`
}

type savedProfile struct {
	Version int64  `json:"version"`
	Diff    string `json:"diff"`
}

func (t *tools) addProfileTools(s *mcp.Server) {
	add(t, s, "get_setup_state", reads, func(ctx context.Context, _ call, _ noInput) (any, error) {
		st, err := core.GetSetupState(ctx, t.Store)
		if err != nil {
			return nil, err
		}
		c := st.Counts
		return struct {
			Now            string   `json:"now"`
			TimeZone       string   `json:"time_zone"`
			UTCOffset      string   `json:"utc_offset"`
			ProfileVersion int64    `json:"profile_version"`
			ResumeVersion  int64    `json:"resume_version"`
			FiltersVersion int64    `json:"filters_version"`
			SearchesOn     int      `json:"search_sources_on"`
			Companies      int      `json:"companies"`
			BoardCompanies int      `json:"companies_with_boards"`
			Memories       int      `json:"memories"`
			Inferred       int      `json:"inferred_memories"`
			OpenJobs       int      `json:"open_jobs"`
			Screenings     int      `json:"screenings"`
			LastRefreshAt  string   `json:"last_refresh_at,omitempty"`
			Missing        []string `json:"next_steps"`
		}{
			timeOrEmpty(st.Now), st.TimeZone, st.UTCOffset, st.ProfileVersion, st.ResumeVersion, st.FiltersVersion,
			st.SearchesOn, c.Companies, c.BoardCompanies, c.Memories, c.Inferred, c.Jobs, c.Screenings,
			timeOrEmpty(c.LastRefreshAt), orEmpty(st.Missing),
		}, nil
	})

	add(t, s, "get_context", reads, func(ctx context.Context, _ call, _ noInput) (any, error) {
		sc, err := core.GetContext(ctx, t.Store)
		if err != nil {
			return nil, err
		}
		var out struct {
			Profile  *profileOut            `json:"profile"`
			Resume   *resumeOut             `json:"resume"`
			Memories map[string][]memoryOut `json:"memories"`
			Filters  *filtersOut            `json:"filters"`
		}
		if p := sc.Profile; p != nil {
			out.Profile = &profileOut{p.Version, p.Markdown, string(p.CreatedBy), timeOrEmpty(p.CreatedAt)}
		}
		if r := sc.Resume; r != nil {
			out.Resume = &resumeOut{r.Version, r.Source, r.Text}
		}
		out.Memories = map[string][]memoryOut{}
		for _, m := range sc.Memories {
			out.Memories[m.Kind] = append(out.Memories[m.Kind], memory(m, false))
		}
		if f := sc.Filters; f != nil {
			out.Filters = &filtersOut{f.Version, f.Rationale, string(f.CreatedBy), f.Filters}
		}
		return out, nil
	})

	type updateProfileIn struct {
		Markdown string `json:"markdown" jsonschema:"the whole profile, in markdown"`
		Summary  string `json:"summary" jsonschema:"what changed, in one line"`
	}
	add(t, s, "update_profile", writes, func(ctx context.Context, c call, in updateProfileIn) (any, error) {
		p, err := core.UpdateProfile(ctx, t.Store, c.by, in.Markdown, in.Summary)
		return savedProfile{p.Version, p.Diff}, err
	})

	type editSectionIn struct {
		Heading string `json:"heading" jsonschema:"the section's name, such as Highlights"`
		Content string `json:"content" jsonschema:"the section's text, without its heading"`
		Mode    string `json:"mode" jsonschema:"replace or append"`
		Summary string `json:"summary" jsonschema:"what changed, in one line"`
	}
	add(t, s, "edit_profile_section", writes, func(ctx context.Context, c call, in editSectionIn) (any, error) {
		p, err := core.EditProfileSection(ctx, t.Store, c.by, in.Heading, in.Content, in.Mode, in.Summary)
		return savedProfile{p.Version, p.Diff}, err
	})

	type historyIn struct {
		Limit int `json:"limit,omitempty" jsonschema:"how many versions, newest first (default 5, at most 20)"`
	}
	add(t, s, "get_profile_history", reads, func(ctx context.Context, _ call, in historyIn) (any, error) {
		limit := min(max(in.Limit, 0), 20)
		if limit == 0 {
			limit = 5
		}
		vs, err := t.Store.ProfileHistory(ctx, limit+1) // one more, to diff the oldest shown
		if err != nil {
			return nil, err
		}
		type version struct {
			Version int64  `json:"version"`
			Summary string `json:"summary"`
			By      string `json:"by"`
			At      string `json:"at"`
			Diff    string `json:"diff"`
		}
		out := []version{}
		for i, v := range vs {
			if i == limit {
				break
			}
			prev := ""
			if i+1 < len(vs) {
				prev = vs[i+1].Markdown
			}
			out = append(out, version{v.Version, v.Summary, string(v.CreatedBy), timeOrEmpty(v.CreatedAt), core.Diff(prev, v.Markdown)})
		}
		return struct {
			Versions []version `json:"versions"`
		}{out}, nil
	})

	type resumeIn struct {
		Text   string `json:"text" jsonschema:"the resume as plain text"`
		Source string `json:"source" jsonschema:"pasted or file"`
	}
	add(t, s, "set_resume_text", writes, func(ctx context.Context, c call, in resumeIn) (any, error) {
		v, err := core.SetResumeText(ctx, t.Store, c.by, in.Text, in.Source)
		return struct {
			Version int64 `json:"version"`
		}{v}, err
	})

	type rememberIn struct {
		Content       string `json:"content" jsonschema:"one short statement"`
		Kind          string `json:"kind" jsonschema:"fact, preference, constraint, strength, gap, claim_limit or feedback"`
		Basis         string `json:"basis" jsonschema:"user_said or inferred"`
		Evidence      string `json:"evidence,omitempty" jsonschema:"where it came from, such as 'answered during initialize'"`
		EvidenceJobID int64  `json:"evidence_job_id,omitempty" jsonschema:"the job number, when drawn from a listing"`
		Supersedes    int64  `json:"supersedes,omitempty" jsonschema:"the id of a memory this one replaces"`
	}
	add(t, s, "remember", writes, func(ctx context.Context, c call, in rememberIn) (any, error) {
		r, err := core.Remember(ctx, t.Store, c.by, core.RememberInput{
			Kind: in.Kind, Content: in.Content, Basis: in.Basis, Evidence: in.Evidence,
			EvidenceJobID: in.EvidenceJobID, Supersedes: in.Supersedes,
		})
		if err != nil {
			return nil, err
		}
		related := []memoryOut{}
		for _, m := range r.Related {
			related = append(related, memory(m, true))
		}
		return struct {
			ID      int64       `json:"id"`
			Related []memoryOut `json:"related"`
		}{r.ID, related}, nil
	})

	type recallIn struct {
		Query           string `json:"query,omitempty" jsonschema:"words that must all appear"`
		Kind            string `json:"kind,omitempty"`
		IncludeArchived bool   `json:"include_archived,omitempty"`
	}
	add(t, s, "recall", reads, func(ctx context.Context, _ call, in recallIn) (any, error) {
		ms, err := t.Store.Memories(ctx, store.MemoryQuery{Text: in.Query, Kind: in.Kind, IncludeArchived: in.IncludeArchived})
		if err != nil {
			return nil, err
		}
		out := []memoryOut{}
		for _, m := range ms {
			out = append(out, memory(m, true))
		}
		return struct {
			Memories []memoryOut `json:"memories"`
		}{out}, nil
	})

	type updateMemoryIn struct {
		ID      int64   `json:"id"`
		Content *string `json:"content,omitempty"`
		Kind    *string `json:"kind,omitempty"`
		Basis   *string `json:"basis,omitempty"`
	}
	add(t, s, "update_memory", writes, func(ctx context.Context, c call, in updateMemoryIn) (any, error) {
		err := core.UpdateMemory(ctx, t.Store, c.by, in.ID, core.MemoryEdit{Content: in.Content, Kind: in.Kind, Basis: in.Basis})
		return ok(err)
	})

	type forgetIn struct {
		ID     int64  `json:"id"`
		Reason string `json:"reason" jsonschema:"why it's wrong or no longer true"`
	}
	add(t, s, "forget", writes, func(ctx context.Context, c call, in forgetIn) (any, error) {
		return ok(core.Forget(ctx, t.Store, c.by, in.ID, in.Reason))
	})
}

func ok(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return struct {
		OK bool `json:"ok"`
	}{true}, nil
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
