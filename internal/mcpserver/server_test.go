package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// harness is a jhh server on a temporary database and a client connected
// to it in memory, named "test-agent".
type harness struct {
	t  *testing.T
	s  *store.Store
	cs *mcp.ClientSession
}

func newHarness(t *testing.T, d Deps) *harness {
	t.Helper()
	ctx := context.Background()
	if d.Store == nil {
		s, err := store.Open(ctx, filepath.Join(t.TempDir(), "jhh.db"), store.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		d.Store = s
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := New(d).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &harness{t: t, s: d.Store, cs: cs}
}

// call calls a tool, fails the test on a tool error, and decodes the
// result into out (when out isn't nil).
func (h *harness) call(name string, args any, out any) {
	h.t.Helper()
	text, isErr := h.raw(name, args)
	if isErr {
		h.t.Fatalf("%s: tool error: %s", name, text)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			h.t.Fatalf("%s: decoding %s: %v", name, text, err)
		}
	}
}

// fail calls a tool that must fail and returns its error text.
func (h *harness) fail(name string, args any) string {
	h.t.Helper()
	text, isErr := h.raw(name, args)
	if !isErr {
		h.t.Fatalf("%s: succeeded with %s; want an error", name, text)
	}
	return text
}

func (h *harness) raw(name string, args any) (string, bool) {
	h.t.Helper()
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.StructuredContent != nil {
		h.t.Errorf("%s: sent structured content as well as text", name)
	}
	return b.String(), res.IsError
}

func TestEveryToolHasADescriptionAndNoOutputSchema(t *testing.T) {
	h := newHarness(t, Deps{})
	res, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != len(descriptions) {
		t.Errorf("%d tools registered, %d descriptions", len(res.Tools), len(descriptions))
	}
	for _, tool := range res.Tools {
		if tool.OutputSchema != nil {
			t.Errorf("%s has an output schema", tool.Name)
		}
		if tool.Annotations == nil {
			t.Errorf("%s has no annotations", tool.Name)
		}
	}
}

func TestProfileAndMemories(t *testing.T) {
	h := newHarness(t, Deps{})

	var setup struct {
		Now       string   `json:"now"`
		NextSteps []string `json:"next_steps"`
	}
	h.call("get_setup_state", nil, &setup)
	if !strings.HasSuffix(setup.Now, "Z") || len(setup.NextSteps) < 4 || !strings.HasPrefix(setup.NextSteps[0], "resume:") {
		t.Errorf("setup state %+v", setup)
	}

	var saved struct {
		Version int64  `json:"version"`
		Diff    string `json:"diff"`
	}
	h.call("update_profile", map[string]any{
		"markdown": "## Summary\n\nA made-up backend engineer.\n\n## Highlights\n\n- Built a made-up queue.\n",
		"summary":  "First draft",
	}, &saved)
	if saved.Version != 1 || !strings.Contains(saved.Diff, "+ A made-up backend engineer.") {
		t.Errorf("first save: %+v", saved)
	}
	h.call("update_profile", map[string]any{
		"heading": "Highlights", "content": "- Led a made-up migration.", "mode": "append", "summary": "Added the migration",
	}, &saved)
	if saved.Version != 2 || saved.Diff != "+ - Led a made-up migration.\n" {
		t.Errorf("section edit: %+v", saved)
	}
	if msg := h.fail("update_profile", map[string]any{"heading": "Gaps", "content": "x", "mode": "merge", "summary": "s"}); !strings.Contains(msg, `mode: "merge" must be replace or append`) {
		t.Errorf("bad mode: %s", msg)
	}

	var mem struct {
		ID      int64             `json:"id"`
		Related []json.RawMessage `json:"related"`
	}
	h.call("remember", map[string]any{"content": "Won't relocate for a job.", "kind": "constraint", "basis": "user_said"}, &mem)
	h.call("remember", map[string]any{"content": "Would not relocate, even for a strong role.", "kind": "constraint", "basis": "user_said"}, &mem)
	if len(mem.Related) != 1 {
		t.Errorf("related memories: %s", mem.Related)
	}
	if msg := h.fail("remember", map[string]any{"content": "x", "kind": "hunch", "basis": "user_said"}); !strings.Contains(msg, `kind: "hunch" must be one of`) {
		t.Errorf("bad kind: %s", msg)
	}
	h.call("update_memory", map[string]any{"id": mem.ID, "archive": "Repeats memory 1."}, nil)

	var ctxOut struct {
		Profile struct {
			Version   int64  `json:"version"`
			UpdatedBy string `json:"updated_by"`
		} `json:"profile"`
		Memories map[string][]struct {
			Content string `json:"content"`
		} `json:"memories"`
	}
	h.call("get_context", nil, &ctxOut)
	if ctxOut.Profile.Version != 2 || ctxOut.Profile.UpdatedBy != "agent:test-agent" || len(ctxOut.Memories["constraint"]) != 1 {
		t.Errorf("context %+v", ctxOut)
	}

	var history struct {
		Versions []struct {
			Version int64  `json:"version"`
			By      string `json:"by"`
		} `json:"versions"`
	}
	h.call("get_profile_history", map[string]any{"limit": 1}, &history)
	if len(history.Versions) != 1 || history.Versions[0].Version != 2 {
		t.Errorf("history %+v", history)
	}
	if msg := h.fail("set_resume_text", map[string]any{"text": "made-up resume", "source": "upload"}); !strings.Contains(msg, "source:") {
		t.Errorf("bad source: %s", msg)
	}
	if msg := h.fail("update_memory", map[string]any{"id": 999, "archive": "gone"}); !strings.Contains(msg, "no memory 999") {
		t.Errorf("missing memory: %s", msg)
	}
	if msg := h.fail("remember", map[string]any{"content": "x", "kind": "fact", "basis": "user_said", "colour": "red"}); !strings.Contains(msg, "colour") {
		t.Errorf("unknown field: %s", msg)
	}
}
