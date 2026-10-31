# Roadmap

The MVP comes first (milestones M0–M5 in [design.md](design.md#12-implementation-plan)). Everything below waits until the MVP has been used on a real search, and that experience decides the order.

Once the repo is public, GitHub Issues become the source of truth and this file shrinks to a link to the project board.

## MVP (M0–M5)

- [x] M0 Scaffold
- [x] M1 Store
- [x] M2 Fetch
- [x] M3 Filter
- [x] M3.5 Search and storage
- [x] M4 MCP (start using it day to day)
- [x] M5 Window (MVP complete)

## Since the MVP

- [x] Hacker News's monthly "Who is hiring?" threads as a search source (`search.hackernews` in the filters)
- [x] Sources screen and tools: turn each search source on and off and change its settings from the dashboard or through MCP (`list_sources`, `set_source`), moving `search` out of the filters
- [x] Himalayas as a search source: remote jobs across employers, searched by the agent's queries, with an optional free key entered in the dashboard for higher limits
- [x] Finding outreach threads again: `list_outreach` (with one thread's history), `update_outreach` by person, outreach in `get_job` and the tracker summary
- [x] Fewer tools ([decision 0010](decisions/0010-fewer-tools.md)): `save_outreach`, archiving with `update_memory`, section edits with `update_profile`, `set_filters` dry runs, `set_company` (33 tools down to 28)
- [ ] Inbound outreach: who wrote first, received and declined statuses with a reason, the pay a recruiter quoted, an agency's client
- [ ] Interviews: `save_interview`, upcoming interviews in the tracker, a job moves to interviewing when its first interview is logged
- [ ] Handled email: links from threads, jobs and interviews to email threads, and a record of which messages the agent has already handled (`handled_messages`)
- [x] Contacts: organization type, warmth, tags and a date to get back in touch; `list_contacts` and `save_contact`
- [ ] Applications made elsewhere: `add_jobs` records a job the user applied to outside jhh, in its status, so a later reply has somewhere to go
- [ ] Later, once there are offers to compare: offer details (base, bonus, equity, sign-on, deadline) and a comparison across jobs

## Next (M6–M8)

### Setup and curation (M6)
- Editing screens for profile, memories, filters and companies
- Restoring a profile version from the UI
- First-run setup screen and Settings/Connect screen

### Self-improvement (M7)
- Filter version history, the guardrail on `set_filters`, restore
- `get_feedback_signals`
- `tune`, `update_profile` and `follow_up_review` playbooks

### Release (M8)
- `jhh serve` with streamable HTTP; MCP resources
- `jhh setup <client>`
- `add_jobs` reading schema.org `JobPosting` data from any URL (with protection against requests to the local network)
- Export (JSON) and backup command
- README, CONTRIBUTING, docs on adding a source or a company
- GitHub setup: branch protection, CI, security settings, issue and PR templates (design.md section 11)
- v0.1 release binaries

## Later

- FTS5 search over memories
- Mute controls (company, team, title pattern)
- Sources defined by config (Workable, Recruitee, SmartRecruiters, Rippling)
- Lever server-side parameters per company
- Workday source
- Finding boards automatically for employers search keeps finding: probe Greenhouse, Lever and Ashby, and add the board only when it lists the same job titles
- A maximum age for jobs from any source, as a filter
- External source plugins over stdin/stdout, if people ask
- Scheduled refresh and notifications
- Re-screening jobs when the profile changes
- Code signing and notarization for release binaries

## Not planned

- "Screen now" with the user's own API key in the UI (it would put an AI inside the window)
- LinkedIn scraping, auto-apply, any hosted service or telemetry
- Sources that cost money or need a paid plan, such as Adzuna, whose "free" key is 25 test calls ([decision 0008](decisions/0008-free-sources-only.md))
