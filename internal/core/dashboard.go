package core

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// What the dashboard does that agents don't: reviewing content flags,
// overriding the prefilter, and the suggested ask.

// ReviewFlag records the user's decision on a content flag: keep (as kind,
// or as it was), hide or reopen. It returns the flagged job.
func ReviewFlag(ctx context.Context, s *store.Store, by store.Actor, flagID int64, action, kind string) (int64, error) {
	var problems []string
	if !slices.Contains([]string{store.FlagKeep, store.FlagHide, store.FlagReopen}, action) {
		problems = append(problems, fmt.Sprintf("action: %q must be keep, hide or reopen", action))
	}
	if kind != "" && (action != store.FlagKeep || !slices.Contains(FlagKinds, kind)) {
		problems = append(problems, fmt.Sprintf("kind: only when keeping, one of %s", strings.Join(FlagKinds, ", ")))
	}
	if len(problems) > 0 {
		return 0, &InputError{Problems: problems}
	}
	id, err := s.ReviewContentFlag(ctx, by, flagID, action, kind)
	return id, inputIfNotFound(err, fmt.Sprintf("flag: no content flag %d", flagID))
}

// SendToScreening moves a job the filters dropped to unscreened, where the
// agent will rate it, and keeps the prefilter from moving it back; off
// undoes that.
func SendToScreening(ctx context.Context, s *store.Store, by store.Actor, id int64, on bool) error {
	return conflictInput(inputIfNotFound(s.SetPrefilterOverride(ctx, by, id, on), fmt.Sprintf("job_id: no job %d", id)))
}

// AskSetting is the settings key for how far up a posted pay range the
// suggested ask sits, from 0 to 1.
const (
	AskSetting       = "ask_percentile"
	DefaultAskFactor = 0.85
)

// AskFactor returns the ask percentile setting, or the default.
func AskFactor(ctx context.Context, s *store.Store) (float64, error) {
	v, ok, err := s.Setting(ctx, AskSetting)
	if err != nil || !ok {
		return DefaultAskFactor, err
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1 {
		return DefaultAskFactor, nil
	}
	return f, nil
}

// SuggestedAsk is min + factor × (max − min) of a job's yearly pay range in
// currency, rounded to 5,000. It returns 0 unless exactly one such range
// is posted: with several (one per location, say) it would be a guess.
func SuggestedAsk(ranges []normalize.PayRange, currency string, factor float64) float64 {
	if currency == "" {
		return 0
	}
	var match []normalize.PayRange
	for _, r := range ranges {
		if strings.EqualFold(r.Currency, currency) && (r.Period == "year" || r.Period == "") && r.Max > r.Min && r.Min > 0 {
			match = append(match, r)
		}
	}
	if len(match) != 1 {
		return 0
	}
	r := match[0]
	return math.Round((r.Min+factor*(r.Max-r.Min))/5_000) * 5_000
}
