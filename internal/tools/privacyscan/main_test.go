package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanLine(t *testing.T) {
	zw := string(rune(0x200b))
	// Fixtures the scanner would flag are split, so this file passes its own scan.
	tests := []struct {
		name, line string
		want       []string // substrings of the findings, in order
	}{
		{"clean", "func main() {}", nil},
		{"term, any case", "moving to Springfield soon", []string{"private term 2"}},
		{"term text never echoed", "pay floor of 123k", []string{"private term 1"}},
		{"invisible", "a" + zw + "b", []string{"invisible character U+200B"}},
		{"windows path", `see C:` + `\Users\someone\notes.txt`, []string{"local Windows user path"}},
		{"windows path, forward slashes", "d:" + "/users/someone/x", []string{"local Windows user path"}},
		{"unix home in a fixture is fine", "/home/alex/.local/share/jhh", nil},
		{"real email", "write to someone" + "@" + "realmail.io", []string{"email address outside"}},
		{"example emails", "hr@example.com jobs@acme.example pw@mail.test", nil},
		{"commit trailer", "Co-Authored-By: Claude <noreply@anthropic.com>", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			s := scanner{terms: parseTerms("# comment\n123K\n\nspringfield\n"), out: &out}
			s.scanTerms("f:1", tc.line)
			s.scanLine("f:1", tc.line)
			got := strings.Split(strings.TrimSpace(out.String()), "\n")
			if out.Len() == 0 {
				got = nil
			}
			if len(got) != len(tc.want) {
				t.Fatalf("findings = %q, want %q", got, tc.want)
			}
			for i, w := range tc.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("finding %d = %q, want it to contain %q", i, got[i], w)
				}
			}
			for _, term := range []string{"123k", "springfield"} {
				if strings.Contains(strings.ToLower(out.String()), term) {
					t.Errorf("output echoes a private term: %q", out.String())
				}
			}
		})
	}
}

func TestDataFile(t *testing.T) {
	for name, bad := range map[string]bool{
		"data/jobs.json":                   true,
		"internal/store/testdata/x.db":     true,
		"Profile-me.md":                    true,
		"keys.json":                        true,
		"internal/web/static/app.js":       false,
		"docs/specs/0001-shortlist.md":     false,
		"internal/datadir/datadir_test.go": false,
	} {
		if got := dataFile(name) != ""; got != bad {
			t.Errorf("dataFile(%q) flagged = %v, want %v", name, got, bad)
		}
	}
}

func TestScanPatch(t *testing.T) {
	patch := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -10,0 +11,3 @@ func f() {
+	ok := 1
+	// springfield
+++ not a header, springfield
@@ -20 +22 @@
-	old springfield line
+	new line
diff --git a/springfield.md b/springfield.md
--- /dev/null
+++ b/springfield.md
@@ -0,0 +1 @@
+Springfield again
`
	var out strings.Builder
	s := scanner{terms: parseTerms("springfield"), out: &out}
	s.scanPatch("", patch)
	want := "a.go:12: private term 1\na.go:13: private term 1\n" +
		"springfield.md (file name): private term 1\nspringfield.md:1: private term 1\n"
	if out.String() != want {
		t.Errorf("findings = %q, want %q", out.String(), want)
	}
}

// TestScanCommits checks each commit's own changes, so a term added in one
// commit and removed in the next, or brought in by a rename, is caught.
func TestScanCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.txt", "one\n")
	git("add", "a.txt")
	git("commit", "-q", "-m", "base")
	git("tag", "base")
	write("a.txt", "one\nmoving to Springfield\n")
	git("commit", "-q", "-am", "add a line")
	write("a.txt", "one\n")
	git("commit", "-q", "-am", "take it out again")
	git("mv", "a.txt", "springfield.txt")
	git("commit", "-q", "-m", "rename")
	write("springfield.png", "\x00binary")
	git("add", "springfield.png")
	git("commit", "-q", "-m", "write to someone"+"@"+"realmail.io")

	var out strings.Builder
	s := scanner{terms: parseTerms("springfield"), out: &out, dir: dir}
	if err := s.scanCommits("base..HEAD"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		" a.txt:2: private term 1",
		" springfield.txt (file name): private term 1",
		" springfield.png (file name): private term 1",
		":1: email address outside the example domains",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("findings = %q, want one containing %q", got, want)
		}
	}
	if s.findings != 4 {
		t.Errorf("findings = %d (%q), want 4", s.findings, got)
	}
}
