# 0001: An "apply next" shortlist the agent sets and the user can adjust

Status: Draft

## Problem

The agent's judgment of which reviewed jobs to apply to first has nowhere to live. Fit has four levels, and in a real search "good" held about 35 jobs. Within a level, `list_candidates` and the dashboard's To review list sort by the prefilter's keyword score. A listing stuffed with matching keywords outranks a better job written plainly.

So the agent ranks the jobs in chat, and that ranking is lost when the context is compacted or a new session starts. The user sees one order on the dashboard and hears a different one from the agent.

Example (made up): the agent tells the user to apply to Northwind Labs first, then Acme Robotics, then Example Health. The dashboard's To review list shows Example Health first, because its listing mentions RAG, agents, MCP and evals in every paragraph, even though its pay tops out below what the user wants.

## Design

**A shortlist** is an ordered list of jobs in review, each with a rank and a one-line reason. It's the answer to "what should I apply to next?". Jobs on it sort first; everything else keeps today's order underneath.

### Tools

- **New tool `set_shortlist`.** It replaces the whole shortlist with the jobs given, in order: `jobs: [{job_id, reason}]`, at most 30.
  - An empty list clears it.
  - `reason` is required. It's at most 120 characters, the agent's own words, saying why now (for example "pay fits, Principal level, closing soon"). It never quotes the listing.
  - Every job must be in review, not hidden and not closed. If any isn't, nothing is saved, and the error names those job ids and why.
  - Returns the saved list, each entry with its rank, job id, company, title and reason. Company and title go inside `untrusted`, as elsewhere.
  - Description: the agent re-ranks the whole list in one call when it screens new jobs or when the user asks what's next. It reads the current list first (from `list_candidates`) so it keeps the user's own changes unless the user says otherwise.
- **`list_candidates` with `status: review`** sorts shortlisted jobs first, by rank, then the rest as today.
  - Each shortlisted job carries `shortlist: {rank, reason, set_by}`, where `set_by` is `agent` or `user`.
  - The ranks returned are 1..N over the jobs still on the list, whatever gaps the stored ranks have.
  - Other statuses are unchanged.
- **`whats_new`** says how many jobs are on the shortlist. When strong or good jobs have been screened since the list was last set, it says that too, so the agent knows to re-rank.

### Tool budget

The tool list is at 39,942 bytes against `TestOutputSizes`'s 40,000-byte cap.
- The owner has agreed to raise the cap to 42,000 bytes for this tool.
- `set_shortlist` is a new kind of record, so it fits decision 0010. That makes 33 tools, still under 35.
- Keep the new description and schema short. If they'd push the list past 42,000 bytes, trim existing descriptions rather than raise the cap again.

### Dashboard

On the To review tab:
- Shortlisted jobs come first, each with its rank number and its reason. The rest follow under a divider, as today.
- Each shortlisted job has **up**, **down** and **remove** buttons.
- Any job in review that isn't on the list has **add to shortlist**, which adds it at the bottom with the reason "added by you".
- The user's changes are saved at once, logged with actor `user`, and visible to the agent through `list_candidates`. The same goes for the agent's changes, logged with actor `agent`. The latest change wins.
- No drag and drop.

### Data

- **Migration 0010** adds a `shortlist` table: `job_id` (primary key, references jobs, cascade delete), `rank` (integer), `reason` (text), `set_at` (UTC), `set_by` (actor).
- `set_shortlist` replaces all rows in one transaction.
- The dashboard's up and down buttons swap two ranks. Add puts a job after the current last.
- Each change writes an event: `shortlist_set` with the count, or `shortlist_changed` with the job id and what changed. As with other events, it carries ids and counts, never the reason text.

### Rules

- **A job leaves the shortlist when it leaves review**: applied, skipped, hidden, closed, or moved to any other status, by anyone. Its row is deleted in the same transaction as the status change. If it comes back to review, it isn't on the list until someone adds it.
- **Re-screening a job keeps its place.** So does a profile or filter change. The agent decides when to re-rank.
- **A job closing** while on the list is removed with the next refresh that closes it.

## Out of scope

- A finer fit scale (1–10) or numeric scores.
- Buckets such as next, soon and later.
- Weighing posting age or closing dates into the order automatically. The agent can mention them in the reason.
- Drag and drop on the dashboard.
- Shortlists for any status other than review.

## Acceptance checks

- After `set_shortlist` with jobs C, A, B (all in review, with A's prefilter score highest), `list_candidates(status: review)` returns C, A, B first, in that order, with ranks 1, 2, 3 and their reasons. The rest follow in today's order.
- The order is the same from a new store opened on the same database (a new session).
- The dashboard's To review list shows C, A, B first, with their numbers and reasons.
- Marking A applied removes it from the shortlist. `list_candidates(status: review)` then returns C and B with ranks 1 and 2.
- `set_shortlist` with a job that isn't in review saves nothing and names that job id. With 31 jobs, it's refused.
- `set_shortlist` with an empty list clears it.
- The dashboard's up, down, remove and add buttons change the order that `list_candidates` returns, with `set_by: user`.
- `whats_new` reports the shortlist's size, and notes newly screened good or strong jobs when there are any since the list was set.
- `TestOutputSizes` passes with the cap at 42,000 bytes.

## Open questions

None.
