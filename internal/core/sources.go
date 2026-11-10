package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// Sources are turned on and set up one by one (docs/design.md, "Turning
// sources on and off"). The filters say what the user is looking for; a
// source's settings say where jhh looks.

// Source statuses.
const (
	SourceOn         = "on"
	SourceOff        = "off"
	SourceNeedsSetup = "needs setup"
	// SourceRetired is a search source jhh no longer reads, because the
	// service behind it is gone. It can't be turned on.
	SourceRetired = "retired"
)

// Source kinds.
const (
	KindSearch = "search" // finds postings across employers, with its own switch
	KindBoard  = "board"  // reads the boards of the companies added; turned on and off per company
)

// SourceInfo is one source as the user and the agent see it.
type SourceInfo struct {
	Name   string
	Title  string // its name for people: "Hacker News"
	Kind   string
	Status string // for a search source: on, off, needs setup or retired
	// Settings are a search source's settings, with defaults filled in.
	Settings json.RawMessage
	// Setup says what the source still needs, when it needs setup.
	Setup string
	// Retired says why a retired source is no longer read.
	Retired string
	// Chosen is true once someone turned the source on or off.
	Chosen bool
	// Board sources: how many companies use it, and how many are read.
	Companies, EnabledCompanies int
	LastRunAt                   time.Time
	LastPostings                int
	LastError                   string // may quote the service's reply
	Reason                      string
	UpdatedAt                   time.Time
	UpdatedBy                   store.Actor
	// TakesKey says the source can use an optional key; KeySaved (set by
	// MarkKeys) whether one is saved, and KeyRefused whether the service
	// refused it on its last use. The key itself is never here.
	TakesKey, KeySaved, KeyRefused bool
}

// searchSpec is what jhh knows about a search source's settings.
type searchSpec struct {
	title string
	// check reads settings, fills in defaults, and returns what's wrong
	// with them, field by field, and what the source still needs to run.
	check func(raw json.RawMessage) (settings any, problems []string, setup string)
	// takesKey says the source can use an optional key, entered only on
	// the dashboard.
	takesKey bool
}

// searchOrder is the order search sources are listed in.
var searchOrder = []string{"hackernews", "himalayas"}

var searchSpecs = map[string]searchSpec{
	"hackernews": {title: sources.SearchTitle("hackernews"), check: checkHackerNews},
	"himalayas":  {title: sources.SearchTitle("himalayas"), check: checkHimalayas, takesKey: true},
}

// retiredSources are search sources jhh used to read, with why it stopped.
// They are listed only where someone set them up, so their jobs, which
// stay, can be explained. They are never read and can't be turned on.
var retiredSources = []struct{ name, why string }{
	{"themuse", "The Muse removed its public jobs API in November 2026, so jhh no longer searches it. " +
		"Jobs already found through it stay, but jhh can't tell when they close."},
}

// RetiredTitles are the names people know the retired search sources by
// (a job's via), so their jobs can say the source is gone.
func RetiredTitles() []string {
	out := make([]string, 0, len(retiredSources))
	for _, r := range retiredSources {
		out = append(out, sources.SearchTitle(r.name))
	}
	return out
}

func retired(name string) (string, bool) {
	for _, r := range retiredSources {
		if r.name == name {
			return r.why, true
		}
	}
	return "", false
}

// boardTitles are the board sources' names for people.
var boardTitles = map[string]string{"greenhouse": "Greenhouse", "lever": "Lever", "ashby": "Ashby"}

// HackerNewsSettings say how many of the latest monthly "Who is hiring?"
// threads a refresh reads. Ads aren't searched on Hacker News's side: every
// ad in those threads is judged by the filters.
type HackerNewsSettings struct {
	// Months is 1 to MaxHackerNewsMonths; 0 means DefaultHackerNewsMonths.
	Months int `json:"months,omitempty"`
}

// Hacker News thread limits. Two months by default, so the days after a
// new thread opens, when it holds few ads, still see last month's.
const (
	DefaultHackerNewsMonths = 2
	MaxHackerNewsMonths     = 3
)

// Query limits, shared by the search sources that take queries.
const (
	maxQueries       = 10
	maxQueryValues   = 20
	maxQueryValueLen = 100
)

// decodeSettings reads a source's settings, refusing fields it doesn't
// take, so a misspelled setting can't be silently ignored.
func decodeSettings(raw json.RawMessage, v any) []string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return []string{"settings: " + err.Error()}
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return []string{"settings: unexpected data after the settings object"}
	}
	return nil
}

func checkHackerNews(raw json.RawMessage) (any, []string, string) {
	var s HackerNewsSettings
	if p := decodeSettings(raw, &s); p != nil {
		return nil, p, ""
	}
	if s.Months < 0 || s.Months > MaxHackerNewsMonths {
		return nil, []string{fmt.Sprintf("settings.months: must be between 1 and %d", MaxHackerNewsMonths)}, ""
	}
	if s.Months == 0 {
		s.Months = DefaultHackerNewsMonths
	}
	return s, nil, ""
}

// searchStatus works out a search source's status from its stored row: a
// source runs once it has what it needs, unless someone turned it off.
func searchStatus(spec searchSpec, row store.SourceRow) (settings any, setup, status string) {
	settings, problems, setup := spec.check(row.Settings)
	switch {
	case problems != nil:
		// Stored settings were checked when saved, so this only happens when
		// a newer check is stricter. Ask for them again.
		settings, setup = nil, "its settings need to be saved again: "+strings.Join(problems, "; ")
		status = SourceNeedsSetup
	case setup != "":
		status = SourceNeedsSetup
	case row.Enabled != nil && !*row.Enabled:
		status = SourceOff
	default:
		status = SourceOn
	}
	return settings, setup, status
}

// ListSources returns every search source jhh ships, then the board
// sources (boards names them), with their status.
func ListSources(ctx context.Context, s *store.Store, boards []string) ([]SourceInfo, error) {
	rows, err := s.SourceRows(ctx)
	if err != nil {
		return nil, err
	}
	var out []SourceInfo
	for _, name := range searchOrder {
		spec, row := searchSpecs[name], rows[name]
		settings, setup, status := searchStatus(spec, row)
		info := SourceInfo{Name: name, Title: spec.title, Kind: KindSearch, Status: status, Setup: setup, Chosen: row.Enabled != nil,
			LastRunAt: row.LastRunAt, LastPostings: row.LastPostings, LastError: row.LastError,
			Reason: row.Reason, UpdatedAt: row.UpdatedAt, UpdatedBy: row.UpdatedBy, TakesKey: spec.takesKey}
		if spec.takesKey {
			info.KeyRefused = !readHimalayasState(row.State).KeyRefusedAt.IsZero()
		}
		if settings != nil {
			info.Settings = mustJSON(settings)
		}
		out = append(out, info)
	}
	for _, r := range retiredSources {
		row, ok := rows[r.name]
		if !ok {
			continue // never set up here, so there's nothing to explain
		}
		out = append(out, SourceInfo{Name: r.name, Title: sources.SearchTitle(r.name), Kind: KindSearch, Status: SourceRetired, Retired: r.why,
			Chosen: row.Enabled != nil, LastRunAt: row.LastRunAt, LastPostings: row.LastPostings, LastError: row.LastError,
			Reason: row.Reason, UpdatedAt: row.UpdatedAt, UpdatedBy: row.UpdatedBy})
	}
	counts, err := s.BoardCounts(ctx)
	if err != nil {
		return nil, err
	}
	boards = slices.Clone(boards)
	slices.Sort(boards)
	for _, name := range boards {
		title := boardTitles[name]
		if title == "" {
			title = name
		}
		c := counts[name]
		out = append(out, SourceInfo{Name: name, Title: title, Kind: KindBoard, Companies: c.Companies, EnabledCompanies: c.Enabled})
	}
	return out, nil
}

// SourceUpdate is a change to a search source. A nil field stays as it is.
type SourceUpdate struct {
	Enabled *bool
	// Settings replace the source's settings as a whole.
	Settings json.RawMessage
	Reason   string
}

// maxSourceReason caps the reason given for a change.
const maxSourceReason = 500

// SetSource turns a search source on or off, or replaces its settings, and
// returns it as it is now. Board sources are turned on and off per
// company.
func SetSource(ctx context.Context, s *store.Store, by store.Actor, name string, u SourceUpdate) (SourceInfo, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	spec, ok := searchSpecs[name]
	why, gone := retired(name)
	switch {
	case gone:
		return SourceInfo{}, &InputError{Problems: []string{fmt.Sprintf("name: %s is retired. %s Use one of %s.", sources.SearchTitle(name), why, strings.Join(searchOrder, ", "))}}
	case !ok && boardTitles[name] != "":
		return SourceInfo{}, &InputError{Problems: []string{fmt.Sprintf("name: %s is a board source, read for each company added; turn a company's board on or off with set_company", boardTitles[name])}}
	case !ok:
		return SourceInfo{}, &InputError{Problems: []string{fmt.Sprintf("name: %q is not a search source; use one of %s", name, strings.Join(searchOrder, ", "))}}
	case u.Enabled == nil && u.Settings == nil:
		return SourceInfo{}, &InputError{Problems: []string{"enabled or settings: say what to change"}}
	}
	u.Reason = strings.TrimSpace(u.Reason)
	if len(u.Reason) > maxSourceReason {
		return SourceInfo{}, &InputError{Problems: []string{fmt.Sprintf("reason: longer than %d characters", maxSourceReason)}}
	}
	c := store.SourceChange{Enabled: u.Enabled, Reason: u.Reason}
	if spec.takesKey && mentionsKey(u.Settings) {
		return SourceInfo{}, &InputError{Problems: []string{fmt.Sprintf("settings: %s's key is entered only by the user, on the dashboard's Sources tab (open_dashboard); it never goes in settings", spec.title)}}
	}
	if u.Settings != nil {
		settings, problems, _ := spec.check(u.Settings)
		if problems != nil {
			return SourceInfo{}, &InputError{Problems: problems}
		}
		c.Settings = mustJSON(settings)
	}
	if err := s.SetSource(ctx, by, name, c); err != nil {
		return SourceInfo{}, err
	}
	all, err := ListSources(ctx, s, nil)
	if err != nil {
		return SourceInfo{}, err
	}
	for _, info := range all {
		if info.Name == name {
			return info, nil
		}
	}
	return SourceInfo{}, fmt.Errorf("source %s missing after saving it", name)
}

// mentionsKey reports whether settings have a top-level field that looks
// like a key, so the error can say where keys go instead of only that the
// field is unknown.
func mentionsKey(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	for k := range m {
		switch strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(k)) {
		case "key", "apikey", "token", "apitoken", "secret":
			return true
		}
	}
	return false
}

// searchPlan is what a refresh searches: the settings of each search
// source that is on.
type searchPlan struct {
	hackerNews     *HackerNewsSettings
	himalayas      *HimalayasSettings
	himalayasState json.RawMessage
}

func plannedSearches(ctx context.Context, s *store.Store) (searchPlan, error) {
	var p searchPlan
	rows, err := s.SourceRows(ctx)
	if err != nil {
		return p, err
	}
	for _, name := range searchOrder {
		settings, _, status := searchStatus(searchSpecs[name], rows[name])
		if status != SourceOn {
			continue
		}
		switch v := settings.(type) {
		case HackerNewsSettings:
			p.hackerNews = &v
		case HimalayasSettings:
			p.himalayas, p.himalayasState = &v, rows[name].State
		}
	}
	return p, nil
}

// Keys is where the optional keys are kept (keys.File); nil means none.
type Keys interface{ Has(source string) bool }

// MarkKeys fills in whether a key is saved for each source that takes one.
func MarkKeys(ss []SourceInfo, k Keys) {
	for i := range ss {
		if ss[i].TakesKey && k != nil {
			ss[i].KeySaved = k.Has(ss[i].Name)
		}
	}
}

// SearchesOn counts the search sources that are on.
func SearchesOn(ss []SourceInfo) int {
	n := 0
	for _, info := range ss {
		if info.Kind == KindSearch && info.Status == SourceOn {
			n++
		}
	}
	return n
}

// PausedSearches are the titles of the search sources someone turned off.
func PausedSearches(ss []SourceInfo) []string {
	var out []string
	for _, info := range ss {
		if info.Kind == KindSearch && info.Status == SourceOff {
			out = append(out, info.Title)
		}
	}
	return out
}
