package mcpserver

import (
	"context"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// contactOut is one contact. Its fields are the agent's record of another
// person; the company can come from a job's listing, so it is inside
// untrusted, as in list_outreach.
type contactOut struct {
	ID            int64    `json:"contact_id"`
	Name          string   `json:"name"`
	Role          string   `json:"role,omitempty"`
	Relationship  string   `json:"relationship,omitempty"`
	Source        string   `json:"source,omitempty"`
	OrgType       string   `json:"org_type,omitempty"`
	Warmth        string   `json:"warmth,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	ReEngageAfter string   `json:"re_engage_after,omitempty"`
	Email         string   `json:"email,omitempty"`
	Phone         string   `json:"phone,omitempty"`
	ProfileURL    string   `json:"profile_url,omitempty"`
	Why           string   `json:"why,omitempty"`
	Notes         string   `json:"notes,omitempty"`
	Cut           bool     `json:"truncated,omitempty"`
	Threads       int      `json:"threads"`
	LastThreadAt  string   `json:"last_thread_at,omitempty"`
	Updated       string   `json:"updated_at,omitempty"`
	Untrusted     struct {
		Company string `json:"company,omitempty"`
	} `json:"untrusted"`
}

func contact(c store.Contact, cut bool) contactOut {
	o := contactOut{ID: c.ID, Name: c.Name, Role: c.Title, Relationship: c.Relationship, Source: c.Source, OrgType: c.OrgType,
		Warmth: c.Warmth, Tags: c.Tags, ReEngageAfter: timeOrEmpty(c.ReEngageAfter), Email: c.Email, Phone: c.Phone,
		ProfileURL: c.ProfileURL, Why: c.Why, Notes: c.Notes, Threads: c.Threads, LastThreadAt: timeOrEmpty(c.LastThreadAt),
		Updated: timeOrEmpty(c.UpdatedAt)}
	o.Untrusted.Company = c.Company
	if cut {
		for _, f := range []*string{&o.Why, &o.Notes} {
			if r := []rune(*f); len(r) > listNoteChars {
				*f, o.Cut = string(r[:listNoteChars]), true
			}
		}
	}
	return o
}

func contacts(cs []store.Contact, cut bool) []contactOut {
	out := make([]contactOut, 0, len(cs))
	for _, c := range cs {
		out = append(out, contact(c, cut))
	}
	return out
}

func (t *tools) addContactTools(s *mcp.Server) {
	type saveIn struct {
		ContactID     int64     `json:"contact_id,omitempty" jsonschema:"a contact to change; leave out to add one"`
		Name          *string   `json:"name,omitempty" jsonschema:"needed for a new contact; the same name at the same company is reused"`
		Role          *string   `json:"role,omitempty" jsonschema:"their job title"`
		Company       *string   `json:"company,omitempty" jsonschema:"who they work for: the agency, for a recruiter at one"`
		Relationship  *string   `json:"relationship,omitempty" jsonschema:"who they are to the user, such as recruiter, hiring_manager, referral, former_coworker, alumni, friend"`
		Source        *string   `json:"source,omitempty" jsonschema:"where the user found them, such as linkedin, referral, event, email"`
		OrgType       *string   `json:"org_type,omitempty" jsonschema:"agency, employer or other"`
		Warmth        *string   `json:"warmth,omitempty" jsonschema:"cold, warm or advocate"`
		Tags          *[]string `json:"tags,omitempty" jsonschema:"short lowercase words; replaces the whole list"`
		ReEngageAfter *string   `json:"re_engage_after,omitempty" jsonschema:"UTC, RFC 3339: when to get back in touch; empty clears it"`
		Email         *string   `json:"email,omitempty"`
		Phone         *string   `json:"phone,omitempty"`
		ProfileURL    *string   `json:"profile_url,omitempty"`
		Why           *string   `json:"why,omitempty" jsonschema:"why they matter, in a sentence or two"`
		Notes         *string   `json:"notes,omitempty" jsonschema:"replaces the notes"`
	}
	add(t, s, "save_contact", writes, func(ctx context.Context, c call, in saveIn) (any, error) {
		saved, err := core.SaveContact(ctx, t.Store, c.by, in.ContactID, core.ContactInput{Name: in.Name, Role: in.Role, Company: in.Company,
			Relationship: in.Relationship, Source: in.Source, Why: in.Why, ProfileURL: in.ProfileURL, Email: in.Email, Phone: in.Phone,
			Notes: in.Notes, OrgType: in.OrgType, Warmth: in.Warmth, Tags: in.Tags, ReEngageAfter: in.ReEngageAfter})
		return struct {
			ID  int64 `json:"contact_id"`
			New bool  `json:"new"`
		}{saved.ID, saved.New}, err
	})

	type listIn struct {
		ContactID    int64  `json:"contact_id,omitempty" jsonschema:"one contact, with every thread and the jobs they're about; give it alone"`
		Name         string `json:"name,omitempty" jsonschema:"part of the name"`
		Company      string `json:"company,omitempty" jsonschema:"part of the company's name"`
		OrgType      string `json:"org_type,omitempty" jsonschema:"agency, employer or other"`
		Warmth       string `json:"warmth,omitempty" jsonschema:"cold, warm or advocate"`
		Relationship string `json:"relationship,omitempty" jsonschema:"part of it"`
		Tag          string `json:"tag,omitempty"`
		ReEngage     string `json:"re_engage,omitempty" jsonschema:"due (the date to get back in touch has come) or set"`
		Since        string `json:"since,omitempty" jsonschema:"UTC, RFC 3339: last in touch (newest thread) at or after"`
		Until        string `json:"until,omitempty" jsonschema:"UTC, RFC 3339: last in touch before"`
		Limit        int    `json:"limit,omitempty" jsonschema:"default 20, at most 50"`
		Cursor       string `json:"cursor,omitempty" jsonschema:"next, from the previous page"`
	}
	add(t, s, "list_contacts", reads, func(ctx context.Context, _ call, in listIn) (any, error) {
		p, err := core.ListContacts(ctx, t.Store, core.ContactFilter{ID: in.ContactID, Name: in.Name, Company: in.Company, OrgType: in.OrgType,
			Warmth: in.Warmth, Relationship: in.Relationship, Tag: in.Tag, ReEngage: in.ReEngage, Since: in.Since, Until: in.Until,
			Limit: in.Limit, Cursor: in.Cursor})
		if err != nil {
			return nil, err
		}
		if in.ContactID != 0 {
			type jobRef struct {
				Job       int64 `json:"job"`
				Untrusted struct {
					Title string `json:"title"`
				} `json:"untrusted"`
			}
			jobs := []jobRef{}
			seen := map[int64]bool{}
			for _, o := range p.Threads {
				if o.JobID != 0 && !seen[o.JobID] {
					seen[o.JobID] = true
					j := jobRef{Job: o.JobID}
					j.Untrusted.Title = o.JobTitle
					jobs = append(jobs, j)
				}
			}
			return struct {
				contactOut
				Outreach []outreachOut `json:"outreach"`
				Jobs     []jobRef      `json:"jobs"`
			}{contact(p.Contacts[0], false), cutNotes(outreach(p.Threads)), jobs}, nil
		}
		return struct {
			Total    int          `json:"total"`
			Contacts []contactOut `json:"contacts"`
			Next     string       `json:"next,omitempty"`
		}{p.Total, contacts(p.Contacts, true), p.Next}, nil
	})
}
