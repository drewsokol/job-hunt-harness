package core

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Contact limits.
const (
	maxContactField = 200
	maxContactText  = 2_000
	maxTags         = 20
	maxTagLen       = 40
	contactPage     = 20
	maxContactPage  = 50
)

var tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ContactInput is a contact to save, as an agent sends it. Nil fields are
// left alone; an empty string clears one.
type ContactInput struct {
	Name, Role, Company, Relationship, Source, Why   *string
	ProfileURL, Email, Phone, Notes, OrgType, Warmth *string
	Tags                                             *[]string
	ReEngageAfter                                    *string // RFC 3339; empty clears it
}

// SavedContact is what SaveContact did.
type SavedContact struct {
	ID  int64
	New bool
}

// SaveContact checks and saves a contact: a new one when id is 0 (name
// required; the same name at the same company is reused), or a change to
// contact id.
func SaveContact(ctx context.Context, s *store.Store, by store.Actor, id int64, in ContactInput) (SavedContact, error) {
	var (
		problems []string
		c        store.ContactChange
	)
	text := func(name string, v *string, max int) *string {
		if v == nil {
			return nil
		}
		t := strings.TrimSpace(*v)
		if utf8.RuneCountInString(t) > max {
			problems = append(problems, fmt.Sprintf("%s: up to %d characters", name, max))
		}
		return &t
	}
	c.Name = text("name", in.Name, maxContactField)
	c.Title = text("role", in.Role, maxContactField)
	c.Company = text("company", in.Company, maxContactField)
	c.Relationship = text("relationship", in.Relationship, maxChannelLen)
	c.Source = text("source", in.Source, maxChannelLen)
	c.Why = text("why", in.Why, maxContactText)
	c.ProfileURL = text("profile_url", in.ProfileURL, maxContactField)
	c.Email = text("email", in.Email, maxContactField)
	c.Phone = text("phone", in.Phone, maxChannelLen)
	c.Notes = text("notes", in.Notes, maxContactText)
	c.OrgType = text("org_type", in.OrgType, maxChannelLen)
	c.Warmth = text("warmth", in.Warmth, maxChannelLen)
	if c.Name != nil && *c.Name == "" {
		problems = append(problems, "name: a contact keeps a name; give 1 to 200 characters")
	}
	if id == 0 && c.Name == nil {
		problems = append(problems, "name: a new contact needs a name; to change one, give contact_id")
	}
	if c.ProfileURL != nil && *c.ProfileURL != "" && !strings.HasPrefix(strings.ToLower(*c.ProfileURL), "https://") && !strings.HasPrefix(strings.ToLower(*c.ProfileURL), "http://") {
		problems = append(problems, "profile_url: an http or https link")
	}
	if c.OrgType != nil && *c.OrgType != "" && !slices.Contains(store.ContactOrgTypes, *c.OrgType) {
		problems = append(problems, fmt.Sprintf("org_type: %q must be one of %s", *c.OrgType, strings.Join(store.ContactOrgTypes, ", ")))
	}
	if c.Warmth != nil && *c.Warmth != "" && !slices.Contains(store.ContactWarmths, *c.Warmth) {
		problems = append(problems, fmt.Sprintf("warmth: %q must be one of %s", *c.Warmth, strings.Join(store.ContactWarmths, ", ")))
	}
	if in.Tags != nil {
		tags := []string{}
		for _, t := range *in.Tags {
			t = strings.ToLower(strings.TrimSpace(t))
			switch {
			case !tagPattern.MatchString(t) || len(t) > maxTagLen:
				problems = append(problems, fmt.Sprintf("tags: %q must be a short lowercase word (letters, digits, - and _, up to %d)", t, maxTagLen))
			case !slices.Contains(tags, t):
				tags = append(tags, t)
			}
		}
		if len(tags) > maxTags {
			problems = append(problems, fmt.Sprintf("tags: at most %d", maxTags))
		}
		c.Tags = &tags
	}
	if in.ReEngageAfter != nil {
		var at time.Time
		if *in.ReEngageAfter != "" {
			t, err := utc.Parse(*in.ReEngageAfter)
			switch {
			case err != nil:
				problems = append(problems, "re_engage_after: "+err.Error())
			case t.After(utc.Now().Add(maxFollowUpFuture * 2)):
				problems = append(problems, "re_engage_after: more than two years away")
			default:
				at = t
			}
		}
		c.ReEngageAfter = &at
	}
	if id != 0 && c == (store.ContactChange{}) {
		problems = append(problems, "nothing to change: give a field to set")
	}
	if len(problems) > 0 {
		return SavedContact{}, &InputError{Problems: problems}
	}
	saved, isNew, err := s.SaveContact(ctx, by, id, c)
	if err != nil {
		return SavedContact{}, inputIfNotFound(err, fmt.Sprintf("contact_id: no contact %d", id))
	}
	return SavedContact{ID: saved, New: isNew}, nil
}

// ContactFilter selects contacts, as an agent asks for them. Every field
// given must match.
type ContactFilter struct {
	ID           int64 // one contact, with its threads; nothing else may be set
	Name         string
	Company      string
	OrgType      string
	Warmth       string
	Relationship string
	Tag          string
	ReEngage     string // due or set
	Since, Until string // RFC 3339, on the newest thread
	Limit        int
	Cursor       string
}

// ContactReEngage are the re_engage filters ListContacts takes.
var ContactReEngage = []string{"due", "set"}

// ContactPage is a page of contacts. For one contact asked for by id,
// Threads holds every thread with them, newest first.
type ContactPage struct {
	Contacts []store.Contact
	Total    int
	Next     string
	Threads  []store.Outreach
}

// ListContacts finds contacts, most recently in touch first.
func ListContacts(ctx context.Context, s *store.Store, in ContactFilter) (ContactPage, error) {
	var problems []string
	q := store.ContactQuery{Name: strings.TrimSpace(in.Name), Company: strings.TrimSpace(in.Company), OrgType: in.OrgType,
		Warmth: in.Warmth, Relationship: strings.TrimSpace(in.Relationship), Tag: strings.ToLower(strings.TrimSpace(in.Tag))}
	if in.ID != 0 {
		if q.Name != "" || q.Company != "" || q.OrgType != "" || q.Warmth != "" || q.Relationship != "" || q.Tag != "" || in.ReEngage != "" || in.Since != "" || in.Until != "" || in.Cursor != "" {
			problems = append(problems, "contact_id: give it alone for one contact, or leave it out to filter")
		}
		q.IDs = []int64{in.ID}
	}
	if q.OrgType != "" && !slices.Contains(store.ContactOrgTypes, q.OrgType) {
		problems = append(problems, fmt.Sprintf("org_type: %q must be one of %s", q.OrgType, strings.Join(store.ContactOrgTypes, ", ")))
	}
	if q.Warmth != "" && !slices.Contains(store.ContactWarmths, q.Warmth) {
		problems = append(problems, fmt.Sprintf("warmth: %q must be one of %s", q.Warmth, strings.Join(store.ContactWarmths, ", ")))
	}
	switch in.ReEngage {
	case "":
	case "due":
		q.ReEngageDueBy = utc.Now()
	case "set":
		q.ReEngageSet = true
	default:
		problems = append(problems, fmt.Sprintf("re_engage: %q must be one of %s", in.ReEngage, strings.Join(ContactReEngage, ", ")))
	}
	for _, d := range []struct {
		name, v string
		dst     *time.Time
	}{{"since", in.Since, &q.Since}, {"until", in.Until, &q.Until}} {
		if d.v == "" {
			continue
		}
		t, err := utc.Parse(d.v)
		if err != nil {
			problems = append(problems, d.name+": "+err.Error())
			continue
		}
		*d.dst = t
	}
	q.Limit = in.Limit
	if q.Limit <= 0 {
		q.Limit = contactPage
	}
	if q.Limit > maxContactPage {
		problems = append(problems, fmt.Sprintf("limit: at most %d at a time", maxContactPage))
	}
	if in.Cursor != "" {
		n, err := strconv.Atoi(in.Cursor)
		if err != nil || n < 0 {
			problems = append(problems, "cursor: pass the next value from the previous page unchanged")
		}
		q.Offset = n
	}
	if len(problems) > 0 {
		return ContactPage{}, &InputError{Problems: problems}
	}
	cs, total, err := s.ListContacts(ctx, q)
	if err != nil {
		return ContactPage{}, err
	}
	page := ContactPage{Contacts: cs, Total: total}
	if in.ID != 0 {
		if total == 0 {
			return page, &InputError{Problems: []string{fmt.Sprintf("contact_id: no contact %d", in.ID)}}
		}
		page.Threads, _, err = s.ListOutreach(ctx, store.OutreachQuery{ContactID: in.ID, NewestFirst: true, Limit: maxOutreachPage})
		return page, err
	}
	if q.Offset+len(cs) < total {
		page.Next = strconv.Itoa(q.Offset + len(cs))
	}
	return page, nil
}
