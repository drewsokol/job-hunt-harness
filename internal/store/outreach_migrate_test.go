package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInboundOutreachMigration rebuilds the outreach table under a thread
// and its events: the thread keeps its data and becomes outbound, and the
// events still point at it.
func TestInboundOutreachMigration(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jhh.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if err := migrate(ctx, db, ms[:4], "", now); err != nil {
		t.Fatal(err)
	}
	const at = "2026-10-20T09:00:00.000Z"
	for _, q := range []string{
		`INSERT INTO contacts (id, name, company_name, created_at, created_by, updated_at, updated_by)
		 VALUES (1, 'Alex Example', 'Exampleco', '` + at + `', 'user', '` + at + `', 'user')`,
		`INSERT INTO outreach (id, contact_id, channel, sent_at, status, follow_up_at, note, created_at, created_by, updated_at, updated_by)
		 VALUES (7, 1, 'email', '` + at + `', 'replied', '2026-10-27T09:00:00.000Z', 'A made-up note.', '` + at + `', 'user', '` + at + `', 'user')`,
		`INSERT INTO events (at, actor, kind, outreach_id, contact_id) VALUES ('` + at + `', 'user', 'outreach_logged', 7, 1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(ctx, db, ms[:5], "", now); err != nil {
		t.Fatal(err)
	}

	var direction, status, note, follow string
	if err := db.QueryRowContext(ctx, "SELECT direction, status, note, follow_up_at FROM outreach WHERE id = 7").Scan(&direction, &status, &note, &follow); err != nil {
		t.Fatal(err)
	}
	if direction != "outbound" || status != "replied" || note != "A made-up note." || follow != "2026-10-27T09:00:00.000Z" {
		t.Errorf("after the rebuild: %s %s %q %s", direction, status, note, follow)
	}
	var events int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM events e JOIN outreach o ON o.id = e.outreach_id").Scan(&events); err != nil || events != 1 {
		t.Errorf("events joined to the thread: %d, %v", events, err)
	}
	var fkErrors int
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		fkErrors++
	}
	rows.Close()
	if fkErrors != 0 {
		t.Errorf("%d foreign key errors after the rebuild", fkErrors)
	}
	var indexes int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN ('outreach_follow_up', 'outreach_sent')").Scan(&indexes); err != nil || indexes != 2 {
		t.Errorf("indexes: %d, %v", indexes, err)
	}
	var on int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil || on != 1 {
		t.Errorf("foreign keys after migrating: %d, %v", on, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE outreach SET status = 'received' WHERE id = 7"); err != nil {
		t.Errorf("the new status: %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE outreach SET decline_reason = 'bored' WHERE id = 7"); err == nil {
		t.Error("an unknown decline reason was stored")
	}
}

// TestMigrationLeavingBrokenLinksFails checks that foreign keys, off while
// migrating, are checked before the migration commits.
func TestMigrationLeavingBrokenLinksFails(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jhh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms := []migration{{version: 1, name: "0001_broken.sql", sql: `
		CREATE TABLE parent (id INTEGER PRIMARY KEY) STRICT;
		CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent (id)) STRICT;
		INSERT INTO child VALUES (1, 99);`}}
	err = migrate(ctx, db, ms, "", time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "missing parent") {
		t.Fatalf("migrate: %v", err)
	}
	if v, _ := userVersion(ctx, db); v != 0 {
		t.Errorf("user_version = %d after a failed migration", v)
	}
}
