package hackernews

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
)

// ad is what a post's header line says. Every field is untrusted text from
// the post, trimmed but not reworded.
type ad struct {
	Company string
	// Title holds the roles the header names, joined with
	// normalize.RoleSeparator, or is empty when it names none.
	Title      string
	Location   string // location and workplace parts, joined with " | "
	Pay        string
	Employment string
}

// Limits on a header that can be read. A longer company name is a sentence,
// not a name.
const (
	maxCompanyLen   = 60
	maxCompanyWords = 8
	maxRoles        = 8
	// maxRolesLen is the longest part read as roles; longer ones are prose.
	maxRolesLen = 160
)

var (
	// The convention separates parts with "|"; some posts use dashes.
	dashSeparator = regexp.MustCompile(`\s+[–—]\s+`)
	webAddress    = regexp.MustCompile(`(?i)\bhttps?://[^\s()\[\]]+|\bwww\.[^\s()\[\]]+`)
	trailingParen = regexp.MustCompile(`\s*[(\[]([^()\[\]]*)[)\]]\s*$`)
	// money is a figure that can only be pay: a currency sign or "k". A
	// 401(k) retirement plan is a benefit, not pay (see hasMoney).
	money    = regexp.MustCompile(`(?i)[$€£¥]\s*\d|\b\d+(?:[.,]\d+)?\s*k\b`)
	plan401k = regexp.MustCompile(`(?i)\b401\s*\(?k\)?`)
	// fundingFigure is a figure about the company, not pay: "$250k seed
	// raised", "$3M ARR", "raised $12M".
	fundingFigure = regexp.MustCompile(`(?i)[$€£]?\s*\d+(?:[.,]\d+)?\s*[kmb]?\+?\s+(?:in\s+)?(?:pre-seed|seed|series [a-f]|raised|funding|arr|mrr|revenue|valuation)\b|\b(?:raised|valued at|valuation of|arr of|revenue of)\s+[$€£]?\s*\d+(?:[.,]\d+)?\s*[kmb]?\b`)
	payWords      = regexp.MustCompile(`(?i)\b(?:salary|equity|compensation|usd|eur|gbp|cad)\b`)
	employment    = regexp.MustCompile(`(?i)^(?:full[- ]?time|part[- ]?time|contract(?:or)?s?|freelance|internships?|interns?|perm(?:anent)?|fte|temporary|employment|or|and|[\s/,&+-])+$`)
	filledWords   = regexp.MustCompile(`(?i)\b(?:closed now|now closed|no longer hiring|(?:positions?|roles?|jobs?) (?:(?:has|have) been |(?:is|are) )?(?:closed|filled))\b|[\[(]\s*(?:closed|filled)\s*[\])]`)
	// roleWords mark a part of the header that names roles. Level words
	// ("senior", "staff") aren't among them: alone they also describe the
	// company ("20 staff").
	roleWords     = regexp.MustCompile(`(?i)\b(?:engineers?|engineering|eng|engrs?|developers?|devs?|swes?|sres?|programmers?|scientists?|researchers?|designers?|architects?|analysts?|managers?|leads?|directors?|head of|vp|cto|ceo|coo|cfo|chief|president|interns?|recruiters?|marketing|marketers?|sales|account executives?|devops|mlops|pms?|roles|positions|openings|specialists?|consultants?|writers?|administrators?|technicians?|technologists?|mts|member of technical staff|coordinators?|strategists?)\b`)
	parenthetical = regexp.MustCompile(`\([^()]*\)`)
	leadingJoin   = regexp.MustCompile(`(?i)^(?:and|or)\s+`)
	// notEmployer marks a first part that names a role, a request or a
	// question instead of the employer ("Senior Backend Engineer | Remote").
	notEmployer = regexp.MustCompile(`(?i)\b(?:engineers?|developers?|designers?|scientists?|co-?founders?|hiring|looking for)\b|\?`)
)

// adState says whether a header could be read.
type adState int

const (
	adOK adState = iota
	adUnreadable
	adFilled // the poster marked it closed or filled
)

// parseAd reads the header line of a post: the employer first, then roles,
// places, pay and employment type in any order.
func parseAd(html string) (ad, adState) {
	text := normalize.HTMLToText(html)
	header, _, _ := strings.Cut(text, "\n")
	header = strings.TrimSpace(header)
	if filledWords.MatchString(header) {
		return ad{}, adFilled
	}
	parts := split(header)
	if len(parts) < 2 {
		return ad{}, adUnreadable
	}
	company, notes := companyName(parts[0])
	if company == "" {
		return ad{}, adUnreadable
	}
	a := ad{Company: company}
	var roles, places, pay []string
	for _, part := range append(notes, parts[1:]...) {
		p := strings.TrimSpace(webAddress.ReplaceAllString(part, ""))
		switch {
		case !hasLetterOrDigit(p):
		case employment.MatchString(p):
			if a.Employment == "" {
				a.Employment = p
			}
		case isRoles(p):
			roles = append(roles, splitRoles(p)...)
		case hasMoney(p):
			pay = append(pay, p)
		case isPlace(p):
			places = append(places, p)
		case payWords.MatchString(p):
			pay = append(pay, p)
		}
	}
	if len(roles) > maxRoles {
		roles = roles[:maxRoles]
	}
	a.Title = strings.Join(roles, normalize.RoleSeparator)
	a.Location = strings.Join(places, " | ")
	a.Pay = strings.Join(pay, " | ")
	return a, adOK
}

// split cuts a header into its parts.
func split(header string) []string {
	var parts []string
	switch {
	case strings.Contains(header, "|"):
		parts = strings.Split(header, "|")
	case dashSeparator.MatchString(header):
		parts = dashSeparator.Split(header, -1)
	}
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// companyName reads the employer from the first part, leaving out web
// addresses and trailing notes in parentheses ("Exampleco (YC S24)"). The
// notes are returned, since some say where the job is ("(Remote US)").
func companyName(part string) (string, []string) {
	s := webAddress.ReplaceAllString(part, " ")
	var notes []string
	for {
		m := trailingParen.FindStringSubmatchIndex(s)
		if m == nil {
			break
		}
		notes = append(notes, s[m[2]:m[3]])
		s = s[:m[0]]
	}
	s = strings.Trim(strings.Join(strings.Fields(s), " "), " -–—:,")
	if len(s) > maxCompanyLen || len(strings.Fields(s)) > maxCompanyWords || !hasLetterOrDigit(s) || notEmployer.MatchString(s) {
		return "", nil
	}
	return s, notes
}

// splitRoles splits a part naming several roles at its commas and
// semicolons, outside parentheses. A piece without a role word stays with
// the role before it, so "Senior Engineer, Payments" is one role.
func splitRoles(part string) []string {
	var pieces []string
	depth, start := 0, 0
	for i, r := range part {
		switch r {
		case '(', '[':
			depth++
		case ')', ']':
			depth = max(depth-1, 0)
		case ',', ';':
			if depth == 0 {
				pieces = append(pieces, part[start:i])
				start = i + 1
			}
		}
	}
	pieces = append(pieces, part[start:])
	var roles []string
	for _, p := range pieces {
		p = leadingJoin.ReplaceAllString(strings.TrimSpace(p), "")
		switch {
		case p == "":
		case len(roles) > 0 && !roleWords.MatchString(p):
			roles[len(roles)-1] += ", " + p
		default:
			roles = append(roles, p)
		}
	}
	return roles
}

// isPlace reports whether a part says where the job is or whether it's
// remote. A name jhh can't place in a country or region ("Series B") isn't
// taken as a place.
func isPlace(s string) bool {
	loc := normalize.ParseStatedLocation(s)
	return loc.Workplace != normalize.Unknown ||
		slices.ContainsFunc(loc.Places, func(p normalize.Place) bool { return p.Country != "" || p.Region != "" })
}

func hasLetterOrDigit(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
}

// isRoles reports whether a part names roles: it has a role word, is short,
// and states pay only in parentheses ("Junior SWE ($120k)"). A part stating
// pay outside them is about pay, even when it names a role ("$200k for the
// Staff Engineer role").
func isRoles(s string) bool {
	return roleWords.MatchString(s) && len(s) <= maxRolesLen && !hasMoney(parenthetical.ReplaceAllString(s, ""))
}

// hasMoney reports whether s states pay. A figure about the company's
// funding or revenue isn't pay.
func hasMoney(s string) bool {
	return money.MatchString(fundingFigure.ReplaceAllString(plan401k.ReplaceAllString(s, ""), ""))
}
