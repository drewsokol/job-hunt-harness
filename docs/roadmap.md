# Roadmap

The MVP comes first (milestones M0–M5 in [design.md](design.md#12-implementation-plan)). Everything below waits until the MVP has been used on a real search, and that experience decides the order.

Once the repo is public, GitHub Issues become the source of truth and this file shrinks to a link to the project board.

## MVP (M0–M5)

- [x] M0 Scaffold
- [x] M1 Store
- [x] M2 Fetch
- [x] M3 Filter
- [x] M3.5 Search and storage
- [ ] M4 MCP (start using it day to day)
- [ ] M5 Window (MVP complete)

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
- Broader starter company list
- README, CONTRIBUTING, docs on adding a source or a company
- GitHub setup: branch protection, CI, security settings, issue and PR templates (design.md section 11)
- v0.1 release binaries

## Later

- FTS5 search over memories
- Mute controls (company, team, title pattern)
- Sources defined by config (Workable, Recruitee, SmartRecruiters, Rippling)
- Lever server-side parameters per company
- Workday source
- Connect search services that need an account and key (Adzuna and others), with the key kept on the user's machine
- Finding boards automatically for employers search keeps finding: probe Greenhouse, Lever and Ashby, and add the board only when it lists the same job titles
- A maximum age for jobs from any source, as a filter
- External source plugins over stdin/stdout, if people ask
- Scheduled refresh and notifications
- Re-screening jobs when the profile changes
- Code signing and notarization for release binaries

## Not planned

- "Screen now" with the user's own API key in the UI (it would put an AI inside the window)
- LinkedIn scraping, auto-apply, any hosted service or telemetry
