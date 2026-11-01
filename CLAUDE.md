# Job Hunt Harness

An open-source, local-first job search tool: an MCP server with a GUI. A job seeker connects their own AI agent (Claude Code, Cursor, or any other MCP client) to it. jhh finds and tracks jobs and stores everything the agent learns about the candidate. The agent does all the judgment: interviewing the user, writing the profile and filters, and screening jobs.

Status: the MVP (milestones M0–M5) is built: jhh works from an agent over MCP and has a local dashboard. Next it gets used on a real search; features after the MVP wait for that. **Read `docs/design.md` before writing code.** It is the spec. `docs/decisions/` records why the key decisions were made, and `docs/roadmap.md` holds everything after the MVP. If a change would contradict the design, raise it with the owner before writing code.

## Key decisions (details in docs/)

- **Go**, shipped as a single binary `jhh`. Module path `github.com/drewsokol/job-hunt-harness`. Official MCP Go SDK, `modernc.org/sqlite`, and the standard library for HTTP. Everything lives under `internal/`.
- **The server has no AI.** It fetches, stores, filters with rules and displays. Every capability is an MCP tool first, and the web UI is only a window onto the same data.
- **The agent launches jhh over stdio.** That process also serves the UI on `127.0.0.1:7878`. **Nothing may write to stdout except the MCP protocol**; logs go to stderr and a file.
- **The agent writes the profile, memories and filters through MCP**, and every change is versioned or logged with who made it. The data lives in jhh, so the user can switch agents.
- **The agent is also a source**: `add_jobs` adds jobs from anything the agent can read.
- **All times are UTC**: stored, sent and received. Only the UI converts, and only for display.
- **Listing content is untrusted.** It reaches the agent only inside an `untrusted` envelope. Prompt injection is flagged for the user to review (`canary`, `hostile`, `hidden_text`, `unclear`), never removed automatically.
- **Dedupe uses an identity ladder** (posting ID, board opening ID, canonical URL, description fingerprint, reposts), not company + title.
- **Runs locally.** One SQLite file, no hosted service, no telemetry. The project never holds anyone's resume, salary or search data.
- **Public company feeds only.** No LinkedIn scraping, no auto-apply.
- **MVP first** (milestones M0–M5). Features after the MVP wait until it has been used on a real search.
- **Open source, built in public.** Write code, commits and docs as public from day one.

## Reference code

The original personal version's scripts and dashboard were removed from the repo once ported: fetch and probe into `internal/sources`, `internal/prefilter` and `jhh probe`, and the tracker page into `internal/web`. There is no starter company list: each user's companies come from their own search and choices.

## Lessons from the reference code

- **Ashby's `isRemote` is true for hybrid roles.** Check `workplaceType` (Hybrid or OnSite means not remote).
- **Some boards put the location in the title** (Grafana: "Staff AI Engineer | Canada | Remote").
- **Company + title is not a unique key.** Greenhouse's `internal_job_id` groups city duplicates, and different openings can share a title.
- **Greenhouse `absolute_url` often points at the company's own careers site** (with `gh_jid`). That's fine; it's the official apply link.
- **Greenhouse's job list without `content=true` is about 20× smaller.** Fetch descriptions only for jobs that pass stage 1.
- **All three feeds support ETag / 304**, so unchanged boards cost nothing to re-check.
- **Pay ranges come in many formats** (`$158,000 — $248,000`, `$231K – $340K`, `$156.6K`, several ranges for different locations, non-USD). Parse defensively and never guess.
- **Workday and custom career sites** have no simple public feed; the agent can add those jobs through `add_jobs`.
- **The Muse's location filter also returns every remote job**, its results aren't in date order (new jobs land on any page), and it keeps listings up for years. So jhh reads every page of a query and skips results over 90 days old. An unknown category returns nothing instead of an error, so names are checked against `themuse.Categories`.
- **Himalayas's search takes one `country`** ("US,CA" is a 400), matches `q` against descriptions too (a bare "engineer" hits its 5,000-result cap), and caches replies for an hour at its edge. `pubDate` is the last update and `expiryDate` is always 60 days after it. A single job is `/jobs/api/companies/<company>/jobs/<slug>`: 410 when closed, 404 when gone. Keyless requests share one public allowance (429 when it's used up); a key returns 401 once refused, and jhh then carries on without it.
- **Keys never pass through the agent.** They are typed on the dashboard, kept in `keys.json` (not the database), and nothing returns them: the API, tools, logs and events only say whether one is saved.
- **The Muse's CloudFront blocks curl's default user agent** (403, which is also its rate-limit answer). Send jhh's user agent when checking it by hand.
- **Hacker News ads are free text.** The `whoishiring` account also posts "Who wants to be hired?" and freelancer threads, so threads are picked by title. `items/<id>` on the HN Search API returns a whole thread in one request. Header parsing in `internal/sources/hackernews` was tuned against a real month's thread; when changing it, check it the same way on a scratch data dir (Hacker News is on unless `jhh sources off hackernews`; set any filters, then `jhh refresh` and `jhh candidates`), and add any misread header, made up, to `TestParseAd`.
- **Injection rules must match text addressed to an AI, not text about AI.** Listings from AI companies say "as an AI Architect" and "system prompt" all the time. Check rule changes against a live refresh of a few dozen public boards (added with `jhh companies add` to a scratch data dir; keep the list outside the repo), and add any false positive as a test in `internal/untrusted`. Greenhouse descriptions are only fetched once filters are set, so a live check needs `jhh filters set examples/filters.json` and a second refresh.
- **Hidden text stays out of the plain description.** It is kept in the raw HTML and the flag excerpt for review, so the agent screens what a human reader sees.
- **Prefilter reasons never quote the listing.** They name filter terms and jhh's own vocabulary (countries, states, regions), so they can sit outside the untrusted envelope. City names can be raw listing text, so reasons leave them out.
- **Never write invisible characters into source files.** Build them from code points (`string(rune(0x200b))`) so the repo itself is free of them.

## Working in this repo

- New features start as specs in `docs/specs/` ([README](docs/specs/README.md)). The spec review workflow comments with questions on a spec PR; an owner's `/review` comment runs it again.
- Releases come from the Release workflow, which an owner runs with a version (`gh workflow run release.yml -f version=v0.2.0`); it tags main and GoReleaser builds the binaries (`.goreleaser.yaml`). Never tag by hand.
- Line endings are LF (`.gitattributes`). Editors or scripts on Windows that write CRLF will show every line as changed.
- Tests never touch the network. Sources are tested against made-up fixtures through `internal/sources/conformance`. For a live check, run `jhh refresh` with `JHH_DATA_DIR` pointing at a scratch directory, never at real data.
- Tests that start jhh set `JHH_UI_PORT=0`, so they never take the real dashboard's port.
- The dashboard page never parses HTML: it builds elements with its `h` helper, which makes text nodes only, and a test fails on `innerHTML` and the like. Keep it that way; listing text is untrusted.
- To check the dashboard by eye, run it on a scratch database with made-up data and use headless Edge or Chrome. `--screenshot` never finishes while the page holds its event stream, so pass `--timeout=5000`, or drive the browser over the DevTools protocol to click and type.

## Privacy rules for this repo

- Never commit real user data: profiles, resumes, salary numbers, notes, contacts or tracker databases. They are in `.gitignore`. Tests use made-up fixtures only.
- Never log profile, resume, memory or description text. Logs contain IDs and counts.
- The owner's personal job search lives elsewhere and must not be copied here. That includes their location, target roles and pay, in code, docs, tests, examples or commit messages. The history was rewritten once to remove such details; anything committed is public.
- The privacy scan (`internal/tools/privacyscan`) checks every file for user-data files, invisible characters, local Windows user paths and real email addresses; CI runs it on every PR. The owner's private terms never leave their machine: not the repo, not CI, not a secret. Before every push, run the scan with them, from a file outside the repo, on the lines you add and your commit messages: `go run ./internal/tools/privacyscan -terms-file <file> -commits origin/main..HEAD`, which checks each commit's own changes. `scripts/pre-push` does this as a git hook.
