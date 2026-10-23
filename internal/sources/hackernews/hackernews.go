// Package hackernews reads Hacker News's monthly "Ask HN: Who is hiring?"
// threads through the public HN Search API by Algolia
// (https://hn.algolia.com/api), as a search source.
//
// The API needs no key. Each month the whoishiring account posts the
// thread, and every top-level comment is one employer's ad, by convention
// starting with a header line such as "Exampleco | Senior Engineer |
// Remote (US) | $150k-$180k". One request lists the recent threads and one
// more reads a whole thread. A post's link is its page on Hacker News,
// where the ad names how to apply.
package hackernews

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// DefaultBaseURL is the HN Search API.
const DefaultBaseURL = "https://hn.algolia.com/api/v1"

// Author is the account that posts the monthly threads.
const Author = "whoishiring"

// threadTitle matches the hiring thread, not the "Who wants to be hired?"
// or freelancer threads the same account posts.
var threadTitle = regexp.MustCompile(`^Ask HN: Who is hiring\? \([A-Z][a-z]+ \d{4}\)$`)

// Source is the Hacker News search source.
type Source struct {
	Client  *sources.Client
	BaseURL string // DefaultBaseURL, or a test server
}

// New returns the Hacker News source using client.
func New(client *sources.Client) *Source {
	return &Source{Client: client, BaseURL: DefaultBaseURL}
}

func (s *Source) Name() string   { return "hackernews" }
func (s *Source) Prefix() string { return "hn" }

type hit struct {
	ObjectID  string `json:"objectID"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
}

// Threads returns the newest n "Who is hiring?" threads, newest first.
func (s *Source) Threads(ctx context.Context, n int) ([]sources.Thread, error) {
	var res struct {
		Hits []hit `json:"hits"`
	}
	u := s.BaseURL + "/search_by_date?tags=story,author_" + Author + "&hitsPerPage=20"
	if _, _, err := s.Client.GetJSON(ctx, u, "", &res); err != nil {
		return nil, explain(err)
	}
	var out []sources.Thread
	for _, h := range res.Hits {
		if len(out) == n {
			break
		}
		if h.Author != Author || !threadTitle.MatchString(h.Title) || !numeric(h.ObjectID) {
			continue
		}
		out = append(out, sources.Thread{ID: h.ObjectID, Title: h.Title, PostedAt: sources.ParseTime(h.CreatedAt)})
	}
	if len(out) == 0 {
		return nil, errors.New("hacker news: no \"Who is hiring?\" thread found")
	}
	return out, nil
}

type item struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Author    string `json:"author"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
	Children  []item `json:"children"`
}

// ReadThread returns the ads in one thread: its top-level comments, each
// with an employer. Replies under them are conversation, not ads.
func (s *Source) ReadThread(ctx context.Context, id string) (sources.ThreadResult, error) {
	if !numeric(id) {
		return sources.ThreadResult{}, fmt.Errorf("hacker news: %q is not an item id", id)
	}
	var t item
	if _, _, err := s.Client.GetJSON(ctx, s.BaseURL+"/items/"+id, "", &t); err != nil {
		return sources.ThreadResult{}, explain(err)
	}
	if t.Type != "story" || t.Author != Author || !threadTitle.MatchString(t.Title) {
		return sources.ThreadResult{}, fmt.Errorf("hacker news: item %s is not a \"Who is hiring?\" thread", id)
	}
	var out sources.ThreadResult
	for _, c := range t.Children {
		if c.Type != "comment" || c.Author == "" || strings.TrimSpace(c.Text) == "" {
			continue // deleted
		}
		switch p, state := c.posting(); state {
		case adOK:
			out.Postings = append(out.Postings, p)
		case adUnreadable:
			out.Unreadable++
		}
	}
	return out, nil
}

// Lookup returns one ad, or sources.ErrPostingNotFound once it's deleted
// or says the job is filled.
func (s *Source) Lookup(ctx context.Context, externalID string) (sources.Posting, error) {
	if !numeric(externalID) {
		return sources.Posting{}, fmt.Errorf("hacker news: %q is not an item id", externalID)
	}
	var c item
	if _, _, err := s.Client.GetJSON(ctx, s.BaseURL+"/items/"+externalID, "", &c); err != nil {
		if errors.Is(err, sources.ErrBoardNotFound) {
			return sources.Posting{}, sources.ErrPostingNotFound
		}
		return sources.Posting{}, explain(err)
	}
	if c.Author == "" || strings.TrimSpace(c.Text) == "" {
		return sources.Posting{}, sources.ErrPostingNotFound
	}
	switch p, state := c.posting(); state {
	case adFilled:
		return sources.Posting{}, sources.ErrPostingNotFound
	case adUnreadable:
		// Still there, just without a header jhh can read. Callers only
		// need to know it isn't gone; the company and title are empty.
		return sources.Posting{ExternalID: externalID, URL: ItemURL(externalID), DescriptionHTML: c.Text}, nil
	default:
		return p, nil
	}
}

// ItemURL is a post's page on Hacker News.
func ItemURL(id string) string { return "https://news.ycombinator.com/item?id=" + id }

func (c item) posting() (sources.Posting, adState) {
	ad, state := parseAd(c.Text)
	if state != adOK {
		return sources.Posting{}, state
	}
	id := strconv.FormatInt(c.ID, 10)
	return sources.Posting{
		ExternalID:      id,
		Company:         ad.Company,
		Title:           ad.Title,
		LocationRaw:     ad.Location,
		PayRaw:          ad.Pay,
		EmploymentType:  ad.Employment,
		URL:             ItemURL(id),
		PostedAt:        sources.ParseTime(c.CreatedAt),
		DescriptionHTML: c.Text,
	}, adOK
}

func numeric(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0
}

// explain turns a failed request into an error that says what happened.
func explain(err error) error {
	var se *sources.StatusError
	if errors.As(err, &se) {
		if se.Code == http.StatusTooManyRequests {
			return fmt.Errorf("hacker news: %w (HTTP %d); the rest waits for a later refresh", sources.ErrRateLimited, se.Code)
		}
		return fmt.Errorf("hacker news: HTTP %d", se.Code)
	}
	return fmt.Errorf("hacker news: %w", err)
}
