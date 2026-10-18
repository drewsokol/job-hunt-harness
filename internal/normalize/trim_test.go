package normalize

import (
	"reflect"
	"strings"
	"testing"
)

func TestTrimBoilerplate(t *testing.T) {
	desc := strings.Join([]string{
		"Exampleco makes made-up software for tests.",
		"About the role",
		"You will build the ingestion service and its evals.",
		"- Design APIs",
		"- Review code",
		"Benefits",
		"- Health, dental and vision",
		"- A home office stipend",
		"Compensation",
		"The base salary range is $150,000 - $190,000.",
		"About Exampleco",
		"We were founded in a garage that doesn't exist.",
		"What you'll bring",
		"- Five years of Go",
		"Exampleco is an equal opportunity employer and considers applicants without regard to race or religion.",
		"Perks & Benefits:",
		"- Free snacks",
		"Pay: $70/hr - $90/hr",
		"#LI-Remote",
	}, "\n")
	want := strings.Join([]string{
		"Exampleco makes made-up software for tests.",
		"About the role",
		"You will build the ingestion service and its evals.",
		"- Design APIs",
		"- Review code",
		"Compensation",
		"The base salary range is $150,000 - $190,000.",
		"What you'll bring",
		"- Five years of Go",
		"Pay: $70/hr - $90/hr",
	}, "\n")
	if got := TrimBoilerplate(desc, "Exampleco"); got != want {
		t.Errorf("TrimBoilerplate:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestParsePlace(t *testing.T) {
	tests := []struct {
		in   string
		want Place
		ok   bool
	}{
		{"Columbus, OH", Place{City: "Columbus", Region: "OH", Country: "US"}, true},
		{"Ohio", Place{Region: "OH", Country: "US"}, true},
		{"US", Place{Country: "US"}, true},
		{"Germany", Place{Country: "DE"}, true},
		{"Europe", Place{Region: "Europe"}, true},
		{"Toronto", Place{City: "Toronto", Region: "ON", Country: "CA"}, true},
		{"Springfield", Place{}, false}, // no state or country
		{"Austin, TX | Boston, MA", Place{}, false},
		{"", Place{}, false},
	}
	for _, tt := range tests {
		got, ok := ParsePlace(tt.in)
		if ok != tt.ok || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParsePlace(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRegions(t *testing.T) {
	for _, r := range regions {
		if !IsRegion(r) {
			t.Errorf("region %q from the vocabulary has no members", r)
		}
	}
	for r, cs := range regionMembers {
		for _, c := range cs {
			if !isoCodes[c] {
				t.Errorf("region %s: %s is not in the countries table", r, c)
			}
		}
	}
	if !InRegion("DE", "EMEA") || !InRegion("US", "North America") || !InRegion("JP", Worldwide) || InRegion("US", "Europe") {
		t.Error("InRegion is wrong")
	}
	if got := (Place{City: "Columbus", Region: "OH", Country: "US"}).String(); got != "Columbus, OH, US" {
		t.Errorf("Place.String() = %q", got)
	}
}
