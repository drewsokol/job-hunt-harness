# 0011: Features are built from specs by a workflow, and the owner steps in only when asked

Status: Accepted, 2026-11-01.

## Context

Since the MVP, features have come from using jhh: the agent running the search notices a gap, the owner talks it through with a coding agent, and the coding agent writes the code, opens a PR and fixes what review finds. The owner reads less and less of the code and decides more and more by talking it over. Most of the waiting in that loop is a person passing messages between agents.

The steps that need the owner are few: agreeing what to build, answering a question only they can answer, and accepting the result. Everything between can run on its own in GitHub Actions, if the checks are strong enough to trust and the workflows can't be started or steered by anyone else.

## Decision

**A feature starts as a spec.** It is a file in `docs/specs/` (`NNNN-short-name.md`, from [the template](../specs/TEMPLATE.md)): the problem, the design, what's left out, and acceptance checks. The owner and a coding agent write it together and open it as a PR. Specs are public like everything else here: a gap found in a real search is described with made-up examples, never with the user's jobs, companies, contacts or pay.

**A spec review asks the questions a developer would.** On a PR that changes `docs/specs/`, a workflow reads the spec against the design and the code and comments with clarifying questions, not a code review. `/review` runs it again after the answers.

**Merging a spec starts the build.** One workflow run builds one feature, and one feature is built at a time, so migrations and the tool budget never race. In that run:

1. The implementer works on a branch `spec/NNNN-short-name`, runs the checks (gofmt, vet, staticcheck, tests, the privacy scan) and opens a PR that links the spec.
2. A reviewer with a fresh context reviews the PR with the same prompt as the review workflow, and comments.
3. The implementer fixes what the review found, and the reviewer reviews again: up to **10 rounds**.
4. When the checks and the review both pass, the PR gets the `final-review` label and a comment tagging the owner.

**The workflow decides for itself.** The implementer and the reviewer make every call they can, the way a trusted developer would, and list the calls they made in the PR description so the owner can read them in one place. A wrong call is fixed by a later spec, which is cheaper than waiting. They stop and ask only when they can't decide:

- the spec contradicts itself, or would contradict an accepted decision or `docs/design.md` in a way the spec doesn't address;
- the change needs something only the owner can do: a secret, a repo setting, a change to `.github/`;
- they can't tell whether something is personal data;
- they still disagree after 10 rounds.

To ask, the workflow posts one comment tagging the owner with numbered questions, adds the `needs-drew` label, and **ends the run**. Nothing waits on a runner; everything the next run needs is on the PR.

**The owner steers with commands.** A comment by the owner whose first line is one of these starts the next step; comments from anyone else, and comments without a command, start nothing.

| Command | Where | What it does |
|---|---|---|
| `/review` | spec PR | Reviews the spec again |
| `/continue` | build PR | Carries on from where the run stopped, with the comments since as answers |
| `/stop` | build PR | Ends the attempt: the PR is closed and the branch kept |
| `/restart` | build PR | Throws the branch away and builds again from the spec and the comments |

**Labels show where a feature stands:** `building` while a run is going, `needs-drew` when it's waiting on the owner, `final-review` when it's ready to merge.

**The owner merges.** No workflow merges a PR. Letting the workflow merge after it has run well for a while is a later decision, with its own record.

**Trust comes from the checks.** CI (gofmt, vet, staticcheck, tests, the migration numbering, the privacy scan) is required on `main`. The privacy scan reads the owner's private terms from a repository secret, so the public repo never lists them, and it reports a file and line, never the term it matched.

**Only the owner can start anything.** Every workflow that holds a secret checks that the event came from the owner (`github.actor`, or the comment's author) and never runs for forks. No workflow uses `pull_request_target` with the PR's code. The implementer pushes only its own branch, as the Claude GitHub App, so CI runs on its pushes; it can't change `.github/` or a merged spec, and it never merges.

**A coding agent watches for questions.** In a session the owner leaves open, a coding agent checks for `needs-drew` every few minutes, reads the question and the code it's about, and brings the owner a summary and a recommendation. After they talk it over, it posts the answer and `/continue` from the owner's account, once the owner has approved the wording, and says it drafted the reply. GitHub's own notification of the tag covers the times no session is open.

**Releases are built by a workflow.** A release PR collects merged changes, and merging it tags a version; GoReleaser then builds the binaries for Windows, macOS and Linux with checksums and notes. Users install a release instead of building from source.

## Consequences

- The owner's work on a feature is the spec, the occasional answer, and the final review.
- Every run uses the owner's Claude plan; the round limit and a turn limit on each step bound what one feature can use.
- Features come one at a time. A spec merged while another is building waits its turn.
- Plans for features move from GitHub Issues and a project board (design section 11) to specs in the repo, which also become the record of why each feature works the way it does. Issues stay for bug reports.
- Some features will land with calls the owner would have made differently. That is the trade for not waiting, and a later spec corrects it.
