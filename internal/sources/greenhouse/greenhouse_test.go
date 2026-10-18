package greenhouse

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/sources/conformance"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestConformance(t *testing.T) {
	conformance.Run(t, conformance.Case{
		New: func(base string, c *sources.Client) sources.Source {
			return &Source{Client: c, BaseURL: base}
		},
		BasePath: "/v1/boards",
		Fixtures: map[string]string{
			"/exampleco/jobs":      fixture(t, "list.json"),
			"/exampleco/jobs/1001": fixture(t, "job-1001.json"),
			"/exampleco/jobs/1002": fixture(t, "job-1002.json"),
			"/exampleco/jobs/1003": fixture(t, "job-1003.json"),
		},
		URLs: map[string]sources.PostingRef{
			"https://boards.greenhouse.io/exampleco/jobs/555":                    {Source: "greenhouse", Slug: "exampleco", ExternalID: "555"},
			"https://job-boards.eu.greenhouse.io/exampleco/jobs/556/":            {Source: "greenhouse", Slug: "exampleco", ExternalID: "556"},
			"https://boards.greenhouse.io/embed/job_app?for=exampleco&token=557": {Source: "greenhouse", Slug: "exampleco", ExternalID: "557"},
			"https://www.exampleco.example/careers?gh_jid=558&gh_src=x":          {Source: "greenhouse", ExternalID: "558"},
		},
		NotURLs: []string{
			"https://boards.greenhouse.io/exampleco",
			"https://boards.greenhouse.io/exampleco/jobs/abc",
			"https://jobs.lever.co/exampleco/1001",
			"https://www.exampleco.example/careers?gh_jid=../1",
		},
		Check: func(t *testing.T, ps []sources.Posting) {
			if ps[0].OpeningKey != "9001" || ps[1].OpeningKey != "9001" || ps[2].OpeningKey != "9002" {
				t.Errorf("opening keys = %q %q %q, want internal_job_id", ps[0].OpeningKey, ps[1].OpeningKey, ps[2].OpeningKey)
			}
			if !ps[0].Partial || ps[0].DescriptionHTML != "" {
				t.Error("list postings should be partial, without descriptions")
			}
			if got := ps[0].PostedAt.Format("2006-01-02T15:04:05Z07:00"); got != "2026-10-02T15:31:50Z" {
				t.Errorf("PostedAt = %s, want the -04:00 time in UTC", got)
			}
		},
	})
}

func TestDetailUnescapesContentAndReadsPay(t *testing.T) {
	base, _ := conformance.Serve(t, "/v1/boards", map[string]string{"/exampleco/jobs/1001": fixture(t, "job-1001.json")})
	s := &Source{Client: sources.NewClient(), BaseURL: base}
	p, err := s.Detail(context.Background(), sources.Board{Slug: conformance.Slug}, "1001")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.DescriptionHTML, "<p>Exampleco makes") {
		t.Errorf("DescriptionHTML = %q, want unescaped HTML", p.DescriptionHTML)
	}
	want := []normalize.PayRange{{Min: 180000, Max: 220000, Currency: "USD", Period: "year", Label: "Annual base salary range"}}
	if !reflect.DeepEqual(p.Pay, want) {
		t.Errorf("Pay = %+v, want %+v", p.Pay, want)
	}
	if p.PayRaw == "" {
		t.Error("PayRaw is empty")
	}
}
