// Package himalayas searches Himalayas's remote jobs API
// (https://himalayas.app/api), a search source that works without a key
// and takes an optional free one for higher limits (decision 0009).
//
// The API searches by free text, optionally narrowed by one country,
// seniority and employment type, and sorts by last update. Every job is
// remote; its locationRestrictions are the countries it may be done from,
// and an empty list means anywhere. Each job links to its page on
// Himalayas (applicationLink), which the terms ask to be used as given,
// and a job's own URL returns 410 once it has closed.
//
// A key is sent as a Bearer header, never in the URL, so it can't appear
// in an error.
package himalayas

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/keys"
	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// DefaultBaseURL is the jobs API.
const DefaultBaseURL = "https://himalayas.app/jobs/api"

// PageSize is how many jobs a search page holds.
const PageSize = 20

// DefaultInterval spaces requests one second apart, within the 60 a
// minute a key allows.
const DefaultInterval = time.Second

// Seniorities are the seniority names Himalayas accepts (2026-10-24).
var Seniorities = []string{"Entry-level", "Mid-level", "Senior", "Manager", "Director", "Executive"}

// EmploymentTypes are the employment type names Himalayas accepts.
var EmploymentTypes = []string{"Full Time", "Part Time", "Contractor", "Temporary", "Intern", "Volunteer", "Other"}

// Source is the Himalayas search source.
type Source struct {
	Client  *sources.Client
	BaseURL string // DefaultBaseURL, or a test server
	// Keys holds the optional key; nil means none.
	Keys     *keys.File
	Interval time.Duration

	mu   sync.Mutex
	last time.Time
}

// New returns the Himalayas source, reading its optional key from k.
func New(client *sources.Client, k *keys.File) *Source {
	return &Source{Client: client, BaseURL: DefaultBaseURL, Keys: k, Interval: DefaultInterval}
}

func (s *Source) Name() string   { return "himalayas" }
func (s *Source) Prefix() string { return "hm" }

// Check reports seniority and employment type names Himalayas doesn't
// have, suggesting the right spelling when only the case differs.
func Check(q sources.TextQuery) []string {
	var problems []string
	check := func(field string, values, known []string) {
		for i, v := range values {
			if slices.Contains(known, v) {
				continue
			}
			msg := fmt.Sprintf("%s[%d]: %q is not one of Himalayas's names", field, i, v)
			if j := slices.IndexFunc(known, func(k string) bool { return strings.EqualFold(k, v) }); j >= 0 {
				msg += fmt.Sprintf("; did you mean %q?", known[j])
			} else {
				msg += "; use one of " + strings.Join(known, ", ")
			}
			problems = append(problems, msg)
		}
	}
	check("seniority", q.Seniority, Seniorities)
	check("employment_types", q.EmploymentTypes, EmploymentTypes)
	return problems
}

type job struct {
	Title                string   `json:"title"`
	CompanyName          string   `json:"companyName"`
	EmploymentType       string   `json:"employmentType"`
	MinSalary            *float64 `json:"minSalary"`
	MaxSalary            *float64 `json:"maxSalary"`
	SalaryPeriod         string   `json:"salaryPeriod"`
	Currency             *string  `json:"currency"`
	LocationRestrictions []string `json:"locationRestrictions"`
	Description          string   `json:"description"`
	PubDate              int64    `json:"pubDate"`
	ExpiryDate           int64    `json:"expiryDate"`
	ApplicationLink      string   `json:"applicationLink"`
	GUID                 string   `json:"guid"`
}

type page struct {
	TotalCount int   `json:"totalCount"`
	Jobs       []job `json:"jobs"`
}

// SearchRecent returns one page of q, newest first by last update,
// numbered from 1.
func (s *Source) SearchRecent(ctx context.Context, q sources.TextQuery, n int) (sources.SearchResult, error) {
	v := url.Values{}
	v.Set("q", q.Q)
	v.Set("sort", "recent")
	v.Set("page", strconv.Itoa(n))
	if c := strings.TrimSpace(q.Country); c != "" {
		v.Set("country", c)
	}
	if q.ExcludeWorldwide {
		v.Set("exclude_worldwide", "true")
	}
	if len(q.Seniority) > 0 {
		v.Set("seniority", strings.Join(q.Seniority, ","))
	}
	if len(q.EmploymentTypes) > 0 {
		v.Set("employment_type", strings.Join(q.EmploymentTypes, ","))
	}
	var p page
	if err := s.get(ctx, "/search?"+v.Encode(), &p); err != nil {
		return sources.SearchResult{}, err
	}
	out := sources.SearchResult{Pages: (p.TotalCount + PageSize - 1) / PageSize, Postings: make([]sources.Posting, 0, len(p.Jobs))}
	for _, j := range p.Jobs {
		if p, ok := j.posting(); ok {
			out.Postings = append(out.Postings, p)
		}
	}
	return out, nil
}

// slugPart is what one part of a job's path may hold. Ids go into URLs, so
// anything else is refused before a request is made.
var slugPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)

// externalID turns a job's guid (https://himalayas.app/companies/<company>/jobs/<slug>)
// into "<company>/<slug>".
func externalID(guid string) (string, bool) {
	u, err := url.Parse(guid)
	if err != nil {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "companies" || parts[2] != "jobs" || !slugPart.MatchString(parts[1]) || !slugPart.MatchString(parts[3]) {
		return "", false
	}
	return parts[1] + "/" + parts[3], true
}

// Lookup returns one job, or sources.ErrPostingNotFound once it has
// closed (410) or its page no longer exists (404).
func (s *Source) Lookup(ctx context.Context, id string) (sources.Posting, error) {
	company, slug, ok := strings.Cut(id, "/")
	if !ok || !slugPart.MatchString(company) || !slugPart.MatchString(slug) {
		return sources.Posting{}, fmt.Errorf("himalayas: %q is not a job id", id)
	}
	var reply struct {
		Job job `json:"job"`
	}
	err := s.get(ctx, "/companies/"+company+"/jobs/"+slug, &reply)
	var se *sources.StatusError
	switch {
	case errors.Is(err, sources.ErrBoardNotFound), errors.As(err, &se) && se.Code == http.StatusGone:
		return sources.Posting{}, sources.ErrPostingNotFound
	case err != nil:
		return sources.Posting{}, err
	}
	p, ok := reply.Job.posting()
	if !ok {
		return sources.Posting{}, fmt.Errorf("himalayas: job %s came back without an employer or link", id)
	}
	return p, nil
}

// Verify checks a key with one small request, before it is saved. It
// returns sources.ErrKeyRefused when Himalayas doesn't accept it.
func (s *Source) Verify(ctx context.Context, key string) error {
	var p page
	return s.request(ctx, "/search?q=engineer&page=1", key, &p)
}

// get makes one request with the saved key, unless ctx says to go without
// it (sources.WithoutKey).
func (s *Source) get(ctx context.Context, path string, out any) error {
	key := ""
	if !sources.Keyless(ctx) {
		k, ok, err := s.Keys.Get(s.Name())
		if err != nil {
			return fmt.Errorf("himalayas: %w", err)
		}
		if ok {
			key = k
		}
	}
	return s.request(ctx, path, key, out)
}

// HasKey reports whether a key is saved for Himalayas.
func (s *Source) HasKey() bool { return s.Keys.Has(s.Name()) }

// request makes one request, spaced from the last one.
func (s *Source) request(ctx context.Context, path, key string, out any) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	var h http.Header
	if key != "" {
		h = http.Header{"Authorization": {"Bearer " + key}}
	}
	_, _, err := s.Client.GetJSONWith(ctx, s.BaseURL+path, "", h, out)
	if err == nil || ctx.Err() != nil || errors.Is(err, sources.ErrBoardNotFound) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	var se *sources.StatusError
	switch {
	case errors.As(err, &se) && se.Code == http.StatusUnauthorized && key != "":
		return fmt.Errorf("himalayas: %w (HTTP 401)", sources.ErrKeyRefused)
	case errors.As(err, &se) && se.Code == http.StatusTooManyRequests:
		return fmt.Errorf("himalayas: %w (HTTP 429); the rest waits for a later refresh", sources.ErrRateLimited)
	case errors.As(err, &se) && se.Code == http.StatusGone:
		return err // a closed job, for Lookup
	case errors.As(err, &se):
		return fmt.Errorf("himalayas: HTTP %d", se.Code)
	}
	return fmt.Errorf("himalayas: %w", err)
}

// wait spaces requests at least Interval apart.
func (s *Source) wait(ctx context.Context) error {
	s.mu.Lock()
	next := s.last.Add(s.Interval)
	if now := time.Now(); next.Before(now) {
		next = now
	}
	s.last = next
	s.mu.Unlock()
	t := time.NewTimer(time.Until(next))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (j job) posting() (sources.Posting, bool) {
	id, ok := externalID(j.GUID)
	if !ok || strings.TrimSpace(j.CompanyName) == "" || j.ApplicationLink == "" {
		return sources.Posting{}, false // a job without an employer or a page can't be placed
	}
	loc := "Worldwide"
	if len(j.LocationRestrictions) > 0 {
		loc = strings.Join(j.LocationRestrictions, " | ")
	}
	return sources.Posting{
		ExternalID:      id,
		Title:           j.Title,
		Company:         strings.TrimSpace(j.CompanyName),
		LocationRaw:     loc,
		Workplace:       normalize.Remote,
		URL:             j.ApplicationLink,
		PostedAt:        unix(j.PubDate),
		UpdatedAt:       unix(j.PubDate),
		ExpiresAt:       unix(j.ExpiryDate),
		Pay:             j.pay(),
		EmploymentType:  j.EmploymentType,
		DescriptionHTML: j.Description,
	}, true
}

// periods are the salary periods jhh reads as they are. Weekly and
// fortnightly pay is left out: turning it into a year would guess how many
// weeks are worked.
var periods = map[string]string{"annual": "year", "monthly": "month", "hourly": "hour"}

// pay reads the structured salary, only when it has a currency and a
// period jhh can state. A missing figure takes the other one's value.
func (j job) pay() []normalize.PayRange {
	period, ok := periods[j.SalaryPeriod]
	if !ok || j.Currency == nil || strings.TrimSpace(*j.Currency) == "" {
		return nil
	}
	var lo, hi float64
	if j.MinSalary != nil {
		lo = *j.MinSalary
	}
	if j.MaxSalary != nil {
		hi = *j.MaxSalary
	}
	switch {
	case lo <= 0 && hi <= 0:
		return nil
	case lo <= 0:
		lo = hi
	case hi <= 0:
		hi = lo
	}
	return []normalize.PayRange{{Min: lo, Max: hi, Currency: strings.ToUpper(strings.TrimSpace(*j.Currency)), Period: period}}
}

func unix(secs int64) time.Time {
	if secs <= 0 {
		return time.Time{}
	}
	return time.Unix(secs, 0).UTC()
}
