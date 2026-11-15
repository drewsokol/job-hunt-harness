package core

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// Story limits (docs/specs/0003-interview-prep.md).
const (
	maxStoryTitle   = 120
	maxStorySummary = 600
	maxStoryBody    = 4_000
	maxStoryClaims  = 10
	maxStoryClaim   = 200
	maxStoryTags    = 10
	maxStoryTag     = 40
	maxStoryNote    = 300
	storyPage       = 50
)

// StoryInput is a story, or a change to one, as the agent gives it. nil
// fields are left alone on a change; Claims and Tags replace the whole
// list.
type StoryInput struct {
	Title, Summary, Body *string
	Claims, Tags         []string
	Archive              string // archive the story, with this reason
}

// StorySaved is a saved story and the active claim_limit and gap memories
// that share words with its claims, for the agent to check before the
// user approves.
type StorySaved struct {
	ID      int64
	New     bool
	Related []store.Memory
}

// SaveStory adds a story (id 0), changes one, or archives one.
func SaveStory(ctx context.Context, s *store.Store, by store.Actor, id int64, in StoryInput) (StorySaved, error) {
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if in.Archive = strings.TrimSpace(in.Archive); in.Archive != "" {
		switch {
		case id == 0:
			bad("archive: give the story_id of the story to archive")
		case in.Title != nil || in.Summary != nil || in.Body != nil || in.Claims != nil || in.Tags != nil:
			bad("archive: archives the story as it is; leave out the other fields")
		case utf8.RuneCountInString(in.Archive) > maxReasonLen:
			bad("archive: say why in a sentence (up to %d characters)", maxReasonLen)
		}
		if len(problems) > 0 {
			return StorySaved{}, &InputError{Problems: problems}
		}
		err := s.ArchiveStory(ctx, by, id, in.Archive)
		return StorySaved{ID: id}, inputIfNotFound(err, fmt.Sprintf("story_id: no story %d", id))
	}

	text := func(name string, v *string, limit int) *string {
		if v == nil {
			if id == 0 {
				bad("%s: needed for a new story", name)
			}
			return nil
		}
		t := strings.TrimSpace(*v)
		if n := utf8.RuneCountInString(t); n == 0 || n > limit {
			bad("%s: 1 to %d characters", name, limit)
		}
		return &t
	}
	title, summary, body := text("title", in.Title, maxStoryTitle), text("summary", in.Summary, maxStorySummary), text("body", in.Body, maxStoryBody)
	var claims, tags []string
	if in.Claims != nil || id == 0 {
		claims = []string{}
		if len(in.Claims) == 0 {
			bad("claims: at least one, each a short sentence of what the story asserts about the user")
		}
		if len(in.Claims) > maxStoryClaims {
			bad("claims: at most %d", maxStoryClaims)
		}
		for i, c := range in.Claims {
			c = strings.TrimSpace(c)
			if n := utf8.RuneCountInString(c); n == 0 || n > maxStoryClaim {
				bad("claims[%d]: 1 to %d characters", i, maxStoryClaim)
			}
			claims = append(claims, c)
		}
	}
	if in.Tags != nil {
		tags = []string{}
		if len(in.Tags) > maxStoryTags {
			bad("tags: at most %d", maxStoryTags)
		}
		for i, t := range in.Tags {
			t = strings.ToLower(strings.TrimSpace(t))
			if n := utf8.RuneCountInString(t); n == 0 || n > maxStoryTag {
				bad("tags[%d]: 1 to %d characters", i, maxStoryTag)
			}
			if !slices.Contains(tags, t) {
				tags = append(tags, t)
			}
		}
	}
	if id != 0 && title == nil && summary == nil && body == nil && claims == nil && tags == nil {
		bad("nothing to change: give the fields to change with story_id, or archive with a reason")
	}
	if len(problems) > 0 {
		return StorySaved{}, &InputError{Problems: problems}
	}

	saved := StorySaved{ID: id, New: id == 0}
	if id == 0 {
		var err error
		if saved.ID, err = s.AddStory(ctx, by, store.Story{Title: *title, Summary: *summary, Body: *body, Claims: claims, Tags: tags}); err != nil {
			return StorySaved{}, err
		}
	} else {
		c := store.StoryChange{Title: title, Summary: summary, Body: body}
		if claims != nil {
			c.Claims = &claims
		}
		if tags != nil {
			c.Tags = &tags
		}
		if err := s.UpdateStory(ctx, by, id, c); err != nil {
			return StorySaved{}, inputIfNotFound(err, fmt.Sprintf("story_id: no story %d", id))
		}
		if claims == nil {
			st, err := s.GetStory(ctx, id)
			if err != nil {
				return StorySaved{}, err
			}
			claims = st.Claims
		}
	}
	var err error
	saved.Related, err = ClaimChecks(ctx, s, claims)
	return saved, err
}

// ClaimChecks returns the active claim_limit and gap memories that share a
// significant word with any of the claims. It errs towards showing more:
// the agent and the user decide whether a claim goes too far.
func ClaimChecks(ctx context.Context, s *store.Store, claims []string) ([]store.Memory, error) {
	words := map[string]bool{}
	for _, c := range claims {
		for w := range significantWords(c) {
			if !commonWords[w] {
				words[w] = true
			}
		}
	}
	var out []store.Memory
	for _, kind := range []string{"claim_limit", "gap"} {
		ms, err := s.Memories(ctx, store.MemoryQuery{Kind: kind})
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			for w := range significantWords(m.Content) {
				if words[w] {
					out = append(out, m)
					break
				}
			}
		}
	}
	return out, nil
}

// commonWords are words too common to link a claim to a memory.
var commonWords = map[string]bool{}

func init() {
	for w := range strings.FieldsSeq("about also been could does from have into just more most only over than that their them then there they this were what when which while with would your") {
		commonWords[w] = true
	}
}

// StoryFilter is what recall's stories mode takes.
type StoryFilter struct {
	Query, Tag      string
	IncludeArchived bool
	Cursor          string
}

// StoryPage is a page of stories, each with its uses.
type StoryPage struct {
	Stories []store.Story
	Uses    map[int64][]store.StoryUse
	Total   int
	Next    string
}

// ListStories returns a page of stories, oldest first.
func ListStories(ctx context.Context, s *store.Store, f StoryFilter) (StoryPage, error) {
	q := store.StoryQuery{Text: f.Query, Tag: strings.ToLower(strings.TrimSpace(f.Tag)), IncludeArchived: f.IncludeArchived, Limit: storyPage}
	if f.Cursor != "" {
		n, err := strconv.Atoi(f.Cursor)
		if err != nil || n < 0 {
			return StoryPage{}, &InputError{Problems: []string{"cursor: pass the next value from the previous page unchanged"}}
		}
		q.Offset = n
	}
	var (
		p   StoryPage
		err error
	)
	if p.Stories, p.Total, err = s.Stories(ctx, q); err != nil {
		return p, err
	}
	if end := q.Offset + len(p.Stories); end < p.Total {
		p.Next = strconv.Itoa(end)
	}
	ids := make([]int64, 0, len(p.Stories))
	for _, st := range p.Stories {
		ids = append(ids, st.ID)
	}
	p.Uses, err = s.StoryUses(ctx, ids)
	return p, err
}

// GetStory returns one story with every use.
func GetStory(ctx context.Context, s *store.Store, id int64) (store.Story, []store.StoryUse, error) {
	st, err := s.GetStory(ctx, id)
	if err != nil {
		return st, nil, inputIfNotFound(err, fmt.Sprintf("story_id: no story %d", id))
	}
	uses, err := s.StoryUses(ctx, []int64{id})
	return st, uses[id], err
}

// StoryLinkInput is a story in an interview, as the agent gives it.
type StoryLinkInput struct {
	StoryID int64
	Landed  string
	Note    string
}

// checkStoryLinks checks an interview's stories and returns them for the
// store. A story without landed is planned.
func checkStoryLinks(in []StoryLinkInput) ([]store.InterviewStory, []string) {
	var problems []string
	out := []store.InterviewStory{}
	seen := map[int64]bool{}
	for i, x := range in {
		landed := strings.ToLower(strings.TrimSpace(x.Landed))
		if landed == "" {
			landed = "planned"
		}
		note := strings.TrimSpace(x.Note)
		switch {
		case x.StoryID <= 0:
			problems = append(problems, fmt.Sprintf("stories[%d].story_id: needed (recall with stories set lists them)", i))
		case seen[x.StoryID]:
			problems = append(problems, fmt.Sprintf("stories[%d].story_id: story %d is listed twice", i, x.StoryID))
		}
		seen[x.StoryID] = true
		if !slices.Contains(store.StoryLandings, landed) {
			problems = append(problems, fmt.Sprintf("stories[%d].landed: %q must be one of %s", i, landed, strings.Join(store.StoryLandings, ", ")))
		}
		if utf8.RuneCountInString(note) > maxStoryNote {
			problems = append(problems, fmt.Sprintf("stories[%d].note: up to %d characters", i, maxStoryNote))
		}
		out = append(out, store.InterviewStory{StoryID: x.StoryID, Landed: landed, Note: note})
	}
	return out, problems
}
