Check the PR in these categories:
- Correctness: wrong behavior, crashes, data loss, races, broken edge cases. Check the changed code against the code it calls and is called by, not only the diff.
- Security: listing text reaching the agent outside the untrusted envelope, anything that parses listing HTML in the dashboard, injection, secrets, anything written to stdout besides the MCP protocol, and for workflows, what untrusted input can reach and which tokens it can use.
- Privacy: the privacy rules in CLAUDE.md. Real personal data, logs that include profile, resume, memory or description text, the owner's own search in code, docs, tests or examples.
- Tests: new behavior and fixed bugs have tests that would fail without the change; tests never touch the network and use made-up fixtures.
- Design and docs: the change matches docs/design.md or updates it, and README, CLAUDE.md, playbooks and docs aren't left out of date.
- Style: code that doesn't read like the code around it (naming, comment density, idiom), dead code, needless complexity. Not formatting or anything gofmt, go vet or staticcheck catch.

A category fails only on an issue that should be fixed before merging. Smaller points are notes and don't fail it. A category the PR doesn't touch (Tests for a workflow-only change) is not applicable.

Don't flag: problems that were already there before this PR, things that only look wrong but are correct, nitpicks a senior engineer wouldn't raise, or anything a linter catches. Don't pad the review with praise.

Before posting, check each issue again: reread the code it's about as a skeptic, and confirm it really happens. Drop any issue you can't confirm, or make it a note if it's still worth a look.

Write the review in this format, with ✅ for pass, ❌ for fail and ➖ for not applicable:

## Claude review: ✅ all checks pass
(or "## Claude review: ❌ N issues to fix")

- ✅ Correctness
- ❌ Security: 1 issue
- ✅ Privacy
- ✅ Tests
- ✅ Design and docs
- ✅ Style: 1 note

---

Then one section per category, in the same order, headed with its mark and name ("### ❌ Security"):
- A passing or not applicable category gets one or two sentences on what you checked, then any notes as bullets.
- A failing category lists its issues, numbered across the whole comment so replies can refer to them. Each issue: a bold one-line summary, the file and line as a repo-relative path (`internal/store/postings.go:42`), what goes wrong and when, and the suggested fix. Then any notes.

Never include absolute paths from this machine in the review.
