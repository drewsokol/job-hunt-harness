package normalize

import (
	"slices"
	"strings"
)

// ParsePlace reads a single place written by a person, such as an entry in
// the filters: "Columbus, OH", "Ohio", "US", "Germany" or "Europe". It
// reports false unless the text names exactly one place that has a country
// or region.
func ParsePlace(s string) (Place, bool) {
	if len(segmentSep.Split(strings.TrimSpace(s), -1)) != 1 {
		return Place{}, false
	}
	ps := parsePlaces(s, lenient)
	if len(ps) != 1 || (ps[0].Country == "" && ps[0].Region == "") {
		return Place{}, false
	}
	return ps[0], true
}

// IsRegion reports whether r is one of the wide regions ParseLocation
// produces ("EMEA", "Europe", "Worldwide").
func IsRegion(r string) bool {
	_, ok := regionMembers[r]
	return ok || r == Worldwide
}

// Worldwide is the region that contains every country.
const Worldwide = "Worldwide"

// InRegion reports whether country (an ISO code) is in region.
func InRegion(country, region string) bool {
	if region == Worldwide {
		return country != ""
	}
	return slices.Contains(regionMembers[region], country)
}

// RegionMembers returns the countries in region; nil for Worldwide, which
// has no list.
func RegionMembers(region string) []string { return regionMembers[region] }

// String renders the place for people: "Columbus, OH, US", "EMEA".
func (p Place) String() string {
	var parts []string
	for _, s := range []string{p.City, p.Region, p.Country} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// regionMembers lists the countries in each region the vocabulary knows,
// limited to the countries in the countries table.
var regionMembers = func() map[string][]string {
	northAmerica := []string{"US", "CA", "MX"}
	latam := []string{"MX", "BR", "AR", "CL", "CO", "PE", "UY", "CR"}
	europe := []string{
		"GB", "IE", "DE", "FR", "ES", "PT", "IT", "NL", "BE", "CH", "AT", "SE", "NO", "DK", "FI", "IS",
		"PL", "CZ", "RO", "HU", "GR", "UA", "RS", "BG", "HR", "EE", "LV", "LT", "LU", "SI", "SK", "CY", "MT",
	}
	emea := append([]string{"TR", "IL", "AE", "SA", "QA", "EG", "MA", "ZA", "NG", "KE", "GH"}, europe...)
	return map[string][]string{
		"North America": northAmerica,
		"LATAM":         latam,
		"Americas":      append([]string{"US", "CA"}, latam...),
		"Europe":        europe,
		"EMEA":          emea,
		"APAC": {
			"IN", "PK", "LK", "BD", "NP", "SG", "JP", "KR", "CN", "HK", "TW",
			"PH", "VN", "TH", "MY", "ID", "AU", "NZ",
		},
		"Nordics": {"SE", "NO", "DK", "FI", "IS"},
		"DACH":    {"DE", "AT", "CH"},
	}
}()
