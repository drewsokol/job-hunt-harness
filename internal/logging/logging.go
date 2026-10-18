// Package logging sets up jhh's logger.
//
// Logs go to stderr and to logs/jhh.log in the data directory, never to
// stdout: in stdio mode stdout carries the MCP protocol. Log records contain
// IDs and counts only, never profile, resume, memory or job description text.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// LevelEnvVar sets the log level: debug, info (the default), warn or error.
const LevelEnvVar = "JHH_LOG_LEVEL"

// maxSize is the size at which the log file is rotated on startup.
const maxSize = 10 << 20

// New returns a logger writing to stderr and to jhh.log in logDir. Close the
// returned io.Closer when the process exits.
func New(logDir string, stderr io.Writer) (*slog.Logger, io.Closer, error) {
	level, err := parseLevel(os.Getenv(LevelEnvVar))
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(logDir, "jhh.log")
	if err := rotate(path); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("opening log file: %w", err)
	}
	h := slog.NewTextHandler(io.MultiWriter(stderr, f), &slog.HandlerOptions{Level: level, ReplaceAttr: utcTime})
	return slog.New(h), f, nil
}

// utcTime makes record timestamps UTC; slog uses local time by default.
func utcTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 && a.Value.Kind() == slog.KindTime {
		a.Value = slog.TimeValue(a.Value.Time().UTC())
	}
	return a
}

// rotate keeps one previous log file. Several jhh processes may share the
// log, so rotation only happens at startup and a failed rename is not fatal.
func rotate(path string) error {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < maxSize {
		return nil
	}
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
	return nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("%s=%q: use debug, info, warn or error", LevelEnvVar, s)
}
