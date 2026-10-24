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
	return parseLocation(raw, title, workplaceHint, countryHint, lenient)
}

// placeMode says how parsePlaces treats words it doesn't recognize.
type placeMode int

const (
	// lenient reads a location field: a known place inside a longer phrase
	// is found ("DE Frankfurt a.M."), and other text is kept as a city.
	lenient placeMode = iota
	// freeform reads location text a person wrote freely: known places are
	// found inside phrases, and other words are ignored.
	freeform
	// entry reads a place a person wrote on purpose, such as a filter
	// entry: other text is kept as a city, but a phrase isn't searched for
	// places, so free text stays an error for the agent to rewrite.
	entry
	// exact reads text that may not be a location at all, such as a title
	// part: only a part that is a known place counts ("Boston Dynamics"
	// isn't Boston).
	exact
)

// searchesPhrases reports whether m looks for a known place inside a
// longer phrase.
func (m placeMode) searchesPhrases() bool { return m == lenient || m == freeform }

// keepsUnknown reports whether m keeps text it can't place as a city.
func (m placeMode) keepsUnknown() bool { return m == lenient || m == entry }

// ParseFreeformLocation is ParseLocation for location text a person wrote
// freely, such as a Hacker News ad's header ("Remote (some overlap with
// PST)"): only places jhh recognizes count, and other words are ignored
// instead of kept as a city.
func ParseFreeformLocation(raw, title, workplaceHint, countryHint string) Location {
	return parseLocation(raw, title, workplaceHint, countryHint, freeform)
}

// ParseStatedLocation is ParseLocation for text that may not be about a
// location at all, such as one part of a Hacker News ad's header: a known
// place inside a longer phrase isn't searched for, so "Making US healthcare
// cheaper" names no place.
func ParseStatedLocation(raw string) Location {
	return parseLocation(raw, "", "", "", entry)
}

func parseLocation(raw, title, workplaceHint, countryHint string, mode placeMode) Location {
	var loc Location
	var remote, hybrid, onsite bool
	add := func(p Place) {
		if p != (Place{}) && !slices.Contains(loc.Places, p) {
			loc.Places = append(loc.Places, p)
		}
	}
	scan := func(seg string, mode placeMode) {
		remote = remote || remoteWords.MatchString(seg)
		hybrid = hybrid || hybridWords.MatchString(seg)
		onsite = onsite || onsiteWords.MatchString(seg)
		for _, p := range parsePlaces(seg, mode) {
			add(p)
		}
	}
	for _, seg := range segmentSep.Split(raw, -1) {
		scan(seg, mode)
	}
	// The first title part is the role itself; only trailing parts can be
	// locations, and only recognized places count.
	if parts := titleSep.Split(title, -1); len(parts) > 1 {
		for _, seg := range parts[1:] {
			scan(seg, exact)
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

// parsePlaces reads the places in one segment; mode says what happens to
// words it doesn't recognize.
func parsePlaces(seg string, mode placeMode) []Place {
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
		if mode.searchesPhrases() {
			if ps := placesInWords(parts[0]); len(ps) > 0 {
				return dropCovered(ps)
			}
		}
		if !mode.keepsUnknown() || !looksLikeName(parts[0]) {
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
		} else if mode.searchesPhrases() {
			known = append(known, placesInWords(part)...)
		}
	}
	if len(known) > 0 {
		return dropCovered(known)
	}
	if !mode.keepsUnknown() || !looksLikeName(strings.Join(parts, " ")) {
		return nil
	}
	return []Place{{City: strings.Join(parts, ", ")}}
}

// placesInWords finds the places named inside a longer phrase ("DE
// Frankfurt a.M.", "Greater Boston Area", "US & Canada"): runs of up to
// three words that are places jhh knows, longest runs first, none sharing a
// word, in the order the phrase names them. A run that the word before it
// makes part of a longer name ("New England", "Central America") doesn't
// count, and "us" counts only in capitals ("work with us" isn't the US). A
// phrase that rules places out ("not available in California") names none.
func placesInWords(s string) []Place {
	words := strings.Fields(s)
	if slices.ContainsFunc(words, func(w string) bool { return exclusionWords[strings.ToLower(strings.Trim(w, edgeJunk))] }) {
		return nil
	}
	used := make([]bool, len(words))
	type found struct {
		at int
		p  Place
	}
	var out []found
	for n := min(3, len(words)-1); n >= 1; n-- {
		for i := 0; i+n <= len(words); i++ {
			if slices.Contains(used[i:i+n], true) {
				continue
			}
			if i > 0 && nameJoiners[strings.ToLower(strings.Trim(words[i-1], edgeJunk))] {
				continue
			}
			run := strings.Trim(strings.Join(words[i:i+n], " "), edgeJunk)
			if strings.EqualFold(run, "us") && run != "US" {
				continue
			}
			if p, ok := knownPlace(run); ok {
				out = append(out, found{i, p})
				for j := i; j < i+n; j++ {
					used[j] = true
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b found) int { return cmp.Compare(a.at, b.at) })
	var ps []Place
	for _, f := range out {
		ps = append(ps, f.p)
	}
	return ps
}

// nameJoiners are words that start a longer place name around a known one:
// "New England" isn't England, and "Central America" isn't the US.
var nameJoiners = map[string]bool{
	"new": true, "north": true, "south": true, "east": true, "west": true, "central": true,
	"northern": true, "southern": true, "eastern": true, "western": true, "latin": true,
}

// exclusionWords say a phrase names places the job is not in.
var exclusionWords = map[string]bool{
	"not": true, "except": true, "excluding": true, "excludes": true, "outside": true, "no": true,
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
		return rest == "" || len(parsePlaces(rest, exact)) > 0
	}
	return len(parsePlaces(s, exact)) > 0
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
