package utc

import (
	"errors"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in     string
		want   string // formatted result; empty when an error is expected
		noZone bool   // the error should be ErrNoZone
	}{
		{in: "2026-10-18T14:00:00Z", want: "2026-10-18T14:00:00.000Z"},
		{in: "2026-10-18T10:00:00-04:00", want: "2026-10-18T14:00:00.000Z"},
		{in: "2026-10-18T23:30:00-04:00", want: "2026-10-19T03:30:00.000Z"},
		{in: "2026-10-18T14:00:00.123456Z", want: "2026-10-18T14:00:00.123Z"},
		{in: "2026-10-18T19:30:00+05:30", want: "2026-10-18T14:00:00.000Z"},
		{in: "2026-10-18", noZone: true},
		{in: "2026-10-18T10:00", noZone: true},
		{in: "2026-10-18T10:00:00", noZone: true},
		{in: "2026-10-18T10:00:00.5", noZone: true},
		{in: "next tuesday"},
		{in: ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Parse(tt.in)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("Parse(%q) = %v, want an error", tt.in, got)
				}
				if errors.Is(err, ErrNoZone) != tt.noZone {
					t.Fatalf("Parse(%q) error %q: errors.Is(ErrNoZone) = %v, want %v", tt.in, err, !tt.noZone, tt.noZone)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.in, err)
			}
			if got.Location() != time.UTC {
				t.Errorf("Parse(%q) location = %v, want UTC", tt.in, got.Location())
			}
			if s := Format(got); s != tt.want {
				t.Errorf("Format(Parse(%q)) = %q, want %q", tt.in, s, tt.want)
			}
		})
	}
}

func TestFormatConvertsToUTC(t *testing.T) {
	ny := time.FixedZone("EDT", -4*60*60)
	in := time.Date(2026, 10, 18, 22, 15, 0, 0, ny)
	if got, want := Format(in), "2026-10-19T02:15:00.000Z"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
}

func TestFormatSortsAsText(t *testing.T) {
	// Fixed width means string order matches time order, even across a
	// change in the number of fractional digits.
	a := Format(time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC))
	b := Format(time.Date(2026, 10, 18, 14, 0, 0, 5_000_000, time.UTC))
	if !(a < b) {
		t.Errorf("%q should sort before %q", a, b)
	}
}

func TestNowIsUTC(t *testing.T) {
	if loc := Now().Location(); loc != time.UTC {
		t.Errorf("Now().Location() = %v, want UTC", loc)
	}
}
