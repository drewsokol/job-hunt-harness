# 0003: Dedupe with a ladder of identity signals, not company + title

Status: Accepted, 2026-10-18

## Context

The reference grouped postings by company + normalized title. Live Greenhouse feeds checked on 2026-10-18 showed both city duplicates (Elastic: 380 postings, 188 openings) and different openings that share a title (6–12 per board at Elastic, Toast and GitLab). Grouping by title alone silently hides real openings.

## Decision

Keep three levels of identity: posting, opening and job. Match postings to openings with these signals, strongest first:
1. exact posting ID;
2. the board's own opening ID (Greenhouse `internal_job_id`);
3. canonical apply URL, through each source's `MatchURL`;
4. a description fingerprint, so the same title with a different description stays separate;
5. repost detection against jobs closed in the last 60 days.

Grouping is a link between rows, never a merge, so `merge_jobs` and `split_job` can correct mistakes.

## Consequences

- Postings store `opening_key`, `canonical_url` and `fingerprint`.
- The UI groups same-title openings in one row, so the extra precision doesn't clutter the list.
