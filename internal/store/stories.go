package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// StoryLandings are how a story went in an interview: planned (prep chose
// it, not yet told), then well, mixed, poorly or not_used from the debrief.
var StoryLandings = []string{"planned", "well", "mixed", "poorly", "not_used"}

// Story is one of the candidate's career stories, kept so every session
// tells it the same way. The agent writes it from the user's words.
type Story struct {
	ID            int64
	Title         string
	Summary       string
	Body          string
	Claims        []string
	Tags          []string
	ArchivedAt    time.Time // zero while active
	ArchiveReason string
	CreatedAt     time.Time
	CreatedBy     Actor
	UpdatedAt     time.Time
	UpdatedBy     Actor
}

// StoryUse is a story in one interview, and how it landed. Company and
// JobTitle can come from a listing.
type StoryUse struct {
	StoryID     int64
	InterviewID int64
	Stage       string
	ScheduledAt time.Time
	JobID       int64
	OutreachID  int64
	Company     string
	JobTitle    string
	Landed      string
	Note        string
}

// InterviewStory links a story to an interview. Title is filled in when
// read.
type InterviewStory struct {
	StoryID int64
	Title   string
	Landed  string
	Note    string
}

// StoryChange holds the fields UpdateStory changes; nil leaves a field.
type StoryChange struct {
	Title, Summary, Body *string
	Claims, Tags         *[]string
}

// AddStory saves a new story.
func (s *Store) AddStory(ctx context.Context, by Actor, st Story) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		claims, err := jsonText(noStrings(st.Claims))
		if err != nil {
			return err
		}
		tags, err := jsonText(noStrings(st.Tags))
		if err != nil {
			return err
		}
		now := s.stamp()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO stories (title, summary, body, claims, tags, created_at, created_by, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, st.Title, st.Summary, st.Body, claims, tags, now, by, now, by)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "story_added", eventRefs{}, map[string]any{"story": id})
	})
	return id, err
}

// UpdateStory changes a story's fields. The event names the fields that
// changed, never their text.
func (s *Store) UpdateStory(ctx context.Context, by Actor, id int64, c StoryChange) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		old, err := scanStory(tx.QueryRowContext(ctx, "SELECT "+storyColumns+" FROM stories WHERE id = ?", id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("story %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		var (
			sets    []string
			args    []any
			changed []string
		)
		for _, f := range []struct {
			name     string
			now, was *string
		}{{"title", c.Title, &old.Title}, {"summary", c.Summary, &old.Summary}, {"body", c.Body, &old.Body}} {
			if f.now != nil && *f.now != *f.was {
				sets, args, changed = append(sets, f.name+" = ?"), append(args, *f.now), append(changed, f.name)
			}
		}
		for _, f := range []struct {
			name     string
			now, was *[]string
		}{{"claims", c.Claims, &old.Claims}, {"tags", c.Tags, &old.Tags}} {
			if f.now != nil && !slices.Equal(*f.now, *f.was) {
				text, err := jsonText(noStrings(*f.now))
				if err != nil {
					return err
				}
				sets, args, changed = append(sets, f.name+" = ?"), append(args, text), append(changed, f.name)
			}
		}
		if len(sets) == 0 {
			return nil
		}
		now := s.stamp()
		if _, err := tx.ExecContext(ctx, "UPDATE stories SET "+strings.Join(sets, ", ")+", updated_at = ?, updated_by = ? WHERE id = ?",
			append(args, now, by, id)...); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "story_changed", eventRefs{}, map[string]any{"story": id, "changed": changed})
	})
}

// ArchiveStory archives a story with the reason. It stays visible, and
// its uses stay linked. The event leaves out the reason, which may
// describe the story.
func (s *Store) ArchiveStory(ctx context.Context, by Actor, id int64, reason string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		res, err := tx.ExecContext(ctx, `
			UPDATE stories SET archived_at = coalesce(archived_at, ?), archive_reason = ?, updated_at = ?, updated_by = ? WHERE id = ?`,
			now, reason, now, by, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("story %d: %w", id, ErrNotFound)
		}
		return insertEvent(ctx, tx, now, by, "story_archived", eventRefs{}, map[string]any{"story": id})
	})
}

// StoryQuery narrows Stories. Empty fields don't narrow.
type StoryQuery struct {
	// Text matches stories containing every word, in any field, ignoring
	// case.
	Text            string
	Tag             string
	IncludeArchived bool
	Limit, Offset   int // Limit 0 for no limit
}

// Stories returns the stories matching q, oldest first, and how many match
// in all.
func (s *Store) Stories(ctx context.Context, q StoryQuery) ([]Story, int, error) {
	where := []string{"1 = 1"}
	var args []any
	if !q.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if q.Tag != "" {
		where, args = append(where, "EXISTS (SELECT 1 FROM json_each(stories.tags) WHERE value = ?)"), append(args, q.Tag)
	}
	for w := range strings.FieldsSeq(q.Text) {
		where = append(where, `(title || ' ' || summary || ' ' || body || ' ' || claims || ' ' || tags) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscaper.Replace(w)+"%")
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM stories WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	page := ""
	if q.Limit > 0 {
		page = fmt.Sprintf(" LIMIT %d OFFSET %d", q.Limit, max(q.Offset, 0))
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+storyColumns+" FROM stories WHERE "+cond+" ORDER BY id"+page, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Story
	for rows.Next() {
		st, err := scanStory(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, st)
	}
	return out, total, rows.Err()
}

// GetStory returns one story.
func (s *Store) GetStory(ctx context.Context, id int64) (Story, error) {
	st, err := scanStory(s.db.QueryRowContext(ctx, "SELECT "+storyColumns+" FROM stories WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Story{}, fmt.Errorf("story %d: %w", id, ErrNotFound)
	}
	return st, err
}

// StoryUses returns the uses of each of the stories, by story, newest
// interview first.
func (s *Store) StoryUses(ctx context.Context, storyIDs []int64) (map[int64][]StoryUse, error) {
	out := map[int64][]StoryUse{}
	if len(storyIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(storyIDs))
	for _, id := range storyIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT x.story_id, i.id, i.stage, i.scheduled_at, coalesce(i.job_id, 0), coalesce(i.outreach_id, 0),
		       coalesce(c.name, ct.company_name, ''), coalesce(j.title, ''), x.landed, x.note
		FROM interview_stories x
		JOIN interviews i ON i.id = x.interview_id
		LEFT JOIN jobs j ON j.id = i.job_id
		LEFT JOIN companies c ON c.id = j.company_id
		LEFT JOIN outreach o ON o.id = i.outreach_id
		LEFT JOIN contacts ct ON ct.id = o.contact_id
		WHERE x.story_id IN (`+placeholders(len(storyIDs))+`)
		ORDER BY i.scheduled_at DESC, i.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			u  StoryUse
			at sql.NullString
		)
		if err := rows.Scan(&u.StoryID, &u.InterviewID, &u.Stage, &at, &u.JobID, &u.OutreachID, &u.Company, &u.JobTitle, &u.Landed, &u.Note); err != nil {
			return nil, err
		}
		if u.ScheduledAt, err = parseTime(at); err != nil {
			return nil, err
		}
		out[u.StoryID] = append(out[u.StoryID], u)
	}
	return out, rows.Err()
}

// InterviewStories returns the stories linked to each of the interviews,
// by interview, in the order they were given.
func (s *Store) InterviewStories(ctx context.Context, interviewIDs []int64) (map[int64][]InterviewStory, error) {
	out := map[int64][]InterviewStory{}
	if len(interviewIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(interviewIDs))
	for _, id := range interviewIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT x.interview_id, x.story_id, s.title, x.landed, x.note
		FROM interview_stories x JOIN stories s ON s.id = x.story_id
		WHERE x.interview_id IN (`+placeholders(len(interviewIDs))+`)
		ORDER BY x.interview_id, x.rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			ivID int64
			x    InterviewStory
		)
		if err := rows.Scan(&ivID, &x.StoryID, &x.Title, &x.Landed, &x.Note); err != nil {
			return nil, err
		}
		out[ivID] = append(out[ivID], x)
	}
	return out, rows.Err()
}

// setInterviewStories replaces an interview's stories inside the caller's
// transaction, and reports whether anything changed. Every story must
// exist.
func setInterviewStories(ctx context.Context, tx *sql.Tx, interviewID int64, links []InterviewStory) (bool, error) {
	var old []InterviewStory
	rows, err := tx.QueryContext(ctx, "SELECT story_id, landed, note FROM interview_stories WHERE interview_id = ? ORDER BY rowid", interviewID)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var x InterviewStory
		if err := rows.Scan(&x.StoryID, &x.Landed, &x.Note); err != nil {
			rows.Close()
			return false, err
		}
		old = append(old, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, x := range links {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM stories WHERE id = ?", x.StoryID).Scan(&n); err != nil {
			return false, err
		}
		if n == 0 {
			return false, fmt.Errorf("story %d: %w", x.StoryID, ErrNotFound)
		}
	}
	if slices.Equal(old, links) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM interview_stories WHERE interview_id = ?", interviewID); err != nil {
		return false, err
	}
	for _, x := range links {
		if _, err := tx.ExecContext(ctx, "INSERT INTO interview_stories (interview_id, story_id, landed, note) VALUES (?, ?, ?, ?)",
			interviewID, x.StoryID, x.Landed, x.Note); err != nil {
			return false, err
		}
	}
	return true, nil
}

// storyLandings is what an event records about an interview's stories:
// their ids and how they landed, never a note.
func storyLandings(links []InterviewStory) map[string]string {
	out := make(map[string]string, len(links))
	for _, x := range links {
		out[fmt.Sprint(x.StoryID)] = x.Landed
	}
	return out
}

const storyColumns = `id, title, summary, body, claims, tags, archived_at, archive_reason, created_at, created_by, updated_at, updated_by`

func scanStory(row interface{ Scan(...any) error }) (Story, error) {
	var (
		st                        Story
		claims, tags              string
		archived, created, update sql.NullString
		createdBy, updatedBy      string
	)
	if err := row.Scan(&st.ID, &st.Title, &st.Summary, &st.Body, &claims, &tags, &archived, &st.ArchiveReason,
		&created, &createdBy, &update, &updatedBy); err != nil {
		return Story{}, err
	}
	if err := json.Unmarshal([]byte(claims), &st.Claims); err != nil {
		return Story{}, fmt.Errorf("story %d claims: %w", st.ID, err)
	}
	if err := json.Unmarshal([]byte(tags), &st.Tags); err != nil {
		return Story{}, fmt.Errorf("story %d tags: %w", st.ID, err)
	}
	st.CreatedBy, st.UpdatedBy = Actor(createdBy), Actor(updatedBy)
	var err error
	if st.ArchivedAt, err = parseTime(archived); err != nil {
		return Story{}, err
	}
	if st.CreatedAt, err = parseTime(created); err != nil {
		return Story{}, err
	}
	st.UpdatedAt, err = parseTime(update)
	return st, err
}

func noStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
