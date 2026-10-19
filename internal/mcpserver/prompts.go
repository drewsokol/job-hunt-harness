package mcpserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/playbooks"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxScreenLimit caps screen_new_jobs' limit argument.
const maxScreenLimit = 200

// playbookText fills in a playbook, checking its arguments: every one is
// a count today.
func playbookText(name string, args map[string]string) (string, error) {
	p, ok := playbooks.Get(name)
	if !ok {
		return "", &core.InputError{Problems: []string{fmt.Sprintf("name: %q isn't a playbook; use one of %s", name, strings.Join(playbooks.Names(), ", "))}}
	}
	for _, a := range p.Args {
		v := strings.TrimSpace(args[a.Name])
		if n, err := strconv.Atoi(v); v != "" && (err != nil || n < 1 || n > maxScreenLimit) {
			return "", &core.InputError{Problems: []string{fmt.Sprintf("%s: a number from 1 to %d", a.Name, maxScreenLimit)}}
		}
	}
	return p.Text(args), nil
}

func (t *tools) addPlaybooks(s *mcp.Server) {
	for _, p := range playbooks.All {
		prompt := &mcp.Prompt{Name: p.Name, Title: p.Title, Description: p.Description}
		for _, a := range p.Args {
			prompt.Arguments = append(prompt.Arguments, &mcp.PromptArgument{Name: a.Name, Description: a.Description + "; default " + a.Default})
		}
		s.AddPrompt(prompt, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			text, err := playbookText(p.Name, req.Params.Arguments)
			if err != nil {
				return nil, err
			}
			return &mcp.GetPromptResult{
				Description: p.Description,
				Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
			}, nil
		})
	}

	type playbookIn struct {
		Name  string `json:"name" jsonschema:"initialize or screen_new_jobs"`
		Limit int    `json:"limit,omitempty" jsonschema:"screen_new_jobs only: how many jobs to screen; default 30"`
	}
	add(t, s, "get_playbook", reads, func(_ context.Context, _ call, in playbookIn) (any, error) {
		args := map[string]string{}
		if in.Limit != 0 {
			args["limit"] = strconv.Itoa(in.Limit)
		}
		text, err := playbookText(in.Name, args)
		return struct {
			Name string `json:"name"`
			Text string `json:"text"`
		}{in.Name, text}, err
	})
}
