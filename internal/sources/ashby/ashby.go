// Package ashby reads Ashby job boards through the public Job Posting API
// (https://developers.ashbyhq.com/docs/public-job-posting-api).
package ashby

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// DefaultBaseURL is the public Job Posting API.
const DefaultBaseURL = "https://api.ashbyhq.com/posting-api/job-board"

// Source is the Ashby source.
type Source struct {
	Client  *sources.Client
	BaseURL string // DefaultBaseURL, or a test server
}

// New returns an Ashby source using client.
func New(client *sources.Client) *Source {
	return &Source{Client: client, BaseURL: DefaultBaseURL}
}

func (s *Source) Name() string   { return "ashby" }
func (s *Source) Prefix() string { return "ab" }

type boardResponse struct {
	Jobs []job `json:"jobs"`
}

type job struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	EmploymentType     string `json:"employmentType"`
	Location           string `json:"location"`
	SecondaryLocations []struct {
		Location string `json:"location"`
	} `json:"secondaryLocations"`
	PublishedAt   string `json:"publishedAt"`
	IsListed      *bool  `json:"isListed"`
	IsRemote      bool   `json:"isRemote"`
	WorkplaceType string `json:"workplaceType"` // Remote, Hybrid, OnSite
	Address       struct {
		PostalAddress struct {
			AddressCountry string `json:"addressCountry"`
		} `json:"postalAddress"`
	} `json:"address"`
	JobURL          string `json:"jobUrl"`
	DescriptionHTML string `json:"descriptionHtml"`
	Compensation    *struct {
		SalarySummary string `json:"scrapeableCompensationSalarySummary"`
		Tiers         []struct {
			Title      *string     `json:"title"`
			Components []component `json:"components"`
		} `json:"compensationTiers"`
		SummaryComponents []component `json:"summaryComponents"`
	} `json:"compensation"`
}

type component struct {
	CompensationType string   `json:"compensationType"`
	Interval         string   `json:"interval"`
	CurrencyCode     *string  `json:"currencyCode"`
	MinValue         *float64 `json:"minValue"`
	MaxValue         *float64 `json:"maxValue"`
}

func (s *Source) List(ctx context.Context, b sources.Board, etag string) (sources.ListResult, error) {
	var resp boardResponse
	notModified, newETag, err := s.Client.GetJSON(ctx, s.boardURL(b.Slug)+"?includeCompensation=true", etag, &resp)
	if err != nil || notModified {
		return sources.ListResult{NotModified: notModified, ETag: newETag}, err
	}
	out := sources.ListResult{ETag: newETag, Postings: make([]sources.Posting, 0, len(resp.Jobs))}
	for _, j := range resp.Jobs {
		if j.IsListed != nil && !*j.IsListed {
			continue // unlisted postings are only reachable by direct link
		}
		out.Postings = append(out.Postings, j.posting())
	}
	return out, nil
}

func (j job) posting() sources.Posting {
	locs := []string{j.Location}
	for _, l := range j.SecondaryLocations {
		if l.Location != "" {
			locs = append(locs, l.Location)
		}
	}
	p := sources.Posting{
		ExternalID:      j.ID,
		Title:           j.Title,
		LocationRaw:     strings.Join(locs, " | "),
		Workplace:       workplace(j.WorkplaceType, j.IsRemote),
		Country:         j.Address.PostalAddress.AddressCountry,
		URL:             j.JobURL,
		PostedAt:        sources.ParseTime(j.PublishedAt),
		EmploymentType:  j.EmploymentType,
		DescriptionHTML: j.DescriptionHTML,
	}
	if c := j.Compensation; c != nil {
		p.PayRaw = c.SalarySummary
		// Tiers carry labels ("Zone A"); the summary is the fallback.
		for _, t := range c.Tiers {
			label := ""
			if t.Title != nil {
				label = *t.Title
			}
			p.Pay = append(p.Pay, salaries(t.Components, label)...)
		}
		if len(p.Pay) == 0 {
			p.Pay = salaries(c.SummaryComponents, "")
		}
	}
	return p
}

// workplace reads Ashby's workplace fields. workplaceType wins: Ashby sets
// isRemote on hybrid roles too.
func workplace(workplaceType string, isRemote bool) string {
	switch strings.ToLower(workplaceType) {
	case "remote":
		return normalize.Remote
	case "hybrid":
		return normalize.Hybrid
	case "onsite", "on-site":
		return normalize.Onsite
	}
	if isRemote {
		return normalize.Remote
	}
	return ""
}

// salaries returns the salary components as pay ranges; equity and bonus
// components are skipped.
func salaries(cs []component, label string) []normalize.PayRange {
	var out []normalize.PayRange
	for _, c := range cs {
		if c.CompensationType != "Salary" || c.CurrencyCode == nil || c.MinValue == nil {
			continue
		}
		hi := *c.MinValue
		if c.MaxValue != nil {
			hi = *c.MaxValue
		}
		if r, ok := normalize.NewPayRange(*c.MinValue, hi, *c.CurrencyCode, c.Interval, label); ok {
			out = append(out, r)
		}
	}
	return out
}

func (s *Source) Probe(ctx context.Context, slug string) (int, error) {
	if err := sources.ValidSlug(slug); err != nil {
		return 0, err
	}
	res, err := s.List(ctx, sources.Board{Slug: slug}, "")
	return len(res.Postings), err
}

func (s *Source) boardURL(slug string) string { return s.BaseURL + "/" + url.PathEscape(slug) }

var (
	// jobs.ashbyhq.com/exampleco/<uuid>, with an optional /application.
	postingPath = regexp.MustCompile(`^/([a-z0-9][a-z0-9_.-]*)/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(?:/application)?$`)
	uuid        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// MatchURL recognizes jobs.ashbyhq.com posting and application URLs, and
// company careers pages that embed Ashby with ?ashby_jid=<uuid>. For an
// ashby_jid URL the slug is unknown and left empty.
func (s *Source) MatchURL(u *url.URL) (sources.PostingRef, bool) {
	if strings.ToLower(u.Hostname()) == "jobs.ashbyhq.com" {
		if m := postingPath.FindStringSubmatch(strings.TrimRight(strings.ToLower(u.Path), "/")); m != nil && sources.ValidSlug(m[1]) == nil {
			return sources.PostingRef{Source: s.Name(), Slug: m[1], ExternalID: m[2]}, true
		}
		return sources.PostingRef{}, false
	}
	if id := strings.ToLower(u.Query().Get("ashby_jid")); uuid.MatchString(id) {
		return sources.PostingRef{Source: s.Name(), ExternalID: id}, true
	}
	return sources.PostingRef{}, false
}
