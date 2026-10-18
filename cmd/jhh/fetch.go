package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/datadir"
	"github.com/drewsokol/job-hunt-harness/internal/logging"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/sources/builtin"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

// refresh fetches every enabled company's board, or the named ones.
func refresh(ctx context.Context, args []string, stdout io.Writer, stderr *os.File) int {
	fs := flag.NewFlagSet("refresh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var names multiFlag
	fs.Var(&names, "company", "refresh only this company (name or board key, e.g. greenhouse:exampleco); repeatable")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir, err := datadir.Resolve()
	if err == nil {
		err = dir.Ensure()
	}
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	logger, closeLog, err := logging.New(dir.Logs(), stderr)
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	defer closeLog.Close()
	st, err := openStore(ctx, dir)
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	defer st.Close()

	srcs := builtin.All(sources.NewClient())
	if n, err := core.SeedRegistry(ctx, st, srcs); err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	} else if n > 0 {
		fmt.Fprintf(stdout, "Added %d companies from the starter list.\n", n)
	}

	var opts core.RefreshOptions
	if len(names) > 0 {
		if opts.CompanyIDs, err = companyIDs(ctx, st, names); err != nil {
			fmt.Fprintln(stderr, "jhh:", err)
			return 1
		}
	}
	r := &core.Refresher{Store: st, Sources: srcs, Logger: logger, By: store.ActorUser}
	res, err := r.Refresh(ctx, opts)
	printRefresh(stdout, res)
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	return 0
}

func printRefresh(w io.Writer, res core.RefreshResult) {
	fmt.Fprintf(w, "Refreshed %d companies in %s (%d unchanged since the last refresh).\n",
		res.Companies, res.Duration.Round(100*time.Millisecond), res.NotModified)
	line := func(label string, v any) { fmt.Fprintf(w, "  %-16s %v\n", label, v) }
	line("postings listed", res.Postings)
	line("new postings", res.NewPostings)
	line("new jobs", len(res.NewJobs))
	line("closed jobs", len(res.ClosedJobs))
	line("reposted jobs", len(res.RepostedJobs))
	line("content flags", res.Flags)
	if res.Filtered {
		line("descriptions", fmt.Sprintf("%d fetched, %d failed, %d left for the next refresh", res.Details, res.DetailErrors, res.Deferred))
		line("new candidates", len(res.Passed))
		line("filtered out", len(res.FilteredOut))
	} else {
		fmt.Fprintln(w, "  No filters are set, so jobs weren't judged and Greenhouse descriptions weren't fetched.")
	}
	if len(res.Suspect) > 0 {
		slices.Sort(res.Suspect)
		line("suspect feeds", strings.Join(res.Suspect, ", "))
	}
	if len(res.Errors) > 0 {
		line("errors", len(res.Errors))
		slices.SortFunc(res.Errors, func(a, b store.FetchError) int { return strings.Compare(a.Company, b.Company) })
		for _, e := range res.Errors {
			fmt.Fprintf(w, "    %s: %s\n", e.Company, e.Error)
		}
	}
}

// companyIDs resolves company names or board keys.
func companyIDs(ctx context.Context, st *store.Store, names []string) ([]int64, error) {
	cs, err := st.Companies(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, n := range names {
		found := false
		for _, c := range cs {
			if strings.EqualFold(c.Name, n) || strings.EqualFold(c.BoardKey, n) {
				ids = append(ids, c.ID)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("no company named %q", n)
		}
	}
	return ids, nil
}

// probe finds which board a company uses: given a slug, it asks every
// source; given a posting URL, it says which source and posting it is.
func probe(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: jhh probe <board slug or posting URL>")
		return 2
	}
	srcs := builtin.All(sources.NewClient())
	names := make([]string, 0, len(srcs))
	for n := range srcs {
		names = append(names, n)
	}
	slices.Sort(names)

	arg := strings.TrimSpace(args[0])
	if u, err := url.Parse(arg); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		for _, n := range names {
			if ref, ok := srcs[n].MatchURL(u); ok {
				slug := ref.Slug
				if slug == "" {
					slug = "(board unknown from this URL)"
				}
				fmt.Fprintf(stdout, "%s posting %s on board %s\n", ref.Source, ref.ExternalID, slug)
				return 0
			}
		}
		fmt.Fprintln(stdout, "No supported board recognizes this URL.")
		return 1
	}

	slug := strings.ToLower(arg)
	if err := sources.ValidSlug(slug); err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 2
	}
	found := false
	for _, n := range names {
		count, err := srcs[n].Probe(ctx, slug)
		switch {
		case errors.Is(err, sources.ErrBoardNotFound):
			fmt.Fprintf(stdout, "%-11s %s: not found\n", n, slug)
		case err != nil:
			fmt.Fprintf(stdout, "%-11s %s: error: %v\n", n, slug, err)
		default:
			found = true
			fmt.Fprintf(stdout, "%-11s %s: %d open postings\n", n, slug, count)
		}
	}
	if !found {
		return 1
	}
	return 0
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
