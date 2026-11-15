package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestInterviewPrepMigration runs migration 0011 on a database with
// memories (one superseding another) and an interview, and checks none of
// them change, the practice kind is accepted, and an interview's stories
// go with it.
func TestInterviewPrepMigration(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jhh.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 11 {
		t.Fatalf("%d migrations", len(ms))
	}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	if err := migrate(ctx, db, ms[:10], "", now); err != nil {
		t.Fatal(err)
	}
	const at = "2026-11-01T09:00:00.000Z"
	for _, q := range []string{
		`INSERT INTO memories (id, kind, content, basis, evidence, status, created_at, created_by, updated_at, updated_by)
		 VALUES (1, 'claim_limit', 'Led four people, not eight.', 'user_said', 'A made-up interview', 'superseded', '` + at + `', 'agent:x', '` + at + `', 'agent:x')`,
		`INSERT INTO memories (id, kind, content, basis, supersedes_id, created_at, created_by, updated_at, updated_by)
		 VALUES (2, 'claim_limit', 'Led a team of four.', 'user_said', 1, '` + at + `', 'agent:x', '` + at + `', 'user')`,
		`INSERT INTO contacts (id, name, company_name, created_at, created_by, updated_at, updated_by)
		 VALUES (1, 'Alex Example', 'Exampleco', '` + at + `', 'user', '` + at + `', 'user')`,
		`INSERT INTO outreach (id, contact_id, channel, sent_at, created_at, created_by, updated_at, updated_by)
		 VALUES (3, 1, 'email', '` + at + `', '` + at + `', 'user', '` + at + `', 'user')`,
		`INSERT INTO interviews (id, outreach_id, stage, scheduled_at, prep_notes, created_at, created_by, updated_at, updated_by)
		 VALUES (5, 3, 'recruiter_screen', '2026-11-20T15:00:00.000Z', 'Made-up notes.', '` + at + `', 'user', '` + at + `', 'user')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func(q string) string {
		t.Helper()
		var s string
		if err := db.QueryRowContext(ctx, q).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	const memoriesQ = `SELECT group_concat(id || kind || content || basis || evidence || status || coalesce(supersedes_id, '-') || created_at || created_by || updated_at || updated_by, '|') FROM memories`
	const interviewsQ = `SELECT group_concat(id || outreach_id || stage || scheduled_at || prep_notes || status || outcome || updated_at, '|') FROM interviews`
	memories, interviews := snapshot(memoriesQ), snapshot(interviewsQ)

	if err := migrate(ctx, db, ms[:11], "", now); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(memoriesQ); got != memories {
		t.Errorf("memories changed:\n%s\n%s", memories, got)
	}
	if got := snapshot(interviewsQ); got != interviews {
		t.Errorf("interviews changed:\n%s\n%s", interviews, got)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO memories (kind, content, basis, created_at, created_by, updated_at, updated_by)
		VALUES ('practice', 'Answers run long.', 'inferred', '`+at+`', 'agent:x', '`+at+`', 'agent:x')`); err != nil {
		t.Errorf("a practice memory: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO memories (kind, content, basis, created_at, created_by, updated_at, updated_by)
		VALUES ('rumor', 'x', 'inferred', '`+at+`', 'agent:x', '`+at+`', 'agent:x')`); err == nil {
		t.Error("an unknown kind was stored")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO memories (kind, content, basis, evidence_job_id, created_at, created_by, updated_at, updated_by)
		VALUES ('fact', 'x', 'user_said', 99, '`+at+`', 'agent:x', '`+at+`', 'agent:x')`); err == nil {
		t.Error("the rebuilt table lost its check on memories from a listing")
	}

	for _, q := range []string{
		`INSERT INTO stories (id, title, summary, body, claims, tags, created_at, created_by, updated_at, updated_by)
		 VALUES (1, 'Made-up story', 'Short.', 'Long.', '["Led a team of four"]', '["leadership"]', '` + at + `', 'agent:x', '` + at + `', 'agent:x')`,
		`INSERT INTO interview_stories (interview_id, story_id) VALUES (5, 1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO interview_stories (interview_id, story_id) VALUES (5, 1)`); err == nil {
		t.Error("a story was linked to an interview twice")
	}
	if _, err := db.ExecContext(ctx, `UPDATE interview_stories SET landed = 'great'`); err == nil {
		t.Error("an unknown landed value was stored")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM interviews WHERE id = 5`); err != nil {
		t.Fatal(err)
	}
	var links int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM interview_stories").Scan(&links); err != nil || links != 0 {
		t.Errorf("a deleted interview's stories: %d, %v", links, err)
	}
	var on int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil || on != 1 {
		t.Errorf("foreign keys after migrating: %d, %v", on, err)
	}
}
