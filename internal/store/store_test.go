package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path, Options{BackupDir: filepath.Join(filepath.Dir(path), "backups")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newTest(t *testing.T) *Store {
	return openTest(t, filepath.Join(t.TempDir(), "jhh.db"))
}

func TestOpenConfiguresDatabase(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)

	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.SchemaVersion(ctx); err != nil || v != len(ms) {
		t.Errorf("SchemaVersion = %d, %v; want %d", v, err, len(ms))
	}
	if err := s.Check(ctx); err != nil {
		t.Error(err)
	}
	var mode string
	var fk, timeout int
	s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
	s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk)
	s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout)
	if mode != "wal" || fk != 1 || timeout != int(busyTimeout.Milliseconds()) {
		t.Errorf("journal_mode=%s foreign_keys=%d busy_timeout=%d", mode, fk, timeout)
	}
}

func TestReopenDoesNotBackUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jhh.db")
	openTest(t, path).Close()
	openTest(t, path).Close()
	if backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "*.db")); len(backups) != 0 {
		t.Errorf("backups = %v, want none: the schema was already current", backups)
	}
}

func TestMigrateFromOlderVersionBacksUp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "jhh.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	v1 := []migration{{version: 1, name: "0001_a.sql", sql: "CREATE TABLE a (x INTEGER) STRICT;"}}
	v2 := append(v1, migration{version: 2, name: "0002_b.sql", sql: "CREATE TABLE b (y INTEGER) STRICT; INSERT INTO a VALUES (7);"})
	now := time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC)

	if err := migrate(ctx, db, v1, filepath.Join(dir, "backups"), now); err != nil {
		t.Fatal(err)
	}
	if backups, _ := filepath.Glob(filepath.Join(dir, "backups", "*.db")); len(backups) != 0 {
		t.Fatalf("a brand-new database should not be backed up: %v", backups)
	}
	if err := migrate(ctx, db, v2, filepath.Join(dir, "backups"), now); err != nil {
		t.Fatal(err)
	}

	backups, _ := filepath.Glob(filepath.Join(dir, "backups", "*.db"))
	if len(backups) != 1 || !strings.HasSuffix(backups[0], "jhh-20261018T140000.000Z-v1.db") {
		t.Fatalf("backups = %v, want one copy of the v1 database", backups)
	}
	if v, _ := userVersion(ctx, db); v != 2 {
		t.Errorf("user_version = %d, want 2", v)
	}
	// The backup is the database as it was before migrating.
	old, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var n int
	if err := old.QueryRowContext(ctx, "SELECT count(*) FROM a").Scan(&n); err != nil || n != 0 {
		t.Errorf("backup rows in a = %d, %v; want 0", n, err)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jhh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bad := []migration{{version: 1, name: "0001_bad.sql", sql: "CREATE TABLE ok (x INTEGER); CREATE TABLE broken (;"}}
	if err := migrate(ctx, db, bad, "", time.Now().UTC()); err == nil {
		t.Fatal("migrate should fail")
	}
	var n int
	db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'ok'").Scan(&n)
	if v, _ := userVersion(ctx, db); v != 0 || n != 0 {
		t.Errorf("after a failed migration: user_version=%d, table ok exists=%v; want nothing applied", v, n == 1)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jhh.db")
	s := openTest(t, path)
	if _, err := s.db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(context.Background(), path, Options{}); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("Open = %v, want an error about a newer schema", err)
	}
}

func TestPruneBackupsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 7; i++ {
		name := fmt.Sprintf("jhh-2026101%dT000000.000Z-v1.db", i)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneBackups(dir); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "jhh-*.db"))
	if len(left) != keepBackups || !strings.Contains(left[0], "20261013") {
		t.Errorf("left = %v, want the newest %d (13th onward)", left, keepBackups)
	}
}

// TestConcurrentProcesses opens the same file through two independent
// stores at once (separate connection pools, as two jhh processes would
// have) and writes from both. Every write must succeed.
func TestConcurrentProcesses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "jhh.db")

	stores := make([]*Store, 2)
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(ctx, path, Options{})
			if err != nil {
				errs <- fmt.Errorf("open %d: %w", i, err)
				return
			}
			stores[i] = s
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	defer stores[0].Close()
	defer stores[1].Close()

	company, err := stores[0].AddCompany(ctx, ActorSystem, NewCompany{Name: "Example Co", Source: "none", Origin: "user"})
	if err != nil {
		t.Fatal(err)
	}
	const perStore = 40
	errs = make(chan error, 2*perStore)
	for _, s := range stores {
		for i := 0; i < perStore; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := s.AddJob(ctx, ActorSystem, NewJob{CompanyID: company, Title: fmt.Sprintf("Engineer %d", i), Origin: "manual"}); err != nil {
					errs <- err
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write failed: %v", err)
	}
	var n int
	stores[1].db.QueryRowContext(ctx, "SELECT count(*) FROM jobs").Scan(&n)
	if n != 2*perStore {
		t.Errorf("jobs = %d, want %d", n, 2*perStore)
	}
}

func TestJobStatusChangesAreRecorded(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)
	agent := Agent("claude-code")

	company, err := s.AddCompany(ctx, ActorUser, NewCompany{Name: "Example Co", Source: "greenhouse", BoardKey: "greenhouse:exampleco",
		SourceConfig: json.RawMessage(`{"slug":"exampleco"}`), Origin: "registry"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AddJob(ctx, agent, NewJob{CompanyID: company, Title: "Staff Engineer", Origin: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetJobStatus(ctx, agent, id, StatusReview); err != nil {
		t.Fatal(err)
	}
	if err := s.SetJobStatus(ctx, ActorUser, id, StatusReview); err != nil { // no change
		t.Fatal(err)
	}

	job, err := s.Job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusReview || job.UpdatedBy != agent || job.CreatedBy != agent {
		t.Errorf("job = %+v", job)
	}
	events, err := s.JobEvents(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want job_added and one status_changed", len(events))
	}
	if e := events[1]; e.Kind != "status_changed" || e.Actor != agent || string(e.Data) != `{"from":"new","to":"review"}` {
		t.Errorf("status event = %+v (data %s)", e, e.Data)
	}
	if err := s.SetJobStatus(ctx, agent, id, "hired"); err == nil {
		t.Error("an unknown status should be rejected")
	}
	if err := s.SetJobStatus(ctx, agent, 9999, StatusSkipped); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetJobStatus on a missing job = %v, want ErrNotFound", err)
	}
}

func TestJobNumbersAreNeverReused(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)
	company, _ := s.AddCompany(ctx, ActorUser, NewCompany{Name: "Example Co", Source: "none", Origin: "user"})
	first, _ := s.AddJob(ctx, ActorUser, NewJob{CompanyID: company, Title: "A", Origin: "manual"})
	s.db.ExecContext(ctx, "DELETE FROM events WHERE job_id = ?", first)
	s.db.ExecContext(ctx, "DELETE FROM jobs WHERE id = ?", first)
	second, err := s.AddJob(ctx, ActorUser, NewJob{CompanyID: company, Title: "B", Origin: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Errorf("job number %d was reused", first)
	}
}

func TestStoreRejectsNonUTC(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)
	company, _ := s.AddCompany(ctx, ActorUser, NewCompany{Name: "Example Co", Source: "none", Origin: "user"})
	id, _ := s.AddJob(ctx, ActorUser, NewJob{CompanyID: company, Title: "A", Origin: "manual"})

	local := time.Date(2026, 10, 18, 9, 30, 0, 0, time.FixedZone("EDT", -4*60*60))
	if err := s.SetAppliedAt(ctx, ActorUser, id, local); !errors.Is(err, ErrNotUTC) {
		t.Errorf("SetAppliedAt(local) = %v, want ErrNotUTC", err)
	}
	if err := s.SetAppliedAt(ctx, ActorUser, id, local.UTC()); err != nil {
		t.Fatal(err)
	}
	job, _ := s.Job(ctx, id)
	if !job.AppliedAt.Equal(local) || job.AppliedAt.Location() != time.UTC {
		t.Errorf("AppliedAt = %v, want %v in UTC", job.AppliedAt, local.UTC())
	}
}

func TestStampsAreUTCEvenWithALocalClock(t *testing.T) {
	ctx := context.Background()
	local := time.FixedZone("IST", 5*60*60+30*60)
	s, err := Open(ctx, filepath.Join(t.TempDir(), "jhh.db"), Options{
		Now: func() time.Time { return time.Date(2026, 10, 18, 19, 30, 0, 0, local) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	company, _ := s.AddCompany(ctx, ActorUser, NewCompany{Name: "Example Co", Source: "none", Origin: "user"})
	var created string
	s.db.QueryRowContext(ctx, "SELECT created_at FROM companies WHERE id = ?", company).Scan(&created)
	if created != "2026-10-18T14:00:00.000Z" {
		t.Errorf("created_at = %q, want UTC", created)
	}
}

func TestSchemaGuards(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)
	company, _ := s.AddCompany(ctx, ActorUser, NewCompany{Name: "Example Co", Source: "none", Origin: "user"})
	job, _ := s.AddJob(ctx, ActorUser, NewJob{CompanyID: company, Title: "A", Origin: "manual"})
	now := s.stamp()

	tests := []struct {
		name string
		sql  string
		args []any
	}{
		{"a memory inferred from a listing can't be user_said",
			`INSERT INTO memories (kind, content, basis, evidence_job_id, created_at, created_by, updated_at, updated_by)
			 VALUES ('preference', 'fine with relocating', 'user_said', ?, ?, 'agent:x', ?, 'agent:x')`, []any{job, now, now}},
		{"unknown job status", `UPDATE jobs SET status = 'hired' WHERE id = ?`, []any{job}},
		{"strict typing", `UPDATE jobs SET prefilter_score = 'high' WHERE id = ?`, []any{job}},
		{"invalid JSON", `UPDATE jobs SET locations = 'not json' WHERE id = ?`, []any{job}},
		{"foreign keys", `INSERT INTO screenings (job_id, fit, reason, screened_by, screened_at) VALUES (9999, 'good', 'r', 'agent:x', ?)`, []any{now}},
		{"one active filter version",
			`INSERT INTO filter_versions (filters, rationale, active, created_at, created_by) VALUES ('{}', 'a', 1, ?, 'user'), ('{}', 'b', 1, ?, 'user')`, []any{now, now}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.db.ExecContext(ctx, tt.sql, tt.args...); err == nil {
				t.Error("the schema should reject this")
			}
		})
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s := newTest(t)
	if _, ok, err := s.Setting(ctx, "ask_percentile"); ok || err != nil {
		t.Fatalf("unset setting: ok=%v err=%v", ok, err)
	}
	s.SetSetting(ctx, ActorUser, "ask_percentile", "0.85")
	s.SetSetting(ctx, ActorUser, "ask_percentile", "0.8")
	if v, ok, err := s.Setting(ctx, "ask_percentile"); v != "0.8" || !ok || err != nil {
		t.Errorf("Setting = %q, %v, %v", v, ok, err)
	}
}

func TestAgentActor(t *testing.T) {
	tests := map[string]Actor{
		"claude-code":            "agent:claude-code",
		"  Cursor  ":             "agent:Cursor",
		"":                       "agent:unknown",
		"evil\nname\x00":         "agent:evilname",
		strings.Repeat("x", 100): Actor("agent:" + strings.Repeat("x", maxAgentName)),
	}
	for in, want := range tests {
		if got := Agent(in); got != want {
			t.Errorf("Agent(%q) = %q, want %q", in, got, want)
		}
	}
}
