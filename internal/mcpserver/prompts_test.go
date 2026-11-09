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

	// Screening ends by re-ranking the shortlist.
	steps := regexp.MustCompile(`(?m)^\d+\. .*$`).FindAllString(pb.Text, -1)
	if len(steps) == 0 || !strings.Contains(steps[len(steps)-1], "`set_shortlist`") {
		t.Errorf("screen_new_jobs' last step doesn't name set_shortlist: %q", steps)
	}

	// The interview is a prompt of its own, and get_playbook returns it.
	if _, err := h.cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "interview_me"}); err != nil {
		t.Errorf("interview_me prompt: %v", err)
	}
	h.call("get_playbook", map[string]any{"name": "interview_me"}, &pb)
	if !strings.HasPrefix(pb.Text, "# Interview me") {
		t.Errorf("get_playbook interview_me: %.80s", pb.Text)
	}
}

// TestInterviewPlaybook checks the interview reads what's stored first,
// asks about the person before the resume, and covers what the spec asks
// of it.
func TestInterviewPlaybook(t *testing.T) {
	p, _ := playbooks.Get("interview_me")
	text := p.Text(nil)
	at := func(s string) int {
		t.Helper()
		i := strings.Index(text, s)
		if i < 0 {
			t.Errorf("interview_me doesn't say %q", s)
		}
		return i
	}
	if ctx, open, resume := at("`get_context`"), at("what's making them look"), at("`set_resume_text`"); !(ctx < open && open < resume) {
		t.Errorf("interview_me's order: get_context at %d, opening questions at %d, set_resume_text at %d", ctx, open, resume)
	}
	for _, s := range []string{
		"`remember`", "`update_profile`",
		// the topics
		"Why now, and how urgent", "Target roles and level", "Location and work arrangement", "| Pay |",
		"Must-haves and deal-breakers", "Trade-offs", "Strengths and skill depth", "hands-on, can-review or exposure only",
		"Highlights", "Gaps and limits", "second-choice lane", "Who shouldn't know", "confidential", "What to flag",
		// being realistic
		"Say so when the user is reaching", "Name the requirement and what's missing", "Don't invent statistics",
		"Give guidance, not just a verdict", "don't argue it again", "basis inferred",
		// other documents
		"Performance reviews", "other people's names", "Extract, don't store", "basis inferred until the user confirms it",
		// contradictions
		"Show the user both versions and ask which is right", "leave the claim out of the profile",
		// LinkedIn
		"Save to PDF", "paste the text", "Never visit, fetch or log in to LinkedIn", "never claims more than they said",
		// borderline roles
		"made-up roles",
	} {
		at(s)
	}
}

func TestInitializePlaybook(t *testing.T) {
	p, _ := playbooks.Get("initialize")
	text := p.Text(nil)
	for _, s := range []string{
		"name interview_me", "rerun mode", "the next step `get_setup_state` lists", "stopping point",
		"status filtered_out", "dropped by a single rule",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("initialize doesn't say %q", s)
		}
	}
	// The topics live only in the interview playbook.
	for _, s := range []string{"Highlights", "deal-breakers", "claim_limit"} {
		if strings.Contains(text, s) {
			t.Errorf("initialize still has the interview's %q", s)
		}
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
