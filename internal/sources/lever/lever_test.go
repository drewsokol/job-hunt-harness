package lever

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/sources/conformance"
)

func TestConformance(t *testing.T) {
	body, err := os.ReadFile("testdata/postings.json")
	if err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, conformance.Case{
		New: func(base string, c *sources.Client) sources.Source {
			return &Source{Client: c, BaseURL: base}
		},
		BasePath: "/v0/postings",
		Fixtures: map[string]string{"/exampleco": string(body)},
		URLs: map[string]sources.PostingRef{
			"https://jobs.lever.co/exampleco/0b1c2d3e-0000-4000-8000-0000000000aa/apply?lever-source=x": {
				Source: "lever", Slug: "exampleco", ExternalID: "0b1c2d3e-0000-4000-8000-0000000000aa",
			},
		},
		NotURLs: []string{
			"https://jobs.lever.co/exampleco",
			"https://jobs.lever.co/exampleco/not-a-uuid",
			"https://lever.co.evil.example/exampleco/0b1c2d3e-0000-4000-8000-000000000001",
		},
		Check: func(t *testing.T, ps []sources.Posting) {
			p := ps[0]
			if p.Workplace != "remote" || p.Country != "US" || p.EmploymentType != "Full-time" {
				t.Errorf("workplace %q, country %q, type %q", p.Workplace, p.Country, p.EmploymentType)
			}
			if p.LocationRaw != "Remote - US | Denver, CO" {
				t.Errorf("LocationRaw = %q, want every location", p.LocationRaw)
			}
			want := []normalize.PayRange{{Min: 160000, Max: 200000, Currency: "USD", Period: "year"}}
			if !reflect.DeepEqual(p.Pay, want) {
				t.Errorf("Pay = %+v, want %+v", p.Pay, want)
			}
			text := normalize.HTMLToText(p.DescriptionHTML)
			for _, s := range []string{"made-up software", "What you will do", "- Run the platform", "Pay & benefits", "equal opportunity"} {
				if !strings.Contains(text, s) {
					t.Errorf("description text misses %q:\n%s", s, text)
				}
			}
			if ps[1].Workplace != "" {
				t.Errorf("unspecified workplace = %q, want empty", ps[1].Workplace)
			}
			if got := p.PostedAt.Format("2006-01-02T15:04:05.000Z07:00"); got != "2026-08-14T17:03:57.382Z" {
				t.Errorf("PostedAt = %s", got)
			}
		},
	})
}
