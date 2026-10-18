# Job Hunt Harness

An open-source, local-first job search harness: a small web server plus an MCP server. A job seeker connects their own AI agent (Claude Code, Cursor, or any other MCP client) to it. The server finds and tracks jobs. The agent screens them against a profile the user writes in plain English.

Status: design stage. Nothing beyond `reference/` exists yet. **Agree on the design with the owner before writing code.** They want to review the plan first.

## Origin

A personal version was built in one afternoon and works:
- `reference/fetch_jobs.py` pulls public ATS feeds (Greenhouse, Lever, Ashby) for a list of companies, applies a keyword prefilter (level, role, keywords, location), dedupes, and writes candidates for an agent to screen. `seen.json` keeps re-runs to new postings only.
- `reference/probe_ats.py` finds which ATS (and board slug) a company uses.
- `reference/companies.json` lists 91 companies with verified feeds.
- `reference/tracker.html` is the dashboard (a claude.ai artifact backed by a document DB). It has To review, Applied, Outreach and Archive tabs; fit pills with reason and gaps; an expandable description; per-job notes; mark-applied with an editable date; status changes; an outreach log with follow-up dates; and a suggested salary ask computed from the posted range.

That flow worked well: fetch about 12k roles, prefilter to about 90, have the agent screen them down to about 20 with a fit rating, reason and gaps, track applications and outreach. The project is to generalize it so anyone can run it.

## Decisions made so far

- **The server has no AI of its own.** It fetches, stores and displays. The user's own agent does screening and profile parsing through MCP. Optionally, users with no agent can paste their own API key to get a "Screen now" button in the web UI.
- **No "log in with Claude."** Anthropic doesn't let third-party apps offer Claude.ai login or use people's subscriptions. Products use API keys. Verify against the current Agent SDK and consumer terms before relying on anything here.
- **Runs locally.** One process, SQLite, a web UI on localhost, MCP over stdio and/or streamable HTTP. No hosted service, so the project never holds anyone's resume, salary or job search data.
- **Company feeds only.** No LinkedIn scraping.
- **Open source, built in public.** The owner plans to publish the repo now, so write code, commits and docs as public from day one.

## Proposed architecture

- **Sources as plugins:** a common interface (`list_jobs(company) -> [Job]`). Greenhouse, Lever and Ashby first, then manual entry from a URL. Workday later; it's much harder.
- **Profile:** plain-English markdown written by the user (target roles, level, location, pay floor, exclusions, gaps, and claims they can or can't make), plus a structured filter the agent derives from it and the user reviews. The plain-English text stays the screening rubric.
- **Prefilter:** cheap rules driven by the structured profile, to cut volume before the agent sees anything.
- **Tracker:** jobs, applications, a timeline of events, contacts and outreach, and notes.
- **MCP surface (draft):**
  - Tools: `refresh_jobs`, `list_candidates`, `get_job`, `record_screening(job_id, fit, reason, gaps)`, `update_application`, `log_outreach`, `add_job_from_url`, `get_profile`, `update_profile`, `add_company` (with probing)
  - A prompt, something like "screen new jobs", that holds the screening instructions
  - The profile exposed as a resource
- **Web UI:** To review, Applied, Outreach and Archive, plus a profile editor and company list management.

## Proposed first version

Greenhouse, Lever and Ashby sources; plain-English profile with derived filters; MCP tools plus the screening prompt; web UI with To review, Applied and Outreach; a starter company list. Most of the logic exists in `reference/`, so this is mainly a port into a proper server.

## Known hard parts and lessons from the reference code

- **Finding companies:** feeds only cover companies you list. Plan to ship a curated company registry, the probe tool, and a way for users to contribute companies.
- **Ashby's `isRemote` is true for hybrid roles.** Check `workplaceType` (Hybrid or OnSite means not remote).
- **Some boards put the location in the title** (Grafana: "Staff AI Engineer | Canada | Remote").
- **The same role is often posted once per city.** Dedupe on company plus normalized title, and keep the extra IDs so they're also marked seen.
- **Greenhouse `absolute_url` often points at the company's own careers site.** That's fine; it's the official apply link.
- **Pay ranges come in many formats** (`$158,000 — $248,000`, `$231K – $340K`, `$156.6K`, several ranges for different locations, non-USD). Parse defensively.
- **Workday and custom career sites** (common at large companies) have no simple public feed.

## Privacy rules for this repo

- Never commit real user data: profiles, resumes, salary numbers, notes, contacts or tracker databases. Add them to `.gitignore`, and use made-up fixtures in tests.
- The owner's personal job search lives elsewhere and must not be copied here.
