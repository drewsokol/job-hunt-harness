package prefilter

import (
	"strings"
	"unicode"
)

// Terms in filters are matched as whole words, ignoring case and
// punctuation: "part-time" matches "Part Time", "node.js" matches
// "Node.js", and "head of" matches "Head of Platform" but not "ahead of".
// Optionally, a word also matches its plural ("agent" matches "agents").

// words splits s into lowercase words. Letters, digits, '+' and '#' make up
// words ("c++", "c#"); everything else separates them.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '#'
	})
}

// term is one word or phrase from the filters.
type term struct {
	text  string // as written in the filters
	words []string
}

func newTerm(s string) term { return term{text: strings.TrimSpace(s), words: words(s)} }

func newTerms(ss []string) []term {
	ts := make([]term, 0, len(ss))
	for _, s := range ss {
		ts = append(ts, newTerm(s))
	}
	return ts
}

// text is a title or description indexed for matching terms.
type text struct {
	words []string
	at    map[string][]int // word -> positions
}

func newText(s string) *text {
	t := &text{words: words(s), at: map[string][]int{}}
	for i, w := range t.words {
		t.at[w] = append(t.at[w], i)
	}
	return t
}

// has reports whether the text contains the term.
func (t *text) has(tm term, plurals bool) bool {
	if len(tm.words) == 0 {
		return false
	}
	starts := t.at[tm.words[0]]
	if plurals {
		starts = append(append(append([]int(nil), starts...), t.at[tm.words[0]+"s"]...), t.at[tm.words[0]+"es"]...)
	}
	for _, i := range starts {
		if i+len(tm.words) > len(t.words) {
			continue
		}
		ok := true
		for j, w := range tm.words[1:] {
			if !sameWord(t.words[i+1+j], w, plurals) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func sameWord(got, want string, plurals bool) bool {
	return got == want || plurals && (got == want+"s" || got == want+"es")
}

// matching returns the terms the text contains, as written in the filters.
func (t *text) matching(ts []term, plurals bool) []string {
	var out []string
	for _, tm := range ts {
		if t.has(tm, plurals) {
			out = append(out, tm.text)
		}
	}
	return out
}
