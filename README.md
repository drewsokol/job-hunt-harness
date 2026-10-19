# Job Hunt Harness

An MCP server that turns your AI agent into a job search assistant, with a local dashboard.

jhh pulls open roles from the public job boards of the companies you care about, and searches The Muse's public jobs API for roles at companies you didn't know to list. It filters out the obvious misses with simple rules, and tracks your applications and outreach. Your own agent (Claude Code, Cursor or any MCP client) does the judgment: it interviews you, writes your profile and filters, and screens each role for fit. Everything stays on your machine, in one SQLite file.

**Status: early development.** jhh works from your agent; the dashboard is next. The design is in [docs/design.md](docs/design.md), and progress is tracked in [docs/roadmap.md](docs/roadmap.md).

## Connect your agent

Build jhh (see Development below), then add it to your agent as an MCP server that runs the `jhh` binary with no arguments. In Claude Code:

```sh
claude mcp add jhh -- /path/to/jhh
```

Other MCP clients take the same command in their config:

```json
{ "mcpServers": { "jhh": { "command": "/path/to/jhh" } } }
```

Then ask your agent to "initialize job hunt harness". It reads your resume if you point it at one, interviews you, writes your profile and filters with your OK, suggests companies, fetches jobs and screens a first batch with you. After that, "what's new?" is the daily check-in, and "screen new jobs" rates whatever the last refresh found.

Your data lives in one SQLite file in your user data directory (`jhh doctor` shows where). Set `JHH_DATA_DIR` to put it somewhere else.

## Development

Requires Go (the version in `go.mod`). Everything else comes through Go itself.

```sh
go build ./cmd/jhh          # build the jhh binary
go test ./...               # run the tests
go vet ./...                # vet
go tool staticcheck ./...   # lint
gofmt -l .                  # list files that need formatting
```

To keep your real data separate while developing, point jhh at a scratch directory with `JHH_DATA_DIR`:

```sh
JHH_DATA_DIR=./tmp-data go run ./cmd/jhh doctor
JHH_DATA_DIR=./tmp-data go run ./cmd/jhh probe exampleco    # which board does a company use?
JHH_DATA_DIR=./tmp-data go run ./cmd/jhh companies add "Example Co"   # find its board and start reading it
JHH_DATA_DIR=./tmp-data go run ./cmd/jhh refresh            # fetch the boards you added
JHH_DATA_DIR=./tmp-data go run ./cmd/jhh filters preview examples/filters.json   # what would these filters keep?
JHH_DATA_DIR=./tmp-data go run ./cmd/jhh filters set examples/filters.json --rationale "trying the example"
JHH_DATA_DIR=./tmp-data go run ./cmd/jhh refresh            # runs the filters' Muse search; fetches descriptions for jobs that passed stage 1
JHH_DATA_DIR=./tmp-data go run ./cmd/jhh candidates         # the jobs that passed, best first
```

jhh ships no list of companies: yours starts empty and grows from what search finds and what you add. Normally your agent writes the filters through MCP; the `filters` command is for checking them by hand. `examples/` holds made-up examples.

Tests never touch the network: each source is tested against made-up fixtures by the shared conformance suite in `internal/sources/conformance`.

Never commit real profiles, resumes, salary numbers, contacts or databases. Test fixtures are always made up.
