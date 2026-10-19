# Job Hunt Harness

An open-source, local-first job search tool: an MCP server with a GUI. A job seeker connects their own AI agent (Claude Code, Cursor, or any other MCP client) to it. jhh finds and tracks jobs and stores everything the agent learns about the candidate. The agent does all the judgment: interviewing the user, writing the profile and filters, and screening jobs.

Status: the design is agreed and the MVP build is starting. **Read `docs/design.md` before writing code.** It is the spec. `docs/decisions/` records why the key decisions were made, and `docs/roadmap.md` holds everything after the MVP. If a change would contradict the design, raise it with the owner before writing code.

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

`reference/tracker.html` is the dashboard from the original personal version: the design to port in M5. Its inline script must become an external file to satisfy the strict Content Security Policy. Delete it once the UI is ported.

The original fetch and probe scripts were removed from the repo and its history once their logic was ported (`internal/sources`, `internal/prefilter`, `jhh probe`). The starter company list lives in `internal/registry/companies.json`.

## Lessons from the reference code

- **Ashby's `isRemote` is true for hybrid roles.** Check `workplaceType` (Hybrid or OnSite means not remote).
- **Some boards put the location in the title** (Grafana: "Staff AI Engineer | Canada | Remote").
- **Company + title is not a unique key.** Greenhouse's `internal_job_id` groups city duplicates, and different openings can share a title.
- **Greenhouse `absolute_url` often points at the company's own careers site** (with `gh_jid`). That's fine; it's the official apply link.
- **Greenhouse's job list without `content=true` is about 20× smaller.** Fetch descriptions only for jobs that pass stage 1.
- **All three feeds support ETag / 304**, so unchanged boards cost nothing to re-check.
- **Pay ranges come in many formats** (`$158,000 — $248,000`, `$231K – $340K`, `$156.6K`, several ranges for different locations, non-USD). Parse defensively and never guess.
- **Workday and custom career sites** have no simple public feed; the agent can add those jobs through `add_jobs`.
- **Injection rules must match text addressed to an AI, not text about AI.** Listings from AI companies say "as an AI Architect" and "system prompt" all the time. Check rule changes against a live refresh of the starter list, and add any false positive as a test in `internal/untrusted`. Greenhouse descriptions are only fetched once filters are set, so a live check needs `jhh filters set examples/filters.json` and a second refresh.
- **Hidden text stays out of the plain description.** It is kept in the raw HTML and the flag excerpt for review, so the agent screens what a human reader sees.
- **Prefilter reasons never quote the listing.** They name filter terms and jhh's own vocabulary (countries, states, regions), so they can sit outside the untrusted envelope. City names can be raw listing text, so reasons leave them out.
- **Never write invisible characters into source files.** Build them from code points (`string(rune(0x200b))`) so the repo itself is free of them.

## Working in this repo

- Line endings are LF (`.gitattributes`). Editors or scripts on Windows that write CRLF will show every line as changed.
- Tests never touch the network. Sources are tested against made-up fixtures through `internal/sources/conformance`. For a live check, run `jhh refresh` with `JHH_DATA_DIR` pointing at a scratch directory, never at real data.

## Privacy rules for this repo

- Never commit real user data: profiles, resumes, salary numbers, notes, contacts or tracker databases. They are in `.gitignore`. Tests use made-up fixtures only.
- Never log profile, resume, memory or description text. Logs contain IDs and counts.
- The owner's personal job search lives elsewhere and must not be copied here. That includes their location, target roles and pay, in code, docs, tests, examples or commit messages. The history was rewritten once to remove such details; anything committed is public.
