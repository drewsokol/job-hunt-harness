package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/prefilter"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// ActiveFilters returns the active filters compiled, with their version,
// or nil when none have been set.
func ActiveFilters(ctx context.Context, s *store.Store) (*prefilter.Compiled, int64, error) {
	v, ok, err := s.ActiveFilters(ctx)
	if err != nil || !ok {
		return nil, 0, err
	}
	f, err := prefilter.Parse(v.Filters)
	if err != nil {
		return nil, 0, fmt.Errorf("stored filters version %d: %w", v.Version, err)
	}
	c, err := prefilter.Compile(f)
	return c, v.Version, err
}

// RunPrefilter judges the company's jobs (every job when companyID is 0)
// with the active filters and records what changed. With no filters set it
// does nothing.
func RunPrefilter(ctx context.Context, s *store.Store, by store.Actor, companyID int64) (store.PrefilterOutcome, error) {
	c, version, err := ActiveFilters(ctx, s)
	if err != nil || c == nil {
		return store.PrefilterOutcome{}, err
	}
	out, _, err := runPrefilter(ctx, s, by, c, version, companyID)
	return out, err
}

// runPrefilter judges and records, then drops the stored descriptions of
// jobs filtered out by stage 1, which never needed them. It returns how
// many postings lost their description.
func runPrefilter(ctx context.Context, s *store.Store, by store.Actor, c *prefilter.Compiled, version, companyID int64) (store.PrefilterOutcome, int, error) {
	jobs, err := s.PrefilterJobs(ctx, companyID)
	if err != nil {
		return store.PrefilterOutcome{}, 0, err
	}
	ds := make([]store.PrefilterDecision, 0, len(jobs))
	var unneeded []int64
	for _, j := range jobs {
		r := c.Evaluate(prefilterInput(j))
		ds = append(ds, decision(j.ID, r))
		if droppedWithoutDescription(c, j, r) {
			unneeded = append(unneeded, j.ID)
		}
	}
	out, err := s.ApplyPrefilter(ctx, by, version, ds)
	if err != nil || len(unneeded) == 0 {
		return out, 0, err
	}
	cleared, err := s.ClearDescriptions(ctx, unneeded)
	return out, cleared, err
}

// droppedWithoutDescription reports whether a job with a stored description
// is dropped by stage 1 alone, so the description isn't needed to keep the
// decision. Stage 2 drops keep theirs: the decision depends on it.
func droppedWithoutDescription(c *prefilter.Compiled, j store.PrefilterJob, r prefilter.Result) bool {
	if r.Status != prefilter.Drop || j.Description == "" {
		return false
	}
	in := prefilterInput(j)
	in.Description, in.DescriptionKnown = "", false
	return c.Evaluate(in).Status == prefilter.Drop
}

func prefilterInput(j store.PrefilterJob) prefilter.Input {
	in := prefilter.Input{
		Company: j.Company, Title: j.Title, Workplace: j.Workplace, EmploymentType: j.EmploymentType,
		Description: j.Description, DescriptionKnown: j.DescriptionKnown,
		SeveralRoles: sources.TitlesNameRoles(j.Via),
	}
	// Both columns are written by jhh from these types; a row that doesn't
	// decode is judged without it.
	_ = json.Unmarshal(j.Locations, &in.Places)
	_ = json.Unmarshal(j.PayRanges, &in.Pay)
	return in
}

func decision(id int64, r prefilter.Result) store.PrefilterDecision {
	reasons := r.Reasons
	if reasons == nil {
		reasons = []prefilter.Reason{}
	}
	return store.PrefilterDecision{JobID: id, Status: string(r.Status), Reasons: mustJSON(reasons), Score: r.Score}
}

// FilterPreview is the effect a filters change would have on the open jobs
// already stored.
type FilterPreview struct {
	Jobs         int `json:"jobs"`          // open jobs judged
	PassNow      int `json:"pass_now"`      // passing under the active filters
	PassAfter    int `json:"pass_after"`    // passing under the new ones
	PendingAfter int `json:"pending_after"` // waiting for a description under the new ones
	NewlyDropped int `json:"newly_dropped"`
	NewlyKept    int `json:"newly_kept"`
	// DropsTracked counts newly dropped jobs the candidate is already
	// reviewing or applying to. Their status doesn't change, but a filter
	// that drops them is probably wrong.
	DropsTracked int `json:"drops_tracked"`
	// Examples are up to five jobs of each kind. Titles are listing text.
	DroppedExamples []PreviewJob `json:"-"`
	KeptExamples    []PreviewJob `json:"-"`
}

// PreviewJob is an example in a preview.
type PreviewJob struct {
	ID      int64
	Company string
	Title   string // untrusted
	Reasons []prefilter.Reason
}

const previewExamples = 5

// PreviewFilters judges the stored open jobs with f and compares the
// result with their current decisions. It changes nothing.
func PreviewFilters(ctx context.Context, s *store.Store, f prefilter.Filters) (FilterPreview, error) {
	p, _, _, err := preview(ctx, s, f)
	return p, err
}

// preview also returns the decisions, and the jobs whose descriptions
// stage 1 makes unneeded.
func preview(ctx context.Context, s *store.Store, f prefilter.Filters) (FilterPreview, []store.PrefilterDecision, []int64, error) {
	var p FilterPreview
	c, err := prefilter.Compile(f)
	if err != nil {
		return p, nil, nil, err
	}
	jobs, err := s.PrefilterJobs(ctx, 0)
	if err != nil {
		return p, nil, nil, err
	}
	ds := make([]store.PrefilterDecision, 0, len(jobs))
	var unneeded []int64
	for _, j := range jobs {
		r := c.Evaluate(prefilterInput(j))
		ds = append(ds, decision(j.ID, r))
		if droppedWithoutDescription(c, j, r) {
			unneeded = append(unneeded, j.ID)
		}
		if j.Closed {
			continue
		}
		p.Jobs++
		before, after := j.PrefilterStatus == string(prefilter.Pass), r.Status == prefilter.Pass
		if before {
			p.PassNow++
		}
		switch r.Status {
		case prefilter.Pass:
			p.PassAfter++
		case prefilter.Pending:
			p.PendingAfter++
		}
		ex := PreviewJob{ID: j.ID, Company: j.Company, Title: j.Title, Reasons: r.Reasons}
		switch {
		case before && r.Status == prefilter.Drop:
			p.NewlyDropped++
			if tracked(j.Status) {
				p.DropsTracked++
			}
			if len(p.DroppedExamples) < previewExamples {
				p.DroppedExamples = append(p.DroppedExamples, ex)
			}
		case !before && after:
			p.NewlyKept++
			if len(p.KeptExamples) < previewExamples {
				p.KeptExamples = append(p.KeptExamples, ex)
			}
		}
	}
	return p, ds, unneeded, nil
}

// tracked reports whether the candidate has acted on a job in this status.
func tracked(st store.JobStatus) bool {
	switch st {
	case store.StatusReview, store.StatusApplied, store.StatusInterviewing, store.StatusOffer:
		return true
	}
	return false
}

// SetFilters saves f as the new active filters and re-judges every stored
// job with them, without fetching anything. The preview of its effect is
// saved with the version.
func SetFilters(ctx context.Context, s *store.Store, by store.Actor, f prefilter.Filters, rationale string) (int64, FilterPreview, store.PrefilterOutcome, error) {
	if rationale = strings.TrimSpace(rationale); rationale == "" {
		return 0, FilterPreview{}, store.PrefilterOutcome{}, &InputError{Problems: []string{"rationale: say in a sentence why the filters are changing"}}
	}
	p, ds, unneeded, err := preview(ctx, s, f)
	if err != nil {
		return 0, p, store.PrefilterOutcome{}, err
	}
	version, out, err := s.SetFilters(ctx, by, store.NewFilters{Filters: f.JSON(), Rationale: rationale, Effect: mustJSON(p)}, ds)
	if err == nil && len(unneeded) > 0 {
		_, err = s.ClearDescriptions(ctx, unneeded)
	}
	return version, p, out, err
}

// Places decodes a stored locations column for display.
func Places(raw json.RawMessage) []normalize.Place {
	var ps []normalize.Place
	_ = json.Unmarshal(raw, &ps)
	return ps
}

// PayRanges decodes a stored pay column for display.
func PayRanges(raw json.RawMessage) []normalize.PayRange {
	var rs []normalize.PayRange
	_ = json.Unmarshal(raw, &rs)
	return rs
}
