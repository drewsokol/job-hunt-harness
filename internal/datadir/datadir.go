// Package datadir finds and prepares the directory that holds a user's jhh
// data: the SQLite database, logs, backups and services' keys.
//
// The default location is per-user and outside any repository:
//
//	Windows  %LocalAppData%\jhh
//	macOS    ~/Library/Application Support/jhh
//	Linux    $XDG_DATA_HOME/jhh, or ~/.local/share/jhh
//
// JHH_DATA_DIR overrides it.
package datadir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// EnvVar overrides the default data directory.
const EnvVar = "JHH_DATA_DIR"

// Dir is a resolved data directory.
type Dir struct {
	Root string
}

// DB returns the path of the SQLite database.
func (d Dir) DB() string { return filepath.Join(d.Root, "jhh.db") }

// Logs returns the directory for log files.
func (d Dir) Logs() string { return filepath.Join(d.Root, "logs") }

// Keys returns the path of the file holding services' optional keys,
// kept out of the database so backups and exports never contain them.
func (d Dir) Keys() string { return filepath.Join(d.Root, "keys.json") }

// Backups returns the directory for automatic database backups.
func (d Dir) Backups() string { return filepath.Join(d.Root, "backups") }

// Resolve returns the data directory for this user without creating it.
func Resolve() (Dir, error) {
	return resolve(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

func resolve(goos string, getenv func(string) string, home func() (string, error)) (Dir, error) {
	if p := getenv(EnvVar); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return Dir{}, fmt.Errorf("%s=%q: %w", EnvVar, p, err)
		}
		return Dir{Root: abs}, nil
	}
	switch goos {
	case "windows":
		if p := getenv("LocalAppData"); p != "" {
			return Dir{Root: filepath.Join(p, "jhh")}, nil
		}
		return Dir{}, errors.New("LocalAppData is not set; set " + EnvVar + " to choose a data directory")
	case "darwin":
		h, err := home()
		if err != nil {
			return Dir{}, fmt.Errorf("finding home directory: %w", err)
		}
		return Dir{Root: filepath.Join(h, "Library", "Application Support", "jhh")}, nil
	default:
		if p := getenv("XDG_DATA_HOME"); p != "" && filepath.IsAbs(p) {
			return Dir{Root: filepath.Join(p, "jhh")}, nil
		}
		h, err := home()
		if err != nil {
			return Dir{}, fmt.Errorf("finding home directory: %w", err)
		}
		return Dir{Root: filepath.Join(h, ".local", "share", "jhh")}, nil
	}
}

// Ensure creates the data directory and its subdirectories, readable only by
// the current user. On Unix that is mode 0700. On Windows the mode bits are
// ignored, and the per-user default under %LocalAppData% already has an ACL
// that only the user (and administrators) can read.
func (d Dir) Ensure() error {
	for _, p := range []string{d.Root, d.Logs(), d.Backups()} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", p, err)
		}
	}
	// MkdirAll leaves an existing directory's mode alone; tighten it in case
	// the user created it by hand.
	if err := os.Chmod(d.Root, 0o700); err != nil {
		return fmt.Errorf("restricting %s: %w", d.Root, err)
	}
	return nil
}
