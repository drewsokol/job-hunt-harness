package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// TestMergedTools checks the rules of tools that took over another's job
// (decision 0010): each mode refuses the other mode's fields.
func TestMergedTools(t *testing.T) {
	src := &board{boards: map[string][]sources.Posting{"exampleco": {posting("1", "Senior Backend Engineer")}}}
	h := newHarness(t, Deps{Sources: map[string]sources.Source{"fake": src}})

	// update_profile: the whole profile, or one section; replace by default.
	h.call("update_profile", map[string]any{"markdown": "## Summary\n\nA made-up engineer.\n", "summary": "First draft"}, nil)
	var saved struct {
		Version int64  `json:"version"`
		Diff    string `json:"diff"`
	}
	h.call("update_profile", map[string]any{"heading": "Summary", "content": "A made-up staff engineer.", "summary": "Level"}, &saved)
	if saved.Version != 2 || !strings.Contains(saved.Diff, "- A made-up engineer.") {
		t.Errorf("a section edit without mode should replace: %+v", saved)
	}
	for _, bad := range []map[string]any{
		{"summary": "s"},
		{"markdown": "## Summary\n\nx\n", "heading": "Summary", "content": "x", "summary": "s"},
		{"markdown": "## Summary\n\nx\n", "content": "x", "summary": "s"},
	} {
		if msg := h.fail("update_profile", bad); msg == "" {
			t.Errorf("update_profile %v: no error", bad)
		}
	}

	// update_memory: archive alone.
	var mem struct {
		ID int64 `json:"id"`
	}
	h.call("remember", map[string]any{"content": "Prefers made-up teams.", "kind": "preference", "basis": "user_said"}, &mem)
	if msg := h.fail("update_memory", map[string]any{"id": mem.ID, "archive": "Wrong.", "content": "x"}); !strings.Contains(msg, "archive") {
		t.Errorf("archive with an edit: %s", msg)
	}
	h.call("update_memory", map[string]any{"id": mem.ID, "archive": "Wrong."}, nil)

	// set_filters: a dry run saves nothing; saving needs a rationale.
	var filters map[string]any
	if err := json.Unmarshal([]byte(engineersRemoteInUS), &filters); err != nil {
		t.Fatal(err)
	}
	h.call("set_filters", map[string]any{"filters": filters, "dry_run": true}, nil)
	var setup struct {
		Filters int64 `json:"filters_version"`
	}
	text, _ := h.raw("get_setup_state", map[string]any{})
	if err := json.Unmarshal([]byte(text), &setup); err != nil || setup.Filters != 0 {
		t.Errorf("a dry run saved filters: %s", text)
	}
	if msg := h.fail("set_filters", map[string]any{"filters": filters}); msg == "" {
		t.Error("saved filters without a rationale")
	}

	// set_company: add by name, then turn reading on and off by id.
	var added struct {
		Added     bool  `json:"added"`
		CompanyID int64 `json:"company_id"`
	}
	h.call("set_company", map[string]any{"name": "Exampleco"}, &added)
	if !added.Added || added.CompanyID == 0 {
		t.Fatalf("adding: %+v", added)
	}
	h.call("set_company", map[string]any{"company_id": added.CompanyID, "enabled": false}, nil)
	for _, bad := range []map[string]any{
		{"company_id": added.CompanyID},
		{"company_id": added.CompanyID, "enabled": true, "name": "Exampleco"},
		{"name": "Made-Up Labs", "enabled": false},
		{},
	} {
		if msg := h.fail("set_company", bad); msg == "" {
			t.Errorf("set_company %v: no error", bad)
		}
	}
}
