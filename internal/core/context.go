package core

import (
	"context"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// ScreeningContext is everything an agent needs to judge fit: the profile
// (the rubric), the resume, the active memories and the active filters.
type ScreeningContext struct {
	Profile  *store.ProfileVersion
	Resume   *store.ResumeVersion
	Memories []store.Memory // active, grouped by kind
	Filters  *store.FilterVersion
}

// GetContext reads the screening context.
func GetContext(ctx context.Context, s *store.Store) (ScreeningContext, error) {
	var c ScreeningContext
	if p, ok, err := s.Profile(ctx); err != nil {
		return c, err
	} else if ok {
		c.Profile = &p
	}
	if r, ok, err := s.Resume(ctx); err != nil {
		return c, err
	} else if ok {
		c.Resume = &r
	}
	ms, err := s.Memories(ctx, store.MemoryQuery{})
	if err != nil {
		return c, err
	}
	c.Memories = ms
	if f, ok, err := s.ActiveFilters(ctx); err != nil {
		return c, err
	} else if ok {
		c.Filters = &f
	}
	return c, nil
}

// SetupState is what exists and what is still missing, where the
// initialize playbook starts. Now and the machine's zone let the agent turn
// the user's local times into UTC.
type SetupState struct {
	Now            time.Time
	TimeZone       string // the zone's name on this machine, such as "EDT"
	UTCOffset      string // "-04:00"
	ProfileVersion int64  // 0 when there is none
	ResumeVersion  int64
	FiltersVersion int64
	SearchesOn     int // search sources that are on
	Counts         store.SetupCounts
	// Missing lists the next steps, in order, in words the agent can pass on.
	Missing []string
}

// GetSetupState reads the setup state.
func GetSetupState(ctx context.Context, s *store.Store) (SetupState, error) {
	now := utc.Now()
	zone, offset := time.Now().Zone()
	st := SetupState{Now: now, TimeZone: zone, UTCOffset: formatOffset(offset)}
	if p, ok, err := s.Profile(ctx); err != nil {
		return st, err
	} else if ok {
		st.ProfileVersion = p.Version
	}
	if r, ok, err := s.Resume(ctx); err != nil {
		return st, err
	} else if ok {
		st.ResumeVersion = r.Version
	}
	if f, ok, err := s.ActiveFilters(ctx); err != nil {
		return st, err
	} else if ok {
		st.FiltersVersion = f.Version
	}
	ss, err := ListSources(ctx, s, nil)
	if err != nil {
		return st, err
	}
	st.SearchesOn = SearchesOn(ss)
	if st.Counts, err = s.SetupCounts(ctx); err != nil {
		return st, err
	}
	missing := func(cond bool, step string) {
		if cond {
			st.Missing = append(st.Missing, step)
		}
	}
	missing(st.ResumeVersion == 0, "resume: no resume text yet; ask whether there's one you can read, then call set_resume_text")
	missing(st.ProfileVersion == 0, "profile: no profile yet; interview the user, draft it, and save it with update_profile once they agree")
	missing(st.FiltersVersion == 0, "filters: none set; draft them, run set_filters with dry_run, show the user the effect, then save them with set_filters")
	missing(st.FiltersVersion != 0 && st.SearchesOn == 0 && st.Counts.BoardCompanies == 0,
		"sources: no search source is on and no company boards are read, so refresh finds nothing; turn one on with set_source (list_sources shows them), or add companies with set_company")
	missing(st.Counts.LastRefreshAt.IsZero(), "refresh: jobs were never fetched; call refresh_jobs")
	missing(st.Counts.Screenings == 0, "screening: no job has been screened yet; follow the screen_new_jobs playbook")
	return st, nil
}

func formatOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	return sign + time.Time{}.Add(time.Duration(seconds)*time.Second).Format("15:04")
}
