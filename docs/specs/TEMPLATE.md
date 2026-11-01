# NNNN: Short name of the feature

Status: Draft

<!--
Copy this file to docs/specs/NNNN-short-name.md, with the next free number.
Specs are public. Describe gaps found in a real search with made-up
examples: never the user's jobs, companies, contacts, location or pay.
Merging the spec starts the build (decision 0011), so it should say
everything the implementer needs; what it leaves open, the implementer
decides and lists in the PR.
-->

## Problem

What the user or the agent can't do today, and what that costs. One short example of it happening, made up.

## Design

What changes, from the outside in:

- **Tools**: new tools or parameters, with what they take and return. Mind the tool list budget (`TestOutputSizes`) and decision 0010.
- **Dashboard**: what the user sees and can do.
- **Data**: new tables or columns, and the migration.
- **Rules**: what happens at the edges (empty input, limits, a record that's gone, a change by the user versus the agent).

## Out of scope

What this spec leaves out on purpose, so the implementer doesn't build it.

## Acceptance checks

Things a test or a person can check, each one line. For example: "after X, `list_candidates` returns A before B, and still does in a new session."

## Open questions

Questions for the spec review to raise or for the owner to settle before merging. Empty when the spec is ready.
