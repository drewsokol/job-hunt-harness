-- 0010: the "apply next" shortlist (docs/specs/0001-apply-next-shortlist.md).
-- The agent's order of the jobs in review to apply to first, each with a
-- one-line reason in its own words, which the user can adjust on the
-- dashboard. Only jobs in review, open and not hidden are shown; a row
-- stays while its job is elsewhere, so the job comes back to its place.
CREATE TABLE shortlist (
    job_id INTEGER PRIMARY KEY REFERENCES jobs (id) ON DELETE CASCADE,
    rank   INTEGER NOT NULL,
    reason TEXT    NOT NULL,
    set_at TEXT    NOT NULL,
    set_by TEXT    NOT NULL
) STRICT;
