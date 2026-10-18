# 0006: The agent can add jobs

Status: Accepted, 2026-10-18

## Context

Company feeds only cover companies on the user's list, and Workday and custom career sites have no simple public feed. The user's agent can often read pages jhh can't: a link from a friend, a recruiter email, or a career site it opens in its own browser.

## Decision

`add_jobs` accepts a batch of jobs from the agent, and replaces the earlier `add_job_from_url` design.
- If a job's URL matches a supported board, jhh fetches the posting from that board's API itself.
- Otherwise jhh saves the fields the agent supplied, with `origin=agent`.
- Every added job is untrusted (0005), goes through the dedupe ladder (0003), and lands in `unscreened`.
- In the MVP, jhh never fetches arbitrary web pages. Reading schema.org `JobPosting` data from any URL comes later, with private and loopback addresses blocked at connection time and limits on size and redirects.

## Consequences

Any job the agent can read becomes a job in jhh, with no scraping code in jhh. This is the cheapest route to coverage beyond the built-in sources.
