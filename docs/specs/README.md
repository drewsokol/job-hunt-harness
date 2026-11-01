# Specs

Each feature starts here as a spec ([decision 0011](../decisions/0011-feature-pipeline.md)). Once merged, a spec is the record of what was asked for and why; `docs/design.md` says how jhh works now.

## Writing one

1. Copy [TEMPLATE.md](TEMPLATE.md) to `NNNN-short-name.md`, with the next free number.
2. Fill it in. Examples are made up: specs are public, so never use the user's jobs, companies, contacts, location or pay, even when the gap came from a real search.
3. Open a PR with only the spec (and any design or decision changes it needs).

## Review

When an owner opens the PR, the spec review workflow reads the spec against the design, the decisions and the code, and comments with numbered questions, each with a recommended answer. It asks only what would change what gets built; smaller points it leaves to the implementer.

Update the spec so it says what you decided (the implementer reads the spec, not the thread), and answer in a comment that starts with `/review` on its first line, with your answers below it ("1: yes. 2: go with your recommendation"). That runs the review again. A comment with `/review` anywhere else does nothing. `/review` is also how to get a review for a spec pushed to a PR after it was opened. When the review says "ready to build", merge.

## Building

Merging a spec starts the build (`.github/workflows/build.yml`, which runs `.github/scripts/build.sh`). It opens a PR from `spec/NNNN-short-name` right away, labeled `building`, then implements, checks and has a fresh reviewer review, fixing what the review finds, for up to 10 rounds. The commits and the reviews reach the PR when the run ends, after a check that the build changed nothing it may not and passes the privacy scan. The implementer and the reviewer decide what they can and list the calls they made in the PR description.

- **When it needs you**, it comments tagging the owners with numbered questions, labels the PR `needs-owner-input`, and ends the run. Answer in a comment whose first line is `/continue`.
- **When it's done** (checks and review pass), it labels the PR `final-review`, tags the owners and starts CI on the branch. Review it and merge.
- **Other commands**, as a comment's first line: `/restart` throws the branch away and builds again from the spec and the thread; `/stop` closes the PR (and cancels the run if it's going).

One build runs at a time. To build a spec by hand (one merged before this workflow existed, say), run the Build workflow from the Actions tab with its path, or `gh workflow run build.yml -f spec=docs/specs/NNNN-short-name.md`.
