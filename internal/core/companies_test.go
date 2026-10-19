package core

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

func TestSlugGuesses(t *testing.T) {
	tests := map[string][]string{
		"Made-Up Labs, Inc.": {"madeuplabs", "made-up-labs"},
		"Exampleco":          {"exampleco"},
		"Smith & Jones LLP":  {"smithandjones", "smith-and-jones"},
		"!!!":                nil,
	}
	for name, want := range tests {
		if got := SlugGuesses(name); !slices.Equal(got, want) {
			t.Errorf("SlugGuesses(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFindBoards(t *testing.T) {
	ctx := context.Background()
	src := newFake()
	src.set("made-up-labs", fakePosting("1", "Backend Engineer"), fakePosting("2", "Data Engineer"))
	srcs := map[string]sources.Source{"fake": src}

	bs, err := FindBoards(ctx, srcs, "Made-Up Labs, Inc.", "")
	if err != nil || len(bs) != 1 || bs[0] != (Board{Source: "fake", Slug: "made-up-labs", Postings: 2}) {
		t.Errorf("guessed: %+v, %v", bs, err)
	}
	if bs, err := FindBoards(ctx, srcs, "Labs", "fake:made-up-labs"); err != nil || len(bs) != 1 {
		t.Errorf("source:slug: %+v, %v", bs, err)
	}
	if bs, err := FindBoards(ctx, srcs, "Other Co", ""); err != nil || len(bs) != 0 {
		t.Errorf("no board: %+v, %v", bs, err)
	}
	for _, where := range []string{"../admin", "nosuch:made-up-labs", "https://example.com/careers/1"} {
		if _, err := FindBoards(ctx, srcs, "Labs", where); err == nil {
			t.Errorf("FindBoards(%q) accepted it", where)
		}
	}

	s := newRefreshFixture(t).s
	id, err := AddCompany(ctx, s, store.ActorUser, "Made-Up Labs", bs[0], "user")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := AddCompany(ctx, s, store.ActorUser, "Made-Up Labs", bs[0], "user"); !errors.Is(err, ErrCompanyExists) || again != id {
		t.Errorf("second add: %d, %v", again, err)
	}
	c, _ := s.Company(ctx, id)
	if b, err := sources.ParseBoard(c.SourceConfig); err != nil || b.Slug != "made-up-labs" || c.BoardKey != "fake:made-up-labs" {
		t.Errorf("company %+v", c)
	}
}
