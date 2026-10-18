package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWritesToStderrAndFile(t *testing.T) {
	dir := t.TempDir()
	var stderr bytes.Buffer
	logger, closer, err := New(dir, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("refresh finished", "companies", 3)
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(stderr.String(), "refresh finished") {
		t.Errorf("stderr = %q, want the record", stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "jhh.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "companies=3") {
		t.Errorf("log file = %q, want the record", data)
	}
}

func TestTimestampsAreUTC(t *testing.T) {
	var stderr bytes.Buffer
	logger, closer, err := New(t.TempDir(), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	logger.Info("hello")

	fields := strings.Fields(stderr.String())
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "time=") {
		t.Fatalf("unexpected record %q", stderr.String())
	}
	ts := strings.TrimPrefix(fields[0], "time=")
	if !strings.HasSuffix(ts, "Z") {
		t.Errorf("timestamp %q is not UTC", ts)
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jhh.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxSize), 0o600); err != nil {
		t.Fatal(err)
	}
	_, closer, err := New(dir, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	closer.Close()

	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != maxSize {
		t.Errorf("rotated file: %v, %v", fi, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() != 0 {
		t.Errorf("new log file should be empty: %v, %v", fi, err)
	}
}

func TestParseLevel(t *testing.T) {
	tests := map[string]slog.Level{"": slog.LevelInfo, "DEBUG": slog.LevelDebug, " warn ": slog.LevelWarn, "error": slog.LevelError}
	for in, want := range tests {
		got, err := parseLevel(in)
		if err != nil || got != want {
			t.Errorf("parseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseLevel("loud"); err == nil {
		t.Error("parseLevel(loud) should fail")
	}
}
