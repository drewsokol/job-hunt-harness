package hackernews

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

// newTestSource serves the made-up threads in testdata, plus single items.
func newTestSource(t *testing.T, items map[string]string) *Source {
	t.Helper()
	threads, err := os.ReadFile("testdata/threads.json")
	if err != nil {
		t.Fatal(err)
	}
	thread, err := os.ReadFile("testdata/thread.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/search_by_date":
			if r.URL.Query().Get("tags") != "story,author_whoishiring" {
				http.Error(w, "bad tags", http.StatusBadRequest)
				return
			}
			w.Write(threads)
		case r.URL.Path == "/api/v1/items/90000301":
			w.Write(thread)
		case items[strings.TrimPrefix(r.URL.Path, "/api/v1/items/")] != "":
			w.Write([]byte(items[strings.TrimPrefix(r.URL.Path, "/api/v1/items/")]))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s := New(sources.NewClient())
	s.BaseURL = srv.URL + "/api/v1"
	return s
}

func TestThreads(t *testing.T) {
	s := newTestSource(t, nil)
	got, err := s.Threads(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	// Only whoishiring's hiring threads, newest first.
	if len(got) != 2 || got[0].ID != "90000301" || got[1].ID != "90000201" {
		t.Fatalf("threads %+v", got)
	}
	if !got[0].PostedAt.Equal(time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("posted %s", got[0].PostedAt)
	}
	if all, _ := s.Threads(context.Background(), 5); len(all) != 3 {
		t.Errorf("asked for 5, got %d of the 3 there are", len(all))
	}
}

func TestReadThread(t *testing.T) {
	s := newTestSource(t, map[string]string{
		"90000300": `{"id": 90000300, "type": "story", "author": "whoishiring", "title": "Ask HN: Who wants to be hired? (October 2026)", "children": []}`,
	})
	res, err := s.ReadThread(context.Background(), "90000301")
	if err != nil {
		t.Fatal(err)
	}
	// Five ads: the deleted post and the comment that isn't an ad are left
	// out, the filled ad is dropped without counting, and the reply under
	// the first ad is conversation.
	if len(res.Postings) != 4 || res.Unreadable != 2 {
		for _, p := range res.Postings {
			t.Logf("%s: %q", p.ExternalID, p.Company)
		}
		t.Fatalf("%d postings, %d unreadable; want 4 and 2", len(res.Postings), res.Unreadable)
	}
	p := res.Postings[0]
	if p.ExternalID != "90000311" || p.Company != "Exampleco" || p.Title != "Senior Backend Engineer, Payments" {
		t.Errorf("first ad: id %q, company %q, title %q", p.ExternalID, p.Company, p.Title)
	}
	if p.LocationRaw != "REMOTE (US)" {
		t.Errorf("location %q", p.LocationRaw)
	}
	if p.PayRaw != "$160k-$190k + equity" || p.EmploymentType != "Full-time" {
		t.Errorf("pay %q, employment %q", p.PayRaw, p.EmploymentType)
	}
	if p.URL != "https://news.ycombinator.com/item?id=90000311" || !strings.Contains(p.DescriptionHTML, "made-up software") {
		t.Errorf("url %q, description %q", p.URL, p.DescriptionHTML)
	}
	if !p.PostedAt.Equal(time.Date(2026, 10, 1, 15, 4, 5, 0, time.UTC)) {
		t.Errorf("posted %s", p.PostedAt)
	}
	if got := res.Postings[1].Title; got != "Staff Engineer (Rust) · Product Designer · Engineering Manager" {
		t.Errorf("several roles: %q", got)
	}
	if got := res.Postings[2]; got.Company != "Fictional Freight" || got.Title != "" || got.LocationRaw != "ONSITE | Columbus, OH" {
		t.Errorf("an ad naming no role: %+v", got)
	}
	if got := res.Postings[3]; got.Company != "Sample Robotics" || got.Title != "Robotics Software Engineer" || got.LocationRaw != "Remote (EU)" {
		t.Errorf("an ad using dashes: %+v", got)
	}

	if _, err := s.ReadThread(context.Background(), "90000300"); err == nil {
		t.Error("read a thread that isn't the hiring thread")
	}
	if _, err := s.ReadThread(context.Background(), "../admin"); err == nil {
		t.Error("read a non-numeric id")
	}
}

func TestLookup(t *testing.T) {
	s := newTestSource(t, map[string]string{
		"90000311": `{"id": 90000311, "type": "comment", "author": "made_up_one", "text": "Exampleco | Senior Backend Engineer | REMOTE (US)"}`,
		"90000316": `{"id": 90000316, "type": "comment", "author": "made_up_five", "text": "Placeholder Bank | Data Engineer | London, UK | CLOSED NOW"}`,
		"90000320": `{"id": 90000320, "type": "comment", "author": null, "text": null}`,
	})
	ctx := context.Background()
	if p, err := s.Lookup(ctx, "90000311"); err != nil || p.Company != "Exampleco" {
		t.Errorf("Lookup = %+v, %v", p, err)
	}
	for _, id := range []string{"90000316", "90000320", "90000399"} {
		if _, err := s.Lookup(ctx, id); !errors.Is(err, sources.ErrPostingNotFound) {
			t.Errorf("Lookup(%s): %v, want ErrPostingNotFound (filled, deleted, missing)", id, err)
		}
	}
	if _, err := s.Lookup(ctx, "1 OR 1"); err == nil {
		t.Error("a non-numeric id was looked up")
	}
}

func TestRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	s := New(sources.NewClient())
	s.BaseURL = srv.URL
	if _, err := s.Threads(context.Background(), 1); !errors.Is(err, sources.ErrRateLimited) {
		t.Errorf("error %v, want ErrRateLimited", err)
	}
}

func TestParseAd(t *testing.T) {
	tests := []struct {
		header string
		want   ad
		state  adState
	}{
		{"Exampleco | Senior Engineer | Remote (US) | $150k-$180k | Full-time",
			ad{Company: "Exampleco", Title: "Senior Engineer", Location: "Remote (US)", Pay: "$150k-$180k", Employment: "Full-time"}, adOK},
		// Web addresses and notes in parentheses leave the employer's name.
		{`Exampleco (<a href="https:&#x2F;&#x2F;exampleco.example">https:&#x2F;&#x2F;exampleco.example</a>) | Backend Engineer | Columbus, OH`,
			ad{Company: "Exampleco", Title: "Backend Engineer", Location: "Columbus, OH"}, adOK},
		// A note in parentheses can say where the job is.
		{"Made-Up Labs (Remote US) — Full-Stack Engineer — TypeScript / Postgres",
			ad{Company: "Made-Up Labs", Title: "Full-Stack Engineer", Location: "Remote US"}, adOK},
		// Roles split at commas, except inside parentheses, and a piece
		// without a role word stays with the role before it.
		{"Exampleco | Software Engineers (Senior, Staff), Product Designer, Growth Lead, Lifecycle | Remote",
			ad{Company: "Exampleco", Title: "Software Engineers (Senior, Staff) · Product Designer · Growth Lead, Lifecycle", Location: "Remote"}, adOK},
		// A role part that mentions pay is still the role part.
		{"Exampleco | Junior SWE ($120k + equity) | Columbus, OH",
			ad{Company: "Exampleco", Title: "Junior SWE ($120k + equity)", Location: "Columbus, OH"}, adOK},
		// Pay outside parentheses makes a part about pay, even when it names
		// a role, and a long part is prose, not roles.
		{"Exampleco | Staff Engineer | Remote (US) | Pay for the Staff Engineer role is 200k-240k",
			ad{Company: "Exampleco", Title: "Staff Engineer", Location: "Remote (US)", Pay: "Pay for the Staff Engineer role is 200k-240k"}, adOK},
		{"Exampleco | Senior Engineer | Remote (US) | " + strings.Repeat("We build made-up product software for made-up shops. ", 4),
			ad{Company: "Exampleco", Title: "Senior Engineer", Location: "Remote (US)"}, adOK},
		// Level words alone describe the company, not roles; a 401(k) is a
		// benefit, not pay; and "!" can be part of a name.
		// A figure about funding or revenue isn't pay.
		{"Exampleco | 2 Full Stack Engineers, 1 GTM | REMOTE | Full-Time | $250k seed raised",
			ad{Company: "Exampleco", Title: "2 Full Stack Engineers, 1 GTM", Location: "REMOTE", Employment: "Full-Time"}, adOK},
		{"Exampleco | Backend Engineer | Remote | $3M ARR, profitable",
			ad{Company: "Exampleco", Title: "Backend Engineer", Location: "Remote"}, adOK},
		// ...but pay beside the company's stage is still pay.
		{"Exampleco | Backend Engineer | Remote | $180k, Series A",
			ad{Company: "Exampleco", Title: "Backend Engineer", Location: "Remote", Pay: "$180k, Series A"}, adOK},
		{"Exampleco | Series A, 20 staff | Remote (US)",
			ad{Company: "Exampleco", Location: "Remote (US)"}, adOK},
		{"Exampleco | Senior Engineer | Remote (US), 401k match",
			ad{Company: "Exampleco", Title: "Senior Engineer", Location: "Remote (US), 401k match"}, adOK},
		{"Exampleco! | Senior Engineer | Remote (US)",
			ad{Company: "Exampleco!", Title: "Senior Engineer", Location: "Remote (US)"}, adOK},
		// A note that isn't a place isn't taken as one.
		{"Exampleco (Series B) | ONSITE | Columbus, OH",
			ad{Company: "Exampleco", Location: "ONSITE | Columbus, OH"}, adOK},
		{"Exampleco | Data Engineer | Columbus, OH | positions filled", ad{}, adFilled},
		{"Exampleco | Data Engineer [closed]", ad{}, adFilled},
		{"We're hiring engineers at a made-up company, come say hi!", ad{}, adUnreadable},
		{"Senior Backend Engineer | Remote (US)", ad{}, adUnreadable},
		{"Is this role still open? | asking for a friend who would like to know more about it", ad{}, adUnreadable},
	}
	for _, tt := range tests {
		got, state := parseAd(tt.header + "<p>Body text.")
		if state != tt.state || got != tt.want {
			t.Errorf("parseAd(%q)\n got %+v (%d)\nwant %+v (%d)", tt.header, got, state, tt.want, tt.state)
		}
	}
}
