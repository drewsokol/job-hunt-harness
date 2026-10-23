package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/prefilter"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type untrustedName struct {
	Name string `json:"name"`
}

// companyRef names a company: its name as given when the user or an agent
// added it, or inside untrusted when search found it in a listing.
type companyRef struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name,omitempty"`
	Untrusted *untrustedName `json:"untrusted,omitempty"`
}

func companyName(id int64, name, origin string) companyRef {
	if origin == "search" {
		return companyRef{ID: id, Untrusted: &untrustedName{name}}
	}
	return companyRef{ID: id, Name: name}
}

type previewExample struct {
	Job       int64              `json:"job"`
	Reasons   []prefilter.Reason `json:"reasons"`
	Untrusted struct {
		Company string `json:"company"`
		Title   string `json:"title"`
	} `json:"untrusted"`
}

type previewOut struct {
	core.FilterPreview
	Dropped []previewExample `json:"newly_dropped_examples,omitempty"`
	Kept    []previewExample `json:"newly_kept_examples,omitempty"`
}

func preview(p core.FilterPreview) previewOut {
	ex := func(js []core.PreviewJob) []previewExample {
		var out []previewExample
		for _, j := range js {
			e := previewExample{Job: j.ID, Reasons: j.Reasons}
			e.Untrusted.Company, e.Untrusted.Title = j.Company, j.Title
			out = append(out, e)
		}
		return out
	}
	return previewOut{p, ex(p.DroppedExamples), ex(p.KeptExamples)}
}

// parseFilters reads filters sent as a JSON object, with the same checks
// as jhh filters set.
func parseFilters(raw map[string]any) (prefilter.Filters, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return prefilter.Filters{}, &core.InputError{Problems: []string{"filters: " + err.Error()}}
	}
	f, err := prefilter.Parse(b)
	var ve *prefilter.ValidationError
	if err != nil && !errors.As(err, &ve) {
		return f, &core.InputError{Problems: []string{"filters: " + strings.TrimPrefix(err.Error(), "invalid filters JSON: ")}}
	}
	return f, err
}

func (t *tools) addManageTools(s *mcp.Server) {
	type refreshIn struct {
		Companies []string `json:"companies,omitempty" jsonschema:"refresh only these companies, by name or board key; default every enabled company and every search"`
	}
	add(t, s, "refresh_jobs", writes, func(ctx context.Context, c call, in refreshIn) (any, error) {
		if !t.refreshing.TryLock() {
			return nil, shownError{errors.New("a refresh is already running; wait for it to finish")}
		}
		defer t.refreshing.Unlock()
		var opts core.RefreshOptions
		if len(in.Companies) > 0 {
			var err error
			if opts.CompanyIDs, err = core.CompanyIDs(ctx, t.Store, in.Companies); err != nil {
				return nil, err
			}
		}
		r := &core.Refresher{Store: t.Store, Sources: t.Sources, Searchers: t.Searchers, Logger: t.Logger, By: c.by}
		res, err := r.Refresh(ctx, opts)
		if err != nil {
			return nil, err
		}
		type fetchError struct {
			Company   int64 `json:"company_id"`
			Untrusted struct {
				Company string `json:"company"`
				Error   string `json:"error"` // may quote the board's response
			} `json:"untrusted"`
		}
		type searchOut struct {
			Source     string   `json:"source"`
			Queries    int      `json:"queries,omitempty"`
			Threads    int      `json:"threads,omitempty"`
			Requests   int      `json:"requests"`
			Postings   int      `json:"postings"`
			Covered    int      `json:"skipped_board_listed"`
			Old        int      `json:"skipped_too_old"`
			Unreadable int      `json:"skipped_unreadable,omitempty"`
			Closed     int      `json:"closed"`
			Incomplete []string `json:"not_read_in_full,omitempty"`
			Stopped    string   `json:"stopped,omitempty"`
		}
		out := struct {
			Companies       int          `json:"companies"`
			Unchanged       int          `json:"unchanged"`
			NewJobs         int          `json:"new_jobs"`
			Closed          int          `json:"closed_jobs"`
			Reposted        int          `json:"reposted_jobs"`
			Filtered        bool         `json:"filtered"`
			NewCandidates   int          `json:"new_candidates"`
			FilteredOut     int          `json:"filtered_out"`
			Descriptions    int          `json:"descriptions_fetched"`
			Deferred        int          `json:"descriptions_left_for_next_refresh"`
			ContentFlags    int          `json:"content_flags"`
			SuspectFeeds    int          `json:"suspect_feeds"`
			Search          []searchOut  `json:"search,omitempty"`
			Errors          []fetchError `json:"errors,omitempty"`
			DurationSeconds float64      `json:"duration_seconds"`
			Next            string       `json:"next"`
		}{
			Companies: res.Companies, Unchanged: res.NotModified, NewJobs: len(res.NewJobs), Closed: len(res.ClosedJobs),
			Reposted: len(res.RepostedJobs), Filtered: res.Filtered, NewCandidates: len(res.Passed), FilteredOut: len(res.FilteredOut),
			Descriptions: res.Details, Deferred: res.Deferred, ContentFlags: res.Flags, SuspectFeeds: len(res.Suspect),
			DurationSeconds: res.Duration.Round(100 * time.Millisecond).Seconds(),
			Next:            "Screen the new candidates: list_candidates with status unscreened.",
		}
		if !res.Filtered {
			out.Next = "No filters are set, so nothing was judged or searched. Write filters with the user, then refresh again."
		}
		for _, sr := range res.Searches {
			out.Search = append(out.Search, searchOut{sr.Source, sr.Queries, sr.Threads, sr.Requests, sr.Postings, sr.Covered, sr.Old,
				sr.Unreadable, sr.Closed, sr.Incomplete, sr.Stopped})
		}
		slices.SortFunc(res.Errors, func(a, b store.FetchError) int { return strings.Compare(a.Company, b.Company) })
		for _, e := range res.Errors {
			fe := fetchError{Company: e.CompanyID}
			fe.Untrusted.Company, fe.Untrusted.Error = e.Company, e.Error
			out.Errors = append(out.Errors, fe)
		}
		return out, nil
	})

	type previewIn struct {
		Filters map[string]any `json:"filters" jsonschema:"the whole filters object, as get_context returns it"`
	}
	add(t, s, "preview_filters", reads, func(ctx context.Context, _ call, in previewIn) (any, error) {
		f, err := parseFilters(in.Filters)
		if err != nil {
			return nil, err
		}
		p, err := core.PreviewFilters(ctx, t.Store, f)
		if err != nil {
			return nil, err
		}
		return preview(p), nil
	})

	type setIn struct {
		Filters   map[string]any `json:"filters" jsonschema:"the whole filters object; it replaces the active one"`
		Rationale string         `json:"rationale" jsonschema:"one sentence: why the filters are changing"`
	}
	add(t, s, "set_filters", writes, func(ctx context.Context, c call, in setIn) (any, error) {
		f, err := parseFilters(in.Filters)
		if err != nil {
			return nil, err
		}
		version, p, o, err := core.SetFilters(ctx, t.Store, c.by, f, in.Rationale)
		if err != nil {
			return nil, err
		}
		return struct {
			Version     int64      `json:"version"`
			Effect      previewOut `json:"effect"`
			Passed      int        `json:"moved_to_unscreened"`
			FilteredOut int        `json:"moved_to_filtered_out"`
			Waiting     int        `json:"waiting_for_description"`
		}{version, preview(p), len(o.Passed), len(o.FilteredOut), len(o.Waiting)}, nil
	})

	type companiesIn struct {
		Query string `json:"query,omitempty" jsonschema:"only companies whose name or board contains this"`
	}
	add(t, s, "list_companies", reads, func(ctx context.Context, _ call, in companiesIn) (any, error) {
		cs, err := t.Store.Companies(ctx)
		if err != nil {
			return nil, err
		}
		type companyOut struct {
			companyRef
			Board        string `json:"board,omitempty"`
			Enabled      *bool  `json:"enabled,omitempty"` // only for a board
			Origin       string `json:"origin"`
			OpenPostings int    `json:"open_postings"`
			LastFetch    string `json:"last_fetch,omitempty"`
			Failing      bool   `json:"last_fetch_failed,omitempty"`
			Suspect      bool   `json:"suspect,omitempty"`
		}
		out := make([]companyOut, 0, len(cs))
		q := strings.ToLower(strings.TrimSpace(in.Query))
		for _, c := range cs {
			if q != "" && !strings.Contains(strings.ToLower(c.Name), q) && !strings.Contains(c.BoardKey, q) {
				continue
			}
			co := companyOut{companyName(c.ID, c.Name, c.Origin), c.BoardKey, nil, c.Origin,
				c.OpenPostings, timeOrEmpty(c.LastFetchAt), c.LastError != "", c.Suspect}
			if c.BoardKey != "" {
				co.Enabled = &c.Enabled
			}
			out = append(out, co)
		}
		return struct {
			Companies []companyOut `json:"companies"`
		}{out}, nil
	})

	type addCompanyIn struct {
		Name  string `json:"name"`
		Where string `json:"where,omitempty" jsonschema:"a board slug, source:slug (greenhouse:exampleco) or a link to one of the company's postings; default: guess slugs from the name"`
	}
	add(t, s, "add_company", writes, func(ctx context.Context, c call, in addCompanyIn) (any, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" || len(name) > 200 {
			return nil, &core.InputError{Problems: []string{"name: the company's name, 1 to 200 characters"}}
		}
		boards, err := core.FindBoards(ctx, t.Sources, name, in.Where)
		if err != nil {
			return nil, shownError{err}
		}
		type boardOut struct {
			Board    string `json:"board"`
			Postings int    `json:"open_postings"`
		}
		type result struct {
			Added     bool       `json:"added"`
			CompanyID int64      `json:"company_id,omitempty"`
			Boards    []boardOut `json:"boards"`
			Note      string     `json:"note"`
		}
		res := result{Boards: []boardOut{}}
		for _, b := range boards {
			res.Boards = append(res.Boards, boardOut{b.Key(), b.Postings})
		}
		switch len(boards) {
		case 0:
			res.Note = "No Greenhouse, Lever or Ashby board found. Give the board's slug or a link to one of the company's postings; if it uses another system, add its jobs with add_jobs instead."
			return res, nil
		case 1:
		default:
			res.Note = "More than one board matches. Ask the user which is the company's, and call again with where set to it."
			return res, nil
		}
		res.CompanyID, err = core.AddCompany(ctx, t.Store, c.by, name, boards[0], "agent")
		switch {
		case errors.Is(err, core.ErrCompanyExists):
			res.Note = "jhh already reads this board."
		case err != nil:
			return nil, err
		default:
			res.Added = true
			res.Note = "Added. Its jobs are fetched on the next refresh_jobs."
		}
		return res, nil
	})

	type enabledIn struct {
		CompanyID int64 `json:"company_id"`
		Enabled   bool  `json:"enabled"`
	}
	add(t, s, "set_company_enabled", writes, func(ctx context.Context, c call, in enabledIn) (any, error) {
		cm, err := t.Store.Company(ctx, in.CompanyID)
		if err != nil {
			return nil, err
		}
		if cm.Source == "none" {
			return nil, &core.InputError{Problems: []string{"company_id: this company has no board to read; search found it"}}
		}
		return ok(t.Store.SetCompanyEnabled(ctx, c.by, in.CompanyID, in.Enabled))
	})
}
