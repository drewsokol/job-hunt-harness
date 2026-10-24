package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/datadir"
	"github.com/drewsokol/job-hunt-harness/internal/keys"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/sources/builtin"
	"github.com/drewsokol/job-hunt-harness/internal/store"
)

const sourcesUsage = `usage:
  jhh sources                  list where jhh looks for jobs, and whether each is on
  jhh sources on|off <name>    turn a search source on or pause it
  jhh sources set <name> <file>
                               replace a search source's settings with a JSON file
                               (examples/themuse.json and examples/himalayas.json show the queries)
Usually your agent does this. Company boards are turned on and off per company.`

// maxSettingsFile caps a settings file.
const maxSettingsFile = 64 << 10

// sourcesCmd lists the sources or changes a search source.
func sourcesCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var u core.SourceUpdate
	switch {
	case len(args) == 0:
	case len(args) == 2 && (args[0] == "on" || args[0] == "off"):
		on := args[0] == "on"
		u.Enabled = &on
	case len(args) == 3 && args[0] == "set":
		b, err := readSettings(args[2])
		if err != nil {
			fmt.Fprintln(stderr, "jhh:", err)
			return 1
		}
		u.Settings = b
	default:
		fmt.Fprintln(stderr, sourcesUsage)
		return 2
	}
	st, code := openData(ctx, stderr)
	if st == nil {
		return code
	}
	defer st.Close()
	if len(args) == 0 {
		ss, err := core.ListSources(ctx, st, slices.Collect(maps.Keys(builtin.All(sources.NewClient()))))
		if err != nil {
			fmt.Fprintln(stderr, "jhh:", err)
			return 1
		}
		markKeys(ss)
		printSources(stdout, ss)
		return 0
	}
	info, err := core.SetSource(ctx, st, store.ActorUser, args[1], u)
	var in *core.InputError
	if errors.As(err, &in) {
		for _, p := range in.Problems {
			fmt.Fprintln(stderr, "jhh:", p)
		}
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	ss := []core.SourceInfo{info}
	markKeys(ss)
	printSources(stdout, ss)
	return 0
}

// markKeys notes which optional keys are saved. Keys are entered only on
// the dashboard, never here.
func markKeys(ss []core.SourceInfo) {
	if dir, err := datadir.Resolve(); err == nil {
		core.MarkKeys(ss, keys.Open(dir.Keys()))
	}
}

func readSettings(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSettingsFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSettingsFile {
		return nil, fmt.Errorf("%s: larger than %d KB", path, maxSettingsFile>>10)
	}
	return b, nil
}

func printSources(w io.Writer, ss []core.SourceInfo) {
	for _, s := range ss {
		if s.Kind == core.KindBoard {
			fmt.Fprintf(w, "%-12s board        %d companies, %d read\n", s.Name, s.Companies, s.EnabledCompanies)
			continue
		}
		fmt.Fprintf(w, "%-12s %-12s", s.Name, s.Status)
		if len(s.Settings) > 0 {
			fmt.Fprintf(w, " %s", s.Settings)
		}
		fmt.Fprintln(w)
		if s.Setup != "" {
			fmt.Fprintf(w, "             %s\n", s.Setup)
		}
		if !s.LastRunAt.IsZero() {
			fmt.Fprintf(w, "             last ran %s: %d postings\n", s.LastRunAt.Format("2006-01-02 15:04 UTC"), s.LastPostings)
		}
		if s.LastError != "" {
			fmt.Fprintf(w, "             last error: %s\n", oneLine(s.LastError))
		}
		switch {
		case s.TakesKey && s.KeySaved && s.KeyRefused:
			fmt.Fprintln(w, "             key: saved, but refused last time, so it searches without one; replace it on the dashboard")
		case s.TakesKey && s.KeySaved:
			fmt.Fprintln(w, "             key: saved")
		case s.TakesKey:
			fmt.Fprintln(w, "             key: none (optional; a free key raises the limits; enter it on the dashboard's Sources tab)")
		}
	}
}
