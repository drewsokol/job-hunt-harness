package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// Size limits for what agents write. A profile is read in a minute; a
// resume is a few pages.
const (
	maxProfileLen = 50_000
	maxResumeLen  = 100_000
	maxSummaryLen = 300
	maxDiffLen    = 6_000
)

// ProfileSaved is the result of saving the profile.
type ProfileSaved struct {
	Version int64
	// Diff shows the change from the previous version: removed lines start
	// with "- ", added ones with "+ ".
	Diff string
}

// UpdateProfile saves a whole new profile.
func UpdateProfile(ctx context.Context, s *store.Store, by store.Actor, markdown, summary string) (ProfileSaved, error) {
	markdown = strings.TrimSpace(markdown) + "\n"
	if err := checkProfile(markdown, summary); err != nil {
		return ProfileSaved{}, err
	}
	prev, _, err := s.Profile(ctx)
	if err != nil {
		return ProfileSaved{}, err
	}
	if prev.Markdown == markdown {
		return ProfileSaved{Version: prev.Version}, nil
	}
	v, err := s.SaveProfile(ctx, by, markdown, strings.TrimSpace(summary))
	return ProfileSaved{Version: v, Diff: Diff(prev.Markdown, markdown)}, err
}

// EditProfileSection replaces or appends to one "## " section of the
// profile, creating the section at the end when it doesn't exist. Other
// sections are left exactly as they were.
func EditProfileSection(ctx context.Context, s *store.Store, by store.Actor, heading, content, mode, summary string) (ProfileSaved, error) {
	prev, _, err := s.Profile(ctx)
	if err != nil {
		return ProfileSaved{}, err
	}
	md, err := editSection(prev.Markdown, heading, content, mode)
	if err != nil {
		return ProfileSaved{}, err
	}
	return UpdateProfile(ctx, s, by, md, summary)
}

func checkProfile(markdown, summary string) error {
	var problems []string
	if strings.TrimSpace(markdown) == "" {
		problems = append(problems, "markdown: the profile is empty")
	}
	if n := utf8.RuneCountInString(markdown); n > maxProfileLen {
		problems = append(problems, fmt.Sprintf("markdown: %d characters is too long; the limit is %d", n, maxProfileLen))
	}
	if s := strings.TrimSpace(summary); s == "" || utf8.RuneCountInString(s) > maxSummaryLen {
		problems = append(problems, fmt.Sprintf("summary: say what changed in one line (1 to %d characters)", maxSummaryLen))
	}
	if len(problems) > 0 {
		return &InputError{Problems: problems}
	}
	return nil
}

// editSection returns md with the section under heading changed.
func editSection(md, heading, content, mode string) (string, error) {
	heading = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(heading), "#"))
	content = strings.TrimSpace(content)
	var problems []string
	if heading == "" || strings.ContainsAny(heading, "\r\n") {
		problems = append(problems, "heading: give the section's name on one line, like \"Highlights\"")
	}
	if mode != "replace" && mode != "append" {
		problems = append(problems, fmt.Sprintf("mode: %q must be replace or append", mode))
	}
	for line := range strings.SplitSeq(content, "\n") {
		if isHeading(line) {
			problems = append(problems, "content: has its own heading; edit one section at a time, without headings")
			break
		}
	}
	if len(problems) > 0 {
		return "", &InputError{Problems: problems}
	}

	lines := strings.Split(strings.TrimRight(md, "\n"), "\n")
	if md == "" {
		lines = nil
	}
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "## ") && strings.EqualFold(strings.TrimSpace(l[3:]), heading) {
			start = i
			break
		}
	}
	if start < 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "## "+heading, "", content)
		return strings.Join(lines, "\n") + "\n", nil
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if isHeading(lines[i]) {
			end = i
			break
		}
	}
	body := lines[start+1 : end]
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	var section []string
	if mode == "append" && len(body) > 0 {
		section = append(section, body...)
	} else {
		section = append(section, "")
	}
	section = append(section, strings.Split(content, "\n")...)
	if end < len(lines) {
		section = append(section, "")
	}
	out := append(append(append([]string{}, lines[:start+1]...), section...), lines[end:]...)
	return strings.Join(out, "\n") + "\n", nil
}

// isHeading reports whether a markdown line starts a section ("# " or
// "## "); deeper headings belong to the section they're in.
func isHeading(line string) bool {
	return strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ")
}

// SetResumeText saves the resume text.
func SetResumeText(ctx context.Context, s *store.Store, by store.Actor, text, source string) (int64, error) {
	text = strings.TrimSpace(text)
	var problems []string
	if text == "" {
		problems = append(problems, "text: the resume text is empty")
	}
	if n := utf8.RuneCountInString(text); n > maxResumeLen {
		problems = append(problems, fmt.Sprintf("text: %d characters is too long; the limit is %d", n, maxResumeLen))
	}
	if source != "pasted" && source != "file" {
		problems = append(problems, fmt.Sprintf("source: %q must be pasted (the user pasted it into the chat) or file (you read it from a file)", source))
	}
	if len(problems) > 0 {
		return 0, &InputError{Problems: problems}
	}
	return s.SaveResume(ctx, by, text, source)
}

// Diff is a line diff of a and b: lines only in a start with "- ", lines
// only in b with "+ ". Unchanged lines are left out. Long diffs are cut.
func Diff(a, b string) string {
	x, y := diffLines(a), diffLines(b)
	// Longest common subsequence, by dynamic programming. Profiles are a
	// few hundred lines, so the table stays small.
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out strings.Builder
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			i, j = i+1, j+1
		case j < len(y) && (i == len(x) || lcs[i][j+1] >= lcs[i+1][j]):
			out.WriteString("+ " + y[j] + "\n")
			j++
		default:
			out.WriteString("- " + x[i] + "\n")
			i++
		}
		if out.Len() > maxDiffLen {
			out.WriteString("… (diff cut)\n")
			break
		}
	}
	return out.String()
}

func diffLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// InputError lists what is wrong with what an agent or person sent, in
// terms they can act on.
type InputError struct{ Problems []string }

func (e *InputError) Error() string { return strings.Join(e.Problems, "; ") }

// IsInputError reports whether err is an InputError.
func IsInputError(err error) bool {
	var ie *InputError
	return errors.As(err, &ie)
}
