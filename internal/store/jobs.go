package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// JobStatus is where a job is in triage and the application pipeline. See
// the state diagram in docs/design.md section 4.
type JobStatus string

const (
	StatusNew          JobStatus = "new"
	StatusFilteredOut  JobStatus = "filtered_out"
	StatusUnscreened   JobStatus = "unscreened"
	StatusScreenedOut  JobStatus = "screened_out"
	StatusReview       JobStatus = "review"
	StatusSkipped      JobStatus = "skipped"
	StatusApplied      JobStatus = "applied"
	StatusInterviewing JobStatus = "interviewing"
	StatusOffer        JobStatus = "offer"
	StatusRejected     JobStatus = "rejected"
	StatusWithdrawn    JobStatus = "withdrawn"
)

// Valid reports whether s is a known status.
func (s JobStatus) Valid() bool {
	switch s {
	case StatusNew, StatusFilteredOut, StatusUnscreened, StatusScreenedOut, StatusReview, StatusSkipped,
		StatusApplied, StatusInterviewing, StatusOffer, StatusRejected, StatusWithdrawn:
		return true
	}
	return false
}

// Job is one role the user triages. ID is also the short number (#482)
// people and agents use.
type Job struct {
	ID          int64
	CompanyID   int64
	Title       string
	URL         string
	Description string
	Origin      string // feed | search | manual | agent
	Status      JobStatus
	FirstSeenAt time.Time
	ClosedAt    time.Time
	AppliedAt   time.Time
	Notes       string
	CreatedAt   time.Time
	CreatedBy   Actor
	UpdatedAt   time.Time
	UpdatedBy   Actor
}

// NewJob holds the fields needed to add a job. Feed jobs get more fields
// from their postings in M2.
type NewJob struct {
	CompanyID   int64
	Title       string
	URL         string
	Description string
	Origin      string
}

// AddJob inserts a job with status "new" and returns its number.
func (s *Store) AddJob(ctx context.Context, by Actor, j NewJob) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO jobs (company_id, title, url, description, origin, first_seen_at,
			                  created_at, created_by, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.CompanyID, j.Title, nullString(j.URL), nullString(j.Description), j.Origin, now,
			now, by, now, by)
		if err != nil {
			return fmt.Errorf("adding job %q: %w", j.Title, err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "job_added", eventRefs{JobID: id, CompanyID: j.CompanyID},
			map[string]string{"origin": j.Origin})
	})
	return id, err
}

// Job returns one job.
func (s *Store) Job(ctx context.Context, id int64) (Job, error) {
	var (
		j                                                Job
		url, desc                                        sql.NullString
		firstSeen, closed, applied, createdAt, updatedAt sql.NullString
		status, createdBy, updatedBy                     string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT j.id, j.company_id, j.title, j.url, `+jobDescription+`, j.origin, j.status, j.first_seen_at, j.closed_at, j.applied_at,
		       j.notes, j.created_at, j.created_by, j.updated_at, j.updated_by
		FROM jobs j WHERE j.id = ?`, id).
		Scan(&j.ID, &j.CompanyID, &j.Title, &url, &desc, &j.Origin, &status, &firstSeen, &closed, &applied,
			&j.Notes, &createdAt, &createdBy, &updatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, fmt.Errorf("job %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Job{}, err
	}
	j.URL, j.Description, j.Status = url.String, desc.String, JobStatus(status)
	j.CreatedBy, j.UpdatedBy = Actor(createdBy), Actor(updatedBy)
	for _, f := range []struct {
		dst *time.Time
		src sql.NullString
	}{{&j.FirstSeenAt, firstSeen}, {&j.ClosedAt, closed}, {&j.AppliedAt, applied}, {&j.CreatedAt, createdAt}, {&j.UpdatedAt, updatedAt}} {
		if *f.dst, err = parseTime(f.src); err != nil {
			return Job{}, err
		}
	}
	return j, nil
}

// SetJobStatus changes a job's status and records a status_changed event in
// the same transaction. Setting the status a job already has does nothing.
func (s *Store) SetJobStatus(ctx context.Context, by Actor, id int64, to JobStatus) error {
	if !to.Valid() {
		return fmt.Errorf("unknown job status %q", to)
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var from string
		err := tx.QueryRowContext(ctx, "SELECT status FROM jobs WHERE id = ?", id).Scan(&from)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("job %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if JobStatus(from) == to {
			return nil
		}
		now := s.stamp()
		if _, err := tx.ExecContext(ctx, "UPDATE jobs SET status = ?, updated_at = ?, updated_by = ? WHERE id = ?",
			to, now, by, id); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "status_changed", eventRefs{JobID: id},
			map[string]JobStatus{"from": JobStatus(from), "to": to})
	})
}

// SetAppliedAt records when the user applied. The time must be UTC.
func (s *Store) SetAppliedAt(ctx context.Context, by Actor, id int64, at time.Time) error {
	appliedAt, err := timeText(at)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		res, err := tx.ExecContext(ctx, "UPDATE jobs SET applied_at = ?, applied_unconfirmed = 0, updated_at = ?, updated_by = ? WHERE id = ?",
			appliedAt, now, by, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("job %d: %w", id, ErrNotFound)
		}
		return insertEvent(ctx, tx, now, by, "applied_at_set", eventRefs{JobID: id},
			map[string]string{"applied_at": appliedAt.String})
	})
}
