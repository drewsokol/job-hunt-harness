// Package themuse searches The Muse's public jobs API
// (https://www.themuse.com/developers/api/v2), jhh's built-in search source.
//
// The API needs no key. It filters by category, level and location (there
// is no keyword search), returns 20 jobs a page with full descriptions, and
// links each job to its page on The Muse, which has the button to apply on
// the employer's site. jhh only reads the API, never The Muse's web pages.
package themuse

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// DefaultBaseURL is the public jobs API.
const DefaultBaseURL = "https://www.themuse.com/api/public/jobs"

// PageSize is how many jobs a page holds.
const PageSize = 20

// Categories are the category names The Muse accepts, from its API
// documentation (2026-10-19). An unknown name silently matches nothing, so
// queries are checked against this list.
var Categories = []string{
	"Accounting", "Accounting and Finance", "Account Management", "Account Management/Customer Success",
	"Administration and Office", "Advertising and Marketing", "Animal Care", "Arts", "Business Operations",
	"Cleaning and Facilities", "Computer and IT", "Construction", "Corporate", "Customer Service",
	"Data and Analytics", "Data Science", "Design", "Design and UX", "Editor", "Education",
	"Energy Generation and Mining", "Entertainment and Travel Services", "Farming and Outdoors",
	"Food and Hospitality Services", "Healthcare", "HR", "Human Resources and Recruitment",
	"Installation, Maintenance, and Repairs", "IT", "Law", "Legal Services", "Management",
	"Manufacturing and Warehouse", "Marketing", "Mechanic", "Media, PR, and Communications", "Mental Health",
	"Nurses", "Office Administration", "Personal Care and Services", "Physical Assistant", "Product",
	"Product Management", "Project Management", "Protective Services", "Public Relations", "Real Estate",
	"Recruiting", "Retail", "Sales", "Science and Engineering", "Social Services", "Software Engineer",
	"Software Engineering", "Sports, Fitness, and Recreation", "Transportation and Logistics", "Unknown", "UX",
	"Videography", "Writer", "Writing and Editing",
}

// Levels are the level names The Muse accepts.
var Levels = []string{"Entry Level", "Mid Level", "Senior Level", "management", "Internship"}

// Remote is the location name The Muse uses for remote and flexible jobs.
const Remote = "Flexible / Remote"

// Source is The Muse search source.
type Source struct {
	Client  *sources.Client
	BaseURL string // DefaultBaseURL, or a test server
}

// New returns The Muse source using client.
func New(client *sources.Client) *Source {
	return &Source{Client: client, BaseURL: DefaultBaseURL}
}

func (s *Source) Name() string   { return "themuse" }
func (s *Source) Prefix() string { return "tm" }

// Check reports categories and levels The Muse doesn't have.
func (s *Source) Check(q sources.Query) []string { return Check(q) }

// Check reports categories and levels The Muse doesn't have, suggesting
// the right spelling when only the case differs.
func Check(q sources.Query) []string {
	var problems []string
	check := func(field string, values, known []string) {
		for i, v := range values {
			if slices.Contains(known, v) {
				continue
			}
			msg := fmt.Sprintf("%s[%d]: %q is not one of The Muse's %s", field, i, v, field)
			if j := slices.IndexFunc(known, func(k string) bool { return strings.EqualFold(k, v) }); j >= 0 {
				msg += fmt.Sprintf("; did you mean %q?", known[j])
			} else {
				msg += "; use one of " + strings.Join(known, ", ")
			}
			problems = append(problems, msg)
		}
	}
	check("categories", q.Categories, Categories)
	check("levels", q.Levels, Levels)
	return problems
}

type job struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Contents        string `json:"contents"`
	PublicationDate string `json:"publication_date"`
	Locations       []struct {
		Name string `json:"name"`
	} `json:"locations"`
	Company struct {
		Name string `json:"name"`
	} `json:"company"`
	Refs struct {
		LandingPage string `json:"landing_page"`
	} `json:"refs"`
}

type page struct {
	Page      int   `json:"page"`
	PageCount int   `json:"page_count"`
	Results   []job `json:"results"`
}

// Search returns one page of q, numbered from 0.
func (s *Source) Search(ctx context.Context, q sources.Query, n int) (sources.SearchResult, error) {
	v := url.Values{}
	v.Set("page", strconv.Itoa(n))
	for _, c := range q.Categories {
		v.Add("category", c)
	}
	for _, l := range q.Levels {
		v.Add("level", l)
	}
	for _, l := range q.Locations {
		v.Add("location", l)
	}
	var p page
	if _, _, err := s.Client.GetJSON(ctx, s.BaseURL+"?"+v.Encode(), "", &p); err != nil {
		return sources.SearchResult{}, explain(err)
	}
	out := sources.SearchResult{Pages: p.PageCount, Postings: make([]sources.Posting, 0, len(p.Results))}
	for _, j := range p.Results {
		if j.ID == 0 || strings.TrimSpace(j.Company.Name) == "" {
			continue // a job without an employer can't be placed
		}
		out.Postings = append(out.Postings, j.posting())
	}
	return out, nil
}

// Lookup returns one job, or sources.ErrPostingNotFound once The Muse no
// longer has it.
func (s *Source) Lookup(ctx context.Context, externalID string) (sources.Posting, error) {
	if _, err := strconv.ParseInt(externalID, 10, 64); err != nil {
		return sources.Posting{}, fmt.Errorf("the muse: %q is not a job id", externalID)
	}
	var j job
	if _, _, err := s.Client.GetJSON(ctx, s.BaseURL+"/"+externalID, "", &j); err != nil {
		if errors.Is(err, sources.ErrBoardNotFound) {
			return sources.Posting{}, sources.ErrPostingNotFound
		}
		return sources.Posting{}, explain(err)
	}
	return j.posting(), nil
}

func (j job) posting() sources.Posting {
	locs := make([]string, 0, len(j.Locations))
	for _, l := range j.Locations {
		if name := strings.TrimSpace(l.Name); name != "" {
			locs = append(locs, name)
		}
	}
	return sources.Posting{
		ExternalID:      strconv.FormatInt(j.ID, 10),
		Title:           j.Name,
		Company:         strings.TrimSpace(j.Company.Name),
		LocationRaw:     strings.Join(locs, " | "),
		URL:             j.Refs.LandingPage,
		PostedAt:        sources.ParseTime(j.PublicationDate),
		DescriptionHTML: j.Contents,
	}
}

// explain turns a failed request into an error that says what happened.
// The Muse answers 403 when the hourly limit is used up.
func explain(err error) error {
	var se *sources.StatusError
	if errors.As(err, &se) {
		if se.Code == http.StatusForbidden || se.Code == http.StatusTooManyRequests {
			return fmt.Errorf("the muse: %w (HTTP %d); the rest waits for a later refresh", sources.ErrRateLimited, se.Code)
		}
		return fmt.Errorf("the muse: HTTP %d", se.Code)
	}
	return fmt.Errorf("the muse: %w", err)
}
