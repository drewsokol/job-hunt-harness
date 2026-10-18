// Package untrusted cleans text from job listings and looks for content
// aimed at AI agents (docs/decisions/0005-untrusted-content.md).
//
// Cleaning removes characters a human reader can't see; it never rewrites
// wording. Detection is rule-based and only labels what it finds. Deciding
// whether a finding matters is left to the user.
package untrusted

import (
	"strings"
	"unicode/utf8"
)

// Clean removes invisible characters from s: zero-width characters, bidi
// controls, soft hyphens, byte order marks, and Unicode tag characters
// (which can hide ASCII text; see Smuggled). Everything else is unchanged.
func Clean(s string) string {
	if !strings.ContainsFunc(s, invisible) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if invisible(r) {
			return -1
		}
		return r
	}, s)
}

// invisible reports whether r renders as nothing. Code points are written as
// numbers so the source itself contains no invisible characters.
func invisible(r rune) bool {
	switch {
	case r == 0x00ad, // soft hyphen
		r >= 0x200b && r <= 0x200f, // zero-width space, joiners, direction marks
		r >= 0x202a && r <= 0x202e, // bidi embedding and override
		r >= 0x2060 && r <= 0x2064, // word joiner, invisible operators
		r >= 0x2066 && r <= 0x2069, // bidi isolates
		r == 0xfeff,                // byte order mark
		isTag(r):
		return true
	}
	return false
}

// isTag reports whether r is in the Unicode tags block, U+E0000 to U+E007F.
func isTag(r rune) bool { return r >= 0xe0000 && r <= 0xe007f }

// Smuggled returns ASCII text hidden in s with Unicode tag characters, which
// display as nothing but which models read. Each tag character U+E00xx
// stands for the ASCII character 0xxx.
func Smuggled(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isTag(r) {
			if a := r - 0xe0000; a >= 0x20 && a < 0x7f {
				b.WriteRune(a)
			}
		}
	}
	return b.String()
}

// excerpt returns at most maxExcerpt bytes of s around [start, end), on rune
// boundaries, with whitespace collapsed.
func excerpt(s string, start, end int) string {
	const context = 60
	from, to := max(start-context, 0), min(end+context, len(s))
	if to-from > maxExcerpt {
		to = from + maxExcerpt
	}
	for from > 0 && !utf8.RuneStart(s[from]) {
		from++
	}
	for to < len(s) && !utf8.RuneStart(s[to]) {
		to--
	}
	out := strings.Join(strings.Fields(s[from:to]), " ")
	if from > 0 {
		out = "…" + out
	}
	if to < len(s) {
		out += "…"
	}
	return out
}

// maxExcerpt caps the text kept with a finding.
const maxExcerpt = 200
