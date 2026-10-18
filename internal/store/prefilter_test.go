package store

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestPrefilterMovesUnscreenedJobsOnly(t *testing.T) {
	f := newFetchFixture(t)
	ctx := context.Background()
	f.apply(posting("1", described("a")), posting("2", described("b")), posting("3", described("c")))
	j1, j2, j3 := f.jobOf("gh:exampleco:1"), f.jobOf("gh:exampleco:2"), f.jobOf("gh:exampleco:3")
	if err := f.s.SetJobStatus(ctx, ActorUser, j3, StatusApplied); err != nil {
		t.Fatal(err)
	}

	reasons := json.RawMessage(`[{"rule":"titles.exclude_any","effect":"drop","text":"title has \"manager\""}]`)
	version, out, err := f.s.SetFilters(ctx, ActorUser, NewFilters{Filters: json.RawMessage(`{"schema_version":1}`), Rationale: "first"},
		[]PrefilterDecision{
			{JobID: j1, Status: "pass", Score: 4},
			{JobID: j2, Status: "drop", Reasons: reasons},
			{JobID: j3, Status: "drop", Reasons: reasons},
		})
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 || out.Changed != 3 || !slices.Equal(out.Passed, []int64{j1}) || !slices.Equal(out.FilteredOut, []int64{j2}) {
		t.Errorf("version %d, outcome %+v", version, out)
	}
	for id, want := range map[int64]JobStatus{j1: StatusUnscreened, j2: StatusFilteredOut, j3: StatusApplied} {
		if j, _ := f.s.Job(ctx, id); j.Status != want {
			t.Errorf("job %d: status %s, want %s", id, j.Status, want)
		}
	}
	es, _ := f.s.JobEvents(ctx, j2)
	if last := es[len(es)-1]; last.Kind != "status_changed" || last.Actor != ActorUser {
		t.Errorf("last event %+v", last)
	}

	// The same decision again changes nothing; pending sends a job back to new.
	out, err = f.s.ApplyPrefilter(ctx, ActorSystem, version, []PrefilterDecision{{JobID: j1, Status: "pass", Score: 4}, {JobID: j2, Status: "pending"}})
	if err != nil || out.Changed != 1 || !slices.Equal(out.Waiting, []int64{j2}) {
		t.Errorf("rerun: %+v, %v", out, err)
	}

	active, ok, err := f.s.ActiveFilters(ctx)
	if err != nil || !ok || active.Version != 1 || active.Rationale != "first" || active.CreatedBy != ActorUser {
		t.Errorf("active filters %+v, %v, %v", active, ok, err)
	}
	if v, _, _ := f.s.SetFilters(ctx, ActorUser, NewFilters{Filters: json.RawMessage(`{"schema_version":1}`), Rationale: "second"}, nil); v != 2 {
		t.Errorf("second version = %d", v)
	}
	if active, _, _ = f.s.ActiveFilters(ctx); active.Version != 2 {
		t.Errorf("active version %d, want 2", active.Version)
	}

	cs, err := f.s.Candidates(ctx, StatusUnscreened, 10)
	if err != nil || len(cs) != 1 || cs[0].ID != j1 || cs[0].Company != "Exampleco" || cs[0].Score != 4 {
		t.Errorf("candidates %+v, %v", cs, err)
	}
	counts, _ := f.s.StatusCounts(ctx)
	if counts[StatusUnscreened] != 1 || counts[StatusNew] != 1 || counts[StatusApplied] != 1 {
		t.Errorf("counts %v", counts)
	}
}

func TestPrefilterJobsKnowWhenDescriptionsAreMissing(t *testing.T) {
	f := newFetchFixture(t)
	ctx := context.Background()
	p := posting("1", partial, located("Columbus, OH", "hybrid"))
	p.EmploymentType = "Full-time"
	f.apply(p)
	js, err := f.s.PrefilterJobs(ctx, f.company)
	if err != nil || len(js) != 1 {
		t.Fatalf("%+v, %v", js, err)
	}
	j := js[0]
	if j.DescriptionKnown || j.Company != "Exampleco" || j.Workplace != "hybrid" || j.EmploymentType != "Full-time" || string(j.Locations) != `[{"city":"Columbus, OH"}]` {
		t.Errorf("job %+v", j)
	}

	// Nothing needs a detail until the job passes stage 1.
	refs, _ := f.s.PostingsNeedingDetail(ctx, f.company, "greenhouse")
	if len(refs) != 0 {
		t.Errorf("needing detail before the prefilter: %+v", refs)
	}
	if _, err := f.s.ApplyPrefilter(ctx, ActorSystem, 1, []PrefilterDecision{{JobID: j.ID, Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	refs, _ = f.s.PostingsNeedingDetail(ctx, f.company, "greenhouse")
	if len(refs) != 1 || refs[0].ExternalID != "1" {
		t.Fatalf("needing detail: %+v", refs)
	}

	full := posting("1", described("fp"), located("Columbus, OH", "hybrid"))
	full.SourceUpdatedAt = f.clock.Add(-time.Hour)
	if _, err := f.s.ApplyDetails(ctx, ActorSystem, f.company, []PostingInput{full}); err != nil {
		t.Fatal(err)
	}
	js, _ = f.s.PrefilterJobs(ctx, f.company)
	if !js[0].DescriptionKnown || js[0].Description != longDescription {
		t.Errorf("after details: %+v", js[0])
	}
	if refs, _ = f.s.PostingsNeedingDetail(ctx, f.company, "greenhouse"); len(refs) != 0 {
		t.Errorf("still needing detail: %+v", refs)
	}

	// The board says the posting changed after the detail was read.
	f.clock = f.clock.Add(time.Hour)
	later := posting("1", partial, located("Columbus, OH", "hybrid"))
	later.SourceUpdatedAt = f.clock
	f.apply(later)
	if refs, _ = f.s.PostingsNeedingDetail(ctx, f.company, "greenhouse"); len(refs) != 1 {
		t.Errorf("an updated posting should need its detail again: %+v", refs)
	}
}
