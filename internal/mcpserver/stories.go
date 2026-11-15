package mcpserver

import (
	"context"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// storyOut is a story. Its text is the agent's, written from the user's
// words, so it is returned as is.
type storyOut struct {
	ID            int64         `json:"story_id"`
	Title         string        `json:"title"`
	Summary       string        `json:"summary"`
	Body          string        `json:"body,omitempty"`
	Claims        []string      `json:"claims"`
	Tags          []string      `json:"tags,omitempty"`
	Archived      string        `json:"archived_at,omitempty"`
	ArchiveReason string        `json:"archive_reason,omitempty"`
	CreatedBy     string        `json:"created_by,omitempty"`
	LastChanged   string        `json:"last_changed,omitempty"`
	Uses          []storyUseOut `json:"uses,omitempty"`
}

// storyUseOut is a story in one interview. The company and job title can
// come from a listing, so they are inside untrusted.
type storyUseOut struct {
	InterviewID int64  `json:"interview_id"`
	Stage       string `json:"stage"`
	ScheduledAt string `json:"scheduled_at,omitempty"`
	Job         int64  `json:"job,omitempty"`
	OutreachID  int64  `json:"outreach_id,omitempty"`
	Landed      string `json:"landed"`
	Note        string `json:"note,omitempty"`
	Untrusted   *struct {
		Company  string `json:"company,omitempty"`
		JobTitle string `json:"job_title,omitempty"`
	} `json:"untrusted,omitempty"`
}

// story renders a story; whole adds its body and each use in full, where
// a list shows only how each use landed.
func story(st store.Story, uses []store.StoryUse, whole bool) storyOut {
	out := storyOut{ID: st.ID, Title: st.Title, Summary: st.Summary, Claims: orEmpty(st.Claims), Tags: st.Tags,
		Archived: timeOrEmpty(st.ArchivedAt), ArchiveReason: st.ArchiveReason}
	if whole {
		out.Body, out.CreatedBy, out.LastChanged = st.Body, string(st.CreatedBy), timeOrEmpty(st.UpdatedAt)
	}
	for _, u := range uses {
		v := storyUseOut{InterviewID: u.InterviewID, Stage: u.Stage, Landed: u.Landed}
		if whole {
			v.ScheduledAt, v.Job, v.OutreachID, v.Note = timeOrEmpty(u.ScheduledAt), u.JobID, u.OutreachID, u.Note
			v.Untrusted = &struct {
				Company  string `json:"company,omitempty"`
				JobTitle string `json:"job_title,omitempty"`
			}{u.Company, u.JobTitle}
		}
		out.Uses = append(out.Uses, v)
	}
	return out
}

// recallStories is recall's stories mode: a page of stories, or one whole.
func (t *tools) recallStories(ctx context.Context, id int64, f core.StoryFilter) (any, error) {
	if id != 0 {
		if f != (core.StoryFilter{}) {
			return nil, &core.InputError{Problems: []string{"story_id: give it alone for one story, or leave it out to list them"}}
		}
		st, uses, err := core.GetStory(ctx, t.Store, id)
		if err != nil {
			return nil, err
		}
		return story(st, uses, true), nil
	}
	p, err := core.ListStories(ctx, t.Store, f)
	if err != nil {
		return nil, err
	}
	out := make([]storyOut, 0, len(p.Stories))
	for _, st := range p.Stories {
		out = append(out, story(st, p.Uses[st.ID], false))
	}
	return struct {
		Total   int        `json:"total"`
		Stories []storyOut `json:"stories"`
		Next    string     `json:"next,omitempty"`
	}{p.Total, out, p.Next}, nil
}

func (t *tools) addStoryTools(s *mcp.Server) {
	type saveIn struct {
		StoryID int64    `json:"story_id,omitempty" jsonschema:"a story to change; leave out to add one"`
		Title   *string  `json:"title,omitempty" jsonschema:"a short name"`
		Summary *string  `json:"summary,omitempty" jsonschema:"two or three sentences"`
		Body    *string  `json:"body,omitempty" jsonschema:"the situation, what the user did, and the result with its scale"`
		Claims  []string `json:"claims,omitempty" jsonschema:"what it asserts about the user, one short sentence each; replaces the list"`
		Tags    []string `json:"tags,omitempty" jsonschema:"themes it answers, such as leadership or incident; replaces the list"`
		Archive string   `json:"archive,omitempty" jsonschema:"archive the story instead: why"`
	}
	add(t, s, "save_story", writes, func(ctx context.Context, c call, in saveIn) (any, error) {
		saved, err := core.SaveStory(ctx, t.Store, c.by, in.StoryID, core.StoryInput{Title: in.Title, Summary: in.Summary, Body: in.Body,
			Claims: in.Claims, Tags: in.Tags, Archive: in.Archive})
		if err != nil {
			return nil, err
		}
		type relatedOut struct {
			ID      int64  `json:"id"`
			Kind    string `json:"kind"`
			Content string `json:"content"`
		}
		related := []relatedOut{}
		for _, m := range saved.Related {
			related = append(related, relatedOut{m.ID, m.Kind, m.Content})
		}
		return struct {
			ID      int64        `json:"story_id"`
			New     bool         `json:"new"`
			Related []relatedOut `json:"related"`
		}{saved.ID, saved.New, related}, nil
	})
}
