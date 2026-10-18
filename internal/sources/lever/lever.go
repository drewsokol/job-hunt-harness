// Package lever reads Lever job boards through the public Postings API
// (https://github.com/lever/postings-api).
package lever

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// DefaultBaseURL is the public Postings API.
const DefaultBaseURL = "https://api.lever.co/v0/postings"

// Source is the Lever source.
type Source struct {
	Client  *sources.Client
	BaseURL string // DefaultBaseURL, or a test server
}

// New returns a Lever source using client.
func New(client *sources.Client) *Source {
	return &Source{Client: client, BaseURL: DefaultBaseURL}
}

func (s *Source) Name() string   { return "lever" }
func (s *Source) Prefix() string { return "lv" }

type posting struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	HostedURL  string `json:"hostedUrl"`
	CreatedAt  int64  `json:"createdAt"`
	Country    string `json:"country"`
	Workplace  string `json:"workplaceType"` // remote, hybrid, onsite, unspecified
	Categories struct {
		Commitment   string   `json:"commitment"`
		Location     string   `json:"location"`
		AllLocations []string `json:"allLocations"`
	} `json:"categories"`
	Description string `json:"description"`
	Lists       []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	} `json:"lists"`
	Additional  string `json:"additional"`
	SalaryRange *struct {
		Min      float64 `json:"min"`
		Max      float64 `json:"max"`
		Currency string  `json:"currency"`
		Interval string  `json:"interval"`
	} `json:"salaryRange"`
	SalaryDescription string `json:"salaryDescriptionPlain"`
}

func (s *Source) List(ctx context.Context, b sources.Board, etag string) (sources.ListResult, error) {
	var resp []posting
	notModified, newETag, err := s.Client.GetJSON(ctx, s.boardURL(b.Slug)+"?mode=json", etag, &resp)
	if err != nil || notModified {
		return sources.ListResult{NotModified: notModified, ETag: newETag}, err
	}
	out := sources.ListResult{ETag: newETag, Postings: make([]sources.Posting, 0, len(resp))}
	for _, p := range resp {
		out.Postings = append(out.Postings, p.posting())
	}
	return out, nil
}

func (p posting) posting() sources.Posting {
	locs := p.Categories.AllLocations
	if len(locs) == 0 && p.Categories.Location != "" {
		locs = []string{p.Categories.Location}
	}
	out := sources.Posting{
		ExternalID:      p.ID,
		Title:           p.Text,
		LocationRaw:     strings.Join(locs, " | "),
		Country:         p.Country,
		URL:             p.HostedURL,
		PostedAt:        sources.UnixMillis(p.CreatedAt),
		EmploymentType:  p.Categories.Commitment,
		DescriptionHTML: p.descriptionHTML(),
		PayRaw:          p.SalaryDescription,
	}
	switch p.Workplace {
	case "remote", "hybrid", "onsite":
		out.Workplace = p.Workplace
	}
	if r := p.SalaryRange; r != nil {
		if pr, ok := normalize.NewPayRange(r.Min, r.Max, r.Currency, r.Interval, ""); ok {
			out.Pay = []normalize.PayRange{pr}
		}
	}
	return out
}

// descriptionHTML joins Lever's description parts in the order its hosted
// page shows them: the description, each titled list, then the closing text.
func (p posting) descriptionHTML() string {
	var b strings.Builder
	b.WriteString(p.Description)
	for _, l := range p.Lists {
		b.WriteString("<h3>")
		b.WriteString(htmlEscape(l.Text))
		b.WriteString("</h3><ul>")
		b.WriteString(l.Content)
		b.WriteString("</ul>")
	}
	b.WriteString(p.Additional)
	return b.String()
}

var htmlEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

func (s *Source) Probe(ctx context.Context, slug string) (int, error) {
	if err := sources.ValidSlug(slug); err != nil {
		return 0, err
	}
	res, err := s.List(ctx, sources.Board{Slug: slug}, "")
	return len(res.Postings), err
}

func (s *Source) boardURL(slug string) string { return s.BaseURL + "/" + url.PathEscape(slug) }

// jobs.lever.co/exampleco/<uuid>, with an optional /apply.
var postingPath = regexp.MustCompile(`^/([a-z0-9][a-z0-9_-]*)/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(?:/apply)?$`)

// MatchURL recognizes jobs.lever.co posting and apply URLs.
func (s *Source) MatchURL(u *url.URL) (sources.PostingRef, bool) {
	if strings.ToLower(u.Hostname()) != "jobs.lever.co" {
		return sources.PostingRef{}, false
	}
	m := postingPath.FindStringSubmatch(strings.TrimRight(strings.ToLower(u.Path), "/"))
	if m == nil {
		return sources.PostingRef{}, false
	}
	return sources.PostingRef{Source: s.Name(), Slug: m[1], ExternalID: m[2]}, true
}
