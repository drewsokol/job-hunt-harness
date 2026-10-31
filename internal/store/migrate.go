package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// keepBackups is how many pre-migration backups are kept.
const keepBackups = 5

type migration struct {
	version int
	name    string
	sql     string
}

// migrations returns the embedded migrations in order. Files are named
// NNNN_description.sql and numbered from 1 without gaps.
func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %s: name must start with a number and an underscore", e.Name())
		}
		b, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %s: expected version %d", m.name, i+1)
		}
	}
	return ms, nil
}

// migrate brings the database up to the latest schema. Several jhh
// processes can start at once, so the work happens inside BEGIN EXCLUSIVE:
// one process migrates while the others wait, then find nothing left to do.
// Existing data is backed up first. Foreign keys are off while migrations
// run, so a migration can rebuild a table other tables point at (SQLite
// can't change a CHECK in place), and they're checked before committing.
func migrate(ctx context.Context, db *sql.DB, ms []migration, backupDir string, now time.Time) error {
	latest := len(ms)

	current, err := userVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than this jhh supports (%d); upgrade jhh", current, latest)
	}
	if current == latest {
		return nil
	}
	if current > 0 && backupDir != "" {
		if err := backup(ctx, db, backupDir, current, now); err != nil {
			return fmt.Errorf("backing up before migration: %w", err)
		}
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Outside a transaction, as SQLite requires; the connection goes back
	// to the pool with foreign keys on again.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON")
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		return fmt.Errorf("locking database for migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	// Re-read inside the lock: another process may have migrated meanwhile.
	if current, err = userVersion(ctx, conn); err != nil {
		return err
	}
	for _, m := range ms[current:] {
		if _, err := conn.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	if err := checkForeignKeys(ctx, conn); err != nil {
		return err
	}
	// PRAGMA can't take a bound parameter; latest is a trusted integer.
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", latest)); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("committing migration: %w", err)
	}
	committed = true
	return nil
}

// checkForeignKeys fails if any row points at a row that doesn't exist.
func checkForeignKeys(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var (
			table, parent string
			rowid         sql.NullInt64
			fk            int
		)
		if err := rows.Scan(&table, &rowid, &parent, &fk); err != nil {
			return err
		}
		return fmt.Errorf("migration left %s row %d pointing at a missing %s row", table, rowid.Int64, parent)
	}
	return rows.Err()
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func userVersion(ctx context.Context, q querier) (int, error) {
	var v int
	err := q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// backup writes a consistent copy of the database with VACUUM INTO, then
// keeps only the newest keepBackups copies. Names start with the UTC time,
// so they sort oldest first.
func backup(ctx context.Context, db *sql.DB, dir string, version int, now time.Time) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("jhh-%s-v%d.db", now.UTC().Format("20060102T150405.000Z"), version)
	dest := filepath.Join(dir, name)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return err
	}
	return pruneBackups(dir)
}

func pruneBackups(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "jhh-*.db"))
	if err != nil {
		return err
	}
	sort.Strings(matches)
	var errs []error
	for len(matches) > keepBackups {
		if err := os.Remove(matches[0]); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		matches = matches[1:]
	}
	return errors.Join(errs...)
}
