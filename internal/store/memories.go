package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Memory is one short fact the agent learned about the candidate.
type Memory struct {
	ID            int64
	Kind          string // fact | preference | constraint | strength | gap | claim_limit | feedback
	Content       string
	Basis         string // user_said | inferred
	Evidence      string
	EvidenceJobID int64  // set when inferred from a listing
	Status        string // active | archived | superseded
	SupersedesID  int64
	CreatedAt     time.Time
	CreatedBy     Actor
	UpdatedAt     time.Time
	UpdatedBy     Actor
}

// Memory kinds and bases, as the schema allows them.
var (
	MemoryKinds = []string{"fact", "preference", "constraint", "strength", "gap", "claim_limit", "feedback"}
	MemoryBases = []string{"user_said", "inferred"}
)

// NewMemory is what Remember stores.
type NewMemory struct {
	Kind, Content, Basis, Evidence string
	EvidenceJobID                  int64
	Supersedes                     int64 // marks that memory superseded
}

// Remember stores a memory. A memory it supersedes is marked superseded in
// the same transaction.
func (s *Store) Remember(ctx context.Context, by Actor, m NewMemory) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		if m.Supersedes != 0 {
			res, err := tx.ExecContext(ctx, `
				UPDATE memories SET status = 'superseded', updated_at = ?, updated_by = ? WHERE id = ? AND status = 'active'`,
				now, by, m.Supersedes)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("memory %d to supersede: %w (or not active)", m.Supersedes, ErrNotFound)
			}
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO memories (kind, content, basis, evidence, evidence_job_id, supersedes_id,
			                      created_at, created_by, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.Kind, m.Content, m.Basis, m.Evidence, nullID(m.EvidenceJobID), nullID(m.Supersedes), now, by, now, by)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// MemoryQuery narrows Memories. Empty fields don't narrow.
type MemoryQuery struct {
	// Text matches memories containing every word, ignoring case.
	Text            string
	Kind            string
	IncludeArchived bool // also archived and superseded memories
}

// Memories returns the memories matching q, grouped by kind, oldest first.
func (s *Store) Memories(ctx context.Context, q MemoryQuery) ([]Memory, error) {
	where := []string{"1 = 1"}
	var args []any
	if !q.IncludeArchived {
		where = append(where, "status = 'active'")
	}
	if q.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, q.Kind)
	}
	for w := range strings.FieldsSeq(q.Text) {
		where = append(where, `content LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscaper.Replace(w)+"%")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+memoryColumns+` FROM memories WHERE `+strings.Join(where, " AND ")+` ORDER BY kind, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMemory returns one memory.
func (s *Store) GetMemory(ctx context.Context, id int64) (Memory, error) {
	m, err := scanMemory(s.db.QueryRowContext(ctx, "SELECT "+memoryColumns+" FROM memories WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, fmt.Errorf("memory %d: %w", id, ErrNotFound)
	}
	return m, err
}

// MemoryChange holds the fields UpdateMemory changes; nil leaves a field.
type MemoryChange struct {
	Content, Kind, Basis *string
}

// UpdateMemory edits a memory. The schema refuses user_said for a memory
// inferred from a listing.
func (s *Store) UpdateMemory(ctx context.Context, by Actor, id int64, c MemoryChange) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE memories SET content = coalesce(?, content), kind = coalesce(?, kind), basis = coalesce(?, basis),
			       updated_at = ?, updated_by = ?
			WHERE id = ?`, c.Content, c.Kind, c.Basis, s.stamp(), by, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("memory %d: %w", id, ErrNotFound)
		}
		return nil
	})
}

// ForgetMemory archives a memory, keeping it for the user to see. Only the
// user can delete one, in the UI.
func (s *Store) ForgetMemory(ctx context.Context, by Actor, id int64, reason string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		res, err := tx.ExecContext(ctx, `
			UPDATE memories SET status = 'archived', updated_at = ?, updated_by = ? WHERE id = ?`, now, by, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("memory %d: %w", id, ErrNotFound)
		}
		return insertEvent(ctx, tx, now, by, "memory_archived", eventRefs{}, map[string]any{"memory": id, "reason": reason})
	})
}

const memoryColumns = `id, kind, content, basis, evidence, evidence_job_id, status, supersedes_id,
	created_at, created_by, updated_at, updated_by`

func scanMemory(row interface{ Scan(...any) error }) (Memory, error) {
	var (
		m                    Memory
		jobID, supersedes    sql.NullInt64
		created, updated     sql.NullString
		createdBy, updatedBy string
	)
	if err := row.Scan(&m.ID, &m.Kind, &m.Content, &m.Basis, &m.Evidence, &jobID, &m.Status, &supersedes,
		&created, &createdBy, &updated, &updatedBy); err != nil {
		return Memory{}, err
	}
	m.EvidenceJobID, m.SupersedesID = jobID.Int64, supersedes.Int64
	m.CreatedBy, m.UpdatedBy = Actor(createdBy), Actor(updatedBy)
	var err error
	if m.CreatedAt, err = parseTime(created); err != nil {
		return Memory{}, err
	}
	m.UpdatedAt, err = parseTime(updated)
	return m, err
}

// likeEscaper escapes LIKE wildcards in user text.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
