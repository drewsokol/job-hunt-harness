package mcpserver

import (
	"context"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type untrustedCompanyTitle struct {
	Company string `json:"company"`
	Title   string `json:"title"`
}

// shortlistNews is what whats_new says about the shortlist: how many jobs
// it shows, and how many strong or good jobs in review were screened since
// the last set_shortlist and aren't on it.
type shortlistNews struct {
	Size     int `json:"size"`
	Unranked int `json:"unranked_strong_or_good"`
}

func (t *tools) addShortlistTools(s *mcp.Server) {
	type entryIn struct {
		JobID  int64  `json:"job_id"`
		Reason string `json:"reason" jsonschema:"why now, in your words, at most 120 characters; never quote the listing"`
	}
	type setIn struct {
		Jobs []entryIn `json:"jobs" jsonschema:"in order, first to apply to first; at most 30; empty clears the list"`
	}
	add(t, s, "set_shortlist", writes, func(ctx context.Context, c call, in setIn) (any, error) {
		es := make([]store.ShortlistEntry, 0, len(in.Jobs))
		for _, e := range in.Jobs {
			es = append(es, store.ShortlistEntry{JobID: e.JobID, Reason: e.Reason})
		}
		rows, err := core.SetShortlist(ctx, t.Store, c.by, es)
		if err != nil {
			return nil, err
		}
		type entryOut struct {
			Rank      int                   `json:"rank"`
			Job       int64                 `json:"job"`
			Reason    string                `json:"reason"`
			Untrusted untrustedCompanyTitle `json:"untrusted"`
		}
		out := make([]entryOut, 0, len(rows))
		for _, r := range rows {
			out = append(out, entryOut{r.Shortlist.Rank, r.ID, r.Shortlist.Reason, untrustedCompanyTitle{r.Company, r.Title}})
		}
		return struct {
			Shortlist []entryOut `json:"shortlist"`
		}{out}, nil
	})
}
