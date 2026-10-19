package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
)

// Company is a company whose jobs jhh tracks.
type Company struct {
	ID            int64
	Name          string
	Source        string // greenhouse | lever | ashby | none
	BoardKey      string // e.g. "greenhouse:exampleco"; empty when there is no board
	SourceConfig  json.RawMessage
	CareersURL    string
	Enabled       bool
	Origin        string // user | agent | search
	ETag          string
	LastFetchAt   time.Time
	LastError     string
	OpenPostings  int
	Suspect       bool
	SuspectReason string
	CreatedAt     time.Time
	CreatedBy     Actor
	UpdatedAt     time.Time
	UpdatedBy     Actor
}

// NewCompany holds the fields needed to add a company.
type NewCompany struct {
	Name         string
	Source       string
	BoardKey     string
	SourceConfig json.RawMessage // nil means {}
	CareersURL   string
	Origin       string
}

// AddCompany inserts a company and returns its id. A company with no board
// under the same normalized name, which search or an agent added, gets the
// board instead, so the board's postings attach to the jobs found before.
func (s *Store) AddCompany(ctx context.Context, by Actor, c NewCompany) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = addCompany(ctx, tx, s.stamp(), by, c)
		return err
	})
	return id, err
}

func addCompany(ctx context.Context, tx *sql.Tx, now string, by Actor, c NewCompany) (int64, error) {
	cfg := string(c.SourceConfig)
	if cfg == "" {
		cfg = "{}"
	}
	key := dedupe.CompanyKey(c.Name)
	if c.Source != "none" {
		var id int64
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM companies WHERE name_key = ? AND source = 'none' ORDER BY id LIMIT 1`, key).Scan(&id)
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx, `
				UPDATE companies SET name = ?, source = ?, board_key = ?, source_config = ?, careers_url = ?, origin = ?,
				       updated_at = ?, updated_by = ?
				WHERE id = ?`,
				c.Name, c.Source, nullString(c.BoardKey), cfg, nullString(c.CareersURL), c.Origin, now, by, id); err != nil {
				return 0, fmt.Errorf("adding a board to company %q: %w", c.Name, err)
			}
			return id, insertEvent(ctx, tx, now, by, "company_board_added", eventRefs{CompanyID: id},
				map[string]string{"source": c.Source, "origin": c.Origin})
		case !errors.Is(err, sql.ErrNoRows):
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO companies (name, name_key, source, board_key, source_config, careers_url, origin,
		                       created_at, created_by, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Name, key, c.Source, nullString(c.BoardKey), cfg, nullString(c.CareersURL), c.Origin,
		now, by, now, by)
	if err != nil {
		return 0, fmt.Errorf("adding company %q: %w", c.Name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, insertEvent(ctx, tx, now, by, "company_added", eventRefs{CompanyID: id}, nil)
}

const companyColumns = `id, name, source, board_key, source_config, careers_url, enabled, origin,
	etag, last_fetch_at, last_error, open_postings, suspect, suspect_reason,
	created_at, created_by, updated_at, updated_by`

// Company returns one company.
func (s *Store) Company(ctx context.Context, id int64) (Company, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+companyColumns+" FROM companies WHERE id = ?", id)
	c, err := scanCompany(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Company{}, fmt.Errorf("company %d: %w", id, ErrNotFound)
	}
	return c, err
}

// Companies returns all companies, ordered by name.
func (s *Store) Companies(ctx context.Context) ([]Company, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+companyColumns+" FROM companies ORDER BY name COLLATE NOCASE, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cs []Company
	for rows.Next() {
		c, err := scanCompany(rows)
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCompany(r scanner) (Company, error) {
	var (
		c                                                 Company
		boardKey, careersURL, etag, lastError, suspectWhy sql.NullString
		lastFetchAt, createdAt, updatedAt                 sql.NullString
		cfg, createdBy, updatedBy                         string
	)
	err := r.Scan(&c.ID, &c.Name, &c.Source, &boardKey, &cfg, &careersURL, &c.Enabled, &c.Origin,
		&etag, &lastFetchAt, &lastError, &c.OpenPostings, &c.Suspect, &suspectWhy,
		&createdAt, &createdBy, &updatedAt, &updatedBy)
	if err != nil {
		return Company{}, err
	}
	c.BoardKey, c.CareersURL, c.ETag = boardKey.String, careersURL.String, etag.String
	c.LastError, c.SuspectReason = lastError.String, suspectWhy.String
	c.SourceConfig = json.RawMessage(cfg)
	c.CreatedBy, c.UpdatedBy = Actor(createdBy), Actor(updatedBy)
	if c.LastFetchAt, err = parseTime(lastFetchAt); err != nil {
		return Company{}, err
	}
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return Company{}, err
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Company{}, err
	}
	return c, nil
}

// nullString stores an empty string as NULL.
func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// SetCompanyEnabled turns reading a company's board on or off. Its jobs
// stay. Setting the value it already has does nothing.
func (s *Store) SetCompanyEnabled(ctx context.Context, by Actor, id int64, enabled bool) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var was bool
		err := tx.QueryRowContext(ctx, "SELECT enabled FROM companies WHERE id = ?", id).Scan(&was)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("company %d: %w", id, ErrNotFound)
		}
		if err != nil || was == enabled {
			return err
		}
		now := s.stamp()
		if _, err := tx.ExecContext(ctx, "UPDATE companies SET enabled = ?, updated_at = ?, updated_by = ? WHERE id = ?",
			enabled, now, by, id); err != nil {
			return err
		}
		kind := "company_disabled"
		if enabled {
			kind = "company_enabled"
		}
		return insertEvent(ctx, tx, now, by, kind, eventRefs{CompanyID: id}, nil)
	})
}
