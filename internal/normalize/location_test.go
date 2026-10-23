package normalize

import (
	"reflect"
	"testing"
)

func TestParseLocation(t *testing.T) {
	us := func(city, st string) Place { return Place{City: city, Region: st, Country: "US"} }
	country := func(c string) Place { return Place{Country: c} }
	tests := []struct {
		name                    string
		raw, title, hint, cHint string
		wantWorkplace           string
		wantPlaces              []Place
	}{
		{name: "city state", raw: "San Francisco, CA", wantWorkplace: Unknown, wantPlaces: []Place{us("San Francisco", "CA")}},
		{name: "city state country", raw: "Austin, TX, United States", wantWorkplace: Unknown, wantPlaces: []Place{us("Austin", "TX")}},
		{name: "remote us", raw: "Remote - US", wantWorkplace: Remote, wantPlaces: []Place{country("US")}},
		{name: "us dash remote", raw: "US-Remote", wantWorkplace: Remote, wantPlaces: []Place{country("US")}},
		{name: "remote parens", raw: "Remote (US, Canada)", wantWorkplace: Remote, wantPlaces: []Place{country("US"), country("CA")}},
		{name: "remote alone", raw: "Remote", wantWorkplace: Remote},
		{name: "hybrid", raw: "New York, NY (Hybrid)", wantWorkplace: Hybrid, wantPlaces: []Place{us("New York", "NY")}},
		{name: "onsite", raw: "On-site - Seattle", wantWorkplace: Onsite, wantPlaces: []Place{us("Seattle", "WA")}},
		{
			name: "several places", raw: "San Francisco, CA | New York, NY | Remote", wantWorkplace: Remote,
			wantPlaces: []Place{us("San Francisco", "CA"), us("New York", "NY")},
		},
		{name: "province", raw: "Toronto, ON", wantWorkplace: Unknown, wantPlaces: []Place{{City: "Toronto", Region: "ON", Country: "CA"}}},
		{name: "city country", raw: "London, UK", wantWorkplace: Unknown, wantPlaces: []Place{{City: "London", Country: "GB"}}},
		{name: "known city alone", raw: "Berlin", wantWorkplace: Unknown, wantPlaces: []Place{{City: "Berlin", Country: "DE"}}},
		{name: "state name", raw: "California, USA", wantWorkplace: Unknown, wantPlaces: []Place{{Region: "CA", Country: "US"}}},
		{name: "region", raw: "Remote, EMEA", wantWorkplace: Remote, wantPlaces: []Place{{Region: "EMEA"}}},
		{name: "unknown city kept", raw: "Springfield", wantWorkplace: Unknown, wantPlaces: []Place{{City: "Springfield"}}},
		{name: "two-letter code alone is not a country", raw: "IN", wantWorkplace: Unknown},
		{name: "empty", wantWorkplace: Unknown},

		// Formats seen on live boards.
		{
			name: "bullet separated", raw: "San Francisco, CA • New York, NY • United States", wantWorkplace: Unknown,
			wantPlaces: []Place{us("San Francisco", "CA"), us("New York", "NY"), country("US")},
		},
		{name: "state name after comma", raw: "Seattle, Washington", wantWorkplace: Unknown, wantPlaces: []Place{us("Seattle", "WA")}},
		{name: "city and state names", raw: "New York City, New York", wantWorkplace: Unknown, wantPlaces: []Place{us("New York", "NY")}},
		{name: "dotted code", raw: "Washington, D.C.", wantWorkplace: Unknown, wantPlaces: []Place{us("Washington", "DC")}},
		{name: "iso code after comma", raw: "Dublin, IE", wantWorkplace: Unknown, wantPlaces: []Place{{City: "Dublin", Country: "IE"}}},
		{name: "known city wins over state code", raw: "Bangalore, IN", wantWorkplace: Unknown, wantPlaces: []Place{{City: "Bengaluru", Country: "IN"}}},
		{
			name: "list of cities", raw: "Hybrid - San Francisco, New York City, Austin", wantWorkplace: Hybrid,
			wantPlaces: []Place{us("San Francisco", "CA"), us("New York", "NY"), us("Austin", "TX")},
		},
		{name: "city then state", raw: "Austin, Texas", wantWorkplace: Unknown, wantPlaces: []Place{us("Austin", "TX")}},
		{name: "junk around a place", raw: "USA - Austin (dbt)", wantWorkplace: Unknown, wantPlaces: []Place{us("Austin", "TX")}},
		{name: "placeholder", raw: "N/A", wantWorkplace: Unknown},
		{name: "select locations", raw: "Remote - US: Select locations", wantWorkplace: Remote, wantPlaces: []Place{country("US")}},
		{name: "remote position", raw: "Remote Position", wantWorkplace: Remote},
		{
			name: "office names", raw: "San Francisco HQ | Seattle Office | New York City Office", wantWorkplace: Unknown,
			wantPlaces: []Place{us("San Francisco", "CA"), us("Seattle", "WA"), us("New York", "NY")},
		},
		{name: "in office stays onsite", raw: "In-office - Austin", wantWorkplace: Onsite, wantPlaces: []Place{us("Austin", "TX")}},
		{name: "slashed countries", raw: "REMOTE (US/Canada)", wantWorkplace: Remote, wantPlaces: []Place{country("US"), country("CA")}},
		{name: "slash in other text stays whole", raw: "Eastern/Central", wantWorkplace: Unknown, wantPlaces: []Place{{City: "Eastern/Central"}}},
		{name: "everywhere", raw: "Remote(Everywhere)", wantWorkplace: Remote, wantPlaces: []Place{{Region: Worldwide}}},
		{name: "anywhere in the world", raw: "REMOTE ALMOST ANYWHERE IN THE WORLD", wantWorkplace: Remote, wantPlaces: []Place{{Region: Worldwide}}},
		{name: "anywhere alone is not worldwide", raw: "Remote anywhere in the US", wantWorkplace: Remote, wantPlaces: []Place{country("US")}},
		{name: "but", raw: "Remote but Austin based only", wantWorkplace: Remote, wantPlaces: []Place{us("Austin", "TX")}},

		// Titles
		{
			name: "location in title", title: "Staff AI Engineer | Canada | Remote",
			wantWorkplace: Remote, wantPlaces: []Place{country("CA")},
		},
		{name: "title parens", raw: "Remote", title: "Senior Engineer (US)", wantWorkplace: Remote, wantPlaces: []Place{country("US")}},
		{name: "title role words ignored", title: "Engineer, Platform - Go", wantWorkplace: Unknown},
		{name: "role before separator ignored", title: "Remote Sensing Engineer", wantWorkplace: Unknown},

		// Board hints win.
		{name: "hint beats words", raw: "Remote - San Francisco", hint: Hybrid, wantWorkplace: Hybrid, wantPlaces: []Place{us("San Francisco", "CA")}},
		{name: "country hint fills", raw: "Remote", cHint: "US", wantWorkplace: Remote, wantPlaces: []Place{country("US")}},
		{name: "country hint name", raw: "Springfield", cHint: "United States", wantWorkplace: Unknown, wantPlaces: []Place{{City: "Springfield", Country: "US"}}},
		{name: "country hint does not override", raw: "London", cHint: "US", wantWorkplace: Unknown, wantPlaces: []Place{{City: "London", Country: "GB"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseLocation(tt.raw, tt.title, tt.hint, tt.cHint)
			if got.Workplace != tt.wantWorkplace || !reflect.DeepEqual(got.Places, tt.wantPlaces) {
				t.Errorf("ParseLocation(%q, %q, %q, %q)\n got  %s %+v\n want %s %+v",
					tt.raw, tt.title, tt.hint, tt.cHint, got.Workplace, got.Places, tt.wantWorkplace, tt.wantPlaces)
			}
		})
	}
}

// Free-form text, such as a Hacker News ad's header, keeps only places jhh
// recognizes: time zones, schedules and other words don't become cities.
func TestParseFreeformLocation(t *testing.T) {
	tests := []struct {
		raw           string
		wantWorkplace string
		wantPlaces    []Place
	}{
		{"Remote (async)", Remote, nil},
		{"REMOTE (Some overlap w/ PST required)", Remote, nil},
		{"Remote (U.S. Eastern/Central Time)", Remote, nil},
		{"Remote but Austin based only", Remote, []Place{{City: "Austin", Region: "TX", Country: "US"}}},
		{"REMOTE (US/Canada)", Remote, []Place{{Country: "US"}, {Country: "CA"}}},
		{"Onsite | Columbus, OH", Onsite, []Place{{City: "Columbus", Region: "OH", Country: "US"}}},
		{"Springfield", Unknown, nil},
	}
	for _, tt := range tests {
		got := ParseFreeformLocation(tt.raw, "", "", "")
		if got.Workplace != tt.wantWorkplace || !reflect.DeepEqual(got.Places, tt.wantPlaces) {
			t.Errorf("ParseFreeformLocation(%q)\n got  %s %+v\n want %s %+v", tt.raw, got.Workplace, got.Places, tt.wantWorkplace, tt.wantPlaces)
		}
	}
}
