package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// OutreachStatuses are where an outreach thread can be. received is an
// inbound thread not yet answered; declined means the user passed.
var OutreachStatuses = []string{"received", "sent", "accepted", "replied", "scheduled", "no_reply", "declined", "closed"}

// OutreachDirections say who wrote first: the other person (inbound) or
// the user (outbound).
var OutreachDirections = []string{"inbound", "outbound"}

// DeclineReasons are why the user passed on a thread.
var DeclineReasons = []string{"contract", "pay_below_floor", "hybrid_or_onsite", "wrong_role", "level_too_low", "no_details", "suspicious", "other"}

// outreachEnded are the statuses that end a thread: it isn't open.
const outreachEnded = `('declined', 'closed')`

// OutreachPay is pay the person mentioned, apart from any listing's. A
// zero Min or Max wasn't said.
type OutreachPay struct {
	Min, Max float64
	Currency string // ISO 4217
	Period   string // year or hour
	Note     string
}

// IsZero reports whether no pay was mentioned.
func (p OutreachPay) IsZero() bool { return p == OutreachPay{} }

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
	UpdatedAt  time.Time

	Direction     string
	DeclineReason string
	DeclineNote   string
	Pay           OutreachPay
	ClientCompany string // an agency's client, or "undisclosed"
}

// NewOutreach is a thread to log. A contact with the same name at the
// same company is reused.
type NewOutreach struct {
	Person, Role, Company string
	Channel               string
	JobID                 int64
	Direction             string
	Status                string    // empty for the direction's default
	SentAt                time.Time // when the thread started, whoever wrote
	FollowUpAt            time.Time // zero for none
	Note                  string
	DeclineReason         string
	DeclineNote           string
	Pay                   OutreachPay
	ClientCompany         string
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
		status := o.Status
		if status == "" {
			status = map[string]string{"inbound": "received", "outbound": "sent"}[o.Direction]
		}
		p := payColumns(o.Pay)
		res, err := tx.ExecContext(ctx, `
			INSERT INTO outreach (contact_id, job_id, direction, channel, sent_at, status, follow_up_at, note,
			                      decline_reason, decline_note, pay_min, pay_max, pay_currency, pay_period, pay_note, client_company,
			                      created_at, created_by, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			contactID, nullID(o.JobID), o.Direction, o.Channel, sent, status, follow, o.Note,
			nullString(o.DeclineReason), o.DeclineNote, p[0], p[1], p[2], p[3], o.Pay.Note, nullString(o.ClientCompany),
			now, by, now, by)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "outreach_logged", eventRefs{OutreachID: id, ContactID: contactID, JobID: o.JobID},
			map[string]string{"channel": o.Channel, "direction": o.Direction, "status": status})
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

// ErrNotDeclined is returned for a decline reason on a thread that isn't
// declined.
var ErrNotDeclined = errors.New("decline_reason: only with status declined")

// OutreachChange is a change to an outreach thread; empty fields are left
// alone.
type OutreachChange struct {
	Status        string
	FollowUpAt    time.Time // zero leaves it
	ClearFollowUp bool
	AppendNote    string
	Direction     string
	JobID         int64   // the job the thread turned out to be about
	ClientCompany *string // nil leaves it
	Pay           *OutreachPay
	DeclineReason string
	DeclineNote   string
}

// payColumns returns pay's min, max, currency and period as nullable
// column values.
func payColumns(p OutreachPay) [4]any {
	var out [4]any
	if p.Min > 0 {
		out[0] = p.Min
	}
	if p.Max > 0 {
		out[1] = p.Max
	}
	out[2], out[3] = nullString(p.Currency), nullString(p.Period)
	return out
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
			status = c.Status
			if status != "declined" { // a reopened thread's reason no longer holds
				if _, err := tx.ExecContext(ctx, "UPDATE outreach SET decline_reason = NULL WHERE id = ?", id); err != nil {
					return err
				}
			}
		}
		if c.DeclineReason != "" && status != "declined" {
			return ErrNotDeclined
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
		set := func(column string, v any) error {
			_, err := tx.ExecContext(ctx, "UPDATE outreach SET "+column+" = ? WHERE id = ?", v, id)
			return err
		}
		if c.Direction != "" {
			if err := set("direction", c.Direction); err != nil {
				return err
			}
			data["direction"] = c.Direction
		}
		if c.JobID != 0 && c.JobID != job.Int64 {
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE id = ?", c.JobID).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return fmt.Errorf("job %d: %w", c.JobID, ErrNotFound)
			}
			if err := set("job_id", c.JobID); err != nil {
				return err
			}
			data["job_id"] = c.JobID
			job = sql.NullInt64{Int64: c.JobID, Valid: true}
		}
		if c.ClientCompany != nil {
			if err := set("client_company", nullString(*c.ClientCompany)); err != nil {
				return err
			}
			data["client_company"] = true
		}
		if c.Pay != nil {
			p := payColumns(*c.Pay)
			if _, err := tx.ExecContext(ctx, "UPDATE outreach SET pay_min = ?, pay_max = ?, pay_currency = ?, pay_period = ?, pay_note = ? WHERE id = ?",
				p[0], p[1], p[2], p[3], c.Pay.Note, id); err != nil {
				return err
			}
			data["pay"] = true
		}
		if c.DeclineReason != "" {
			if err := set("decline_reason", c.DeclineReason); err != nil {
				return err
			}
			data["decline_reason"] = c.DeclineReason
		}
		if c.DeclineNote != "" {
			if err := set("decline_note", c.DeclineNote); err != nil {
				return err
			}
			data["decline_note"] = true
		}
		if _, err := tx.ExecContext(ctx, "UPDATE outreach SET updated_at = ?, updated_by = ? WHERE id = ?", now, by, id); err != nil {
			return err
		}
		return insertEvent(ctx, tx, now, by, "outreach_updated", eventRefs{OutreachID: id, ContactID: contact, JobID: job.Int64}, data)
	})
}

// OutreachQuery selects outreach for ListOutreach. Every field set must
// match.
type OutreachQuery struct {
	ID           int64
	Statuses     []string
	Person       string // part of the contact's name, in any case
	Company      string // part of the contact's company, in any case
	JobID        int64
	DueBy        time.Time // waiting threads with a follow-up date by then
	QuietSince   time.Time // waiting threads sent before then with no follow-up date
	FollowUpSet  bool      // threads with a follow-up date
	FollowUpNone bool      // threads without one
	Offset       int
	Limit        int
	NewestFirst  bool // by when the message was sent
	ChangedFirst bool // by when the thread last changed
	WaitingOnly  bool

	Direction     string
	Channel       string
	DeclineReason string
	Since, Until  time.Time // when the thread started; zero for no bound
}

// ListOutreach returns outreach, follow-ups due soonest first, and how
// many match in all.
func (s *Store) ListOutreach(ctx context.Context, q OutreachQuery) ([]Outreach, int, error) {
	where, args := []string{"1"}, []any{}
	if q.WaitingOnly || !q.DueBy.IsZero() || !q.QuietSince.IsZero() {
		where = append(where, "o.status IN "+outreachWaiting)
	}
	if q.ID != 0 {
		where, args = append(where, "o.id = ?"), append(args, q.ID)
	}
	if len(q.Statuses) > 0 {
		where = append(where, "o.status IN ("+strings.TrimSuffix(strings.Repeat("?, ", len(q.Statuses)), ", ")+")")
		for _, st := range q.Statuses {
			args = append(args, st)
		}
	}
	if q.Person != "" {
		where, args = append(where, "instr(lower(ct.name), lower(?)) > 0"), append(args, q.Person)
	}
	if q.Company != "" {
		where, args = append(where, "instr(lower(coalesce(ct.company_name, '')), lower(?)) > 0"), append(args, q.Company)
	}
	if q.JobID != 0 {
		where, args = append(where, "o.job_id = ?"), append(args, q.JobID)
	}
	if q.FollowUpSet {
		where = append(where, "o.follow_up_at IS NOT NULL")
	}
	if q.Direction != "" {
		where, args = append(where, "o.direction = ?"), append(args, q.Direction)
	}
	if q.Channel != "" {
		where, args = append(where, "o.channel = ?"), append(args, q.Channel)
	}
	if q.DeclineReason != "" {
		where, args = append(where, "o.decline_reason = ?"), append(args, q.DeclineReason)
	}
	if !q.Since.IsZero() {
		where, args = append(where, "o.sent_at >= ?"), append(args, utc.Format(q.Since))
	}
	if !q.Until.IsZero() {
		where, args = append(where, "o.sent_at < ?"), append(args, utc.Format(q.Until))
	}
	if q.FollowUpNone {
		where = append(where, "o.follow_up_at IS NULL")
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
	switch {
	case q.ChangedFirst:
		order = "o.updated_at DESC"
	case q.NewestFirst:
		order = "o.sent_at DESC"
	}
	out, err := s.outreach(ctx, from+" ORDER BY "+order+", o.id LIMIT ? OFFSET ?", append(args, q.Limit, q.Offset)...)
	return out, total, err
}

// OpenThreads returns the open threads (not declined or closed) whose contact is named person
// (in any case), at a company with company's key when company isn't empty,
// and about jobID when it isn't 0.
func (s *Store) OpenThreads(ctx context.Context, person, company string, jobID int64) ([]Outreach, error) {
	where, args := "lower(ct.name) = lower(?) AND o.status NOT IN "+outreachEnded, []any{person}
	if jobID != 0 {
		where, args = where+" AND o.job_id = ?", append(args, jobID)
	}
	all, err := s.outreach(ctx, `FROM outreach o JOIN contacts ct ON ct.id = o.contact_id LEFT JOIN jobs j ON j.id = o.job_id
		WHERE `+where+" ORDER BY o.sent_at, o.id", args...)
	if err != nil || company == "" {
		return all, err
	}
	key := dedupe.CompanyKey(company)
	var out []Outreach
	for _, o := range all {
		if dedupe.CompanyKey(o.Company) == key {
			out = append(out, o)
		}
	}
	return out, nil
}

// outreach runs a query of outreach joined to contacts (ct) and jobs (j).
func (s *Store) outreach(ctx context.Context, from string, args ...any) ([]Outreach, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.id, ct.id, ct.name, coalesce(ct.title, ''), coalesce(ct.company_name, ''), coalesce(o.job_id, 0), coalesce(j.title, ''),
		       o.channel, o.sent_at, o.status, o.follow_up_at, o.note, o.updated_at,
		       o.direction, coalesce(o.decline_reason, ''), o.decline_note, coalesce(o.pay_min, 0), coalesce(o.pay_max, 0),
		       coalesce(o.pay_currency, ''), coalesce(o.pay_period, ''), o.pay_note, coalesce(o.client_company, '') `+from, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Outreach
	for rows.Next() {
		var (
			o                     Outreach
			sent, follow, updated sql.NullString
		)
		if err := rows.Scan(&o.ID, &o.ContactID, &o.Person, &o.Role, &o.Company, &o.JobID, &o.JobTitle,
			&o.Channel, &sent, &o.Status, &follow, &o.Note, &updated,
			&o.Direction, &o.DeclineReason, &o.DeclineNote, &o.Pay.Min, &o.Pay.Max,
			&o.Pay.Currency, &o.Pay.Period, &o.Pay.Note, &o.ClientCompany); err != nil {
			return nil, err
		}
		if o.SentAt, err = parseTime(sent); err != nil {
			return nil, err
		}
		if o.FollowUpAt, err = parseTime(follow); err != nil {
			return nil, err
		}
		if o.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
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
	Outreach      OutreachCounts
}

// OutreachCounts count outreach threads for the summary.
type OutreachCounts struct {
	// ByDirection counts threads by direction, then status; every
	// direction and status is present.
	ByDirection      map[string]map[string]int
	InboundLast7     int // inbound threads started in the last 7 days
	InboundLast30    int
	Unanswered       int            // inbound threads still received
	DeclinedByReason map[string]int // "not_given" for a decline without a reason
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
	if t.LastRefreshAt, err = parseTime(refresh); err != nil {
		return t, err
	}
	t.Outreach, err = s.outreachCounts(ctx, now)
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

// outreachCounts counts threads by direction and status, recent inbound
// threads, and declines by reason.
func (s *Store) outreachCounts(ctx context.Context, now time.Time) (OutreachCounts, error) {
	c := OutreachCounts{ByDirection: map[string]map[string]int{}, DeclinedByReason: map[string]int{}}
	for _, d := range OutreachDirections {
		c.ByDirection[d] = map[string]int{}
		for _, st := range OutreachStatuses {
			c.ByDirection[d][st] = 0
		}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT direction, status, coalesce(decline_reason, 'not_given'), count(*) FROM outreach
		GROUP BY direction, status, coalesce(decline_reason, 'not_given')`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			d, st, reason string
			n             int
		)
		if err := rows.Scan(&d, &st, &reason, &n); err != nil {
			return c, err
		}
		c.ByDirection[d][st] += n
		if st == "declined" {
			c.DeclinedByReason[reason] += n
		}
	}
	if err := rows.Err(); err != nil {
		return c, err
	}
	c.Unanswered = c.ByDirection["inbound"]["received"]
	err = s.db.QueryRowContext(ctx, `
		SELECT coalesce(sum(sent_at >= ?), 0), coalesce(sum(sent_at >= ?), 0) FROM outreach WHERE direction = 'inbound'`,
		utc.Format(now.AddDate(0, 0, -7)), utc.Format(now.AddDate(0, 0, -30))).Scan(&c.InboundLast7, &c.InboundLast30)
	return c, err
}
