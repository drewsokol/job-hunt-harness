// Package conformance is the test suite every source must pass. A source's
// tests call Run with fixtures for a made-up board; the suite serves them
// from a local test server and checks the behavior refresh relies on.
package conformance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// Slug is the board every fixture describes.
const Slug = "exampleco"

// fixtureETag is what the test server sends as the board's version.
const fixtureETag = `"fixture-v1"`

// Case describes one source under test.
type Case struct {
	// New returns the source, reading from baseURL with client.
	New func(baseURL string, client *sources.Client) sources.Source
	// BasePath is the API's path prefix ("/v1/boards" for Greenhouse).
	BasePath string
	// Fixtures maps request paths under BasePath ("/exampleco/jobs") to the
	// JSON body to return. Query strings are ignored. Any other path is a 404.
	Fixtures map[string]string
	// URLs are posting URLs MatchURL must recognize, beyond the postings'
	// own URLs.
	URLs map[string]sources.PostingRef
	// NotURLs are URLs MatchURL must not recognize.
	NotURLs []string
	// Check makes source-specific assertions about the listed postings.
	Check func(t *testing.T, postings []sources.Posting)
}

// Serve starts a test server for fixtures (see Case.Fixtures) and returns
// its URL, ending in basePath, and a count of the requests it has served.
func Serve(t *testing.T, basePath string, fixtures map[string]string) (string, *atomic.Int64) {
	t.Helper()
	requests := new(atomic.Int64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "job-hunt-harness/") {
			http.Error(w, "missing User-Agent", http.StatusBadRequest)
			return
		}
		body, ok := fixtures[strings.TrimPrefix(r.URL.Path, basePath)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", fixtureETag)
		if r.Header.Get("If-None-Match") == fixtureETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + basePath, requests
}

// Run runs the suite.
func Run(t *testing.T, c Case) {
	base, requests := Serve(t, c.BasePath, c.Fixtures)
	client := sources.NewClient()
	src := c.New(base, client)
	ctx := context.Background()
	board := sources.Board{Slug: Slug}

	res, err := src.List(ctx, board, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	t.Run("identity", func(t *testing.T) {
		if src.Name() == "" || src.Prefix() == "" || strings.Contains(src.Prefix(), ":") {
			t.Errorf("Name %q, Prefix %q: both required, and the prefix can't contain ':'", src.Name(), src.Prefix())
		}
	})

	t.Run("postings are complete", func(t *testing.T) {
		if len(res.Postings) == 0 {
			t.Fatal("fixture board listed no postings")
		}
		seen := map[string]bool{}
		for _, p := range res.Postings {
			if p.ExternalID == "" || p.Title == "" {
				t.Errorf("posting %+v: ExternalID and Title are required", p)
			}
			if seen[p.ExternalID] {
				t.Errorf("ExternalID %q listed twice", p.ExternalID)
			}
			seen[p.ExternalID] = true
			if u, err := url.Parse(p.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				t.Errorf("posting %s: URL %q is not an absolute http(s) URL", p.ExternalID, p.URL)
			}
			for name, tm := range map[string]time.Time{"PostedAt": p.PostedAt, "UpdatedAt": p.UpdatedAt} {
				if !tm.IsZero() && tm.Location() != time.UTC {
					t.Errorf("posting %s: %s %v is not UTC", p.ExternalID, name, tm)
				}
			}
			switch p.Workplace {
			case "", "remote", "hybrid", "onsite":
			default:
				t.Errorf("posting %s: Workplace %q", p.ExternalID, p.Workplace)
			}
			for _, r := range p.Pay {
				if len(r.Currency) != 3 || r.Min > r.Max || r.Min <= 0 {
					t.Errorf("posting %s: bad pay range %+v", p.ExternalID, r)
				}
			}
		}
	})

	t.Run("etag", func(t *testing.T) {
		if res.ETag != fixtureETag {
			t.Fatalf("ETag = %q, want %q", res.ETag, fixtureETag)
		}
		again, err := src.List(ctx, board, res.ETag)
		if err != nil {
			t.Fatal(err)
		}
		if !again.NotModified || len(again.Postings) != 0 || again.ETag != fixtureETag {
			t.Errorf("List with current ETag = %+v, want NotModified and the same ETag", again)
		}
	})

	t.Run("missing board", func(t *testing.T) {
		_, err := src.List(ctx, sources.Board{Slug: "no-such-board"}, "")
		if !errors.Is(err, sources.ErrBoardNotFound) {
			t.Errorf("List on a missing board: %v, want ErrBoardNotFound", err)
		}
		if _, err := src.Probe(ctx, "no-such-board"); !errors.Is(err, sources.ErrBoardNotFound) {
			t.Errorf("Probe on a missing board: %v, want ErrBoardNotFound", err)
		}
	})

	t.Run("probe", func(t *testing.T) {
		n, err := src.Probe(ctx, Slug)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(res.Postings) {
			t.Errorf("Probe = %d, List has %d", n, len(res.Postings))
		}
	})

	t.Run("probe rejects unsafe slugs without a request", func(t *testing.T) {
		before := requests.Load()
		for _, slug := range []string{"", "../admin", "Example Co", "a/b", "x?y=1"} {
			if _, err := src.Probe(ctx, slug); err == nil {
				t.Errorf("Probe(%q) succeeded", slug)
			}
		}
		if n := requests.Load() - before; n != 0 {
			t.Errorf("unsafe slugs made %d requests", n)
		}
	})

	t.Run("response size limit", func(t *testing.T) {
		small := sources.NewClient()
		small.MaxBytes = 16
		_, err := c.New(base, small).List(ctx, board, "")
		if !errors.Is(err, sources.ErrTooLarge) {
			t.Errorf("List over the size limit: %v, want ErrTooLarge", err)
		}
	})

	t.Run("match url", func(t *testing.T) {
		for _, p := range res.Postings {
			u, _ := url.Parse(p.URL)
			ref, ok := src.MatchURL(u)
			if !ok {
				continue // a careers-site URL without a posting id
			}
			if ref.Source != src.Name() || ref.ExternalID != p.ExternalID {
				t.Errorf("MatchURL(%s) = %+v, want posting %s", p.URL, ref, p.ExternalID)
			}
		}
		for raw, want := range c.URLs {
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := src.MatchURL(u); !ok || got != want {
				t.Errorf("MatchURL(%s) = %+v, %v; want %+v", raw, got, ok, want)
			}
		}
		for _, raw := range c.NotURLs {
			u, _ := url.Parse(raw)
			if got, ok := src.MatchURL(u); ok {
				t.Errorf("MatchURL(%s) = %+v, want no match", raw, got)
			}
		}
	})

	t.Run("detail", func(t *testing.T) {
		d, ok := src.(sources.Detailer)
		if !ok {
			for _, p := range res.Postings {
				if p.Partial {
					t.Fatalf("posting %s is Partial but the source has no Detail", p.ExternalID)
				}
			}
			t.Skip("source lists full postings")
		}
		for _, p := range res.Postings {
			full, err := d.Detail(ctx, board, p.ExternalID)
			if err != nil {
				t.Fatalf("Detail(%s): %v", p.ExternalID, err)
			}
			if full.ExternalID != p.ExternalID || full.Title != p.Title || full.Partial || full.DescriptionHTML == "" {
				t.Errorf("Detail(%s) = %+v, want the same posting with a description", p.ExternalID, full)
			}
		}
		if _, err := d.Detail(ctx, board, "../../etc"); err == nil {
			t.Error("Detail accepted an unsafe posting id")
		}
	})

	if c.Check != nil {
		t.Run("source specific", func(t *testing.T) { c.Check(t, res.Postings) })
	}
}
