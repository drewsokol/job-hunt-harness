package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Event is one entry in the timeline.
type Event struct {
	ID         int64
	At         time.Time
	Actor      Actor
	Kind       string
	JobID      int64
	OutreachID int64
	ContactID  int64
	CompanyID  int64
	Data       json.RawMessage
}

// eventRefs says what an event is about. Zero ids are stored as NULL.
type eventRefs struct {
	JobID, OutreachID, ContactID, CompanyID int64
}

// insertEvent records an event inside the caller's transaction, so the
// change and its timeline entry commit or roll back together.
func insertEvent(ctx context.Context, tx *sql.Tx, at string, by Actor, kind string, refs eventRefs, data any) error {
	payload := "{}"
	if data != nil {
		var err error
		if payload, err = jsonText(data); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO events (at, actor, kind, job_id, outreach_id, contact_id, company_id, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		at, by, kind, nullID(refs.JobID), nullID(refs.OutreachID), nullID(refs.ContactID), nullID(refs.CompanyID), payload)
	return err
}

// JobEvents returns a job's timeline, oldest first.
func (s *Store) JobEvents(ctx context.Context, jobID int64) ([]Event, error) {
	return s.events(ctx, "job_id = ?", jobID)
}

// OutreachEvents returns an outreach thread's history, oldest first.
func (s *Store) OutreachEvents(ctx context.Context, outreachID int64) ([]Event, error) {
	return s.events(ctx, "outreach_id = ?", outreachID)
}

// ContactEvents returns a contact's history, oldest first.
func (s *Store) ContactEvents(ctx context.Context, contactID int64) ([]Event, error) {
	return s.events(ctx, "contact_id = ?", contactID)
}

func (s *Store) events(ctx context.Context, where string, args ...any) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, at, actor, kind, job_id, outreach_id, contact_id, company_id, data
		FROM events WHERE `+where+` ORDER BY at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var es []Event
	for rows.Next() {
		var (
			e                                       Event
			at                                      sql.NullString
			actor, data                             string
			jobID, outreachID, contactID, companyID sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &at, &actor, &e.Kind, &jobID, &outreachID, &contactID, &companyID, &data); err != nil {
			return nil, err
		}
		if e.At, err = parseTime(at); err != nil {
			return nil, err
		}
		e.Actor, e.Data = Actor(actor), json.RawMessage(data)
		e.JobID, e.OutreachID, e.ContactID, e.CompanyID = jobID.Int64, outreachID.Int64, contactID.Int64, companyID.Int64
		es = append(es, e)
	}
	return es, rows.Err()
}

func nullID(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: id != 0}
}
