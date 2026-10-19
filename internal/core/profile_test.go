package core

import (
	"context"
	"strings"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/store"
)

const madeUpProfile = `# Profile

## Summary

A made-up backend engineer.

## Highlights

- Built a made-up queue.
### Detail kept with Highlights

## Gaps

- No made-up mobile work.
`

func TestEditSection(t *testing.T) {
	tests := []struct {
		name, md, heading, content, mode, want string
	}{
		{"replace keeps other sections", madeUpProfile, "summary", "Someone else, made up.", "replace",
			strings.Replace(madeUpProfile, "A made-up backend engineer.", "Someone else, made up.", 1)},
		{"append after subheadings", madeUpProfile, "## Highlights", "- Led a made-up migration.", "append",
			strings.Replace(madeUpProfile, "### Detail kept with Highlights\n", "### Detail kept with Highlights\n- Led a made-up migration.\n", 1)},
		{"last section", madeUpProfile, "Gaps", "- None worth noting.", "replace",
			strings.Replace(madeUpProfile, "- No made-up mobile work.", "- None worth noting.", 1)},
		{"new section at the end", madeUpProfile, "Notes for the screener", "Flag customer-facing roles.", "append",
			madeUpProfile + "\n## Notes for the screener\n\nFlag customer-facing roles.\n"},
		{"empty profile", "", "Summary", "Made up.", "replace", "## Summary\n\nMade up.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := editSection(tt.md, tt.heading, tt.content, tt.mode)
			if err != nil || got != tt.want {
				t.Errorf("err %v\ngot:\n%s\nwant:\n%s", err, got, tt.want)
			}
		})
	}
	for _, bad := range [][3]string{{"", "x", "replace"}, {"Gaps", "## Sneaky\nx", "replace"}, {"Gaps", "x", "merge"}} {
		if _, err := editSection(madeUpProfile, bad[0], bad[1], bad[2]); !IsInputError(err) {
			t.Errorf("editSection(%q) = %v; want an input error", bad, err)
		}
	}
}

func TestDiff(t *testing.T) {
	got := Diff("a\nb\nc\n", "a\nc\nd\n")
	if got != "- b\n+ d\n" {
		t.Errorf("Diff = %q", got)
	}
	if Diff("same\n", "same\n") != "" {
		t.Error("no change should be an empty diff")
	}
}

func TestMemoryFromAListingStaysInferred(t *testing.T) {
	ctx := context.Background()
	f := newRefreshFixture(t, "exampleco")
	f.src.set("exampleco", fakePosting("1", "Backend Engineer"))
	job := f.refresh().NewJobs[0]

	_, err := Remember(ctx, f.s, store.Agent("test"), RememberInput{Kind: "preference", Content: "Fine with relocating.", Basis: "user_said", EvidenceJobID: job})
	if !IsInputError(err) || !strings.Contains(err.Error(), "must be inferred") {
		t.Fatalf("user_said from a listing: %v", err)
	}
	r, err := Remember(ctx, f.s, store.Agent("test"), RememberInput{Kind: "preference", Content: "May prefer queue work.", Basis: "inferred", EvidenceJobID: job})
	if err != nil {
		t.Fatal(err)
	}
	basis := "user_said"
	if err := UpdateMemory(ctx, f.s, store.Agent("test"), r.ID, MemoryEdit{Basis: &basis}); !IsInputError(err) {
		t.Errorf("promoting a listing memory: %v", err)
	}
	if _, err := Remember(ctx, f.s, store.Agent("test"), RememberInput{Kind: "fact", Content: "x", Basis: "inferred", EvidenceJobID: 999}); !IsInputError(err) {
		t.Errorf("unknown evidence job: %v", err)
	}
}
