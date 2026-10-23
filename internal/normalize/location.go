package normalize

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Workplace values.
const (
	Remote  = "remote"
	Hybrid  = "hybrid"
	Onsite  = "onsite"
	Unknown = "unknown"
)

// Place is one location a posting names. Country is an ISO 3166 alpha-2 code
// when known. Region is a state or province code ("CA", "ON") or a wider
// region ("EMEA"). Any field may be empty.
type Place struct {
	City    string `json:"city,omitempty"`
	Region  string `json:"region,omitempty"`
	Country string `json:"country,omitempty"`
}

// Location is a posting's workplace and the places it names.
type Location struct {
	Workplace string  `json:"workplace"` // remote, hybrid, onsite or unknown
	Places    []Place `json:"places,omitempty"`
}

var (
	// segmentSep splits location text listing several places.
	segmentSep = regexp.MustCompile(`\s*(?:\||;|\n|\s/\s|\bor\b|•|·)\s*`)
	// partSep splits one segment into parts, like a comma: "USA - Austin (dbt)".
	partSep = regexp.MustCompile(`[()\[\]:]|\s[-–—]\s`)
	// titleSep splits a title into the role and trailing location parts:
	// "Staff AI Engineer | Canada | Remote", "Engineer - Remote (US)".
	titleSep = regexp.MustCompile(`\s*(?:\||\s[-–—]\s|\(|\)|,)\s*`)

	remoteWords = regexp.MustCompile(`(?i)\b(?:fully[- ]remote|remote[- ]first|remote|work from home|wfh|anywhere|distributed)\b`)
	hybridWords = regexp.MustCompile(`(?i)\bhybrid\b`)
	onsiteWords = regexp.MustCompile(`(?i)\b(?:on[- ]?site|in[- ]office|in[- ]person|office[- ]based)\b`)
	fillerWords = regexp.MustCompile(`(?i)\b(?:but|based|only|in|within|from|the|location|locations|all|select|position|positions|role|opportunity|hq|headquarters|office|offices|campus|hub)\b`)
	// worldWords say a job is open in any country. "Anywhere" alone doesn't:
	// "remote anywhere in the US".
	worldWords = regexp.MustCompile(`(?i)\b(?:(?:almost|nearly)\s+)?(?:anywhere\s+in\s+the\s+world|everywhere|world[- ]?wide|globally)\b`)
	edgeJunk   = " \t-–—,.:()[]*+/"
)

// ParseLocation reads a posting's workplace and places from the board's
// location text, location segments in its title, and the board's own
// workplace and country fields (hints, which win when present).
//
// It recognizes "City, ST" (a US state), "City, Province", "City, Country",
// country names and codes, wider regions (EMEA, Europe, North America),
// common cities on their own, and remote, hybrid and onsite wording. Text it
// can't place is kept as a city from the location field and ignored from the
// title. With no hint and no workplace words, the workplace is unknown: a
// city alone doesn't say whether remote work is possible.
func ParseLocation(raw, title, workplaceHint, countryHint string) Location {
	return parseLocation(raw, title, workplaceHint, countryHint, false)
}

// ParseFreeformLocation is ParseLocation for location text a person wrote
// freely, such as a Hacker News ad's header ("Remote (some overlap with
// PST)"): only places jhh recognizes count, and other words are ignored
// instead of kept as a city.
func ParseFreeformLocation(raw, title, workplaceHint, countryHint string) Location {
	return parseLocation(raw, title, workplaceHint, countryHint, true)
}

func parseLocation(raw, title, workplaceHint, countryHint string, strict bool) Location {
	var loc Location
	var remote, hybrid, onsite bool
	add := func(p Place) {
		if p != (Place{}) && !slices.Contains(loc.Places, p) {
			loc.Places = append(loc.Places, p)
		}
	}
	scan := func(seg string, strict bool) {
		remote = remote || remoteWords.MatchString(seg)
		hybrid = hybrid || hybridWords.MatchString(seg)
		onsite = onsite || onsiteWords.MatchString(seg)
		for _, p := range parsePlaces(seg, strict) {
			add(p)
		}
	}
	for _, seg := range segmentSep.Split(raw, -1) {
		scan(seg, strict)
	}
	// The first title part is the role itself; only trailing parts can be
	// locations, and only recognized places count.
	if parts := titleSep.Split(title, -1); len(parts) > 1 {
		for _, seg := range parts[1:] {
			scan(seg, true)
		}
	}

	if countryHint != "" {
		code := countryCode(countryHint)
		if code == "" {
			code = strings.ToUpper(strings.TrimSpace(countryHint))
		}
		if len(loc.Places) == 0 {
			add(Place{Country: code})
		}
		for i := range loc.Places {
			if loc.Places[i].Country == "" && loc.Places[i].Region == "" {
				loc.Places[i].Country = code
			}
		}
	}

	switch {
	case workplaceHint == Remote || workplaceHint == Hybrid || workplaceHint == Onsite:
		loc.Workplace = workplaceHint
	case hybrid:
		loc.Workplace = Hybrid
	case remote && !onsite:
		loc.Workplace = Remote
	case onsite && !remote:
		loc.Workplace = Onsite
	case remote && onsite:
		// "Remote or on-site in Austin": remote is allowed.
		loc.Workplace = Remote
	default:
		loc.Workplace = Unknown
	}
	return loc
}

// parsePlaces reads the places in one segment. In strict mode only
// recognized places are returned.
func parsePlaces(seg string, strict bool) []Place {
	seg = worldWords.ReplaceAllString(seg, " worldwide ")
	seg = remoteWords.ReplaceAllString(seg, " ")
	seg = hybridWords.ReplaceAllString(seg, " ")
	seg = onsiteWords.ReplaceAllString(seg, " ")
	seg = fillerWords.ReplaceAllString(seg, " ")
	seg = partSep.ReplaceAllString(seg, ",")
	var parts []string
	for _, p := range strings.Split(seg, ",") {
		p = strings.Join(strings.Fields(strings.Trim(p, edgeJunk)), " ")
		if p != "" && !placeholders[strings.ToLower(p)] {
			parts = append(parts, slashedPlaces(p)...)
		}
	}
	switch len(parts) {
	case 0:
		return nil
	case 1:
		if p, ok := knownPlace(parts[0]); ok {
			return []Place{p}
		}
		if strict || !looksLikeName(parts[0]) {
			return nil
		}
		return []Place{{City: parts[0]}}
	}

	// Parts that are all known places: "US, Canada", "California, USA",
	// "San Francisco, New York City, Austin".
	var all []Place
	for _, part := range parts {
		p, ok := knownPlace(part)
		if !ok {
			all = nil
			break
		}
		all = append(all, p)
	}
	if all != nil {
		return dropCovered(all)
	}

	// "City, ST", "City, State, Country", "City, Country", "City, IE".
	city, rest := parts[0], parts[1:]
	var p Place
	for _, part := range rest {
		if code := countryCode(part); code != "" {
			p.Country = code
		} else if region, country := subdivision(part); region != "" {
			p.Region = region
			if p.Country == "" {
				p.Country = country
			}
		} else if code := strings.ToUpper(part); len(code) == 2 && isoCodes[code] {
			p.Country = code
		}
	}
	if p != (Place{}) {
		if known, ok := knownPlace(city); ok {
			if known.City == "" {
				// "US, Remote" leftovers: the first part was a place too.
				return dropCovered([]Place{known, p})
			}
			if known.Country == p.Country || p.Country == "" {
				return []Place{known} // "Bangalore, IN": the known city wins
			}
		}
		p.City = city
		return []Place{p}
	}

	// Some known places among unknown words: "USA - Austin (dbt)".
	var known []Place
	for _, part := range parts {
		if kp, ok := knownPlace(part); ok {
			known = append(known, kp)
		}
	}
	if len(known) > 0 {
		return dropCovered(known)
	}
	if strict || !looksLikeName(strings.Join(parts, " ")) {
		return nil
	}
	return []Place{{City: strings.Join(parts, ", ")}}
}

// slashedPlaces splits "US/Canada" into its places when every piece is one
// jhh knows; other text with a slash ("Eastern/Central Time") stays whole.
func slashedPlaces(p string) []string {
	pieces := strings.Split(p, "/")
	if len(pieces) < 2 {
		return []string{p}
	}
	for i, piece := range pieces {
		pieces[i] = strings.TrimSpace(piece)
		if _, ok := knownPlace(pieces[i]); !ok {
			return []string{p}
		}
	}
	return pieces
}

// dropCovered removes places that a more specific place in the list
// already covers: "Austin, Texas" is Austin, not Austin and Texas.
func dropCovered(ps []Place) []Place {
	var out []Place
	for i, p := range ps {
		covered := false
		for j, q := range ps {
			if i != j && covers(q, p) {
				covered = true
				break
			}
		}
		if !covered && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// covers reports whether q is a more specific place inside p.
func covers(q, p Place) bool {
	if p.City != "" || q == p {
		return false
	}
	if p.Region != "" {
		return q.Region == p.Region && q.Country == p.Country && q.City != ""
	}
	return p.Country != "" && q.Country == p.Country && (q.Region != "" || q.City != "")
}

// knownPlace recognizes a single name: a country, a wider region, a US
// state or province, or a common city.
func knownPlace(s string) (Place, bool) {
	if code := countryCode(s); code != "" {
		return Place{Country: code}, true
	}
	key := strings.ToLower(s)
	if r, ok := regions[key]; ok {
		return Place{Region: r}, true
	}
	if c, ok := cities[key]; ok {
		return c, true
	}
	// Only full state and province names stand alone; two-letter codes are
	// too ambiguous ("IN", "OR", "ME").
	if code, ok := usStateNames[key]; ok {
		return Place{Region: code, Country: "US"}, true
	}
	if code, ok := caProvinceNames[key]; ok {
		return Place{Region: code, Country: "CA"}, true
	}
	return Place{}, false
}

// countryCode returns the ISO code for a country name, or for the few codes
// that are unambiguous in location text (US, USA, UK). Other two-letter
// codes collide with state codes and words.
func countryCode(s string) string {
	key := strings.ToLower(strings.Trim(s, edgeJunk))
	if c, ok := countries[key]; ok {
		return c
	}
	return ""
}

// subdivision reads a US state or Canadian province, by code or name. It
// is only used after a comma, where "Washington" means the state.
func subdivision(s string) (region, country string) {
	if code := strings.ToUpper(strings.ReplaceAll(s, ".", "")); len(code) == 2 {
		if slices.Contains(usStateCodes, code) {
			return code, "US"
		}
		if slices.Contains(caProvinceCodes, code) {
			return code, "CA"
		}
	}
	key := strings.ToLower(s)
	if code, ok := usStateNames[key]; ok {
		return code, "US"
	}
	if code, ok := stateNamesAfterComma[key]; ok {
		return code, "US"
	}
	if code, ok := caProvinceNames[key]; ok {
		return code, "CA"
	}
	return "", ""
}

// looksLikeName rejects leftovers that aren't place names, such as numbers
// or long phrases.
func looksLikeName(s string) bool {
	if len(s) > 40 || strings.Count(s, " ") > 4 {
		return false
	}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return false
		}
	}
	return true
}

// IsLocationText reports whether s is only a location or workplace phrase,
// such as "Remote", "Canada" or "New York, NY (Hybrid)". Dedupe uses it to
// drop location parts from titles.
func IsLocationText(s string) bool {
	if remoteWords.MatchString(s) || hybridWords.MatchString(s) || onsiteWords.MatchString(s) {
		rest := strings.Trim(onsiteWords.ReplaceAllString(hybridWords.ReplaceAllString(remoteWords.ReplaceAllString(s, ""), ""), ""), edgeJunk)
		return rest == "" || len(parsePlaces(rest, true)) > 0
	}
	return len(parsePlaces(s, true)) > 0
}

var (
	placeNamesOnce sync.Once
	placeNames     *regexp.Regexp
)

// MaskPlaces replaces place names in s (countries, regions, states,
// provinces, common cities, and ", ST" state codes) with "~", so two texts
// that differ only in the city they mention compare equal.
func MaskPlaces(s string) string {
	placeNamesOnce.Do(func() {
		var names []string
		for _, m := range []map[string]string{countries, regions, usStateNames, caProvinceNames} {
			for k := range m {
				if len(k) > 3 {
					names = append(names, regexp.QuoteMeta(k))
				}
			}
		}
		for k := range cities {
			if len(k) > 3 {
				names = append(names, regexp.QuoteMeta(k))
			}
		}
		// Longest first, so "new york city" wins over "new york".
		slices.SortFunc(names, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
		placeNames = regexp.MustCompile(`(?i)\b(?:` + strings.Join(names, "|") + `)\b|,\s*[A-Z]{2}\b`)
	})
	return placeNames.ReplaceAllString(s, "~")
}
