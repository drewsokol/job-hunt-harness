package main

import (
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
@@ -10,0 +11,2 @@ func f() {
+	ok := 1
+	// springfield
@@ -20 +22 @@
-	old springfield line
+	new line
diff --git a/b.md b/b.md
--- /dev/null
+++ b/b.md
@@ -0,0 +1 @@
+Springfield again
`
	var out strings.Builder
	s := scanner{terms: parseTerms("springfield"), out: &out}
	s.scanPatch(patch)
	want := "a.go:12: private term 1\nb.md:1: private term 1\n"
	if out.String() != want {
		t.Errorf("findings = %q, want %q", out.String(), want)
	}
}
