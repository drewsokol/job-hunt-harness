package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// The shortlist: the agent's answer to "what should I apply to next?",
// which the user can adjust on the dashboard.

const (
	// MaxShortlist is how many jobs the shortlist holds.
	MaxShortlist       = 30
	maxShortlistReason = 120
	// UserShortlistReason is the reason of a job the user added.
	UserShortlistReason = "added by you"
)

// ShortlistActions are the changes the dashboard makes to the shortlist.
var ShortlistActions = []string{"up", "down", "add", "remove"}

// SetShortlist replaces the whole shortlist with entries, in order, and
// returns it as shown.
func SetShortlist(ctx context.Context, s *store.Store, by store.Actor, entries []store.ShortlistEntry) ([]store.JobRow, error) {
	var problems []string
	if len(entries) > MaxShortlist {
		problems = append(problems, fmt.Sprintf("jobs: at most %d; the shortlist is what to apply to next, not every job in review", MaxShortlist))
	}
	seen := map[int64]bool{}
	for i := range entries {
		e := &entries[i]
		field := fmt.Sprintf("jobs[%d]", i)
		e.Reason = strings.TrimSpace(e.Reason)
		if e.JobID <= 0 {
			problems = append(problems, field+".job_id: a job number")
		} else if seen[e.JobID] {
			problems = append(problems, fmt.Sprintf("%s.job_id: job %d is listed twice", field, e.JobID))
		}
		seen[e.JobID] = true
		if n := utf8.RuneCountInString(e.Reason); n == 0 || n > maxShortlistReason {
			problems = append(problems, fmt.Sprintf("%s.reason: why now, in your own words (1 to %d characters)", field, maxShortlistReason))
		}
	}
	if len(problems) > 0 {
		return nil, &InputError{Problems: problems}
	}
	if err := s.SetShortlist(ctx, by, entries); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, &InputError{Problems: []string{"jobs: " + strings.TrimSuffix(err.Error(), ": "+store.ErrConflict.Error())}}
		}
		return nil, err
	}
	return s.ShortlistJobs(ctx)
}

// ChangeShortlist makes one of the dashboard's changes to the shortlist:
// up and down swap a job with its neighbor among the jobs shown, add puts
// a job in review at the bottom, and remove takes it off.
func ChangeShortlist(ctx context.Context, s *store.Store, by store.Actor, jobID int64, action string) error {
	var err error
	switch action {
	case "up", "down":
		err = s.MoveOnShortlist(ctx, by, jobID, action == "up")
	case "add":
		err = s.AddToShortlist(ctx, by, jobID, UserShortlistReason, MaxShortlist)
	case "remove":
		err = s.RemoveFromShortlist(ctx, by, jobID)
	default:
		return &InputError{Problems: []string{fmt.Sprintf("action: %q must be one of %s", action, strings.Join(ShortlistActions, ", "))}}
	}
	if errors.Is(err, store.ErrNotFound) {
		return &InputError{Problems: []string{strings.TrimSuffix(err.Error(), ": "+store.ErrNotFound.Error())}}
	}
	return conflictInput(err)
}

// ShortlistSetBy names who put a job in its place, as tools show it:
// the user or an agent.
func ShortlistSetBy(by store.Actor) string {
	if by == store.ActorUser {
		return "user"
	}
	return "agent"
}
