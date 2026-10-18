// Package utc holds jhh's one rule about time: everything is UTC.
//
// Stored and exchanged timestamps are RFC 3339 strings in UTC with millisecond
// precision and a fixed width, so they also sort correctly as text
// ("2026-10-18T14:05:09.120Z"). Input may carry any offset and is converted;
// input without a time zone is rejected. See docs/decisions/0004-utc-everywhere.md.
package utc

import (
	"errors"
	"fmt"
	"time"
)

// Layout is the format of every stored timestamp. Times are converted to UTC
// before formatting, so the literal Z is always correct.
const Layout = "2006-01-02T15:04:05.000Z"

// ErrNoZone is returned for timestamps that don't say which time zone they're in.
var ErrNoZone = errors.New("timestamp has no time zone")

// Now returns the current time in UTC.
func Now() time.Time { return time.Now().UTC() }

// Format renders t in Layout, converting it to UTC first.
func Format(t time.Time) string { return t.UTC().Format(Layout) }

// Parse reads an RFC 3339 timestamp with a zone ("Z" or an offset such as
// "-04:00") and returns it in UTC. Timestamps without a zone, including bare
// dates, are rejected with an error that explains how to fix the input; the
// caller (usually the agent) is expected to convert local time to UTC first.
func Parse(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t.UTC(), nil
	}
	if missingZone(s) {
		return time.Time{}, fmt.Errorf("%w: %q; send UTC like 2026-10-18T14:00:00Z, or include an offset like 2026-10-18T10:00:00-04:00", ErrNoZone, s)
	}
	return time.Time{}, fmt.Errorf("timestamp %q is not RFC 3339; send UTC like 2026-10-18T14:00:00Z", s)
}

// missingZone reports whether s parses as a date or a local date-time, which
// means the only thing wrong with it is the missing zone.
func missingZone(s string) bool {
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02T15:04:05.999999999"} {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}
