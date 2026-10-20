package core

import (
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
)

func TestSuggestedAsk(t *testing.T) {
	usd := normalize.PayRange{Min: 158_000, Max: 248_000, Currency: "USD", Period: "year"}
	for _, tt := range []struct {
		name     string
		ranges   []normalize.PayRange
		currency string
		want     float64
	}{
		{"one range", []normalize.PayRange{usd}, "USD", 235_000}, // 158 + 0.85 × 90 = 234.5, rounded to 5k
		{"no floor currency", []normalize.PayRange{usd}, "", 0},
		{"another currency", []normalize.PayRange{usd}, "CAD", 0},
		{"hourly", []normalize.PayRange{{Min: 60, Max: 90, Currency: "USD", Period: "hour"}}, "USD", 0},
		{"one per location is a guess", []normalize.PayRange{usd, {Min: 140_000, Max: 220_000, Currency: "USD", Period: "year", Label: "Tier 2"}}, "USD", 0},
		{"a single number", []normalize.PayRange{{Min: 150_000, Max: 150_000, Currency: "USD"}}, "USD", 0},
		{"no period stated", []normalize.PayRange{{Min: 100_000, Max: 140_000, Currency: "usd"}}, "USD", 135_000},
	} {
		if got := SuggestedAsk(tt.ranges, tt.currency, DefaultAskFactor); got != tt.want {
			t.Errorf("%s: SuggestedAsk = %v, want %v", tt.name, got, tt.want)
		}
	}
}
