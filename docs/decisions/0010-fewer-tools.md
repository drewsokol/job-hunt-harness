# 0010: One tool per kind of record, not one per verb

Status: Accepted, 2026-10-31.

## Context

The MVP aimed for about 20 MCP tools and had 33 by the time `list_outreach` landed. The next features (inbound outreach, interviews, handled email, contacts) would have added about nine more. That matters for three reasons:

- Some clients cap the tools they take across every connected server, around 40 in at least one popular client, and drop the rest. Someone with two other servers connected could lose part of jhh.
- Clients without on-demand tool loading send the whole tool list with every request. jhh's was about 28 KB.
- Models pick the wrong tool more often when several look alike, and several of jhh's were pairs that differ by one flag.

## Decision

- **One tool saves each kind of record**, new or changed. Without an id it creates the record; with an id (or a way to find exactly one) it changes it. `log_outreach` and `update_outreach` become `save_outreach`.
- **A change that is a mode of another becomes a parameter**, not a tool: archiving a memory is `update_memory` with `archive`; editing one profile section is `update_profile` with `heading`; previewing filters is `set_filters` with `dry_run`; adding a company and turning its board on or off is `set_company`.
- **A read tool given an id returns that one record in full**, as `list_outreach` already does, instead of a separate `get_` tool.
- **No catch-all tools** such as `save(kind=...)`. Each tool keeps a specific schema, since vague schemas are called wrong more often.
- **Old names are removed, not kept as aliases**, since aliases would keep the count up. jhh has no releases yet, so the cost is reconnecting the agent and the playbooks naming the new tools.
- After the MVP, jhh stays **under 35 tools**.

## Consequences

- 33 tools become 28, and the next four features add four (`save_interview`, `handled_messages`, `list_contacts`, `save_contact`), for 32.
- An agent with old instructions in its context gets "unknown tool" for an old name until it reconnects; the descriptions and playbooks name the new ones.
- `merge_jobs` and `split_job` stay separate: they're rarely used, and merging them would make each one harder to read. `whats_new` stays its own tool, since it's the daily check-in the instructions point to.
