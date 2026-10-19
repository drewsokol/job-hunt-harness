package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// OutreachStatuses are where an outreach thread can be.
var OutreachStatuses = []string{"sent", "accepted", "replied", "scheduled", "no_reply", "closed"}

// outreachWaiting are the statuses that wait on the other person, so a
// follow-up date on them is due.
const outreachWaiting = `('sent', 'accepted')`

// Outreach is one message to a contact. JobTitle is listing text.
type Outreach struct {
	ID         int64
	ContactID  int64
	Person     string
	Role       string // the contact's job title
	Company    string
	JobID      int64
	JobTitle   string
	Channel    string
	SentAt     time.Time
	Status     string
	FollowUpAt time.Time
	Note       string
}

// NewOutreach is a message to log. A contact with the same name at the
// same company is reused.
type NewOutreach struct {
	Person, Role, Company string
	Channel               string
	JobID                 int64
	SentAt                time.Time
	FollowUpAt            time.Time // zero for none
	Note                  string
}

// LogOutreach records a message, adding the contact if jhh doesn't know
// them. Without a company, the job's company is used.
func (s *Store) LogOutreach(ctx context.Context, by Actor, o NewOutreach) (id, contactID int64, newContact bool, err error) {
	err = s.write(ctx, func(tx *sql.Tx) error {
		now := s.stamp()
		var companyID sql.NullInt64
		if o.JobID != 0 {
			var name string
			err := tx.QueryRowContext(ctx, "SELECT j.company_id, c.name FROM jobs j JOIN companies c ON c.id = j.company_id WHERE j.id = ?", o.JobID).Scan(&companyID, &name)
			if errNoRows(err) {
				return fmt.Errorf("job %d: %w", o.JobID, ErrNotFound)
			}
			if err != nil {
				return err
			}
			if o.Company == "" {
				o.Company = name
			} else if dedupe.CompanyKey(o.Company) != dedupe.CompanyKey(name) {
				companyID = sql.NullInt64{}
			}
		}
		if !companyID.Valid && o.Company != "" {
			err := tx.QueryRowContext(ctx, "SELECT id FROM companies WHERE name_key = ? ORDER BY source <> 'none' DESC, id LIMIT 1",
				dedupe.CompanyKey(o.Company)).Scan(&companyID)
			if err != nil && !errNoRows(err) {
				return err
			}
		}
		var err error
		if contactID, err = findContact(ctx, tx, o.Person, o.Company); err != nil {
			return err
		}
		if contactID == 0 {
			newContact = true
			res, err := tx.ExecContext(ctx, `
				INSERT INTO contacts (name, title, company_id, company_name, created_at, created_by, updated_at, updated_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				o.Person, nullString(o.Role), companyID, nullString(o.Company), now, by, now, by)
			if err != nil {
				return err
			}
			if contactID, err = res.LastInsertId(); err != nil {
				return err
			}
		} else if o.Role != "" {
			if _, err := tx.ExecContext(ctx, "UPDATE contacts SET title = ?, updated_at = ?, updated_by = ? WHERE id = ? AND coalesce(title, '') <> ?",
				o.Role, now, by, contactID, o.Role); err != nil {
				return err
			}
		}
		sent, err := timeText(o.SentAt)
		if err != nil {
			return err
		}
		follow, err := timeText(o.FollowUpAt)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO outreach (contact_id, job_id, channel, sent_at, follow_up_at, note, created_at, created_by, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			contactID, nullID(o.JobID), o.Channel, sent, follow, o.Note, now, by, now, by)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "outreach_logged", eventRefs{OutreachID: id, ContactID: contactID, JobID: o.JobID},
			map[string]string{"channel": o.Channel})
	})
	return id, contactID, newContact, err
}

// findContact returns the contact with this name at this company, or 0.
func findContact(ctx context.Context, tx *sql.Tx, name, company string) (int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, coalesce(company_name, '') FROM contacts WHERE lower(name) = lower(?) ORDER BY id", name)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	key := dedupe.CompanyKey(company)
	for rows.Next() {
		var (
			id int64
			c  string
		)
		if err := rows.Scan(&id, &c); err != nil {
			return 0, err
		}
		if dedupe.CompanyKey(c) == key {
			return id, nil
		}
	}
	return 0, rows.Err()
}

// OutreachChange is a change to an outreach thread; empty fields are left
// alone.
type OutreachChange struct {
	Status        string
	FollowUpAt    time.Time // zero leaves it
	ClearFollowUp bool
	AppendNote    string
}

// UpdateOutreach records a reply, a follow-up date or a note.
func (s *Store) UpdateOutreach(ctx context.Context, by Actor, id int64, c OutreachChange) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var (
			status  string
			contact int64
			job     sql.NullInt64
		)
		err := tx.QueryRowContext(ctx, "SELECT status, contact_id, job_id FROM outreach WHERE id = ?", id).Scan(&status, &contact, &job)
		if errNoRows(err) {
			return fmt.Errorf("outreach %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		now := s.stamp()
		data := map[string]any{}
		if c.Status != "" && c.Status != status {
			if _, err := tx.ExecContext(ctx, "UPDATE outreach SET status = ? WHERE id = ?", c.Status, id); err != nil {
				return err
			}
			data["from"], data["to"] = status, c.Status
		}
		switch {
		case c.ClearFollowUp:
			if _, err := tx.ExecContext(ctx, "UPDATE outreach SET follow_up_at = NULL WHERE id = ?", id); err != nil {
				return err
			}
			data["follow_up_at"] = nil
		case !c.FollowUpAt.IsZero():
			at, err := timeText(c.FollowUpAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE outreach SET follow_up_at = ? WHERE id = ?", at, id); err != nil {
				return err
			}
			data["follow_up_at"] = at.String
		}
		if c.AppendNote != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE outreach SET note = note || CASE WHEN note = '' THEN '' ELSE char(10) END || ? WHERE id = ?`,
				c.AppendNote, id); err != nil {
				return err
			}
			data["note"] = true
		}
		if _, err := tx.ExecContext(ctx, "UPDATE outreach SET updated_at = ?, updated_by = ? WHERE id = ?", now, by, id); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "outreach_updated", eventRefs{OutreachID: id, ContactID: contact, JobID: job.Int64}, data)
	})
}

// OutreachQuery selects outreach for ListOutreach.
type OutreachQuery struct {
	DueBy        time.Time // waiting threads with a follow-up date by then
	QuietSince   time.Time // waiting threads sent before then with no follow-up date
	Offset       int
	Limit        int
	NewestFirst  bool
	WaitingOnly  bool
	FollowUpOnly bool
}

// ListOutreach returns outreach, follow-ups due soonest first, and how
// many match in all.
func (s *Store) ListOutreach(ctx context.Context, q OutreachQuery) ([]Outreach, int, error) {
	where, args := []string{"1"}, []any{}
	if q.WaitingOnly || !q.DueBy.IsZero() || !q.QuietSince.IsZero() {
		where = append(where, "o.status IN "+outreachWaiting)
	}
	if !q.DueBy.IsZero() {
		where = append(where, "o.follow_up_at IS NOT NULL AND o.follow_up_at <= ?")
		args = append(args, utc.Format(q.DueBy))
	}
	if !q.QuietSince.IsZero() {
		where = append(where, "o.follow_up_at IS NULL AND o.sent_at < ?")
		args = append(args, utc.Format(q.QuietSince))
	}
	from := `FROM outreach o JOIN contacts ct ON ct.id = o.contact_id LEFT JOIN jobs j ON j.id = o.job_id
		WHERE ` + strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) "+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "o.follow_up_at IS NULL, o.follow_up_at, o.sent_at"
	if q.NewestFirst {
		order = "o.sent_at DESC"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.id, ct.id, ct.name, coalesce(ct.title, ''), coalesce(ct.company_name, ''), coalesce(o.job_id, 0), coalesce(j.title, ''),
		       o.channel, o.sent_at, o.status, o.follow_up_at, o.note `+from+`
		ORDER BY `+order+`, o.id LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Outreach
	for rows.Next() {
		var (
			o            Outreach
			sent, follow sql.NullString
		)
		if err := rows.Scan(&o.ID, &o.ContactID, &o.Person, &o.Role, &o.Company, &o.JobID, &o.JobTitle,
			&o.Channel, &sent, &o.Status, &follow, &o.Note); err != nil {
			return nil, 0, err
		}
		if o.SentAt, err = parseTime(sent); err != nil {
			return nil, 0, err
		}
		if o.FollowUpAt, err = parseTime(follow); err != nil {
			return nil, 0, err
		}
		out = append(out, o)
	}
	return out, total, rows.Err()
}

// TrackerSummary is the summary strip: where the search stands.
type TrackerSummary struct {
	Unscreened    int
	ToReview      int
	Applications  int // applied, interviewing or offer
	AppliedLast7  int
	Interviewing  int
	Offers        int
	FollowUpsDue  int
	OpenFlags     int
	SuspectFeeds  int
	LastRefreshAt time.Time
}

// Tracker counts where the search stands at now.
func (s *Store) Tracker(ctx context.Context, now time.Time) (TrackerSummary, error) {
	var (
		t       TrackerSummary
		refresh sql.NullString
	)
	nowText, weekAgo := utc.Format(now), utc.Format(now.AddDate(0, 0, -7))
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM jobs WHERE status = 'unscreened' AND closed_at IS NULL AND hidden_at IS NULL),
			(SELECT count(*) FROM jobs WHERE status = 'review' AND closed_at IS NULL AND hidden_at IS NULL),
			(SELECT count(*) FROM jobs WHERE status IN ('applied', 'interviewing', 'offer') AND hidden_at IS NULL),
			(SELECT count(*) FROM jobs WHERE applied_at >= ? AND hidden_at IS NULL),
			(SELECT count(*) FROM jobs WHERE status = 'interviewing' AND hidden_at IS NULL),
			(SELECT count(*) FROM jobs WHERE status = 'offer' AND hidden_at IS NULL),
			(SELECT count(*) FROM outreach WHERE status IN `+outreachWaiting+` AND follow_up_at <= ?),
			(SELECT count(*) FROM content_flags WHERE status = 'open'),
			(SELECT count(*) FROM companies WHERE suspect = 1 AND enabled = 1),
			(SELECT max(finished_at) FROM fetch_runs)`,
		weekAgo, nowText).Scan(&t.Unscreened, &t.ToReview, &t.Applications, &t.AppliedLast7, &t.Interviewing, &t.Offers,
		&t.FollowUpsDue, &t.OpenFlags, &t.SuspectFeeds, &refresh)
	if err != nil {
		return t, err
	}
	t.LastRefreshAt, err = parseTime(refresh)
	return t, err
}

// Applications returns the jobs the user applied to and hasn't heard a
// final answer on, most recent first.
func (s *Store) Applications(ctx context.Context, offset, limit int) ([]JobRow, int, error) {
	return s.queryJobs(ctx, "j.status IN ('applied', 'interviewing', 'offer') AND j.hidden_at IS NULL", nil,
		"coalesce(j.applied_at, j.updated_at) DESC, j.id DESC", offset, limit, false)
}

// StaleApplications returns jobs still at applied that were applied to
// before then, oldest first: no news has been recorded since.
func (s *Store) StaleApplications(ctx context.Context, before time.Time, offset, limit int) ([]JobRow, int, error) {
	return s.queryJobs(ctx, "j.status = 'applied' AND j.hidden_at IS NULL AND j.applied_at < ?",
		[]any{utc.Format(before)}, "j.applied_at, j.id", offset, limit, false)
}

// NewJobs returns jobs first seen since then that wait for screening or
// review, best fit first.
func (s *Store) NewJobs(ctx context.Context, since time.Time, limit int) ([]JobRow, int, error) {
	return s.queryJobs(ctx, "j.first_seen_at >= ? AND j.status IN ('unscreened', 'review') AND j.closed_at IS NULL AND j.hidden_at IS NULL",
		[]any{utc.Format(since)},
		`CASE sc.fit WHEN 'strong' THEN 0 WHEN 'good' THEN 1 WHEN 'stretch' THEN 2 ELSE 3 END, j.prefilter_score DESC, j.id`, 0, limit, false)
}

// ChangedApplications returns jobs the user applied to whose posting
// closed or came back since then.
func (s *Store) ChangedApplications(ctx context.Context, since time.Time, limit int) ([]JobRow, int, error) {
	t := utc.Format(since)
	return s.queryJobs(ctx, "j.status IN ('applied', 'interviewing', 'offer') AND j.hidden_at IS NULL AND (j.closed_at >= ? OR j.reposted_at >= ?)",
		[]any{t, t}, "j.id", 0, limit, false)
}

// SuspectCompanies returns the enabled companies whose last fetch looked
// wrong.
func (s *Store) SuspectCompanies(ctx context.Context) ([]Company, error) {
	cs, err := s.Companies(ctx)
	var out []Company
	for _, c := range cs {
		if c.Suspect && c.Enabled {
			out = append(out, c)
		}
	}
	return out, err
}
