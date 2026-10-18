package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolve(t *testing.T) {
	home := func() (string, error) { return filepath.FromSlash("/home/alex"), nil }
	noHome := func() (string, error) { return "", errors.New("no home") }
	override := t.TempDir()

	tests := []struct {
		name    string
		goos    string
		env     map[string]string
		home    func() (string, error)
		want    string
		wantErr bool
	}{
		{name: "override wins", goos: "linux", env: map[string]string{EnvVar: override, "XDG_DATA_HOME": "/xdg"}, home: home, want: override},
		{name: "windows", goos: "windows", env: map[string]string{"LocalAppData": filepath.FromSlash("/Users/alex/AppData/Local")}, home: home, want: filepath.FromSlash("/Users/alex/AppData/Local/jhh")},
		{name: "windows without LocalAppData", goos: "windows", home: home, wantErr: true},
		{name: "macOS", goos: "darwin", home: home, want: filepath.FromSlash("/home/alex/Library/Application Support/jhh")},
		{name: "linux XDG", goos: "linux", env: map[string]string{"XDG_DATA_HOME": filepath.Join(override, "xdg")}, home: home, want: filepath.Join(override, "xdg", "jhh")},
		{name: "linux relative XDG is ignored", goos: "linux", env: map[string]string{"XDG_DATA_HOME": "relative/xdg"}, home: home, want: filepath.FromSlash("/home/alex/.local/share/jhh")},
		{name: "linux default", goos: "linux", home: home, want: filepath.FromSlash("/home/alex/.local/share/jhh")},
		{name: "no home", goos: "linux", home: noHome, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			got, err := resolve(tt.goos, getenv, tt.home)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolve = %q, want an error", got.Root)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Root != tt.want {
				t.Errorf("Root = %q, want %q", got.Root, tt.want)
			}
		})
	}
}

func TestOverrideIsMadeAbsolute(t *testing.T) {
	got, err := resolve("linux", func(k string) string {
		if k == EnvVar {
			return "relative-dir"
		}
		return ""
	}, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got.Root) {
		t.Errorf("Root = %q, want an absolute path", got.Root)
	}
}

func TestEnsure(t *testing.T) {
	d := Dir{Root: filepath.Join(t.TempDir(), "data")}
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(); err != nil { // idempotent
		t.Fatalf("second Ensure: %v", err)
	}
	for _, p := range []string{d.Root, d.Logs(), d.Backups()} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() {
			t.Errorf("%s is not a directory", p)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, want 0700", p, fi.Mode().Perm())
		}
	}
}
