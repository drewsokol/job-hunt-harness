package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/dedupe"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// Board is a company's job board on one source.
type Board struct {
	Source   string
	Slug     string
	Postings int // open postings when it was probed
}

// Key is the board's unique id, "greenhouse:exampleco".
func (b Board) Key() string { return b.Source + ":" + b.Slug }

// ErrCompanyExists means jhh already reads that board.
var ErrCompanyExists = errors.New("jhh already reads this board")

// SlugGuesses returns the board slugs a company most likely uses, from its
// name: "Made-Up Labs, Inc." gives "madeuplabs" and "made-up-labs".
func SlugGuesses(name string) []string {
	words := strings.Fields(dedupe.CompanyKey(name))
	var out []string
	for _, s := range []string{strings.Join(words, ""), strings.Join(words, "-")} {
		if sources.ValidSlug(s) == nil && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// FindBoards looks for a company's board. where can be a posting URL, a
// board slug, "source:slug", or empty to try SlugGuesses(name). Every
// source is asked about every slug, and the boards that exist are returned.
// Only Greenhouse, Lever and Ashby are asked: they host every company's
// board the same way, so nothing here is specific to one employer.
func FindBoards(ctx context.Context, srcs map[string]sources.Source, name, where string) ([]Board, error) {
	where = strings.TrimSpace(where)
	names := make([]string, 0, len(srcs))
	for n := range srcs {
		names = append(names, n)
	}
	slices.Sort(names)

	slugs := SlugGuesses(name)
	if u, err := url.Parse(where); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		matched := false
		for _, n := range names {
			if ref, ok := srcs[n].MatchURL(u); ok {
				if ref.Slug == "" {
					return nil, fmt.Errorf("the link is a %s posting, but it doesn't say which board; give the board's slug instead", ref.Source)
				}
				names, slugs, matched = []string{n}, []string{ref.Slug}, true
				break
			}
		}
		if !matched {
			return nil, errors.New("no supported board (Greenhouse, Lever or Ashby) recognizes this link")
		}
	} else if where != "" {
		slug := strings.ToLower(where)
		if src, s, ok := strings.Cut(slug, ":"); ok {
			if _, known := srcs[src]; !known {
				return nil, fmt.Errorf("no source named %q; use one of %s", src, strings.Join(names, ", "))
			}
			names, slug = []string{src}, s
		}
		if err := sources.ValidSlug(slug); err != nil {
			return nil, err
		}
		slugs = []string{slug}
	}
	if len(slugs) == 0 {
		return nil, fmt.Errorf("can't guess a board slug from %q; give one", name)
	}

	var found []Board
	var errs []error
	for _, n := range names {
		for _, slug := range slugs {
			count, err := srcs[n].Probe(ctx, slug)
			switch {
			case errors.Is(err, sources.ErrBoardNotFound):
			case err != nil:
				errs = append(errs, fmt.Errorf("%s:%s: %w", n, slug, err))
			default:
				found = append(found, Board{Source: n, Slug: slug, Postings: count})
			}
		}
	}
	if len(found) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return found, nil
}

// AddCompany starts reading a company's board. A company search found
// under the same name gets the board (see store.AddCompany).
func AddCompany(ctx context.Context, s *store.Store, by store.Actor, name string, b Board, origin string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("the company needs a name")
	}
	cs, err := s.Companies(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range cs {
		if c.BoardKey == b.Key() {
			return c.ID, fmt.Errorf("%s (%s): %w", c.Name, b.Key(), ErrCompanyExists)
		}
	}
	cfg, _ := json.Marshal(sources.Board{Slug: b.Slug})
	return s.AddCompany(ctx, by, store.NewCompany{Name: name, Source: b.Source, BoardKey: b.Key(), SourceConfig: cfg, Origin: origin})
}

// CompanyIDs resolves company names or board keys, ignoring case. A name
// shared by several companies resolves to all of them.
func CompanyIDs(ctx context.Context, s *store.Store, names []string) ([]int64, error) {
	cs, err := s.Companies(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var missing []string
	for _, n := range names {
		found := false
		for _, c := range cs {
			if strings.EqualFold(c.Name, n) || c.BoardKey != "" && strings.EqualFold(c.BoardKey, n) {
				ids = append(ids, c.ID)
				found = true
			}
		}
		if !found {
			missing = append(missing, fmt.Sprintf("no company named %q", n))
		}
	}
	if len(missing) > 0 {
		return nil, &InputError{Problems: missing}
	}
	return ids, nil
}
