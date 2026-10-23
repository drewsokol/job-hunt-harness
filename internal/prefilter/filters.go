// Package prefilter decides which jobs are worth an agent's attention, using
// the rules in the active filters (docs/design.md, "Prefilter" and
// "Filters"). It is cheap and rule-based: the agent writes the filters, and
// the agent does the real screening.
//
// A job is judged in two stages. Stage 1 uses the listing alone (title,
// location, employment type, listed pay); stage 2 also needs the
// description (body keywords, pay stated in the text, remote work mentioned
// only in the text). A job that fails stage 1 never needs its description
// fetched.
package prefilter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
)

// SchemaVersion is the filters format this build reads.
const SchemaVersion = 1

// Filters are the prefilter's rules, written by the agent. Every section is
// optional; an empty filter passes everything.
type Filters struct {
	SchemaVersion int     `json:"schema_version"`
	Titles        *Titles `json:"titles,omitempty"`
	// Levels a title may state; see Levels for the vocabulary. A title
	// stating only other levels is dropped.
	Levels []string `json:"levels,omitempty"`
	// LevelUnknown says what happens to a title that states no level:
	// "pass" (the default) or "drop".
	LevelUnknown      string    `json:"level_unknown,omitempty"`
	Location          *Location `json:"location,omitempty"`
	EmploymentExclude []string  `json:"employment_exclude,omitempty"`
	Keywords          *Keywords `json:"keywords,omitempty"`
	PayFloor          *PayFloor `json:"pay_floor,omitempty"`
	Flag              *Flag     `json:"flag,omitempty"`
	// Search holds the queries search sources run. It finds jobs; the rest
	// of the filters judge them, wherever they came from.
	Search *Search `json:"search,omitempty"`
}

// Titles are words and phrases matched against the job title.
type Titles struct {
	RequireAny []string `json:"require_any,omitempty"`
	ExcludeAny []string `json:"exclude_any,omitempty"`
}

// Location says where the candidate can work.
type Location struct {
	// RemoteIn are countries ("US", "Canada") or regions ("Europe") where
	// remote work is OK.
	RemoteIn []string `json:"remote_in,omitempty"`
	// OnsiteOrHybridIn are places where going into an office is OK: a city
	// with its state or country ("Columbus, OH"), a state, or a country.
	OnsiteOrHybridIn []string `json:"onsite_or_hybrid_in,omitempty"`
	// Unknown says what happens when a job's location can't be judged:
	// "pass" (the default) or "drop".
	Unknown string `json:"unknown,omitempty"`
}

// Keywords look for the candidate's field in the title and description.
type Keywords struct {
	TitleAny []string `json:"title_any,omitempty"`
	BodyAny  []string `json:"body_any,omitempty"`
	// MinBodyHitsWithoutTitleMatch is how many different body keywords a
	// description needs when the title has none. It defaults to 1.
	MinBodyHitsWithoutTitleMatch int `json:"min_body_hits_without_title_match,omitempty"`
}

// PayFloor drops a job only when its stated pay tops out below the floor.
// Amount is a yearly figure; hourly and monthly pay are converted.
type PayFloor struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// Flag marks jobs that pass but that the candidate wants pointed out.
type Flag struct {
	TitleAny []string `json:"title_any,omitempty"`
}

// Search holds each search source's queries.
type Search struct {
	TheMuse []MuseQuery `json:"themuse,omitempty"`
	// HackerNews, when present, reads the monthly "Who is hiring?" threads.
	HackerNews *HackerNews `json:"hackernews,omitempty"`
}

// HackerNews says how many of the latest monthly "Who is hiring?" threads
// a refresh reads. Ads aren't searched on Hacker News's side: every ad in
// those threads is judged by the rest of the filters.
type HackerNews struct {
	// Months is 1 to MaxHackerNewsMonths; 0 means DefaultHackerNewsMonths.
	Months int `json:"months,omitempty"`
}

// Hacker News thread limits. Two months by default, so the days after a
// new thread opens, when it holds few ads, still see last month's.
const (
	DefaultHackerNewsMonths = 2
	MaxHackerNewsMonths     = 3
)

// MonthsOrDefault is how many threads to read.
func (h HackerNews) MonthsOrDefault() int {
	if h.Months == 0 {
		return DefaultHackerNewsMonths
	}
	return h.Months
}

// MuseQuery is one search of The Muse. A job matches when it is in any of
// the categories, at any of the levels and in any of the locations; a field
// left out doesn't narrow. Category and level names are checked against The
// Muse's lists when the filters are saved. A location also matches every
// remote job ("Flexible / Remote"). Pages optionally caps how many pages of
// 20 a refresh reads; by default it reads them all, within the refresh's
// request limit.
type MuseQuery struct {
	Categories []string `json:"categories,omitempty"`
	Levels     []string `json:"levels,omitempty"`
	Locations  []string `json:"locations,omitempty"`
	Pages      int      `json:"pages,omitempty"`
}

// Search limits. MaxSearchPages matches the default request limit of a
// refresh (core.DefaultMaxSearchRequests).
const (
	MaxSearchPages   = 250
	maxQueries       = 10
	maxQueryValues   = 20
	maxQueryValueLen = 100
)

// Policies for unclear cases.
const (
	PolicyPass = "pass"
	PolicyDrop = "drop"
)

// Limits that keep filters reasonable.
const (
	maxTerms   = 200
	maxTermLen = 60
	maxPlaces  = 50
)

// ValidationError lists everything wrong with a filters document, each
// problem prefixed with the field it's about.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid filters: " + strings.Join(e.Problems, "; ")
}

// Parse decodes and validates a filters document. Unknown fields are
// errors, so a misspelled rule can't be silently ignored.
func Parse(data []byte) (Filters, error) {
	var f Filters
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Filters{}, fmt.Errorf("invalid filters JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Filters{}, errors.New("invalid filters JSON: unexpected data after the filters object")
	}
	return f, f.Validate()
}

// JSON returns the filters as compact JSON, the form they are stored in.
func (f Filters) JSON() []byte {
	b, err := json.Marshal(f)
	if err != nil {
		panic(err) // plain structs only
	}
	return b
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// Validate checks the filters and returns a *ValidationError naming every
// problem in a way an agent can act on.
func (f Filters) Validate() error {
	var p []string
	add := func(format string, args ...any) { p = append(p, fmt.Sprintf(format, args...)) }
	terms := func(field string, ss []string) {
		if len(ss) > maxTerms {
			add("%s: %d terms is too many; the limit is %d", field, len(ss), maxTerms)
		}
		for i, s := range ss {
			switch {
			case len(s) > maxTermLen:
				add("%s[%d]: longer than %d characters", field, i, maxTermLen)
			case len(words(s)) == 0:
				add("%s[%d]: %q has no letters or digits to match", field, i, s)
			}
		}
	}
	policy := func(field, v string) {
		if v != "" && v != PolicyPass && v != PolicyDrop {
			add("%s: %q must be %q or %q", field, v, PolicyPass, PolicyDrop)
		}
	}

	if f.SchemaVersion != SchemaVersion {
		add("schema_version: must be %d", SchemaVersion)
	}
	if t := f.Titles; t != nil {
		terms("titles.require_any", t.RequireAny)
		terms("titles.exclude_any", t.ExcludeAny)
	}
	for i, l := range f.Levels {
		if !slices.Contains(Levels, l) {
			add("levels[%d]: %q is not a known level; use one of %s", i, l, strings.Join(Levels, ", "))
		}
	}
	policy("level_unknown", f.LevelUnknown)
	if f.LevelUnknown != "" && len(f.Levels) == 0 {
		add("level_unknown: only applies when levels are set")
	}
	if l := f.Location; l != nil {
		policy("location.unknown", l.Unknown)
		if len(l.RemoteIn) == 0 && len(l.OnsiteOrHybridIn) == 0 {
			add("location: set remote_in, onsite_or_hybrid_in or both, or leave location out")
		}
		if len(l.RemoteIn)+len(l.OnsiteOrHybridIn) > maxPlaces {
			add("location: more than %d places", maxPlaces)
		}
		for i, s := range l.RemoteIn {
			if pl, ok := normalize.ParsePlace(s); !ok || pl.City != "" {
				add("location.remote_in[%d]: %q is not a country, state or region jhh knows; use a name like \"US\", \"Canada\" or \"Europe\"", i, s)
			}
		}
		for i, s := range l.OnsiteOrHybridIn {
			if _, ok := normalize.ParsePlace(s); !ok {
				add("location.onsite_or_hybrid_in[%d]: %q is not a place jhh can read; write a city with its state or country (\"Columbus, OH\", \"Lyon, France\"), a state or a country", i, s)
			}
		}
	}
	for i, e := range f.EmploymentExclude {
		if !slices.Contains(EmploymentTypes, e) {
			add("employment_exclude[%d]: %q is not a known employment type; use one of %s", i, e, strings.Join(EmploymentTypes, ", "))
		}
	}
	if k := f.Keywords; k != nil {
		terms("keywords.title_any", k.TitleAny)
		terms("keywords.body_any", k.BodyAny)
		switch n := k.MinBodyHitsWithoutTitleMatch; {
		case n < 0:
			add("keywords.min_body_hits_without_title_match: must not be negative")
		case n > 0 && len(k.BodyAny) == 0:
			add("keywords.min_body_hits_without_title_match: needs body_any")
		case n > len(k.BodyAny):
			add("keywords.min_body_hits_without_title_match: %d is more than the %d body_any terms, so no description could meet it", n, len(k.BodyAny))
		}
	}
	if pf := f.PayFloor; pf != nil {
		switch {
		case pf.Amount < 0:
			add("pay_floor.amount: must not be negative")
		case pf.Amount > 0 && !currencyCode.MatchString(pf.Currency):
			add("pay_floor.currency: %q must be an ISO 4217 code such as \"USD\"", pf.Currency)
		}
	}
	if fl := f.Flag; fl != nil {
		terms("flag.title_any", fl.TitleAny)
	}
	if s := f.Search; s != nil {
		switch n := len(s.TheMuse); {
		case n == 0 && s.HackerNews == nil:
			add("search: add themuse queries, hackernews or both, or leave search out")
		case n > maxQueries:
			add("search.themuse: %d queries is too many; the limit is %d", n, maxQueries)
		}
		for i, q := range s.TheMuse {
			field := fmt.Sprintf("search.themuse[%d]", i)
			if len(q.Categories) == 0 && len(q.Locations) == 0 {
				add("%s: set categories, locations or both, so the query doesn't ask for every job", field)
			}
			for _, list := range []struct {
				name   string
				values []string
			}{{"categories", q.Categories}, {"levels", q.Levels}, {"locations", q.Locations}} {
				if len(list.values) > maxQueryValues {
					add("%s.%s: more than %d values", field, list.name, maxQueryValues)
				}
				for j, v := range list.values {
					if strings.TrimSpace(v) == "" || len(v) > maxQueryValueLen {
						add("%s.%s[%d]: must be 1 to %d characters", field, list.name, j, maxQueryValueLen)
					}
				}
			}
			if q.Pages < 0 || q.Pages > MaxSearchPages {
				add("%s.pages: must be between 1 and %d", field, MaxSearchPages)
			}
		}
		if h := s.HackerNews; h != nil && (h.Months < 0 || h.Months > MaxHackerNewsMonths) {
			add("search.hackernews.months: must be between 1 and %d", MaxHackerNewsMonths)
		}
	}
	if len(p) > 0 {
		return &ValidationError{Problems: p}
	}
	return nil
}
