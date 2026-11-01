# Specs

Each feature starts here as a spec ([decision 0011](../decisions/0011-feature-pipeline.md)). Once merged, a spec is the record of what was asked for and why; `docs/design.md` says how jhh works now.

## Writing one

1. Copy [TEMPLATE.md](TEMPLATE.md) to `NNNN-short-name.md`, with the next free number.
2. Fill it in. Examples are made up: specs are public, so never use the user's jobs, companies, contacts, location or pay, even when the gap came from a real search.
3. Open a PR with only the spec (and any design or decision changes it needs).

## Review

When an owner opens the PR, the spec review workflow reads the spec against the design, the decisions and the code, and comments with numbered questions, each with a recommended answer. It asks only what would change what gets built; smaller points it leaves to the implementer.

Answer in a comment ("1: yes. 2: go with your recommendation"), update the spec so it says what you decided (the implementer reads the spec, not the thread), and comment `/review` on a line of its own to have it reviewed again. When it says "ready to build", merge.

## Building

Merging a spec starts the build. That workflow is the next phase of the pipeline and isn't running yet; until it is, a coding agent builds the merged spec by hand.
