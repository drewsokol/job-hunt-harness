# 0007: Sources are set up per source, not in the filters

Status: Proposed, 2026-10-24

## Context

A search source ran only when the filters named it: `search.themuse` held The Muse's queries, and `search.hackernews` turned Hacker News on. So which sources jhh read was part of what the agent wrote into the filters, and the only way to pause one was to ask the agent to rewrite them. The owner wants to see every source in the dashboard and turn each one on and off there, with the agent able to do the same.

Services that need an account and key, such as Adzuna, were on the roadmap with the key "kept on the user's machine", but there was nowhere to put one. The agent writes everything else jhh stores, and keys shouldn't pass through it.

## Decision

- **The filters say what you're looking for; source settings say where to look.** Each source has a settings row: on or off, plus JSON settings the source checks (The Muse's queries, Hacker News's `months`, Adzuna's countries). The filters lose their `search` section, and a migration moves existing ones over without changing what runs.
- **Off pauses.** A paused source keeps its settings and its jobs; nothing from it is refreshed or closed until it's back on.
- **Search sources have switches; board sources don't.** Boards are already turned on and off per company.
- **The dashboard and the agent both change sources.** `list_sources` and `set_source` are MCP tools, and the Sources screen uses the same core functions. Every change is recorded with who made it, as for the filters.
- **Keys are the one thing only the dashboard can set.** They go in `keys.json` in the data directory (owner-only permissions), never in SQLite, never back out of the API or a tool, never in logs, exports or backups.

## Consequences

- The agent no longer has to name sources to search. The filters are only judgment, wherever jobs came from.
- The Muse still needs source-specific queries, because it searches by its own category list; the agent sets them with `set_source` instead of `set_filters`.
- Source changes aren't versioned with restore the way filters are; they're small enough that an event log with the actor is enough.
- `set_filters` with a `search` section becomes an error that names `set_source`, which existing prompts and agents have to follow.
- The UI gains a second deliberate UI-only control after permanent deletes and restores: entering a key. That keeps keys out of the agent's context, at the cost of the agent being unable to finish setting up a keyed source alone; it can only tell the user where to paste the key.
- Two tools join a surface that aims for about 20.
