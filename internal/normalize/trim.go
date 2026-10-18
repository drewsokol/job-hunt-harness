package normalize

import (
	"regexp"
	"strings"
)

// TrimBoilerplate drops the parts of a plain-text description that say
// nothing about the role: benefits lists, "about us" sections, equal
// opportunity and privacy statements. It is used for keyword matching and
// for descriptions sent to agents; people always see the full text.
//
// A section starts at a heading line (short, not a list item) and runs to
// the next heading. Sections under a boilerplate heading are dropped, and
// so are lines anywhere that are boilerplate on their own (an equal
// opportunity paragraph with no heading). Lines that state pay are always
// kept. company is the employer's name, for headings like "About Exampleco".
func TrimBoilerplate(text, company string) string {
	companyKey := headingKey(company)
	var kept []string
	dropping := false
	for _, line := range strings.Split(text, "\n") {
		if isHeading(line) {
			dropping = boilerplateHeading(headingKey(line), companyKey)
		}
		if (dropping || boilerplateLine.MatchString(line)) && !statesPay(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// isHeading reports whether a line looks like a section heading: short, not
// a list item, and not a sentence.
func isHeading(line string) bool {
	l := strings.TrimSpace(line)
	if l == "" || len(l) > 60 || strings.HasPrefix(l, "- ") || len(strings.Fields(l)) > 8 {
		return false
	}
	return !strings.HasSuffix(l, ".") || strings.HasSuffix(l, "...")
}

var nonWord = regexp.MustCompile(`[^a-z0-9' ]+`)

// headingKey lowercases a heading and reduces it to words: "Perks &
// Benefits:" becomes "perks and benefits".
func headingKey(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "&", " and ")
	s = strings.ReplaceAll(s, "’", "'")
	return strings.Join(strings.Fields(nonWord.ReplaceAllString(s, " ")), " ")
}

// boilerplateHeadings start the headings of sections to drop. A heading
// matches when it is one of these or begins with one followed by more
// words ("benefits at exampleco").
var boilerplateHeadings = []string{
	"benefits", "our benefits", "perks", "our perks", "perks and benefits", "benefits and perks",
	"what we offer", "what we provide", "what's in it for you", "why join us", "why work with us", "why work here",
	"why you'll love working here", "life at", "working at",
	"about us", "who we are", "about the company", "our mission", "our values", "our culture",
	"equal opportunity", "equal employment opportunity", "eeo", "diversity", "our commitment to diversity",
	"commitment to diversity", "inclusion", "accommodations", "reasonable accommodation",
	"privacy", "applicant privacy", "privacy notice", "data privacy", "recruitment fraud", "beware of fraud", "e verify",
}

func boilerplateHeading(key, company string) bool {
	if key == "" {
		return false
	}
	// "Compensation and benefits" holds the pay range.
	if strings.Contains(key, "compensation") || strings.Contains(key, "salary") || strings.Contains(key, "pay") {
		return false
	}
	if company != "" && (key == "about "+company || key == "why "+company || key == "life at "+company) {
		return true
	}
	for _, h := range boilerplateHeadings {
		if key == h || strings.HasPrefix(key, h+" ") {
			return true
		}
	}
	return false
}

// boilerplateLine matches lines that are boilerplate wherever they appear.
var boilerplateLine = regexp.MustCompile(`(?i)` +
	`equal (?:employment )?opportunity|without regard to (?:race|age|sex|gender)|regardless of (?:race|age|sex|gender)|` +
	`reasonable accommodations?|e-verify|applicant privacy|privacy (?:notice|policy)|` +
	`unsolicited (?:resumes|agency)|recruitment fraud|^#LI-`)

// statesPay reports whether a line gives a pay figure, which is always worth
// keeping.
func statesPay(line string) bool {
	return strings.ContainsAny(line, "0123456789") && len(ParsePay(line)) > 0
}
