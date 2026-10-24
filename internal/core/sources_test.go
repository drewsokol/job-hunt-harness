package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

func sourceByName(t *testing.T, s *store.Store, name string) SourceInfo {
	t.Helper()
	ss, err := ListSources(context.Background(), s, []string{"lever", "greenhouse"})
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range ss {
		if info.Name == name {
			return info
		}
	}
	t.Fatalf("no source %s in %+v", name, ss)
	return SourceInfo{}
}

func TestSourceDefaults(t *testing.T) {
	f := newRefreshFixture(t, "exampleco")
	ss, err := ListSources(context.Background(), f.s, []string{"lever", "fake"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, info := range ss {
		got = append(got, info.Name+":"+info.Kind+":"+info.Status)
	}
	// Hacker News needs no setup, so it's on; The Muse waits for a query.
	if strings.Join(got, " ") != "hackernews:search:on himalayas:search:needs setup themuse:search:needs setup fake:board: lever:board:" {
		t.Errorf("sources = %v", got)
	}
	if hn := ss[0]; string(hn.Settings) != `{"months":2}` || hn.Chosen {
		t.Errorf("hacker news defaults: %+v", hn)
	}
	if m := ss[2]; !strings.Contains(m.Setup, "at least one query") || string(m.Settings) != `{"queries":[]}` {
		t.Errorf("the muse: %+v", m)
	}
	if b := ss[3]; b.Title != "fake" || b.Companies != 1 || b.EnabledCompanies != 1 {
		t.Errorf("the board of the one company: %+v", b)
	}
	if l := ss[4]; l.Title != "Lever" || l.Companies != 0 {
		t.Errorf("lever: %+v", l)
	}
}

func TestSetSource(t *testing.T) {
	ctx := context.Background()
	f := newRefreshFixture(t)
	agent := store.Agent("test")

	// Setting The Muse up turns it on.
	info, err := SetSource(ctx, f.s, agent, "themuse", SourceUpdate{Settings: json.RawMessage(`{"queries": [{"categories": ["Software Engineering"]}]}`)})
	if err != nil || info.Status != SourceOn || info.UpdatedBy != agent {
		t.Fatalf("%+v, %v", info, err)
	}
	// Turning it off pauses it and keeps its queries.
	off := false
	if info, err = SetSource(ctx, f.s, agent, "TheMuse", SourceUpdate{Enabled: &off, Reason: " The user wants startups only. "}); err != nil {
		t.Fatal(err)
	}
	if info.Status != SourceOff || !strings.Contains(string(info.Settings), "Software Engineering") || info.Reason != "The user wants startups only." {
		t.Errorf("after turning it off: %+v", info)
	}
	// Turned off, it stays off when its settings change.
	if info, _ = SetSource(ctx, f.s, store.ActorUser, "themuse", SourceUpdate{Settings: json.RawMessage(`{"queries": [{"locations": ["Columbus, OH"]}]}`)}); info.Status != SourceOff {
		t.Errorf("new settings turned a paused source back on: %+v", info)
	}

	for _, tt := range []struct {
		name string
		u    SourceUpdate
		want string
	}{
		{"greenhouse", SourceUpdate{Enabled: &off}, "set_company_enabled"},
		{"monster", SourceUpdate{Enabled: &off}, `"monster" is not a search source; use one of hackernews, himalayas, themuse`},
		{"hackernews", SourceUpdate{}, "say what to change"},
		{"hackernews", SourceUpdate{Settings: json.RawMessage(`{"months": 4}`)}, "settings.months: must be between 1 and 3"},
		{"hackernews", SourceUpdate{Settings: json.RawMessage(`{"month": 2}`)}, `unknown field "month"`},
		{"themuse", SourceUpdate{Settings: json.RawMessage(`{"queries": [{"levels": ["Senior Level"]}]}`)}, "settings.queries[0]: set categories, locations or both"},
		{"themuse", SourceUpdate{Settings: json.RawMessage(`{"queries": [{"locations": ["Remote"], "pages": 251}]}`)}, "settings.queries[0].pages: must be between 1 and 250"},
		{"hackernews", SourceUpdate{Enabled: &off, Reason: strings.Repeat("x", maxSourceReason+1)}, "reason: longer than"},
	} {
		_, err := SetSource(ctx, f.s, agent, tt.name, tt.u)
		var in *InputError
		if !errors.As(err, &in) || !strings.Contains(in.Error(), tt.want) {
			t.Errorf("SetSource(%s, %+v) = %v, want an input error containing %q", tt.name, tt.u, err, tt.want)
		}
	}
	if hn := sourceByName(t, f.s, "hackernews"); hn.Status != SourceOn || hn.Chosen {
		t.Errorf("a refused change was saved: %+v", hn)
	}
}

func TestPausedSourceIsNotReadOrClosed(t *testing.T) {
	ctx := context.Background()
	f, s := newSearchFixture(t, musePosting(1, "Made-Up Labs", "Platform Engineer"))
	f.setFilters(searchFilters(10))
	res := f.refresh()
	if muse(res).Postings != 1 {
		t.Fatalf("first refresh %+v", res.Searches)
	}
	if m := sourceByName(t, f.s, "themuse"); m.LastRunAt.IsZero() || m.LastPostings != 1 || m.LastError != "" {
		t.Errorf("last run not recorded: %+v", m)
	}

	off := false
	if _, err := SetSource(ctx, f.s, store.ActorUser, "themuse", SourceUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	s.postings = nil // the job is gone from The Muse
	searched := s.searches
	res = f.refresh()
	if len(res.Searches) != 0 || s.searches != searched {
		t.Errorf("a paused source was read: %+v", res.Searches)
	}
	if j := f.jobsByTitle()["Platform Engineer"]; j.Closed {
		t.Error("a paused source's job was closed")
	}

	on := true
	if _, err := SetSource(ctx, f.s, store.ActorUser, "themuse", SourceUpdate{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	if j := f.jobsByTitle()["Platform Engineer"]; !j.Closed {
		t.Error("back on, a job the source no longer lists should close")
	}
}

func TestSourceRunRecordsTheFirstError(t *testing.T) {
	f, s := newSearchFixture(t, musePosting(1, "Made-Up Labs", "Platform Engineer"))
	s.fail = errors.New("the service answered 500")
	f.setFilters(searchFilters(10))
	f.refresh()
	if m := sourceByName(t, f.s, "themuse"); m.LastRunAt.IsZero() || !strings.Contains(m.LastError, "answered 500") {
		t.Errorf("%+v", m)
	}
}

func TestSetupStateCountsSearchSources(t *testing.T) {
	ctx := context.Background()
	f := newRefreshFixture(t)
	f.setFilters(`{"schema_version": 1}`)
	st, err := GetSetupState(ctx, f.s)
	if err != nil || st.SearchesOn != 1 {
		t.Fatalf("%+v, %v", st, err)
	}
	off := false
	if _, err := SetSource(ctx, f.s, store.ActorUser, "hackernews", SourceUpdate{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	st, _ = GetSetupState(ctx, f.s)
	if st.SearchesOn != 0 || !strings.Contains(strings.Join(st.Missing, "\n"), "no search source is on") {
		t.Errorf("%+v", st)
	}
	if paused := PausedSearches(mustSources(t, f.s)); len(paused) != 1 || paused[0] != sources.SearchTitle("hackernews") {
		t.Errorf("paused = %v", paused)
	}
}

func mustSources(t *testing.T, s *store.Store) []SourceInfo {
	t.Helper()
	ss, err := ListSources(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}
