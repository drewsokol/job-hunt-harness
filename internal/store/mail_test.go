package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestMailRowsOutliveTheirJob checks what happens to handled messages and
// email links when the job they point to is deleted, and that the events
// for links carry counts only.
func TestMailRowsOutliveTheirJob(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)
	agent := Agent("claude-code")
	company, err := s.AddCompany(ctx, ActorUser, NewCompany{Name: "Example Co", Source: "greenhouse", BoardKey: "greenhouse:exampleco",
		SourceConfig: json.RawMessage(`{"slug":"exampleco"}`), Origin: "user"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.AddJob(ctx, agent, NewJob{CompanyID: company, Title: "Staff Engineer", Origin: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if n, existing, err := s.MarkHandled(ctx, agent, "gmail", []NewHandled{{MessageID: "m1", Action: "marked the job rejected", JobID: job}}); err != nil || n != 1 || len(existing) != 0 {
		t.Fatalf("MarkHandled = %d, %v, %v", n, existing, err)
	}
	if err := s.ChangeRefs(ctx, agent, RefTarget{JobID: job}, []NewRef{{System: "gmail", Ref: "thread-1", URL: "https://mail.example/thread-1"}}, nil); err != nil {
		t.Fatal(err)
	}

	var data string
	if err := s.db.QueryRowContext(ctx, "SELECT data FROM events WHERE kind = 'refs_changed'").Scan(&data); err != nil {
		t.Fatal(err)
	}
	if data != `{"added":1,"removed":0}` || strings.Contains(data, "thread") {
		t.Errorf("event data = %s, want counts only", data)
	}

	if _, err := s.db.ExecContext(ctx, "DELETE FROM events WHERE job_id = ?; DELETE FROM jobs WHERE id = ?", job, job); err != nil {
		t.Fatal(err)
	}
	ms, err := s.HandledMessages(ctx, "gmail", []string{"m1"})
	if err != nil || len(ms) != 1 || ms[0].JobID != 0 || ms[0].Action != "marked the job rejected" {
		t.Errorf("after the job is deleted: %+v, %v; want the row kept without its job", ms, err)
	}
	var refs int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM external_refs").Scan(&refs); err != nil || refs != 0 {
		t.Errorf("%d links left after the job is deleted, %v; want none", refs, err)
	}
}
