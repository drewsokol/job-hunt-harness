package prefilter

import "strings"

// Levels, from most junior to most senior. Filters name levels from this
// list, and titles are read with the words below.
var Levels = []string{"intern", "junior", "mid", "senior", "lead", "staff", "principal", "distinguished"}

// levelWords are the title words that state each level. Roman numerals
// follow the common ladder (Engineer II is mid-level, III is senior); words
// for roles, like "architect", are not levels.
var levelWords = map[string][]term{
	"intern":        newTerms([]string{"intern", "internship", "co-op"}),
	"junior":        newTerms([]string{"junior", "jr", "entry level", "new grad", "graduate"}),
	"mid":           newTerms([]string{"mid level", "intermediate", "ii"}),
	"senior":        newTerms([]string{"senior", "sr", "iii"}),
	"lead":          newTerms([]string{"lead"}),
	"staff":         newTerms([]string{"staff"}),
	"principal":     newTerms([]string{"principal"}),
	"distinguished": newTerms([]string{"distinguished", "fellow"}),
}

// levelsIn returns the levels a title states, most junior first.
func levelsIn(title *text) []string {
	var out []string
	for _, l := range Levels {
		for _, tm := range levelWords[l] {
			if title.has(tm, false) {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

func levelRank(l string) int {
	for i, x := range Levels {
		if x == l {
			return i
		}
	}
	return -1
}

// EmploymentTypes are the kinds of employment filters can exclude.
var EmploymentTypes = []string{"full-time", "part-time", "contract", "temporary", "internship"}

// employmentTitleWords are title words that state the kind of employment.
var employmentTitleWords = map[string][]term{
	"part-time":  newTerms([]string{"part time"}),
	"contract":   newTerms([]string{"contract", "contractor", "freelance"}),
	"temporary":  newTerms([]string{"temporary", "temp", "fixed term", "seasonal"}),
	"internship": newTerms([]string{"intern", "internship", "co-op"}),
}

// employmentOf reads the kinds of employment from the board's field
// ("FullTime", "Full-time", "Contract") and the title ("Contract Engineer").
func employmentOf(field string, title *text) map[string]bool {
	out := map[string]bool{}
	key := strings.Join(words(field), "")
	switch {
	case strings.Contains(key, "fulltime"):
		out["full-time"] = true
	case strings.Contains(key, "parttime"):
		out["part-time"] = true
	case strings.Contains(key, "intern"):
		out["internship"] = true
	case strings.Contains(key, "contract"), strings.Contains(key, "freelance"):
		out["contract"] = true
	case strings.Contains(key, "temp"), strings.Contains(key, "seasonal"):
		out["temporary"] = true
	}
	for kind, ts := range employmentTitleWords {
		for _, tm := range ts {
			if title.has(tm, false) {
				out[kind] = true
			}
		}
	}
	return out
}

// remoteInDescription are phrases a description uses to say the role can
// be done remotely. They are only consulted when the location doesn't say.
var remoteInDescription = newTerms([]string{
	"fully remote", "remote first", "remote friendly", "100% remote", "work remotely", "working remotely",
	"remote position", "remote role", "remote opportunity", "this role is remote", "remote eligible", "open to remote",
})
