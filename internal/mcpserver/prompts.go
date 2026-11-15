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

// playbookText fills in a playbook, checking its arguments.
func playbookText(name string, args map[string]string) (string, error) {
	p, ok := playbooks.Get(name)
	if !ok {
		return "", &core.InputError{Problems: []string{fmt.Sprintf("name: %q isn't a playbook; use one of %s", name, strings.Join(playbooks.Names(), ", "))}}
	}
	for _, a := range p.Args {
		v := strings.TrimSpace(args[a.Name])
		if !a.Number || v == "" {
			continue
		}
		if n, err := strconv.Atoi(v); err != nil || n < 1 || (a.Max > 0 && n > a.Max) {
			if a.Max > 0 {
				return "", &core.InputError{Problems: []string{fmt.Sprintf("%s: a number from 1 to %d", a.Name, a.Max)}}
			}
			return "", &core.InputError{Problems: []string{fmt.Sprintf("%s: a whole number, such as 12", a.Name)}}
		}
	}
	return p.Text(args), nil
}

func (t *tools) addPlaybooks(s *mcp.Server) {
	for _, p := range playbooks.All {
		prompt := &mcp.Prompt{Name: p.Name, Title: p.Title, Description: p.Description}
		for _, a := range p.Args {
			desc := a.Description
			if a.Default != "" {
				desc += "; default " + a.Default
			}
			prompt.Arguments = append(prompt.Arguments, &mcp.PromptArgument{Name: a.Name, Description: desc})
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
		Name        string `json:"name" jsonschema:"initialize, interview_me, screen_new_jobs or interview_prep"`
		Limit       int    `json:"limit,omitempty" jsonschema:"screen_new_jobs only: how many jobs to screen; default 30"`
		InterviewID int64  `json:"interview_id,omitempty" jsonschema:"interview_prep only: the interview, if known"`
	}
	add(t, s, "get_playbook", reads, func(_ context.Context, _ call, in playbookIn) (any, error) {
		args := map[string]string{}
		if in.Limit != 0 {
			args["limit"] = strconv.Itoa(in.Limit)
		}
		if in.InterviewID != 0 {
			args["interview_id"] = strconv.FormatInt(in.InterviewID, 10)
		}
		text, err := playbookText(in.Name, args)
		return struct {
			Name string `json:"name"`
			Text string `json:"text"`
		}{in.Name, text}, err
	})
}
