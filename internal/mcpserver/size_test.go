package mcpserver

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// TestOutputSizes keeps tool output within what an agent's context can
// afford, however long the listings are.
func TestOutputSizes(t *testing.T) {
	var ps []sources.Posting
	for i := range 12 {
		id := strconv.Itoa(i + 1)
		p := posting(id, "Backend Engineer "+id)
		p.DescriptionHTML = "<p>" + strings.Repeat("Exampleco builds made-up software on real systems. ", 2_000) + "</p>"
		ps = append(ps, p)
	}
	src := &board{boards: map[string][]sources.Posting{"exampleco": ps}}
	h := newHarness(t, Deps{Sources: map[string]sources.Source{"fake": src}})
	h.call("set_company", map[string]any{"name": "Exampleco"}, nil)
	var filters map[string]any
	if err := json.Unmarshal([]byte(engineersRemoteInUS), &filters); err != nil {
		t.Fatal(err)
	}
	h.call("set_filters", map[string]any{"filters": filters, "rationale": "Engineers, remote in the US."}, nil)
	h.call("refresh_jobs", map[string]any{}, nil)

	perJob := core.DefaultDescriptionChars + 2_500 // the description, cut, and everything else about the job
	text, _ := h.raw("list_candidates", map[string]any{"include_description": true, "limit": 10})
	if len(text) > 10*perJob {
		t.Errorf("list_candidates with 10 descriptions: %d bytes", len(text))
	}
	var page struct {
		Jobs []struct {
			Job int64 `json:"job"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil || len(page.Jobs) != 10 {
		t.Fatalf("list_candidates: %v, %d jobs", err, len(page.Jobs))
	}
	if text, _ := h.raw("get_job", map[string]any{"job_id": page.Jobs[0].Job}); len(text) > perJob+2_000 {
		t.Errorf("get_job: %d bytes", len(text))
	}
	if text, _ := h.raw("get_job", map[string]any{"job_id": page.Jobs[0].Job, "full_description": true}); len(text) > core.MaxDescriptionChars+5_000 {
		t.Errorf("get_job with the full description: %d bytes", len(text))
	}
	if text, _ := h.raw("list_candidates", map[string]any{"limit": 50}); len(text) > 50*2_500 {
		t.Errorf("list_candidates without descriptions: %d bytes", len(text))
	}

	// Every request carries the tool list, so it has a budget too.
	res, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Tools)
	if len(b) > 40_000 {
		t.Errorf("the tool list is %d bytes", len(b))
	}
	t.Logf("tool list: %d tools, %d bytes", len(res.Tools), len(b))
}
