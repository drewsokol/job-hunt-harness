package store

import (
	"context"
	"errors"
	"testing"
)

func TestShortlistRows(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)
	company, _ := s.AddCompany(ctx, ActorUser, NewCompany{Name: "Example Co", Source: "none", Origin: "user"})
	agent := Agent("test-agent")
	var ids []int64
	for _, title := range []string{"A", "B", "C"} {
		id, err := s.AddJob(ctx, ActorUser, NewJob{CompanyID: company, Title: title, Origin: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetJobStatus(ctx, ActorUser, id, StatusReview); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := s.SetShortlist(ctx, agent, []ShortlistEntry{{ids[2], "First"}, {ids[0], "Second"}, {ids[1], "Third"}}); err != nil {
		t.Fatal(err)
	}

	// A job that leaves review keeps its row; the ranks shown close up.
	if err := s.SetJobStatus(ctx, ActorUser, ids[0], StatusSkipped); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ShortlistJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != ids[2] || rows[1].ID != ids[1] || rows[1].Shortlist.Rank != 2 || rows[1].Shortlist.SetBy != agent {
		t.Errorf("with the second job skipped: %+v", rows)
	}
	var stored int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM shortlist").Scan(&stored); err != nil || stored != 3 {
		t.Errorf("rows kept: %d, %v; want 3", stored, err)
	}
	if err := s.AddToShortlist(ctx, ActorUser, ids[0], "added by you", 30); !errors.Is(err, ErrConflict) {
		t.Errorf("adding a skipped job: %v; want a conflict", err)
	}

	// Deleting a job deletes its row.
	for _, q := range []string{"DELETE FROM events WHERE job_id = ?", "DELETE FROM jobs WHERE id = ?"} {
		if _, err := s.db.ExecContext(ctx, q, ids[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM shortlist").Scan(&stored); err != nil || stored != 2 {
		t.Errorf("rows after deleting a job: %d, %v; want 2", stored, err)
	}

	// Only the agent's set_shortlist records shortlist_set.
	c, err := s.ShortlistCounts(ctx)
	if err != nil || c.Size != 1 || c.LastSet.IsZero() {
		t.Errorf("counts: %+v, %v", c, err)
	}
	if err := s.RemoveFromShortlist(ctx, ActorUser, ids[2]); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFromShortlist(ctx, ActorUser, ids[2]); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v; want not found", err)
	}
	if after, _ := s.ShortlistCounts(ctx); after.Size != 0 || !after.LastSet.Equal(c.LastSet) {
		t.Errorf("after the user removed a job: %+v", after)
	}
}
