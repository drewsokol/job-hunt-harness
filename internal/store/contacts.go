package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Contact org types and warmth levels.
var (
	ContactOrgTypes = []string{"agency", "employer", "other"}
	ContactWarmths  = []string{"cold", "warm", "advocate"}
)

// Contact is someone the user is in touch with. Everything but Company is
// the user's record of another person: never log it. Company can come
// from a job's listing.
type Contact struct {
	ID            int64
	Name          string
	Title         string
	Company       string
	Relationship  string
	Source        string
	Why           string
	ProfileURL    string
	Email         string
	Phone         string
	Notes         string
	OrgType       string
	Warmth        string
	Tags          []string
	ReEngageAfter time.Time
	Threads       int       // outreach threads with them
	LastThreadAt  time.Time // when the newest thread started
	UpdatedAt     time.Time
}

// ContactChange is a contact to save. Nil fields are left alone; an empty
// string clears a field, and a zero ReEngageAfter clears the date.
type ContactChange struct {
	Name, Title, Company, Relationship, Source, Why  *string
	ProfileURL, Email, Phone, Notes, OrgType, Warmth *string
	Tags                                             *[]string
	ReEngageAfter                                    *time.Time
}

// fields returns the columns the change sets, with their values, in a
// fixed order. Company is handled apart, since it sets company_id too.
func (c ContactChange) fields() ([]string, []any, error) {
	var (
		cols []string
		vals []any
	)
	for _, f := range []struct {
		col string
		v   *string
	}{{"name", c.Name}, {"title", c.Title}, {"relationship", c.Relationship}, {"source", c.Source}, {"why", c.Why},
		{"profile_url", c.ProfileURL}, {"email", c.Email}, {"phone", c.Phone}, {"notes", c.Notes}, {"org_type", c.OrgType}, {"warmth", c.Warmth}} {
		if f.v == nil {
			continue
		}
		cols = append(cols, f.col)
		switch f.col {
		case "name", "why", "notes": // NOT NULL
			vals = append(vals, *f.v)
		default:
			vals = append(vals, nullString(*f.v))
		}
	}
	if c.Tags != nil {
		t, err := jsonText(orEmptyStrings(*c.Tags))
		if err != nil {
			return nil, nil, err
		}
		cols, vals = append(cols, "tags"), append(vals, t)
	}
	if c.ReEngageAfter != nil {
		t, err := timeText(*c.ReEngageAfter)
		if err != nil {
			return nil, nil, err
		}
		cols, vals = append(cols, "re_engage_after"), append(vals, t)
	}
	return cols, vals, nil
}

func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// SaveContact creates a contact when id is 0, reusing one with the same
// name at the same company as LogOutreach does, or changes contact id. It
// returns the contact's id and whether it was new. The event names the
// fields changed, never their values.
func (s *Store) SaveContact(ctx context.Context, by Actor, id int64, c ContactChange) (int64, bool, error) {
	created := false
	err := s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		if id == 0 {
			if c.Name == nil || *c.Name == "" {
				return fmt.Errorf("a new contact needs a name")
			}
			company := ""
			if c.Company != nil {
				company = *c.Company
			}
			found, err := findContact(ctx, tx, *c.Name, company)
			if err != nil {
				return err
			}
			if found == 0 {
				companyID, err := companyByName(ctx, tx, company)
				if err != nil {
					return err
				}
				res, err := tx.ExecContext(ctx, `
					INSERT INTO contacts (name, company_id, company_name, created_at, created_by, updated_at, updated_by)
					VALUES (?, ?, ?, ?, ?, ?, ?)`, *c.Name, companyID, nullString(company), now, by, now, by)
				if err != nil {
					return err
				}
				if found, err = res.LastInsertId(); err != nil {
					return err
				}
				created = true
			}
			// Written above, or the contact found has them already.
			c.Name, c.Company = nil, nil
			id = found
		} else {
			err := tx.QueryRowContext(ctx, "SELECT id FROM contacts WHERE id = ?", id).Scan(&id)
			if errNoRows(err) {
				return fmt.Errorf("contact %d: %w", id, ErrNotFound)
			}
			if err != nil {
				return err
			}
		}
		cols, vals, err := c.fields()
		if err != nil {
			return err
		}
		changed := append([]string(nil), cols...)
		if c.Company != nil {
			companyID, err := companyByName(ctx, tx, *c.Company)
			if err != nil {
				return err
			}
			cols, vals = append(cols, "company_name", "company_id"), append(vals, nullString(*c.Company), companyID)
			changed = append(changed, "company")
		}
		if created {
			changed = append([]string{"name", "company"}, changed...)
		}
		if len(cols) > 0 {
			set := make([]string, 0, len(cols)+2)
			for _, col := range cols {
				set = append(set, col+" = ?")
			}
			set = append(set, "updated_at = ?", "updated_by = ?")
			if _, err := tx.ExecContext(ctx, "UPDATE contacts SET "+strings.Join(set, ", ")+" WHERE id = ?",
				append(vals, now, by, id)...); err != nil {
				return err
			}
		}
		kind := "contact_updated"
		if created {
			kind = "contact_added"
		}
		return insertEvent(ctx, tx, now, by, kind, eventRefs{ContactID: id}, map[string][]string{"fields": changed})
	})
	return id, created, err
}

// companyByName returns the company jhh knows by this name, as LogOutreach
// finds it, or NULL.
func companyByName(ctx context.Context, tx *sql.Tx, name string) (sql.NullInt64, error) {
	var id sql.NullInt64
	if name == "" {
		return id, nil
	}
	err := tx.QueryRowContext(ctx, "SELECT id FROM companies WHERE name_key = ? ORDER BY source <> 'none' DESC, id LIMIT 1",
		dedupe.CompanyKey(name)).Scan(&id)
	if errNoRows(err) {
		err = nil
	}
	return id, err
}

// ContactQuery selects contacts for ListContacts. Every field set must
// match.
type ContactQuery struct {
	IDs           []int64
	Name          string // part of the name, in any case
	Company       string // part of the company, in any case
	OrgType       string
	Warmth        string
	Relationship  string // part of it, in any case
	Tag           string
	ReEngageDueBy time.Time // a date to get back in touch by then
	ReEngageSet   bool      // any date to get back in touch
	Since, Until  time.Time // the newest thread started in this range
	Offset, Limit int
}

// ListContacts returns contacts, most recently in touch first (by their
// newest thread, then when they were last changed), and how many match.
func (s *Store) ListContacts(ctx context.Context, q ContactQuery) ([]Contact, int, error) {
	where, args := []string{"1"}, []any{}
	if len(q.IDs) > 0 {
		where = append(where, "c.id IN ("+strings.TrimSuffix(strings.Repeat("?, ", len(q.IDs)), ", ")+")")
		for _, id := range q.IDs {
			args = append(args, id)
		}
	}
	if q.Name != "" {
		where, args = append(where, "instr(lower(c.name), lower(?)) > 0"), append(args, q.Name)
	}
	if q.Company != "" {
		where, args = append(where, "instr(lower(coalesce(c.company_name, '')), lower(?)) > 0"), append(args, q.Company)
	}
	if q.Relationship != "" {
		where, args = append(where, "instr(lower(coalesce(c.relationship, '')), lower(?)) > 0"), append(args, q.Relationship)
	}
	if q.OrgType != "" {
		where, args = append(where, "c.org_type = ?"), append(args, q.OrgType)
	}
	if q.Warmth != "" {
		where, args = append(where, "c.warmth = ?"), append(args, q.Warmth)
	}
	if q.Tag != "" {
		where, args = append(where, "EXISTS (SELECT 1 FROM json_each(c.tags) WHERE value = ?)"), append(args, q.Tag)
	}
	if !q.ReEngageDueBy.IsZero() {
		where, args = append(where, "c.re_engage_after IS NOT NULL AND c.re_engage_after <= ?"), append(args, utc.Format(q.ReEngageDueBy))
	}
	if q.ReEngageSet {
		where = append(where, "c.re_engage_after IS NOT NULL")
	}
	if !q.Since.IsZero() {
		where, args = append(where, "c.last_thread >= ?"), append(args, utc.Format(q.Since))
	}
	if !q.Until.IsZero() {
		where, args = append(where, "c.last_thread < ?"), append(args, utc.Format(q.Until))
	}
	from := `FROM (SELECT ct.*,
			(SELECT count(*) FROM outreach o WHERE o.contact_id = ct.id) AS threads,
			(SELECT max(o.sent_at) FROM outreach o WHERE o.contact_id = ct.id) AS last_thread
		FROM contacts ct) c WHERE ` + strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) "+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, coalesce(c.title, ''), coalesce(c.company_name, ''), coalesce(c.relationship, ''), coalesce(c.source, ''),
		       c.why, coalesce(c.profile_url, ''), coalesce(c.email, ''), coalesce(c.phone, ''), c.notes,
		       coalesce(c.org_type, ''), coalesce(c.warmth, ''), c.tags, c.re_engage_after, c.threads, c.last_thread, c.updated_at `+from+`
		ORDER BY c.last_thread IS NULL, c.last_thread DESC, c.updated_at DESC, c.id DESC LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var (
			c                       Contact
			tags                    string
			reEngage, last, updated sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.Name, &c.Title, &c.Company, &c.Relationship, &c.Source, &c.Why, &c.ProfileURL, &c.Email, &c.Phone, &c.Notes,
			&c.OrgType, &c.Warmth, &tags, &reEngage, &c.Threads, &last, &updated); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal([]byte(tags), &c.Tags); err != nil {
			return nil, 0, fmt.Errorf("contact %d tags: %w", c.ID, err)
		}
		if c.ReEngageAfter, err = parseTime(reEngage); err != nil {
			return nil, 0, err
		}
		if c.LastThreadAt, err = parseTime(last); err != nil {
			return nil, 0, err
		}
		if c.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}
