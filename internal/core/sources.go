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
	"github.com/drewsokol/job-hunt-harness/internal/sources/themuse"
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
)

// Source kinds.
const (
	KindSearch = "search" // finds postings across employers, with its own switch
	KindBoard  = "board"  // reads the boards of the companies added; turned on and off per company
)

// SourceInfo is one source as the user and the agent see it.
type SourceInfo struct {
	Name   string
	Title  string // its name for people: "The Muse"
	Kind   string
	Status string // for a search source: on, off or needs setup
	// Settings are a search source's settings, with defaults filled in.
	Settings json.RawMessage
	// Setup says what the source still needs, when it needs setup.
	Setup string
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
}

// searchSpec is what jhh knows about a search source's settings.
type searchSpec struct {
	title string
	// check reads settings, fills in defaults, and returns what's wrong
	// with them, field by field, and what the source still needs to run.
	check func(raw json.RawMessage) (settings any, problems []string, setup string)
}

// searchOrder is the order search sources are listed in.
var searchOrder = []string{"hackernews", "themuse"}

var searchSpecs = map[string]searchSpec{
	"hackernews": {title: sources.SearchTitle("hackernews"), check: checkHackerNews},
	"themuse":    {title: sources.SearchTitle("themuse"), check: checkMuse},
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

// MuseSettings are The Muse's queries. The Muse searches by its own
// category list, so the filters can't stand in for them.
type MuseSettings struct {
	Queries []MuseQuery `json:"queries"`
}

// MuseQuery is one search of The Muse. A job matches when it is in any of
// the categories, at any of the levels and in any of the locations; a field
// left out doesn't narrow. Category and level names must come from The
// Muse's lists. A location also matches every remote job ("Flexible /
// Remote"). Pages optionally caps how many pages of 20 a refresh reads; by
// default it reads them all, within the refresh's request limit.
type MuseQuery struct {
	Categories []string `json:"categories,omitempty"`
	Levels     []string `json:"levels,omitempty"`
	Locations  []string `json:"locations,omitempty"`
	Pages      int      `json:"pages,omitempty"`
}

// Muse query limits. MaxSearchPages matches the default request limit of a
// refresh (DefaultMaxSearchRequests).
const (
	MaxSearchPages   = DefaultMaxSearchRequests
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

func checkMuse(raw json.RawMessage) (any, []string, string) {
	s := MuseSettings{Queries: []MuseQuery{}}
	if p := decodeSettings(raw, &s); p != nil {
		return nil, p, ""
	}
	if s.Queries == nil {
		s.Queries = []MuseQuery{}
	}
	var p []string
	add := func(format string, args ...any) { p = append(p, fmt.Sprintf(format, args...)) }
	if n := len(s.Queries); n > maxQueries {
		add("settings.queries: %d queries is too many; the limit is %d", n, maxQueries)
	}
	for i, q := range s.Queries {
		field := fmt.Sprintf("settings.queries[%d]", i)
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
		for _, problem := range themuse.Check(museQuery(q)) {
			add("%s.%s", field, problem)
		}
	}
	if len(p) > 0 {
		return nil, p, ""
	}
	setup := ""
	if len(s.Queries) == 0 {
		setup = "needs at least one query: The Muse searches by its own categories, levels and locations"
	}
	return s, nil, setup
}

func museQuery(q MuseQuery) sources.Query {
	return sources.Query{Categories: q.Categories, Levels: q.Levels, Locations: q.Locations}
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
			Reason: row.Reason, UpdatedAt: row.UpdatedAt, UpdatedBy: row.UpdatedBy}
		if settings != nil {
			info.Settings = mustJSON(settings)
		}
		out = append(out, info)
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
// company, and keys can't be set here.
func SetSource(ctx context.Context, s *store.Store, by store.Actor, name string, u SourceUpdate) (SourceInfo, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	spec, ok := searchSpecs[name]
	switch {
	case !ok && boardTitles[name] != "":
		return SourceInfo{}, &InputError{Problems: []string{fmt.Sprintf("name: %s is a board source, read for each company added; turn a company's board on or off with set_company_enabled", boardTitles[name])}}
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

// searchPlan is what a refresh searches: the settings of each search
// source that is on.
type searchPlan struct {
	muse       *MuseSettings
	hackerNews *HackerNewsSettings
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
		case MuseSettings:
			p.muse = &v
		case HackerNewsSettings:
			p.hackerNews = &v
		}
	}
	return p, nil
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
