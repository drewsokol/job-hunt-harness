package mcpserver

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/playbooks"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPlaybooks(t *testing.T) {
	h := newHarness(t, Deps{})
	ctx := context.Background()
	res, err := h.cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Prompts) != len(playbooks.All) {
		t.Errorf("%d prompts; want %d", len(res.Prompts), len(playbooks.All))
	}
	got, err := h.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "screen_new_jobs", Arguments: map[string]string{"limit": "12"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := got.Messages[0].Content.(*mcp.TextContent).Text; !strings.Contains(text, "Screen up to 12 jobs") {
		t.Errorf("screen_new_jobs: %s", text)
	}
	if _, err := h.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "screen_new_jobs", Arguments: map[string]string{"limit": "all of them"}}); err == nil {
		t.Error("a limit that isn't a number was accepted")
	}

	var pb struct {
		Text string `json:"text"`
	}
	h.call("get_playbook", map[string]any{"name": "screen_new_jobs"}, &pb)
	if !strings.Contains(pb.Text, "Screen up to 30 jobs") {
		t.Errorf("get_playbook default limit: %s", pb.Text)
	}
	if msg := h.fail("get_playbook", map[string]any{"name": "tune"}); !strings.Contains(msg, "isn't a playbook") {
		t.Errorf("unknown playbook: %s", msg)
	}
}

// TestPlaybooksNameRealTools keeps the playbooks and the server
// instructions in step with the tools: every tool they name must exist,
// and every argument must be filled in.
func TestPlaybooksNameRealTools(t *testing.T) {
	toolName := regexp.MustCompile("`([a-z]+(?:_[a-z]+)+)`")
	check := func(where, text string) {
		for _, m := range toolName.FindAllStringSubmatch(text, -1) {
			if _, ok := descriptions[m[1]]; !ok {
				t.Errorf("%s names %s, which isn't a tool", where, m[1])
			}
		}
		if strings.Contains(text, "{{") {
			t.Errorf("%s has an argument left unfilled", where)
		}
	}
	for _, p := range playbooks.All {
		check(p.Name, p.Text(nil))
	}
	for _, word := range regexp.MustCompile(`\b[a-z]+(?:_[a-z]+)+\b`).FindAllString(instructions, -1) {
		if _, tool := descriptions[word]; !tool && !strings.Contains(strings.Join(playbooks.Names(), " "), word) {
			t.Errorf("the instructions name %s, which is neither a tool nor a playbook", word)
		}
	}
}
