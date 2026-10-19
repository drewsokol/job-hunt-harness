package mcpserver

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/prefilter"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolTimeout bounds every tool call but those in longTimeouts.
const toolTimeout = 2 * time.Minute

// longTimeouts are the tools that wait on the network.
var longTimeouts = map[string]time.Duration{"refresh_jobs": 10 * time.Minute, "add_jobs": 5 * time.Minute}

// call is what a handler knows about the request beyond its input.
type call struct {
	by store.Actor // the agent, named by its client in the MCP handshake
}

// handler is a tool's logic: decode is done, encode is left to add.
type handler[In any] func(ctx context.Context, c call, in In) (any, error)

// kind says how a tool affects data, for the client's annotations.
type kind int

const (
	reads  kind = iota // changes nothing
	writes             // adds or changes data; never deletes
)

// add registers a tool. Its description comes from descriptions, its input
// schema from In. The result is compact JSON in one text block: no output
// schema and no structured copy, so neither the tool list nor the result
// costs the agent's context twice.
func add[In any](t *tools, s *mcp.Server, name string, k kind, h handler[In]) {
	desc, ok := descriptions[name]
	if !ok {
		panic("mcpserver: no description for tool " + name)
	}
	ann := &mcp.ToolAnnotations{ReadOnlyHint: k == reads, IdempotentHint: k == reads, DestructiveHint: new(false), OpenWorldHint: new(false)}
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc, Annotations: ann},
		func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			ctx, cancel := context.WithTimeout(ctx, cmp.Or(longTimeouts[name], toolTimeout))
			defer cancel()
			out, err := h(ctx, call{by: actor(req)}, in)
			if err != nil {
				return nil, nil, t.toolError(name, err)
			}
			b, err := json.Marshal(out)
			if err != nil {
				return nil, nil, t.toolError(name, err)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
		})
}

// actor names the agent from its client's handshake.
func actor(req *mcp.CallToolRequest) store.Actor {
	name := ""
	if req != nil && req.Session != nil {
		if p := req.Session.InitializeParams(); p != nil && p.ClientInfo != nil {
			name = p.ClientInfo.Name
		}
	}
	return store.Agent(name)
}

// toolError turns an error into what the agent sees. Input problems are
// passed on so the agent can fix its call; anything else is logged and
// reported without detail, since it may contain the user's data.
func (t *tools) toolError(name string, err error) error {
	var (
		ie *core.InputError
		ve *prefilter.ValidationError
		se shownError
	)
	switch {
	case errors.As(err, &ie), errors.As(err, &ve):
		return fmt.Errorf("invalid input: %w", err)
	case errors.As(err, &se):
		return err
	case errors.Is(err, store.ErrNotFound):
		return err
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("jhh took too long to answer; try again, or ask for less at once")
	}
	t.Logger.Error("tool failed", "tool", name, "err", err)
	return errors.New("jhh hit an internal error; the details are in jhh's log")
}

// shownError is an error whose text is safe and useful to show the agent
// as it is, such as a board that couldn't be reached.
type shownError struct{ error }

// timeOrEmpty renders t as UTC RFC 3339, or "" for the zero time.
func timeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return utc.Format(t)
}
