package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/sources/builtin"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

const companiesUsage = `usage:
  jhh companies                      list the companies jhh knows
  jhh companies add <name> [where]   find the company's job board and start reading it
<where> is a board slug, source:slug (greenhouse:exampleco) or a link to one of
the company's postings. Without it, jhh tries slugs made from the name. Only
Greenhouse, Lever and Ashby boards are supported; other employers' jobs come
through search or your agent.`

// companies lists the companies jhh knows, or adds one with its board.
func companies(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	switch {
	case len(args) == 0:
		st, code := openData(ctx, stderr)
		if st == nil {
			return code
		}
		defer st.Close()
		cs, err := st.Companies(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "jhh:", err)
			return 1
		}
		printCompanies(stdout, cs)
		return 0
	case args[0] == "add" && (len(args) == 2 || len(args) == 3):
		where := ""
		if len(args) == 3 {
			where = args[2]
		}
		return addCompany(ctx, args[1], where, stdout, stderr)
	default:
		fmt.Fprintln(stderr, companiesUsage)
		return 2
	}
}

func addCompany(ctx context.Context, name, where string, stdout, stderr io.Writer) int {
	boards, err := core.FindBoards(ctx, builtin.All(sources.NewClient()), name, where)
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	switch len(boards) {
	case 0:
		fmt.Fprintf(stdout, "No Greenhouse, Lever or Ashby board found for %s.\n", oneLine(name))
		if where == "" {
			fmt.Fprintln(stdout, "If you know its board, give the slug or a link to one of its postings: jhh companies add <name> <where>")
		}
		return 1
	case 1:
	default:
		fmt.Fprintln(stdout, "More than one board matches; choose one with jhh companies add <name> <source:slug>:")
		for _, b := range boards {
			fmt.Fprintf(stdout, "  %-30s %d open postings\n", b.Key(), b.Postings)
		}
		return 1
	}
	b := boards[0]
	st, code := openData(ctx, stderr)
	if st == nil {
		return code
	}
	defer st.Close()
	if _, err := core.AddCompany(ctx, st, store.ActorUser, name, b, "user"); err != nil {
		if errors.Is(err, core.ErrCompanyExists) {
			fmt.Fprintln(stdout, "Already added:", err)
			return 0
		}
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Added %s (%s, %d open postings). Run jhh refresh to fetch them.\n", oneLine(name), b.Key(), b.Postings)
	return 0
}

func printCompanies(w io.Writer, cs []store.Company) {
	if len(cs) == 0 {
		fmt.Fprintln(w, "No companies yet. Add one with jhh companies add <name>, or set filters with a search and run jhh refresh.")
		return
	}
	boards := 0
	for _, c := range cs {
		if c.Source != "none" {
			boards++
		}
	}
	fmt.Fprintf(w, "%d companies, %d with a board jhh reads.\n", len(cs), boards)
	for _, c := range cs {
		board := c.BoardKey
		switch {
		case c.Source == "none":
			board = "no board (found by search)"
		case !c.Enabled:
			board += " (disabled)"
		}
		open := fmt.Sprintf("%d open", c.OpenPostings)
		if c.Source == "none" {
			open = ""
		}
		fmt.Fprintf(w, "  %-30s %-40s %s\n", oneLine(c.Name), board, open)
		if c.LastError != "" {
			fmt.Fprintf(w, "    last error: %s\n", oneLine(c.LastError))
		}
	}
}
