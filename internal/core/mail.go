package core

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Limits for email the agent has handled. jhh keeps ids, links and a
// one-line note of what was done, never a message's text.
const (
	maxMessages  = 100 // checked or marked in one call
	maxActionLen = 200
	maxMailIDLen = 500
	maxRefURLLen = 2_000
	maxRefs      = 20 // added or removed in one call
)

var mailSystem = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,19}$`)

// checkSystem checks a mail system's name, such as gmail.
func checkSystem(field, system string) []string {
	if !mailSystem.MatchString(system) {
		return []string{fmt.Sprintf("%s: a short lowercase word naming where the mail lives, such as gmail or outlook", field)}
	}
	return nil
}

// checkMailID checks a message, thread or other id from a mail system.
func checkMailID(field, id string) []string {
	n := utf8.RuneCountInString(id)
	if n == 0 || n > maxMailIDLen || strings.IndexFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return []string{fmt.Sprintf("%s: the system's id, 1 to %d characters without spaces", field, maxMailIDLen)}
	}
	return nil
}

// MarkInput is a message to mark handled.
type MarkInput struct {
	MessageID, Action string
	JobID, OutreachID int64
}

// HandledInput is a handled_messages call.
type HandledInput struct {
	System         string
	Check          []string
	Mark           []MarkInput
	CheckedThrough string // RFC 3339
}

// HandledResult is what handled_messages returns. CheckedThrough is
// how far the agent has read the system, or zero.
type HandledResult struct {
	Handled        []store.HandledMessage // of the ids checked
	Marked         int
	AlreadyHandled []store.HandledMessage // of the ids marked: kept as first marked
	CheckedThrough time.Time
}

// HandledMessages checks and marks messages the agent handled, and keeps
// how far it has read.
func HandledMessages(ctx context.Context, s *store.Store, by store.Actor, in HandledInput) (HandledResult, error) {
	var (
		r        HandledResult
		problems = checkSystem("system", strings.TrimSpace(in.System))
		system   = strings.TrimSpace(in.System)
	)
	if len(in.Check) > maxMessages {
		problems = append(problems, fmt.Sprintf("check: at most %d ids at a time", maxMessages))
	}
	for i, id := range in.Check {
		problems = append(problems, checkMailID(fmt.Sprintf("check[%d]", i), id)...)
	}
	if len(in.Mark) > maxMessages {
		problems = append(problems, fmt.Sprintf("mark: at most %d messages at a time", maxMessages))
	}
	marks := make([]store.NewHandled, 0, len(in.Mark))
	seen := map[string]bool{}
	for i, m := range in.Mark {
		field := fmt.Sprintf("mark[%d]", i)
		problems = append(problems, checkMailID(field+".message_id", m.MessageID)...)
		if seen[m.MessageID] {
			problems = append(problems, field+".message_id: listed twice")
		}
		seen[m.MessageID] = true
		action := strings.TrimSpace(m.Action)
		if n := utf8.RuneCountInString(action); n == 0 || n > maxActionLen || strings.ContainsAny(action, "\r\n") {
			problems = append(problems, fmt.Sprintf("%s.action: what you did, in one line of up to %d characters, such as \"marked the job rejected\"", field, maxActionLen))
		}
		marks = append(marks, store.NewHandled{MessageID: m.MessageID, Action: action, JobID: m.JobID, OutreachID: m.OutreachID})
	}
	var through time.Time
	if in.CheckedThrough != "" {
		t, err := utc.Parse(in.CheckedThrough)
		switch {
		case err != nil:
			problems = append(problems, "checked_through: "+err.Error())
		case t.After(utc.Now().Add(futureAllowance)):
			problems = append(problems, "checked_through: is in the future")
		default:
			through = t
		}
	}
	if len(problems) > 0 {
		return r, &InputError{Problems: problems}
	}
	var err error
	if len(marks) > 0 {
		if r.Marked, r.AlreadyHandled, err = s.MarkHandled(ctx, by, system, marks); err != nil {
			return r, notFoundInput(err)
		}
	}
	if !through.IsZero() {
		if err := s.SetMailCheck(ctx, by, system, through); err != nil {
			return r, err
		}
	}
	if r.Handled, err = s.HandledMessages(ctx, system, in.Check); err != nil {
		return r, err
	}
	r.CheckedThrough, err = s.MailCheck(ctx, system)
	return r, err
}

// RefInput is a link to the email thread about something.
type RefInput struct {
	System, Ref, URL string
}

// HasRefs reports whether a call adds or removes links.
func HasRefs(add []RefInput, remove []int64) bool { return len(add) > 0 || len(remove) > 0 }

// ChangeRefs checks and applies links to email threads for a job (its
// application) or an outreach thread.
func ChangeRefs(ctx context.Context, s *store.Store, by store.Actor, t store.RefTarget, add []RefInput, remove []int64) error {
	if !HasRefs(add, remove) {
		return nil
	}
	refs, problems := checkRefs(add, remove)
	if len(problems) > 0 {
		return &InputError{Problems: problems}
	}
	return notFoundInput(s.ChangeRefs(ctx, by, t, refs, remove))
}

// CheckRemovals checks that every link to remove belongs to the job or
// thread, before anything else in the call is saved.
func CheckRemovals(ctx context.Context, s *store.Store, t store.RefTarget, remove []int64) error {
	if len(remove) == 0 {
		return nil
	}
	return notFoundInput(s.RefsBelong(ctx, t, remove))
}

// CheckRefs checks links before anything else in a call is saved.
func CheckRefs(add []RefInput, remove []int64) error {
	if _, problems := checkRefs(add, remove); len(problems) > 0 {
		return &InputError{Problems: problems}
	}
	return nil
}

func checkRefs(add []RefInput, remove []int64) ([]store.NewRef, []string) {
	var problems []string
	if len(add) > maxRefs || len(remove) > maxRefs {
		problems = append(problems, fmt.Sprintf("add_refs: at most %d links added or removed at a time", maxRefs))
	}
	refs := make([]store.NewRef, 0, len(add))
	for i, r := range add {
		field := fmt.Sprintf("add_refs[%d]", i)
		system, ref, link := strings.TrimSpace(r.System), strings.TrimSpace(r.Ref), strings.TrimSpace(r.URL)
		problems = append(problems, checkSystem(field+".system", system)...)
		problems = append(problems, checkMailID(field+".ref", ref)...)
		if link != "" {
			u, err := url.Parse(link)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(link) > maxRefURLLen {
				problems = append(problems, fmt.Sprintf("%s.url: an https link to open the thread, up to %d characters", field, maxRefURLLen))
			}
		}
		refs = append(refs, store.NewRef{System: system, Ref: ref, URL: link})
	}
	for i, id := range remove {
		if id <= 0 || slices.Contains(remove[:i], id) {
			problems = append(problems, fmt.Sprintf("remove_refs[%d]: a link's id, once", i))
		}
	}
	return refs, problems
}

// notFoundInput turns the store's "no such job or thread" into an input
// error. The store's messages hold only ids.
func notFoundInput(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return &InputError{Problems: []string{strings.TrimSuffix(err.Error(), ": "+store.ErrNotFound.Error())}}
	}
	return err
}
