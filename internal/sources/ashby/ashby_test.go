package ashby

import (
	"os"
	"reflect"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/sources/conformance"
)

func TestConformance(t *testing.T) {
	body, err := os.ReadFile("testdata/board.json")
	if err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, conformance.Case{
		New: func(base string, c *sources.Client) sources.Source {
			return &Source{Client: c, BaseURL: base}
		},
		BasePath: "/posting-api/job-board",
		Fixtures: map[string]string{"/exampleco": string(body)},
		URLs: map[string]sources.PostingRef{
			"https://jobs.ashbyhq.com/exampleco/6a7b8c9d-0000-4000-8000-0000000000aa/application": {
				Source: "ashby", Slug: "exampleco", ExternalID: "6a7b8c9d-0000-4000-8000-0000000000aa",
			},
			"https://www.exampleco.example/careers?ashby_jid=6a7b8c9d-0000-4000-8000-0000000000bb": {
				Source: "ashby", ExternalID: "6a7b8c9d-0000-4000-8000-0000000000bb",
			},
		},
		NotURLs: []string{
			"https://jobs.ashbyhq.com/exampleco",
			"https://www.exampleco.example/careers?ashby_jid=1234",
		},
		Check: func(t *testing.T, ps []sources.Posting) {
			if len(ps) != 2 {
				t.Fatalf("got %d postings, want 2 (unlisted skipped)", len(ps))
			}
			if ps[0].Workplace != "hybrid" {
				t.Errorf("Workplace = %q: workplaceType Hybrid must win over isRemote", ps[0].Workplace)
			}
			if ps[0].LocationRaw != "Springfield, OR | Portland, OR" || ps[0].Country != "USA" {
				t.Errorf("LocationRaw %q, Country %q", ps[0].LocationRaw, ps[0].Country)
			}
			want := []normalize.PayRange{
				{Min: 170000, Max: 190000, Currency: "USD", Period: "year", Label: "Zone A"},
				{Min: 150000, Max: 170000, Currency: "USD", Period: "year", Label: "Zone B"},
			}
			if !reflect.DeepEqual(ps[0].Pay, want) {
				t.Errorf("Pay = %+v, want tiers %+v", ps[0].Pay, want)
			}
			hourly := []normalize.PayRange{{Min: 90, Max: 110, Currency: "USD", Period: "hour"}}
			if !reflect.DeepEqual(ps[1].Pay, hourly) {
				t.Errorf("Pay = %+v, want summary %+v", ps[1].Pay, hourly)
			}
			if ps[1].Workplace != "remote" || ps[1].EmploymentType != "Contract" {
				t.Errorf("workplace %q, type %q", ps[1].Workplace, ps[1].EmploymentType)
			}
		},
	})
}
