// Command jhh is Job Hunt Harness: an MCP server that turns your AI agent
// into a job search assistant, with a local dashboard.
//
// Usage:
//
//	jhh            run as an MCP server over stdio (what your agent launches)
//	jhh refresh    fetch job boards and run searches now
//	jhh sources    list where jhh looks for jobs, or turn a search source on or off
//	jhh companies  list companies, or add one with its job board
//	jhh probe      find which job board a company uses
//	jhh candidates list the jobs that passed the filters
//	jhh dashboard  open the dashboard in the browser
//	jhh filters    show, preview or set the filters
//	jhh doctor     check the installation and data directory
//	jhh version    print the version
//	jhh help       show this help
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/drewsokol/job-hunt-harness/internal/buildinfo"
	"github.com/drewsokol/job-hunt-harness/internal/datadir"
	"github.com/drewsokol/job-hunt-harness/internal/logging"
	"github.com/drewsokol/job-hunt-harness/internal/mcpserver"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/sources/builtin"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/web"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const usage = `jhh: Job Hunt Harness

Usage:
  jhh            run as an MCP server over stdio (what your agent launches)
  jhh refresh    fetch job boards and run searches now [--company NAME ...]
  jhh sources    list where jhh looks for jobs; jhh sources on|off|set <name> (usually your agent does this)
  jhh companies  list companies; jhh companies add <name> [slug or posting URL]
  jhh probe      find which job board a company uses: jhh probe <slug or posting URL>
  jhh candidates list the jobs that passed the filters [--status STATUS] [--limit N]
  jhh dashboard  open the dashboard in the browser (serves it until Ctrl+C if no agent does)
  jhh filters    show the filters; jhh filters preview|set <file> (usually your agent does this)
  jhh doctor     check the installation and data directory
  jhh version    print the version
  jhh help       show this help

Register jhh with your agent once, for example in Claude Code:
  claude mcp add jhh -- jhh

Environment:
  JHH_DATA_DIR   where jhh keeps its data (default: a per-user directory)
  JHH_LOG_LEVEL  debug, info, warn or error (default: info)
  JHH_UI_PORT    the dashboard's port on 127.0.0.1 (default: 7878)
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is main with its inputs passed in, so tests can drive it.
func run(ctx context.Context, args []string, stdin io.ReadCloser, stdout io.WriteCloser, stderr *os.File) int {
	if len(args) == 0 {
		return serveStdio(ctx, stdin, stdout, stderr)
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, "jhh", buildinfo.Version())
		return 0
	case "doctor":
		return doctor(ctx, stdout)
	case "refresh":
		return refresh(ctx, args[1:], stdout, stderr)
	case "companies":
		return companies(ctx, args[1:], stdout, stderr)
	case "sources":
		return sourcesCmd(ctx, args[1:], stdout, stderr)
	case "probe":
		return probe(ctx, args[1:], stdout, stderr)
	case "dashboard":
		return dashboard(ctx, stdout, stderr)
	case "candidates":
		return candidates(ctx, args[1:], stdout, stderr)
	case "filters":
		return filters(ctx, args[1:], stdin, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "jhh: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// afterStdoutRedirect runs once os.Stdout points at stderr. Tests replace it
// to print where a stray print would, proving the protocol stream stays clean.
var afterStdoutRedirect = func() {}

// serveStdio runs the MCP server on stdin and stdout until the client
// disconnects or the process is interrupted.
func serveStdio(ctx context.Context, stdin io.ReadCloser, stdout io.WriteCloser, stderr *os.File) int {
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

	// stdout belongs to the MCP protocol. Point os.Stdout at stderr, so a
	// stray print anywhere in jhh or its dependencies ends up in the logs
	// instead of corrupting the protocol stream.
	prevStdout := os.Stdout
	os.Stdout = stderr
	defer func() { os.Stdout = prevStdout }()
	afterStdoutRedirect()

	logger.Info("starting", "mode", "stdio", "version", buildinfo.Version())
	st, err := openStore(ctx, dir)
	if err != nil {
		logger.Error("opening database", "err", err)
		return 1
	}
	defer st.Close()
	client := sources.NewClient()
	srcs := builtin.All(client)
	// The dashboard is served alongside, unless another jhh already serves it.
	port, err := web.Port()
	if err != nil {
		logger.Error("starting the dashboard", "err", err)
		return 1
	}
	dash := web.NewDashboard(ctx, web.Deps{Store: st, Logger: logger, Sources: srcs, Instance: web.Instance(dir.DB())}, port)
	if err := dash.Start(); err != nil {
		logger.Warn("not serving the dashboard", "err", err)
	}
	defer dash.Close()
	srv := mcpserver.New(mcpserver.Deps{Store: st, Logger: logger, Sources: srcs, Searchers: builtin.Searchers(client),
		Dashboard: dash, OpenBrowser: web.OpenBrowser})
	err = srv.Run(ctx, &mcp.IOTransport{Reader: stdin, Writer: stdout})
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		logger.Error("server stopped", "err", err)
		return 1
	}
	logger.Info("stopped")
	return 0
}

// openStore opens the database in dir, migrating it if needed.
func openStore(ctx context.Context, dir datadir.Dir) (*store.Store, error) {
	return store.Open(ctx, dir.DB(), store.Options{BackupDir: dir.Backups()})
}

// doctor checks that jhh can run here and reports where its data lives.
func doctor(ctx context.Context, out io.Writer) int {
	failed := false
	report := func(ok bool, check, detail string) {
		status := "ok  "
		if !ok {
			status = "FAIL"
			failed = true
		}
		fmt.Fprintf(out, "%s  %-10s %s\n", status, check, detail)
	}

	report(true, "version", buildinfo.Version())
	dir, err := datadir.Resolve()
	if err != nil {
		report(false, "data dir", err.Error())
		return 1
	}
	if err := dir.Ensure(); err != nil {
		report(false, "data dir", err.Error())
		return 1
	}
	if err := checkWritable(dir.Root); err != nil {
		report(false, "data dir", fmt.Sprintf("%s is not writable: %v", dir.Root, err))
	} else {
		report(true, "data dir", dir.Root)
	}
	if _, err := os.Stat(dir.DB()); errors.Is(err, os.ErrNotExist) {
		report(true, "database", dir.DB()+" (not created yet)")
	} else if err != nil {
		report(false, "database", err.Error())
	} else if st, err := openStore(ctx, dir); err != nil {
		report(false, "database", err.Error())
	} else {
		defer st.Close()
		version, err := st.SchemaVersion(ctx)
		if err == nil {
			err = st.Check(ctx)
		}
		if err != nil {
			report(false, "database", err.Error())
		} else {
			report(true, "database", fmt.Sprintf("%s (schema v%d, integrity ok)", dir.DB(), version))
		}
	}
	report(true, "logs", filepath.Join(dir.Logs(), "jhh.log"))

	if failed {
		return 1
	}
	return 0
}

func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
