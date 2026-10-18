package normalize

import (
	"reflect"
	"testing"
)

func TestParsePay(t *testing.T) {
	year := func(lo, hi float64, cur string) PayRange {
		return PayRange{Min: lo, Max: hi, Currency: cur, Period: "year"}
	}
	labeled := func(r PayRange, label string) PayRange { r.Label = label; return r }
	tests := []struct {
		in   string
		want []PayRange
	}{
		{"$158,000 — $248,000", []PayRange{year(158000, 248000, "USD")}},
		{"$231K – $340K", []PayRange{year(231000, 340000, "USD")}},
		{"$120K - 150K", []PayRange{year(120000, 150000, "USD")}},
		{"$120 - 150K", []PayRange{year(120000, 150000, "USD")}},
		{"$156.6K", []PayRange{year(156600, 156600, "USD")}},
		{"USD 120,000 - 150,000", []PayRange{year(120000, 150000, "USD")}},
		{"120,000 - 150,000 USD", []PayRange{year(120000, 150000, "USD")}},
		{"CA$140,000 to CA$170,000", []PayRange{year(140000, 170000, "CAD")}},
		{"€80.000 - €95.000", []PayRange{year(80000, 95000, "EUR")}},
		{"£70k-£90k", []PayRange{year(70000, 90000, "GBP")}},
		{"$60/hr - $80/hr", []PayRange{{Min: 60, Max: 80, Currency: "USD", Period: "hour"}}},
		{"$45 - $55 per hour", []PayRange{{Min: 45, Max: 55, Currency: "USD", Period: "hour"}}},
		{"$150,000 - $180,000 per year", []PayRange{year(150000, 180000, "USD")}},
		{
			"San Francisco: $200,000 - $250,000\nRemote US: $180,000 - $225,000",
			[]PayRange{labeled(year(200000, 250000, "USD"), "San Francisco"), labeled(year(180000, 225000, "USD"), "Remote US")},
		},
		{"Range is $1.2M - $1.5M", []PayRange{year(1200000, 1500000, "USD")}},

		// Not pay.
		{"", nil},
		{"Founded 2014-2016", nil},
		{"We raised $50M - $80M", nil},
		{"10 - 15 years of experience", nil},
		{"$500 home office stipend", nil},
		{"$250,000 - $200,000", nil}, // reversed
		{"Competitive salary", nil},
		{
			// Single figures in long text are ignored: descriptions mention
			// funding, stipends and so on.
			"We are a well funded startup that has raised $120,000,000 and offers a $150,000 learning budget to every team.",
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := ParsePay(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParsePay(%q)\n got  %+v\n want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParsePayDedupesRepeats(t *testing.T) {
	got := ParsePay("Pay: $100,000 - $120,000. Again, pay is $100,000 - $120,000.")
	if len(got) != 1 {
		t.Fatalf("got %d ranges, want 1: %+v", len(got), got)
	}
}

func TestNewPayRange(t *testing.T) {
	tests := []struct {
		lo, hi           float64
		currency, period string
		want             PayRange
		ok               bool
	}{
		{160000, 200000, "USD", "1 YEAR", PayRange{Min: 160000, Max: 200000, Currency: "USD", Period: "year"}, true},
		{40, 40, "usd", "Hourly Rate:", PayRange{Min: 40, Max: 40, Currency: "USD", Period: "hour"}, true},
		{120000, 0, "CAD", "", PayRange{Min: 120000, Max: 120000, Currency: "CAD", Period: "year"}, true},
		{9000, 12000, "EUR", "per-month-salary", PayRange{Min: 9000, Max: 12000, Currency: "EUR", Period: "month"}, true},
		{200000, 100000, "USD", "1 YEAR", PayRange{}, false},
		{50, 60, "USD", "", PayRange{}, false}, // no period and too small for a salary
		{100000, 120000, "", "1 YEAR", PayRange{}, false},
	}
	for _, tt := range tests {
		got, ok := NewPayRange(tt.lo, tt.hi, tt.currency, tt.period, "")
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("NewPayRange(%v, %v, %q, %q) = %+v, %v; want %+v, %v", tt.lo, tt.hi, tt.currency, tt.period, got, ok, tt.want, tt.ok)
		}
	}
}
