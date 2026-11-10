# 0012: Retire The Muse after its API closed

Status: Accepted, 2026-11-10.

## Context

The Muse's public jobs API was jhh's first search source (M3.5). Around 2026-11-09 it stopped working: every endpoint (`/api/public/jobs`, single jobs, companies, categories, and `/api/v2/jobs`) answered 404 with the site's "not found" page, whatever the user agent, and the API documentation page was gone. There was no announcement. Other projects that read the API failed from the same morning, so this was the API being removed, not an outage or a block on jhh.

Refresh reported it as `the muse: board not found`. That message comes from the HTTP client, which reads any 404 as a missing company board. For a search source it sounds like a settings mistake, so a user would go looking for a typo in their queries.

The site's search pages still list jobs, built on the server. Reading them would mean scraping HTML, losing the fields jhh used (levels, full descriptions), and working against the site's blocking of automated requests.

## Decision

- **The Muse is retired, and its code removed.** jhh never reads it. `set_source` refuses to set it up or turn it on, and says why. The `QuerySearcher` interface, which only The Muse used, goes with it.
- **It is listed as `retired` only where it was set up**, with the reason, in `list_sources`, `jhh sources` and the Sources tab. A new install never sees it. Its stored settings row stays, unused.
- **Its jobs stay.** They keep "via The Muse". jhh can't tell when they close any more, so the dashboard marks open ones "Source retired" and the user checks the posting. Nothing closes them automatically: a job someone applied to shouldn't close because a source went away.
- **No scraper.** jhh still reads only public feeds (design principle 7).
- **A search source's errors name what happened.** A 404 from its API reads as the source being unavailable, and a 401 or 403 as it refusing jhh's requests, each with the HTTP status. Company boards keep "board not found", which fits them.

## Consequences

- Search covers Hacker News's startups and Himalayas's remote jobs. Large employers with office roles are found only through their boards (Greenhouse, Lever, Ashby) or jobs the agent adds, until another free source is found ([0008](0008-free-sources-only.md)).
- The shared search behavior that The Muse's tests covered (closing after a complete read, lookups of pursued jobs, board postings joining search jobs, skipping old listings) is now tested through Hacker News.
- If The Muse brings back a public API, it comes back as a new source with its own check, not by undoing this.
