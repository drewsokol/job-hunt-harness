package themuse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

func newTestSource(t *testing.T, h http.HandlerFunc) *Source {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s := New(sources.NewClient())
	s.BaseURL = srv.URL + "/api/public/jobs"
	return s
}

func TestSearch(t *testing.T) {
	body, err := os.ReadFile("testdata/page.json")
	if err != nil {
		t.Fatal(err)
	}
	var got *http.Request
	s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Write(body)
	})
	q := sources.Query{Categories: []string{"Software Engineering", "Data Science"}, Levels: []string{"Senior Level"}, Locations: []string{Remote}}
	res, err := s.Search(context.Background(), q, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/api/public/jobs" {
		t.Errorf("path %s", got.URL.Path)
	}
	params := got.URL.Query()
	if params.Get("page") != "1" || !reflect.DeepEqual(params["category"], q.Categories) ||
		!reflect.DeepEqual(params["level"], q.Levels) || !reflect.DeepEqual(params["location"], q.Locations) {
		t.Errorf("query %v", params)
	}
	if params.Has("api_key") {
		t.Error("an api key was sent")
	}

	if res.Pages != 2 || len(res.Postings) != 2 {
		t.Fatalf("pages %d, %d postings; want 2 pages, 2 postings (one has no employer)", res.Pages, len(res.Postings))
	}
	p := res.Postings[0]
	if p.ExternalID != "90000001" || p.Title != "Senior Backend Engineer" || p.Company != "Exampleco, Inc." {
		t.Errorf("id %q, title %q, company %q", p.ExternalID, p.Title, p.Company)
	}
	if p.LocationRaw != "Flexible / Remote | Columbus, OH" {
		t.Errorf("location %q", p.LocationRaw)
	}
	if p.URL != "https://www.themuse.com/jobs/exampleco/senior-backend-engineer-abc123" {
		t.Errorf("url %q", p.URL)
	}
	if !strings.Contains(p.DescriptionHTML, "made-up software") || p.Partial {
		t.Errorf("description %q, partial %v", p.DescriptionHTML, p.Partial)
	}
	if !p.PostedAt.Equal(time.Date(2026, 10, 12, 14, 5, 9, 0, time.UTC)) {
		t.Errorf("posted %s", p.PostedAt)
	}
}

func TestSearchRateLimit(t *testing.T) {
	s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	_, err := s.Search(context.Background(), sources.Query{Categories: []string{"Software Engineering"}}, 0)
	if !errors.Is(err, sources.ErrRateLimited) {
		t.Errorf("error %v, want ErrRateLimited", err)
	}
}

func TestLookup(t *testing.T) {
	s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/public/jobs/90000001":
			w.Write([]byte(`{"id": 90000001, "name": "Senior Backend Engineer", "company": {"name": "Exampleco"}, "refs": {"landing_page": "https://www.themuse.com/jobs/exampleco/x"}}`))
		default:
			http.NotFound(w, r)
		}
	})
	p, err := s.Lookup(context.Background(), "90000001")
	if err != nil || p.Title != "Senior Backend Engineer" {
		t.Errorf("Lookup = %+v, %v", p, err)
	}
	if _, err := s.Lookup(context.Background(), "90000009"); !errors.Is(err, sources.ErrPostingNotFound) {
		t.Errorf("missing job: %v, want ErrPostingNotFound", err)
	}
	if _, err := s.Lookup(context.Background(), "../admin"); err == nil {
		t.Error("a non-numeric id was looked up")
	}
}

func TestCheck(t *testing.T) {
	s := New(sources.NewClient())
	if p := s.Check(sources.Query{Categories: []string{"Software Engineering"}, Levels: []string{"Senior Level"}, Locations: []string{"Anywhere at all"}}); len(p) != 0 {
		t.Errorf("valid query: %v", p)
	}
	p := s.Check(sources.Query{Categories: []string{"software engineering", "Basket Weaving"}, Levels: []string{"Staff"}})
	if len(p) != 3 || !strings.Contains(p[0], `did you mean "Software Engineering"`) ||
		!strings.Contains(p[1], `"Basket Weaving" is not one of`) || !strings.HasPrefix(p[2], "levels[0]") {
		t.Errorf("problems %q", p)
	}
}
