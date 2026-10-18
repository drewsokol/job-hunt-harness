# 0002: The agent writes the profile, memories and filters; the UI is a window

Status: Accepted, 2026-10-18

## Context

The first draft had the user write the profile and approve filters in the UI. The owner wants to work through their agent instead: set up and tune the system by talking to it, and be able to switch agents without losing anything.

## Decision

- jhh is an MCP server with a GUI. It contains no AI. Every capability is an MCP tool first, and the web UI is a window onto the same data.
- The agent can build everything through MCP, from a short interview if need be: the profile (whole-document and section-level edits), memories about the candidate, filters, and companies.
- Everything the agent learns is stored in jhh's SQLite database, not in the agent, so another agent can pick up where the last one left off.
- Every change is versioned or logged with who made it, and the user can see it and undo it in the UI.

## Consequences

- Consent before saving profile text is a rule in the prompts, not something the server enforces. The backstops are version history, restore, and the "edited by agent" marker.
- Filters take effect without an approval step in the UI. `preview_filters`, the Filtered out view and (after the MVP) the guardrail and version history make up for that.
- Inferred memories are labelled and weighted lower than things the user said.
