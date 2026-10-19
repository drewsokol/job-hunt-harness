// Package dedupe computes the keys jhh uses to recognize the same opening
// across postings (docs/decisions/0003-dedupe-identity-ladder.md): a
// normalized title, a fingerprint of the description, and a canonical URL.
//
// These are signals for the store's matching ladder, not decisions. Two
// postings with the same title key are only the same opening when their
// fingerprints match too.
package dedupe

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
)

var (
	// titleParts splits trailing qualifiers off a title.
	titleParts  = regexp.MustCompile(`\s*(?:\||\s[-–—]\s)\s*`)
	parenthetic = regexp.MustCompile(`\s*[(\[]([^)\]]*)[)\]]`)
	abbrevs     = strings.NewReplacer(
		"sr.", "senior", "jr.", "junior", "&", " and ", "+", " plus ",
	)
	wordAbbrevs = map[string]string{"sr": "senior", "jr": "junior", "snr": "senior", "eng": "engineer", "mgr": "manager", "ii": "2", "iii": "3", "iv": "4"}
)

// TitleKey normalizes a title for comparison: location parts ("| Remote",
// "(Canada)", "- New York, NY") are removed, case and punctuation are
// ignored, and common abbreviations are expanded. "Sr. Software Engineer,
// Platform (Remote - US)" becomes "senior software engineer platform".
func TitleKey(title string) string {
	title = parenthetic.ReplaceAllStringFunc(title, func(m string) string {
		inner := parenthetic.FindStringSubmatch(m)[1]
		if normalize.IsLocationText(inner) {
			return ""
		}
		return " " + inner
	})
	parts := titleParts.Split(title, -1)
	kept := []string{dropLocationSuffix(parts[0])}
	for _, p := range parts[1:] {
		if !normalize.IsLocationText(p) {
			kept = append(kept, dropLocationSuffix(p))
		}
	}
	s := abbrevs.Replace(strings.ToLower(strings.Join(kept, " ")))
	words := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for i, w := range words {
		if long, ok := wordAbbrevs[w]; ok {
			words[i] = long
		}
	}
	return strings.Join(words, " ")
}

// minFingerprintText is the shortest normalized description worth
// fingerprinting. Shorter texts ("Apply on our site") would match unrelated
// postings.
const minFingerprintText = 200

var (
	payLine   = regexp.MustCompile(`[$€£]\s?\d|\b(?:USD|CAD|EUR|GBP)\s?\d|\d\s?(?:USD|CAD|EUR|GBP)\b`)
	labelLine = regexp.MustCompile(`(?i)^\s*(?:locations?|office|based in|workplace|work location)\s*:`)
)

// Fingerprint hashes a description so postings of one opening in different
// cities compare equal: lines that state a location or pay are dropped,
// place names (and the posting's own location strings) are masked, and case,
// punctuation and whitespace are ignored. It returns "" when the text is too
// short to tell openings apart.
func Fingerprint(description string, locations ...string) string {
	text := description
	for _, loc := range locations {
		if loc = strings.TrimSpace(loc); len(loc) > 2 {
			text = replaceFold(text, loc, "~")
		}
	}
	var kept []string
	for line := range strings.SplitSeq(text, "\n") {
		if payLine.MatchString(line) || labelLine.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	text = strings.ToLower(normalize.MaskPlaces(strings.Join(kept, "\n")))
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '~' })
	norm := strings.Join(words, " ")
	if len(norm) < minFingerprintText {
		return ""
	}
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:8])
}

// replaceFold replaces every case-insensitive occurrence of old in s.
func replaceFold(s, old, repl string) string {
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(old))
	return re.ReplaceAllLiteralString(s, repl)
}

// trackingParams are query parameters that say how someone arrived, not
// which posting it is. Posting ids in parameters (gh_jid, ashby_jid, lever's
// path) are kept.
var trackingParams = []string{"gh_src", "ref", "referrer", "source", "src", "lever-source", "lever-origin", "lever-via", "trk", "gclid", "fbclid", "mc_cid", "mc_eid", "_hsenc", "_hsmi"}

// CanonicalURL returns a stable form of an http or https URL for
// comparison: lowercase host without "www.", https, no fragment, no tracking
// parameters, sorted query, no trailing slash. It returns "" for anything
// else, including other schemes.
func CanonicalURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.Scheme = "https"
	u.Host = strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	u.User = nil
	u.Fragment, u.RawFragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") || slices.Contains(trackingParams, strings.ToLower(k)) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode() // Encode sorts by key
	return u.String()
}

// companySuffixes are legal-form words that don't tell companies apart.
var companySuffixes = []string{"inc", "incorporated", "llc", "ltd", "limited", "corp", "corporation", "co", "company", "plc", "gmbh", "ag", "sa", "bv", "pbc", "lp", "llp"}

// CompanyKey normalizes a company name so the employer a search result
// names can be matched to a company jhh already has: case, punctuation, a
// leading "the" and trailing legal forms are ignored, so "Example Corp,
// Inc." and "example corp" are the same company. A name that is nothing but
// those words keeps them.
func CompanyKey(name string) string {
	words := strings.FieldsFunc(strings.ToLower(abbrevs.Replace(name)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	all := words
	for len(words) > 0 && slices.Contains(companySuffixes, words[len(words)-1]) {
		words = words[:len(words)-1]
	}
	if len(words) > 0 && words[0] == "the" {
		words = words[1:]
	}
	if len(words) == 0 {
		words = all
	}
	return strings.Join(words, " ")
}

// dropLocationSuffix removes trailing comma-separated parts that together
// are a location: "Software Engineer, Remote" and "Engineer, New York, NY"
// lose them, "Engineer, Platform" keeps its qualifier.
func dropLocationSuffix(s string) string {
	parts := strings.Split(s, ",")
	for i := 1; i < len(parts); i++ {
		if normalize.IsLocationText(strings.Join(parts[i:], ",")) {
			return strings.Join(parts[:i], ",")
		}
	}
	return s
}
