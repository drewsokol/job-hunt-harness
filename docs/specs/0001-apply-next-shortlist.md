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
- **`whats_new`** says how many jobs are on the shortlist (those shown, per the Rules). It also counts the jobs in review that aren't on the list and whose latest screening is strong or good and newer than the last `set_shortlist` call (the latest `shortlist_set` event), so the agent knows to re-rank. The user's dashboard changes don't reset that time. With no list yet, the size is 0 and the count covers every strong or good job in review, which nudges the agent to make one.

### Playbook and docs

- `internal/playbooks/screen_new_jobs.md` gets a last step: after `record_screening`, read the current shortlist with `list_candidates(status: review)`, then call `set_shortlist` with the re-ranked list, keeping the user's changes unless they said otherwise.
- `docs/design.md` describes the shortlist: the tools table, the To review tab and the data model.

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
- `set_shortlist` replaces all rows in one transaction, including rows for jobs that are out of review.
- The dashboard's up and down buttons swap two ranks among the jobs shown. Add puts a job after the current last. Remove deletes its row.
- Each change writes an event: `shortlist_set` with the count, or `shortlist_changed` with the job id and what changed. As with other events, it carries ids and counts, never the reason text.

### Rules

- **The shortlist shows only jobs that are in review, open and not hidden.** A row stays when its job leaves review (applied, skipped, hidden, closed, screened out, or any other status, by anyone), but the job isn't shown, numbered or counted. If the job comes back (the dashboard's Undo, a posting seen again after a bad fetch, unhiding), it's back in its place. No code that changes a job's status needs to touch the shortlist.
- Only `set_shortlist` (which replaces every row) and the dashboard's remove delete rows. Deleting a job deletes its row (cascade).
- **Re-screening a job keeps its place** unless the new rating moves it out of review: a `no` moves it to screened out, and it's no longer shown, as with any other status change. A profile or filter change keeps every place. The agent decides when to re-rank.

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
- Marking A applied takes it off the shown list: `list_candidates(status: review)` returns C and B with ranks 1 and 2. Undoing that (A back in review) returns C, A, B with ranks 1, 2, 3.
- A shortlisted job whose posting closes isn't shown; when the posting is seen again and reopens, it's back in its place.
- Re-screening B as `no` takes it off the shown list. Re-screening C as `good` keeps its place.
- `set_shortlist` with a job that isn't in review saves nothing and names that job id. With 31 jobs, it's refused.
- `set_shortlist` with an empty list clears it.
- The dashboard's up, down, remove and add buttons change the order that `list_candidates` returns, with `set_by: user`.
- `whats_new` reports the shortlist's size, and counts the strong or good jobs in review, not on the list, screened after the last `set_shortlist`. A dashboard up or down doesn't change that count. With no list, it reports size 0 and counts every strong or good job in review.
- `screen_new_jobs.md` names `set_shortlist` in its last step.
- `TestOutputSizes` passes with the cap at 42,000 bytes.

## Open questions

None.
