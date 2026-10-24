package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrationMovesSearchToSources checks migration 0003: the active
// filters' search section becomes source settings, without changing what
// runs, and the filters are saved again without it.
func TestMigrationMovesSearchToSources(t *testing.T) {
	tests := []struct {
		name    string
		filters string // the active filters before; "" for none
		want    map[string]string
	}{
		{
			name:    "both sources",
			filters: `{"schema_version":1,"titles":{"require_any":["engineer"]},"search":{"themuse":[{"categories":["Software Engineering"],"pages":5}],"hackernews":{"months":1}}}`,
			want:    map[string]string{"themuse": `default {"queries":[{"categories":["Software Engineering"],"pages":5}]}`, "hackernews": `default {"months":1}`},
		},
		{
			name:    "hacker news with its default",
			filters: `{"schema_version":1,"search":{"hackernews":{}}}`,
			want:    map[string]string{"hackernews": "default {}"},
		},
		{
			name:    "no search keeps hacker news off",
			filters: `{"schema_version":1,"titles":{"require_any":["engineer"]}}`,
			want:    map[string]string{"hackernews": "off {}"},
		},
		{name: "no filters", want: map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jhh.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ms, err := migrations()
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
			if err := migrate(ctx, db, ms[:2], "", now); err != nil {
				t.Fatal(err)
			}
			if tt.filters != "" {
				if _, err := db.Exec(`INSERT INTO filter_versions (filters, rationale, active, created_at, created_by)
					VALUES ('{"schema_version":1}', 'old', 0, '2026-10-01T00:00:00.000Z', 'user'),
					       (?, 'current', 1, '2026-10-02T00:00:00.000Z', 'agent:test')`, tt.filters); err != nil {
					t.Fatal(err)
				}
			}
			if err := migrate(ctx, db, ms, "", now); err != nil {
				t.Fatal(err)
			}

			s := &Store{db: db, now: func() time.Time { return now }}
			rows, err := s.SourceRows(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for name, r := range rows {
				on := "default"
				if r.Enabled != nil {
					on = map[bool]string{true: "on", false: "off"}[*r.Enabled]
				}
				got[name] = on + " " + string(r.Settings)
				if r.UpdatedBy != ActorSystem || r.UpdatedAt.IsZero() {
					t.Errorf("%s: updated by %q at %v", name, r.UpdatedBy, r.UpdatedAt)
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("sources = %v, want %v", got, tt.want)
			}
			for name, w := range tt.want {
				if got[name] != w {
					t.Errorf("%s = %q, want %q", name, got[name], w)
				}
			}

			f, ok, err := s.ActiveFilters(ctx)
			switch {
			case err != nil:
				t.Fatal(err)
			case tt.filters == "":
				if ok {
					t.Errorf("filters appeared: %s", f.Filters)
				}
				return
			case !ok:
				t.Fatal("no active filters after migrating")
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(f.Filters, &m); err != nil {
				t.Fatal(err)
			}
			if _, has := m["search"]; has {
				t.Errorf("active filters still have search: %s", f.Filters)
			}
			hadSearch := tt.name != "no search keeps hacker news off"
			switch {
			case hadSearch && (f.Version != 3 || f.CreatedBy != ActorSystem):
				t.Errorf("active version %d by %s, want a new version 3 by jhh", f.Version, f.CreatedBy)
			case !hadSearch && f.Version != 2:
				t.Errorf("filters without search were saved again as version %d", f.Version)
			}
			var n int
			if err := db.QueryRow("SELECT count(*) FROM filter_versions WHERE active = 1").Scan(&n); err != nil || n != 1 {
				t.Errorf("%d active filter versions, %v", n, err)
			}
		})
	}
}

func TestSetSource(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)
	off := false
	if err := s.SetSource(ctx, Agent("test"), "hackernews", SourceChange{Enabled: &off, Reason: "too noisy"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSourceRun(ctx, "hackernews", SourceRun{At: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC), Postings: 12, Error: "busy"}); err != nil {
		t.Fatal(err)
	}
	// Changing the settings keeps the switch and the last run.
	if err := s.SetSource(ctx, ActorUser, "hackernews", SourceChange{Settings: json.RawMessage(`{"months":3}`)}); err != nil {
		t.Fatal(err)
	}
	r := mustSourceRows(t, s)["hackernews"]
	if r.Enabled == nil || *r.Enabled || string(r.Settings) != `{"months":3}` || r.LastPostings != 12 || r.LastError != "busy" ||
		r.UpdatedBy != ActorUser || r.Reason != "" {
		t.Errorf("%+v", r)
	}
	// A run of a source nobody set up records the run only.
	if err := s.RecordSourceRun(ctx, "themuse", SourceRun{At: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if m := mustSourceRows(t, s)["themuse"]; m.Enabled != nil || string(m.Settings) != "{}" || m.UpdatedBy != ActorSystem {
		t.Errorf("%+v", m)
	}
	var kinds int
	if err := s.db.QueryRow("SELECT count(*) FROM events WHERE kind = 'source_changed'").Scan(&kinds); err != nil || kinds != 2 {
		t.Errorf("%d source_changed events, %v; want one per change", kinds, err)
	}
}

func mustSourceRows(t *testing.T, s *Store) map[string]SourceRow {
	t.Helper()
	rows, err := s.SourceRows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
