package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/datadir"
	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/prefilter"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

const filtersUsage = `usage:
  jhh filters                                   show the active filters
  jhh filters preview <file>                    show what a filters file would change
  jhh filters set <file> --rationale "why"      save it and re-judge the stored jobs
<file> is a JSON filters document (see examples/filters.json), or - for stdin.
Usually your agent writes the filters; these commands are for checking them.`

// filters shows, previews or sets the prefilter rules.
func filters(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	sub := "show"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("filters "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	rationale := fs.String("rationale", "", "why the filters are changing (required for set)")
	// Flags may come before or after the file.
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		rest, args = append(rest, fs.Arg(0)), fs.Args()[1:]
	}

	var doc []byte
	switch sub {
	case "show":
		if len(rest) != 0 {
			fmt.Fprintln(stderr, filtersUsage)
			return 2
		}
	case "preview", "set":
		if len(rest) != 1 || (sub == "set" && strings.TrimSpace(*rationale) == "") {
			fmt.Fprintln(stderr, filtersUsage)
			return 2
		}
		var err error
		if rest[0] == "-" {
			doc, err = io.ReadAll(io.LimitReader(stdin, 1<<20))
		} else {
			doc, err = os.ReadFile(rest[0])
		}
		if err != nil {
			fmt.Fprintln(stderr, "jhh:", err)
			return 1
		}
	default:
		fmt.Fprintln(stderr, filtersUsage)
		return 2
	}

	st, code := openData(ctx, stderr)
	if st == nil {
		return code
	}
	defer st.Close()

	if sub == "show" {
		v, ok, err := st.ActiveFilters(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "jhh:", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "No filters are set. Ask your agent to set them up, or use jhh filters set.")
			return 0
		}
		var pretty bytes.Buffer
		_ = json.Indent(&pretty, v.Filters, "", "  ")
		fmt.Fprintf(stdout, "Filters version %d, set by %s at %s.\nRationale: %s\n\n%s\n",
			v.Version, v.CreatedBy, utc.Format(v.CreatedAt), oneLine(v.Rationale), pretty.String())
		return 0
	}

	f, err := prefilter.Parse(doc)
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	if sub == "preview" {
		p, err := core.PreviewFilters(ctx, st, f)
		if err != nil {
			fmt.Fprintln(stderr, "jhh:", err)
			return 1
		}
		printPreview(stdout, p)
		return 0
	}
	version, p, out, err := core.SetFilters(ctx, st, store.ActorUser, f, strings.TrimSpace(*rationale))
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	printPreview(stdout, p)
	fmt.Fprintf(stdout, "\nSaved filters version %d. %d jobs became candidates, %d were filtered out, and %d are waiting for descriptions.\n",
		version, len(out.Passed), len(out.FilteredOut), p.PendingAfter)
	if p.PendingAfter > 0 {
		fmt.Fprintln(stdout, "Run jhh refresh to fetch the descriptions they need.")
	}
	return 0
}

func printPreview(w io.Writer, p core.FilterPreview) {
	fmt.Fprintf(w, "Judged %d open jobs.\n", p.Jobs)
	line := func(label string, v int) { fmt.Fprintf(w, "  %-26s %d\n", label, v) }
	line("pass now", p.PassNow)
	line("pass after", p.PassAfter)
	line("waiting for descriptions", p.PendingAfter)
	line("newly dropped", p.NewlyDropped)
	line("newly kept", p.NewlyKept)
	if p.DropsTracked > 0 {
		fmt.Fprintf(w, "  WARNING: %d newly dropped jobs are ones you're reviewing or applying to.\n", p.DropsTracked)
	}
	examples := func(label string, js []core.PreviewJob) {
		if len(js) == 0 {
			return
		}
		fmt.Fprintf(w, "%s:\n", label)
		for _, j := range js {
			fmt.Fprintf(w, "  #%d %s: %s\n", j.ID, oneLine(j.Company), oneLine(j.Title))
			for _, r := range j.Reasons {
				if r.Effect == prefilter.EffectDrop {
					fmt.Fprintf(w, "      %s: %s\n", r.Rule, r.Text)
				}
			}
		}
	}
	examples("Newly kept, for example", p.KeptExamples)
	examples("Newly dropped, for example", p.DroppedExamples)
}

// candidates lists jobs the prefilter passed, best first, or the jobs in
// another status with the reasons they're there.
func candidates(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("candidates", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("limit", 30, "how many jobs to show")
	status := fs.String("status", string(store.StatusUnscreened), "which jobs: unscreened, filtered_out, new, review, ...")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *limit < 1 || !store.JobStatus(*status).Valid() {
		fmt.Fprintln(stderr, "usage: jhh candidates [--limit N] [--status STATUS]")
		return 2
	}
	st, code := openData(ctx, stderr)
	if st == nil {
		return code
	}
	defer st.Close()

	counts, err := st.StatusCounts(ctx)
	if err == nil {
		var cs []store.Candidate
		if cs, err = st.Candidates(ctx, store.JobStatus(*status), *limit); err == nil {
			printCandidates(ctx, st, stdout, store.JobStatus(*status), cs, counts)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	return 0
}

func printCandidates(ctx context.Context, st *store.Store, w io.Writer, status store.JobStatus, cs []store.Candidate, counts map[store.JobStatus]int) {
	if _, ok, _ := st.ActiveFilters(ctx); !ok && status == store.StatusUnscreened {
		fmt.Fprintf(w, "No filters are set, so none of the %d open jobs have been judged yet.\n", counts[store.StatusNew])
		fmt.Fprintln(w, "Ask your agent to set up filters, or use jhh filters set.")
		return
	}
	fmt.Fprintf(w, "%d open jobs are %s", counts[status], status)
	if len(cs) < counts[status] {
		fmt.Fprintf(w, "; showing the top %d", len(cs))
	}
	fmt.Fprintln(w, ".")
	if status == store.StatusUnscreened {
		fmt.Fprintf(w, "%d more are waiting for descriptions (run jhh refresh) and %d were filtered out (--status filtered_out).\n",
			counts[store.StatusNew], counts[store.StatusFilteredOut])
	}
	for _, c := range cs {
		fmt.Fprintf(w, "\n#%-6d %4.1f  %s: %s\n", c.ID, c.Score, oneLine(c.Company), oneLine(c.Title))
		details := []string{where(c.Workplace, core.Places(c.Locations))}
		if pay := payText(core.PayRanges(c.PayRanges)); pay != "" {
			details = append(details, pay)
		}
		var reasons []prefilter.Reason
		_ = json.Unmarshal(c.Reasons, &reasons)
		for _, r := range reasons {
			if r.Effect == prefilter.EffectFlag || (r.Effect == prefilter.EffectDrop && status == store.StatusFilteredOut) {
				details = append(details, r.Effect+": "+r.Text)
			}
		}
		fmt.Fprintf(w, "         %s\n", strings.Join(details, " · "))
		if c.URL != "" {
			fmt.Fprintf(w, "         %s\n", oneLine(c.URL))
		}
	}
}

// where describes a job's workplace and places: "remote: US; hybrid…".
func where(workplace string, places []normalize.Place) string {
	var names []string
	for i, p := range places {
		if i == 3 {
			names = append(names, fmt.Sprintf("%d more", len(places)-3))
			break
		}
		names = append(names, oneLine(p.String()))
	}
	if len(names) == 0 {
		return workplace
	}
	return workplace + ": " + strings.Join(names, "; ")
}

// payText formats the first pay range: "$180,000–$220,000 a year".
func payText(rs []normalize.PayRange) string {
	if len(rs) == 0 {
		return ""
	}
	r := rs[0]
	amount := func(v float64) string {
		s := strconv.FormatFloat(v, 'f', -1, 64)
		if v >= 1000 && v == float64(int64(v)) {
			s = strconv.FormatInt(int64(v), 10)
			for i := len(s) - 3; i > 0; i -= 3 {
				s = s[:i] + "," + s[i:]
			}
		}
		switch r.Currency {
		case "USD":
			return "$" + s
		case "EUR":
			return "€" + s
		case "GBP":
			return "£" + s
		}
		return s + " " + r.Currency
	}
	s := amount(r.Min)
	if r.Max != r.Min {
		s += "–" + amount(r.Max)
	}
	switch r.Period {
	case "hour":
		s += " an hour"
	case "":
	default:
		s += " a " + r.Period
	}
	switch n := len(rs) - 1; {
	case n == 1:
		s += " (+1 more range)"
	case n > 1:
		s += fmt.Sprintf(" (+%d more ranges)", n)
	}
	return s
}

// oneLine makes listing text safe to print on one terminal line: control
// characters, which a listing could use to rewrite the terminal, become
// spaces. untrusted.Clean already removes them; this is the last guard.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// openData opens the data directory's database for a command. On failure
// it reports the error and returns a nil store and the exit code.
func openData(ctx context.Context, stderr io.Writer) (*store.Store, int) {
	dir, err := datadir.Resolve()
	if err == nil {
		err = dir.Ensure()
	}
	var st *store.Store
	if err == nil {
		st, err = openStore(ctx, dir)
	}
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return nil, 1
	}
	return st, 0
}
