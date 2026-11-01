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
// number, never by its text. With -commits, the terms are checked on the
// messages of the commits in that range and on every line and file name
// each one adds, so a term added in one commit and removed in the next is
// still caught; the rest of the tree is left out, since some terms are
// ordinary words (a state, a round number) that existing code uses for
// other reasons.
//
//	go run ./internal/tools/privacyscan -terms-file ~/.jhh-dev/privacy-terms.txt -commits origin/main..HEAD
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
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	termsFile := flag.String("terms-file", "", "file outside the repo holding the private terms, one per line")
	commits := flag.String("commits", "", "git revision range whose commits to scan, such as origin/main..HEAD")
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
	if err := s.scanTree(*commits == ""); err != nil {
		fmt.Fprintln(os.Stderr, "privacyscan:", err)
		os.Exit(2)
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
	dir      string // the repo; "" is the working directory
	findings int
}

func (s *scanner) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = s.dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (s *scanner) report(where, what string) {
	s.findings++
	fmt.Fprintf(s.out, "%s: %s\n", where, what)
}

// scanTree checks every file git tracks, for the private terms too when
// terms is true.
func (s *scanner) scanTree(terms bool) error {
	list, err := s.git("ls-files", "-z")
	if err != nil {
		return err
	}
	for name := range strings.SplitSeq(strings.TrimRight(list, "\x00"), "\x00") {
		if name == "" {
			continue
		}
		if why := dataFile(name); why != "" {
			s.report(name, why)
		}
		b, err := os.ReadFile(filepath.Join(s.dir, name))
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

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)`)

// scanPatch checks the added lines of a unified diff, and the names of the
// files they're in, for the private terms. Findings start with prefix.
func (s *scanner) scanPatch(prefix, patch string) {
	file, n := "", 0
	header := false // between "diff --git" and the first hunk
	for line := range strings.SplitSeq(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// The name comes from here, not "+++", which binary and
			// empty files don't have.
			header = true
			if _, name, ok := strings.Cut(line, " b/"); ok {
				file = name
				s.scanTerms(prefix+file+" (file name)", file)
			}
		case header && strings.HasPrefix(line, "+++ "):
			// Part of the header, not an added line.
		case strings.HasPrefix(line, "@@"):
			header = false
			if m := hunkHeader.FindStringSubmatch(line); m != nil {
				fmt.Sscan(m[1], &n)
			}
		case strings.HasPrefix(line, "+"):
			s.scanTerms(fmt.Sprintf("%s%s:%d", prefix, file, n), line[1:])
			n++
		}
	}
}

// scanCommits checks the messages of the commits in a revision range and,
// when there are private terms, what each commit adds.
func (s *scanner) scanCommits(rng string) error {
	out, err := s.git("-c", "core.quotepath=off", "log", "--format=%H%n%B%x00", rng)
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(out, "\x00") {
		entry = strings.TrimLeft(entry, "\n")
		hash, msg, ok := strings.Cut(entry, "\n")
		if !ok {
			continue
		}
		s.scanText("commit "+short(hash), msg, true, true)
	}
	if len(s.terms) == 0 {
		return nil
	}
	// Renames are shown as a delete and an add, so a new name and every
	// line under it are scanned. Merge commits show no patch: what a merge
	// brings in is scanned in the commits it merges.
	out, err = s.git("-c", "core.quotepath=off", "log", "-p", "-U0", "--no-color", "--no-ext-diff", "--no-renames", "--format=%x00%H", rng)
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(out, "\x00") {
		hash, patch, ok := strings.Cut(entry, "\n")
		if !ok {
			continue
		}
		s.scanPatch("commit "+short(hash)+" ", patch)
	}
	return nil
}

func short(hash string) string { return hash[:min(len(hash), 12)] }

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
