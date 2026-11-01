// Command privacyscan checks the repo for things that must never be
// published: private terms, files that hold user data, invisible
// characters, local Windows user paths and real email addresses.
//
// The general rules cover every tracked file, and optionally commit
// messages and extra text such as a PR's title and body; CI runs them.
//
// The private terms are checked only on the owner's own machine, from a
// file outside the repo, one per line. They are never given to CI, where
// the code under test could read them. A finding names the term by its line
// number, never by its text. With -base, the terms are checked only on
// lines added since that revision, since some terms are ordinary words (a
// state, a round number) that existing code uses for other reasons.
//
//	go run ./internal/tools/privacyscan -terms-file ~/.jhh-dev/privacy-terms.txt -base origin/main -commits origin/main..HEAD
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
)

func main() {
	termsFile := flag.String("terms-file", "", "file outside the repo holding the private terms, one per line")
	commits := flag.String("commits", "", "git revision range whose commit messages to scan, such as origin/main..HEAD")
	base := flag.String("base", "", "check the private terms only on lines added since this git revision")
	textEnv := flag.String("text-env", "", "comma-separated environment variables whose text to scan, such as PR_TITLE,PR_BODY")
	flag.Parse()

	var terms []string
	if *termsFile != "" {
		b, err := os.ReadFile(*termsFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "privacyscan:", err)
			os.Exit(2)
		}
		terms = parseTerms(string(b))
	}
	if len(terms) == 0 {
		fmt.Fprintln(os.Stderr, "privacyscan: no private terms given; checking the general rules only")
	}

	s := scanner{terms: terms, out: os.Stdout}
	if err := s.scanTree(*base == ""); err != nil {
		fmt.Fprintln(os.Stderr, "privacyscan:", err)
		os.Exit(2)
	}
	if *base != "" {
		if err := s.scanDiff(*base); err != nil {
			fmt.Fprintln(os.Stderr, "privacyscan:", err)
			os.Exit(2)
		}
	}
	if *commits != "" {
		if err := s.scanCommits(*commits); err != nil {
			fmt.Fprintln(os.Stderr, "privacyscan:", err)
			os.Exit(2)
		}
	}
	for name := range strings.SplitSeq(*textEnv, ",") {
		if name = strings.TrimSpace(name); name != "" {
			s.scanText(name, os.Getenv(name), false, true)
		}
	}
	if s.findings > 0 {
		fmt.Fprintf(os.Stderr, "privacyscan: %d findings\n", s.findings)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "privacyscan: clean")
}

// parseTerms reads the private terms, one per line. Blank lines and lines
// starting with # are skipped.
func parseTerms(text string) []string {
	var terms []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		terms = append(terms, strings.ToLower(line))
	}
	return terms
}

type scanner struct {
	terms    []string
	out      io.Writer
	findings int
}

func (s *scanner) report(where, what string) {
	s.findings++
	fmt.Fprintf(s.out, "%s: %s\n", where, what)
}

// scanTree checks every file git tracks, for the private terms too when
// terms is true.
func (s *scanner) scanTree(terms bool) error {
	list, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	for name := range strings.SplitSeq(strings.TrimRight(string(list), "\x00"), "\x00") {
		if name == "" {
			continue
		}
		if why := dataFile(name); why != "" {
			s.report(name, why)
		}
		b, err := os.ReadFile(name)
		if errors.Is(err, os.ErrNotExist) {
			continue // deleted in the working tree
		}
		if err != nil {
			return err
		}
		if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
			continue // binary
		}
		s.scanText(name, string(b), true, terms)
	}
	return nil
}

// scanDiff checks the lines added since a revision for the private terms.
// The general rules already covered them in scanTree.
func (s *scanner) scanDiff(rev string) error {
	out, err := exec.Command("git", "diff", "-U0", "--no-color", "--no-ext-diff", rev, "--").Output()
	if err != nil {
		return fmt.Errorf("git diff %s: %w", rev, err)
	}
	s.scanPatch(string(out))
	return nil
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)`)

// scanPatch checks the added lines of a unified diff, and the names of the
// files they're in, for the private terms.
func (s *scanner) scanPatch(patch string) {
	file, n := "", 0
	header := false // between "diff --git" and the first hunk
	for line := range strings.SplitSeq(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			header = true
		case header && strings.HasPrefix(line, "+++ "):
			if name := strings.TrimPrefix(line, "+++ "); name != "/dev/null" {
				file = strings.TrimPrefix(name, "b/")
				s.scanTerms(file+" (file name)", file)
			}
		case strings.HasPrefix(line, "@@"):
			header = false
			if m := hunkHeader.FindStringSubmatch(line); m != nil {
				fmt.Sscan(m[1], &n)
			}
		case strings.HasPrefix(line, "+"):
			s.scanTerms(fmt.Sprintf("%s:%d", file, n), line[1:])
			n++
		}
	}
}

// scanCommits checks the messages of the commits in a revision range.
func (s *scanner) scanCommits(rng string) error {
	out, err := exec.Command("git", "log", "--format=%H%n%B%x00", rng).Output()
	if err != nil {
		return fmt.Errorf("git log %s: %w", rng, err)
	}
	for entry := range strings.SplitSeq(string(out), "\x00") {
		entry = strings.TrimLeft(entry, "\n")
		hash, msg, ok := strings.Cut(entry, "\n")
		if !ok {
			continue
		}
		s.scanText("commit "+hash[:min(len(hash), 12)], msg, true, true)
	}
	return nil
}

// dataFile says why a tracked file looks like user data, or "".
func dataFile(name string) string {
	base := strings.ToLower(path.Base(name))
	switch {
	case strings.HasPrefix(name, "data/"):
		return "file under data/, which holds user data"
	case strings.HasSuffix(base, ".db"), strings.HasSuffix(base, ".sqlite"):
		return "database file"
	case base == ".env", base == "keys.json":
		return "file that holds keys"
	case strings.HasPrefix(base, "profile") && strings.HasSuffix(base, ".md"):
		return "profile file"
	}
	return ""
}

var (
	windowsUserPath = regexp.MustCompile(`(?i)\b[a-z]:[\\/]+users[\\/]+[^\\/\s"'` + "`" + `]+`)
	emailAddress    = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+\.)+[A-Za-z]{2,}`)
)

// scanText checks one piece of text, reporting line numbers when lines is
// true, and checking the private terms when terms is true.
func (s *scanner) scanText(where, text string, lines, terms bool) {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		at := where
		if lines {
			at = fmt.Sprintf("%s:%d", where, n)
		}
		if terms {
			s.scanTerms(at, sc.Text())
		}
		s.scanLine(at, sc.Text())
	}
}

func (s *scanner) scanTerms(at, line string) {
	lower := strings.ToLower(line)
	for i, t := range s.terms {
		if strings.Contains(lower, t) {
			s.report(at, fmt.Sprintf("private term %d", i+1))
		}
	}
}

// scanLine checks one line against the general rules.
func (s *scanner) scanLine(at, line string) {
	for _, r := range line {
		if invisible(r) {
			s.report(at, fmt.Sprintf("invisible character U+%04X; build it from its code point instead", r))
			break
		}
	}
	if windowsUserPath.MatchString(line) {
		s.report(at, "local Windows user path")
	}
	for _, m := range emailAddress.FindAllStringSubmatch(line, -1) {
		if !exampleEmail(m[0]) {
			s.report(at, "email address outside the example domains")
		}
	}
}

// invisible reports characters that render as nothing or reorder text.
func invisible(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200F, // zero-width spaces and joiners, direction marks
		r >= 0x202A && r <= 0x202E,   // direction embeddings and overrides
		r >= 0x2060 && r <= 0x2064,   // word joiner, invisible operators
		r >= 0x2066 && r <= 0x2069,   // direction isolates
		r == 0xFEFF,                  // byte order mark
		r == 0x00AD,                  // soft hyphen
		r >= 0xE0000 && r <= 0xE007F: // tag characters
		return true
	}
	return false
}

// exampleEmail reports addresses that are safe to publish: reserved
// example domains, and the no-reply addresses commit trailers use.
func exampleEmail(addr string) bool {
	addr = strings.ToLower(addr)
	_, domain, _ := strings.Cut(addr, "@")
	for _, d := range []string{"example.com", "example.org", "example.net"} {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	for _, tld := range []string{".example", ".test", ".invalid", ".localhost"} {
		if strings.HasSuffix(domain, tld) {
			return true
		}
	}
	return strings.HasPrefix(addr, "noreply@") || domain == "users.noreply.github.com"
}
