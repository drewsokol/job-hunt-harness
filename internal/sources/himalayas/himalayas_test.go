package himalayas

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/keys"
	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

const testKey = "made-up-key-0123456789"

func newTestSource(t *testing.T, key string, h http.HandlerFunc) *Source {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	k := keys.Open(filepath.Join(t.TempDir(), "keys.json"))
	if key != "" {
		if err := k.Set("himalayas", key); err != nil {
			t.Fatal(err)
		}
	}
	s := New(sources.NewClient(), k)
	s.BaseURL = srv.URL + "/jobs/api"
	s.Interval = 0
	return s
}

func TestSearchRecent(t *testing.T) {
	body, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	var got *http.Request
	s := newTestSource(t, "", func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Write(body)
	})
	q := sources.TextQuery{Q: "platform engineer", Country: "US", Seniority: []string{"Senior", "Manager"}, EmploymentTypes: []string{"Full Time"}, ExcludeWorldwide: true}
	res, err := s.SearchRecent(context.Background(), q, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/jobs/api/search" {
		t.Errorf("path %s", got.URL.Path)
	}
	v := got.URL.Query()
	if v.Get("q") != "platform engineer" || v.Get("country") != "US" || v.Get("seniority") != "Senior,Manager" ||
		v.Get("employment_type") != "Full Time" || v.Get("exclude_worldwide") != "true" || v.Get("sort") != "recent" || v.Get("page") != "2" {
		t.Errorf("query %v", v)
	}
	if got.Header.Get("Authorization") != "" {
		t.Error("a key was sent without one saved")
	}

	if res.Pages != 3 || len(res.Postings) != 3 {
		t.Fatalf("%d pages, %d postings; want 3 pages (45 jobs) and 3 postings (one has no employer, one an odd link)", res.Pages, len(res.Postings))
	}
	p := res.Postings[0]
	if p.ExternalID != "exampleco/senior-platform-engineer" || p.Title != "Senior Platform Engineer" || p.Company != "Exampleco" ||
		p.LocationRaw != "United States | Canada" || p.Workplace != normalize.Remote || p.EmploymentType != "Full Time" ||
		p.URL != "https://himalayas.app/companies/exampleco/jobs/senior-platform-engineer?utm_source=api" ||
		!strings.Contains(p.DescriptionHTML, "made-up software") {
		t.Errorf("posting %+v", p)
	}
	if want := time.Unix(1792832400, 0).UTC(); !p.PostedAt.Equal(want) || !p.UpdatedAt.Equal(want) {
		t.Errorf("posted %s, updated %s", p.PostedAt, p.UpdatedAt)
	}
	if !p.ExpiresAt.Equal(time.Unix(1798016400, 0).UTC()) {
		t.Errorf("expires %s", p.ExpiresAt)
	}
	if len(p.Pay) != 1 || p.Pay[0] != (normalize.PayRange{Min: 150000, Max: 180000, Currency: "USD", Period: "year"}) {
		t.Errorf("pay %+v", p.Pay)
	}
	if b := res.Postings[1]; b.LocationRaw != "Worldwide" || len(b.Pay) != 1 || b.Pay[0] != (normalize.PayRange{Min: 60, Max: 60, Currency: "EUR", Period: "hour"}) {
		t.Errorf("worldwide hourly job %+v", b)
	}
	if w := res.Postings[2]; len(w.Pay) != 0 {
		t.Errorf("weekly pay was turned into %+v; it should be left out", w.Pay)
	}
}

func TestKey(t *testing.T) {
	var auth []string
	s := newTestSource(t, testKey, func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "" && r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"totalCount": 0, "jobs": []}`))
	})
	ctx := context.Background()
	if _, err := s.SearchRecent(ctx, sources.TextQuery{Q: "engineer"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SearchRecent(sources.WithoutKey(ctx), sources.TextQuery{Q: "engineer"}, 1); err != nil {
		t.Fatal(err)
	}
	if len(auth) != 2 || auth[0] != "Bearer "+testKey || auth[1] != "" {
		t.Errorf("Authorization headers %q; want the key, then none", auth)
	}
	if !s.HasKey() {
		t.Error("HasKey")
	}
	if err := s.Verify(ctx, testKey); err != nil {
		t.Errorf("good key: %v", err)
	}
	err := s.Verify(ctx, "made-up-wrong-key")
	if !errors.Is(err, sources.ErrKeyRefused) || strings.Contains(err.Error(), "made-up-wrong-key") {
		t.Errorf("wrong key: %v", err)
	}
}

func TestErrors(t *testing.T) {
	s := newTestSource(t, "", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	if _, err := s.SearchRecent(context.Background(), sources.TextQuery{Q: "engineer"}, 1); !errors.Is(err, sources.ErrRateLimited) {
		t.Errorf("429: %v", err)
	}
}

func TestLookup(t *testing.T) {
	s := newTestSource(t, "", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jobs/api/companies/exampleco/jobs/senior-platform-engineer":
			w.Write([]byte(`{"job": {"title": "Senior Platform Engineer", "companyName": "Exampleco", "pubDate": 1792900000, "expiryDate": 1798100000,
				"applicationLink": "https://himalayas.app/companies/exampleco/jobs/senior-platform-engineer",
				"guid": "https://himalayas.app/companies/exampleco/jobs/senior-platform-engineer"}}`))
		case "/jobs/api/companies/exampleco/jobs/closed-job":
			w.WriteHeader(http.StatusGone)
			w.Write([]byte(`{"ok": false, "expiryDate": 1792000000}`))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	p, err := s.Lookup(ctx, "exampleco/senior-platform-engineer")
	if err != nil || p.Title != "Senior Platform Engineer" || !p.ExpiresAt.Equal(time.Unix(1798100000, 0).UTC()) {
		t.Errorf("Lookup = %+v, %v", p, err)
	}
	for _, id := range []string{"exampleco/closed-job", "exampleco/no-such-job"} {
		if _, err := s.Lookup(ctx, id); !errors.Is(err, sources.ErrPostingNotFound) {
			t.Errorf("%s: %v, want ErrPostingNotFound", id, err)
		}
	}
	for _, id := range []string{"../admin", "exampleco", "a/b/c"} {
		if _, err := s.Lookup(ctx, id); err == nil || errors.Is(err, sources.ErrPostingNotFound) {
			t.Errorf("%s was looked up: %v", id, err)
		}
	}
}

func TestCheck(t *testing.T) {
	if p := Check(sources.TextQuery{Q: "x", Seniority: []string{"Senior"}, EmploymentTypes: []string{"Full Time"}}); len(p) != 0 {
		t.Errorf("valid query: %v", p)
	}
	p := Check(sources.TextQuery{Seniority: []string{"senior", "Staff"}, EmploymentTypes: []string{"FullTime"}})
	if len(p) != 3 || !strings.Contains(p[0], `did you mean "Senior"`) || !strings.Contains(p[1], `"Staff" is not one of`) ||
		!strings.HasPrefix(p[2], "employment_types[0]") {
		t.Errorf("problems %q", p)
	}
}
