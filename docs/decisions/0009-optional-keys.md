# 0009: Free sources may take an optional key

Status: Accepted, 2026-10-24. Brings back the keys part of [0007](0007-source-settings.md), which [0008](0008-free-sources-only.md) set aside.

## Context

Decision 0008 dropped Adzuna and the key storage planned for it, since nothing else needed a key. Himalayas, the next search source, works without a key, but every keyless caller shares one public allowance of unpublished size, so other people's use can cut a refresh short. A key gives the account its own limit (60 requests a minute, 10,000 a day). Signing up showed the key is free: no card, plan or trial.

## Decision

- **A source may take an optional key** when the key is free (checked by signing up, as 0008 asks) and the source works without it. jhh still ships no source that requires a key or a payment.
- **Keys follow 0007's rules:** entered only in the dashboard, never through MCP or the CLI; kept in `keys.json` in the data directory with owner-only permissions, never in SQLite; never returned by the API or a tool, and never in logs, errors, backups or exports.
- **A key is checked before it's saved**, and sent only to its service, over HTTPS, in a header.
- **A refused key never stops a source.** It carries on without the key and says the key was refused.

## Consequences

- Himalayas runs from the agent's queries without a key, and with more requests a refresh once you add one.
- The dashboard gains the key form 0007 planned, the one setting the agent can't make; it can only tell you where to paste a key.
- A future source with a free optional key reuses the same storage and form.
