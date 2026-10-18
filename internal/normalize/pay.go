package normalize

import (
	"regexp"
	"strconv"
	"strings"
)

// PayRange is one posted pay range. Max equals Min for a single figure.
type PayRange struct {
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	Currency string  `json:"currency"`         // ISO 4217: USD, CAD, EUR, GBP, ...
	Period   string  `json:"period,omitempty"` // year, month or hour; empty when not stated
	Label    string  `json:"label,omitempty"`  // e.g. the location a tier applies to
}

// A money amount: an optional currency marker, a number, and an optional
// K or M suffix. Built as one regexp so a range can be matched in one pass.
const (
	currencyPrefix = `(?:(?P<pre>US\$|CA\$|C\$|AU\$|A\$|NZ\$|SG\$|HK\$|USD|CAD|AUD|EUR|GBP|CHF|INR|\$|€|£)\s?)`
	amount         = `(?P<num>\d{1,3}(?:[,.]\d{3})+(?:\.\d+)?|\d+(?:[.,]\d+)?)\s?(?P<suf>[kKmM]\b)?`
	currencySuffix = `(?:\s?(?P<post>USD|CAD|AUD|EUR|GBP|CHF|INR)\b)?`
)

var (
	moneyRe = regexp.MustCompile(currencyPrefix + `?` + amount + currencySuffix)
	// rangeRe matches "<money> <separator> <money>". Either amount may omit
	// its currency marker ("$120K - 150K", "120,000 - 150,000 USD"), but
	// ParsePay requires one somewhere, so "2024-2026" is never pay.
	rangeRe = regexp.MustCompile(currencyPrefix + `?` + amount + currencySuffix +
		`(?:\s?/\s?(?:hr|hour|mo|month|yr|year)\b)?` +
		`\s*(?:-|–|—|to|and)\s*` +
		strings.NewReplacer("pre", "pre2", "num", "num2", "suf", "suf2").Replace(currencyPrefix) + `?` +
		strings.NewReplacer("num", "num2", "suf", "suf2").Replace(amount) +
		strings.NewReplacer("post", "post2").Replace(currencySuffix))
	hourlyRe  = regexp.MustCompile(`(?i)^\s*(?:/\s?(?:hr|hour)\b|per\s+hour|an\s+hour|hourly)`)
	monthlyRe = regexp.MustCompile(`(?i)^\s*(?:/\s?(?:mo|month)\b|per\s+month|a\s+month|monthly)`)
	yearlyRe  = regexp.MustCompile(`(?i)^\s*(?:/\s?(?:yr|year)\b|per\s+(?:year|annum)|a\s+year|annually|annual)`)

	commaThousands = regexp.MustCompile(`^\d{1,3}(,\d{3})+(\.\d+)?$`)
	dotThousands   = regexp.MustCompile(`^\d{1,3}(\.\d{3})+$`)
)

var currencyCodes = map[string]string{
	"US$": "USD", "$": "USD", "USD": "USD",
	"CA$": "CAD", "C$": "CAD", "CAD": "CAD",
	"AU$": "AUD", "A$": "AUD", "AUD": "AUD",
	"NZ$": "NZD", "SG$": "SGD", "HK$": "HKD",
	"€": "EUR", "EUR": "EUR", "£": "GBP", "GBP": "GBP", "CHF": "CHF", "INR": "INR",
}

// Plausible amounts. Anything outside these bounds (a "$5M Series A", a
// "$500 stipend") is not a salary and is ignored.
const (
	minAnnual = 15_000
	maxAnnual = 5_000_000
	minHourly = 7
	maxHourly = 1_000
)

// ParsePay finds pay ranges in text: a dedicated pay field or a whole job
// description. It recognizes formats such as "$158,000 — $248,000",
// "$231K – $340K", "USD 120,000 - 150,000", "€80.000 - €95.000" and
// "$60/hr - $80/hr", and several ranges for different locations. Single
// figures ("$156.6K") are only accepted from short text such as a pay field,
// because descriptions are full of other dollar amounts.
//
// When the period isn't stated, amounts of 15,000 or more are taken as
// annual; smaller amounts are left without a period. Nothing else is
// guessed: text that doesn't parse cleanly yields no range.
func ParsePay(text string) []PayRange {
	var out []PayRange
	seen := map[PayRange]bool{}
	add := func(r PayRange) {
		key := r
		key.Label = ""
		if !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	sawRange := false
	for _, m := range rangeRe.FindAllStringSubmatchIndex(text, -1) {
		g := groups(rangeRe, text, m)
		lo, ok1 := parseAmount(g["num"], g["suf"])
		hi, ok2 := parseAmount(g["num2"], g["suf2"])
		if !ok1 || !ok2 {
			continue
		}
		// "$120 - 150K": the suffix on the second amount applies to both.
		if g["suf"] == "" && g["suf2"] != "" && lo < 1000 {
			lo, _ = parseAmount(g["num"], g["suf2"])
		}
		cur := currencyOf(g["pre"], g["post"], g["pre2"], g["post2"])
		if cur == "" {
			continue
		}
		sawRange = true
		if lo > hi {
			continue
		}
		if r, ok := plausible(PayRange{Min: lo, Max: hi, Currency: cur, Period: periodAfter(text, m[1], m[5])}); ok {
			r.Label = labelBefore(text, m[0])
			add(r)
		}
	}
	if len(out) > 1 {
		return out
	}
	if len(out) == 1 {
		// A label only tells ranges apart; with one range it's just the
		// text before it ("Salary:").
		out[0].Label = ""
		return out
	}
	if sawRange || len(text) > 80 {
		return nil
	}
	for _, m := range moneyRe.FindAllStringSubmatchIndex(text, -1) {
		g := groups(moneyRe, text, m)
		v, ok := parseAmount(g["num"], g["suf"])
		cur := currencyOf(g["pre"], g["post"])
		if !ok || cur == "" {
			continue
		}
		if r, ok := plausible(PayRange{Min: v, Max: v, Currency: cur, Period: periodAfter(text, m[1], m[1])}); ok {
			add(r)
		}
	}
	return out
}

// groups maps a match's named groups to their text.
func groups(re *regexp.Regexp, text string, m []int) map[string]string {
	g := map[string]string{}
	for i, name := range re.SubexpNames() {
		if name != "" && m[2*i] >= 0 {
			g[name] = text[m[2*i]:m[2*i+1]]
		}
	}
	return g
}

// parseAmount reads "158,000", "158.000", "156.6" with suffix "K", and so on.
func parseAmount(num, suffix string) (float64, bool) {
	var clean string
	switch {
	case commaThousands.MatchString(num): // 158,000 or 1,234.50
		clean = strings.ReplaceAll(num, ",", "")
	case dotThousands.MatchString(num) && suffix == "": // 80.000 (European thousands)
		clean = strings.ReplaceAll(num, ".", "")
	default: // 156.6, 156,6, 158000
		clean = strings.Replace(num, ",", ".", 1)
	}
	v, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(suffix) {
	case "k":
		v *= 1_000
	case "m":
		v *= 1_000_000
	}
	return v, true
}

// currencyOf returns the first currency marker present, preferring explicit
// codes ("USD") over symbols ("$").
func currencyOf(markers ...string) string {
	for _, m := range markers {
		if len(m) == 3 && m != "US$" && m != "CA$" && m != "AU$" {
			if c, ok := currencyCodes[m]; ok {
				return c
			}
		}
	}
	for _, m := range markers {
		if c, ok := currencyCodes[m]; ok {
			return c
		}
	}
	return ""
}

// periodAfter looks for "/hr", "per year" and similar right after the
// range, or right after its first amount ("$60/hr - $80/hr").
func periodAfter(text string, ends ...int) string {
	for _, end := range ends {
		if end < 0 || end > len(text) {
			continue
		}
		rest := text[end:min(end+20, len(text))]
		switch {
		case hourlyRe.MatchString(rest):
			return "hour"
		case monthlyRe.MatchString(rest):
			return "month"
		case yearlyRe.MatchString(rest):
			return "year"
		}
	}
	return ""
}

// plausible fills in an unstated annual period and rejects amounts that
// can't be pay.
func plausible(r PayRange) (PayRange, bool) {
	if r.Period == "" && r.Min >= minAnnual {
		r.Period = "year"
	}
	switch r.Period {
	case "year":
		return r, r.Min >= minAnnual && r.Max <= maxAnnual
	case "month":
		return r, r.Min >= minAnnual/12 && r.Max <= maxAnnual/12
	case "hour":
		return r, r.Min >= minHourly && r.Max <= maxHourly
	}
	return r, false
}

// labelBefore returns short text on the same line before a range, such as
// "San Francisco:" in "San Francisco: $200,000 - $250,000".
func labelBefore(text string, start int) string {
	line := text[:start]
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	line = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line), ":-–—"))
	if len(line) == 0 || len(line) > 60 {
		return ""
	}
	return line
}

var (
	hourWord  = regexp.MustCompile(`(?i)\bhour|hourly|\bhr\b`)
	monthWord = regexp.MustCompile(`(?i)\bmonth`)
	yearWord  = regexp.MustCompile(`(?i)\byear|annual|\bannum|\byr\b|salary`)
)

// NewPayRange builds a range from a board's structured pay fields. period
// is the board's own wording for the interval ("1 YEAR", "per-hour-wage",
// "Annual base salary range"), from which year, month or hour is read; when
// it says nothing, the amount decides as in ParsePay. ok is false when the
// amounts aren't plausible pay.
func NewPayRange(lo, hi float64, currency, period, label string) (PayRange, bool) {
	if hi == 0 {
		hi = lo
	}
	if lo == 0 {
		lo = hi
	}
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if len(cur) != 3 || lo > hi {
		return PayRange{}, false
	}
	r := PayRange{Min: lo, Max: hi, Currency: cur, Label: strings.TrimSpace(strings.TrimRight(label, ": "))}
	switch {
	case hourWord.MatchString(period):
		r.Period = "hour"
	case monthWord.MatchString(period):
		r.Period = "month"
	case yearWord.MatchString(period):
		r.Period = "year"
	}
	return plausible(r)
}
