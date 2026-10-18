// Package greenhouse reads Greenhouse job boards through the public Job Board
// API (https://developers.greenhouse.io/job-board.html).
//
// The list endpoint is fetched without content=true, which makes it about
// 20 times smaller; descriptions and pay come from the detail endpoint, which
// refresh calls only for postings worth it.
package greenhouse

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// DefaultBaseURL is the public Job Board API.
const DefaultBaseURL = "https://boards-api.greenhouse.io/v1/boards"

// Source is the Greenhouse source.
type Source struct {
	Client  *sources.Client
	BaseURL string // DefaultBaseURL, or a test server
}

// New returns a Greenhouse source using client.
func New(client *sources.Client) *Source {
	return &Source{Client: client, BaseURL: DefaultBaseURL}
}

func (s *Source) Name() string   { return "greenhouse" }
func (s *Source) Prefix() string { return "gh" }

type listResponse struct {
	Jobs []job `json:"jobs"`
}

type job struct {
	ID             int64  `json:"id"`
	InternalJobID  int64  `json:"internal_job_id"`
	Title          string `json:"title"`
	AbsoluteURL    string `json:"absolute_url"`
	UpdatedAt      string `json:"updated_at"`
	FirstPublished string `json:"first_published"`
	Location       struct {
		Name string `json:"name"`
	} `json:"location"`
	// Detail only.
	Content string `json:"content"`
	Offices []struct {
		Name     string `json:"name"`
		Location string `json:"location"`
	} `json:"offices"`
	PayInputRanges []struct {
		MinCents     float64 `json:"min_cents"`
		MaxCents     float64 `json:"max_cents"`
		CurrencyType string  `json:"currency_type"`
		Title        string  `json:"title"`
	} `json:"pay_input_ranges"`
}

func (s *Source) List(ctx context.Context, b sources.Board, etag string) (sources.ListResult, error) {
	var resp listResponse
	notModified, newETag, err := s.Client.GetJSON(ctx, s.boardURL(b.Slug)+"/jobs", etag, &resp)
	if err != nil || notModified {
		return sources.ListResult{NotModified: notModified, ETag: newETag}, err
	}
	out := sources.ListResult{ETag: newETag, Postings: make([]sources.Posting, 0, len(resp.Jobs))}
	for _, j := range resp.Jobs {
		p := j.posting()
		p.Partial = true
		out.Postings = append(out.Postings, p)
	}
	return out, nil
}

// Detail fetches one posting with its description and pay.
func (s *Source) Detail(ctx context.Context, b sources.Board, externalID string) (sources.Posting, error) {
	if _, err := strconv.ParseInt(externalID, 10, 64); err != nil {
		return sources.Posting{}, fmt.Errorf("greenhouse posting id %q is not a number", externalID)
	}
	var j job
	if _, _, err := s.Client.GetJSON(ctx, s.boardURL(b.Slug)+"/jobs/"+externalID+"?pay_transparency=true", "", &j); err != nil {
		return sources.Posting{}, err
	}
	p := j.posting()
	// Greenhouse sends content entity-escaped ("&lt;p&gt;").
	p.DescriptionHTML = html.UnescapeString(j.Content)
	for _, r := range j.PayInputRanges {
		if pr, ok := normalize.NewPayRange(r.MinCents/100, r.MaxCents/100, r.CurrencyType, r.Title, r.Title); ok {
			p.Pay = append(p.Pay, pr)
		}
		if p.PayRaw != "" {
			p.PayRaw += "; "
		}
		p.PayRaw += fmt.Sprintf("%s %s %.0f-%.0f", strings.TrimSpace(r.Title), r.CurrencyType, r.MinCents/100, r.MaxCents/100)
	}
	return p, nil
}

func (j job) posting() sources.Posting {
	p := sources.Posting{
		ExternalID:  strconv.FormatInt(j.ID, 10),
		Title:       j.Title,
		LocationRaw: j.Location.Name,
		URL:         j.AbsoluteURL,
		PostedAt:    sources.ParseTime(j.FirstPublished),
		UpdatedAt:   sources.ParseTime(j.UpdatedAt),
	}
	if j.InternalJobID != 0 {
		p.OpeningKey = strconv.FormatInt(j.InternalJobID, 10)
	}
	return p
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
	// boards.greenhouse.io/exampleco/jobs/123, job-boards.greenhouse.io/...,
	// and the EU variants.
	boardPath = regexp.MustCompile(`^/([a-z0-9][a-z0-9_-]*)/jobs/(\d+)$`)
	digits    = regexp.MustCompile(`^\d+$`)
)

// MatchURL recognizes Greenhouse board URLs, embedded application URLs
// (?for=slug&token=id), and company careers pages that carry ?gh_jid=id.
// For a gh_jid URL the slug is unknown and left empty.
func (s *Source) MatchURL(u *url.URL) (sources.PostingRef, bool) {
	host := strings.ToLower(u.Hostname())
	q := u.Query()
	if strings.HasSuffix(host, "greenhouse.io") {
		if m := boardPath.FindStringSubmatch(strings.TrimRight(u.Path, "/")); m != nil {
			return sources.PostingRef{Source: s.Name(), Slug: m[1], ExternalID: m[2]}, true
		}
		if slug, id := q.Get("for"), q.Get("token"); slug != "" && digits.MatchString(id) && sources.ValidSlug(slug) == nil {
			return sources.PostingRef{Source: s.Name(), Slug: slug, ExternalID: id}, true
		}
	}
	if id := q.Get("gh_jid"); digits.MatchString(id) {
		return sources.PostingRef{Source: s.Name(), ExternalID: id}, true
	}
	return sources.PostingRef{}, false
}
