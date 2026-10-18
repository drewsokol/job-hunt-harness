# Job Hunt Harness

An MCP server that turns your AI agent into a job search assistant, with a local dashboard.

jhh pulls open roles from the public job boards of the companies you care about, filters out the obvious misses with simple rules, and tracks your applications and outreach. Your own agent (Claude Code, Cursor or any MCP client) does the judgment: it interviews you, writes your profile and filters, and screens each role for fit. Everything stays on your machine, in one SQLite file.

**Status: early development.** The design is in [docs/design.md](docs/design.md), and progress is tracked in [docs/roadmap.md](docs/roadmap.md). Nothing is ready to use yet.

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
JHH_DATA_DIR=./tmp-data go run ./cmd/jhh refresh            # fetch the starter companies' boards
JHH_DATA_DIR=./tmp-data go run ./cmd/jhh probe exampleco    # which board does a company use?
```

Tests never touch the network: each source is tested against made-up fixtures by the shared conformance suite in `internal/sources/conformance`.

Never commit real profiles, resumes, salary numbers, contacts or databases. Test fixtures are always made up.
