package mcpserver

import (
	"context"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// refIn is a link to the email thread about something, as the agent gives
// it on a save tool's add_refs.
type refIn struct {
	System string `json:"system" jsonschema:"where the mail lives, such as gmail or outlook"`
	Ref    string `json:"ref" jsonschema:"the system's id for the thread"`
	URL    string `json:"url,omitempty" jsonschema:"an https link that opens the thread"`
}

func refInputs(rs []refIn) []core.RefInput {
	out := make([]core.RefInput, 0, len(rs))
	for _, r := range rs {
		out = append(out, core.RefInput{System: r.System, Ref: r.Ref, URL: r.URL})
	}
	return out
}

// refOut is a stored link. The ids and link are the agent's record,
// returned as given.
type refOut struct {
	ID     int64  `json:"id"`
	System string `json:"system"`
	Ref    string `json:"ref"`
	URL    string `json:"url,omitempty"`
}

func refsOut(rs []store.Ref) []refOut {
	if len(rs) == 0 {
		return nil
	}
	out := make([]refOut, 0, len(rs))
	for _, r := range rs {
		out = append(out, refOut{r.ID, r.System, r.Ref, r.URL})
	}
	return out
}

// handledOut is a message the agent already handled.
type handledOut struct {
	MessageID string `json:"message_id"`
	Action    string `json:"action"`
	Job       int64  `json:"job,omitempty"`
	Outreach  int64  `json:"outreach_id,omitempty"`
	Interview int64  `json:"interview_id,omitempty"`
	At        string `json:"handled_at"`
	By        string `json:"handled_by"`
}

func handled(ms []store.HandledMessage) []handledOut {
	out := make([]handledOut, 0, len(ms))
	for _, m := range ms {
		out = append(out, handledOut{m.MessageID, m.Action, m.JobID, m.OutreachID, m.InterviewID, timeOrEmpty(m.HandledAt), string(m.HandledBy)})
	}
	return out
}

func (t *tools) addMailTools(s *mcp.Server) {
	type markIn struct {
		MessageID  string `json:"message_id"`
		Action     string `json:"action" jsonschema:"what you did, in one line, such as marked the job rejected"`
		JobID      int64  `json:"job_id,omitempty"`
		OutreachID int64  `json:"outreach_id,omitempty"`
		Interview  int64  `json:"interview_id,omitempty"`
	}
	type handledIn struct {
		System         string   `json:"system" jsonschema:"where the mail lives, such as gmail or outlook"`
		Check          []string `json:"check,omitempty" jsonschema:"message ids (not thread ids) to look up, up to 100"`
		Mark           []markIn `json:"mark,omitempty" jsonschema:"messages you handled, up to 100"`
		CheckedThrough string   `json:"checked_through,omitempty" jsonschema:"UTC, RFC 3339: you have read everything up to this time"`
	}
	add(t, s, "handled_messages", writes, func(ctx context.Context, c call, in handledIn) (any, error) {
		marks := make([]core.MarkInput, 0, len(in.Mark))
		for _, m := range in.Mark {
			marks = append(marks, core.MarkInput{MessageID: m.MessageID, Action: m.Action, JobID: m.JobID, OutreachID: m.OutreachID, InterviewID: m.Interview})
		}
		r, err := core.HandledMessages(ctx, t.Store, c.by, core.HandledInput{System: in.System, Check: in.Check, Mark: marks, CheckedThrough: in.CheckedThrough})
		if err != nil {
			return nil, err
		}
		return struct {
			Handled        []handledOut `json:"handled"`
			Marked         int          `json:"marked"`
			AlreadyHandled []handledOut `json:"already_handled,omitempty"`
			CheckedThrough string       `json:"checked_through,omitempty"`
		}{handled(r.Handled), r.Marked, handled(r.AlreadyHandled), timeOrEmpty(r.CheckedThrough)}, nil
	})
}
