-- 0011: interview prep (docs/specs/0003-interview-prep.md). The candidate's
-- career stories, kept so every session tells them the same way; the
-- stories planned or told in each interview, with how they landed; and a
-- new memory kind, practice, for what the user should work on in
-- interviews. The memories' kind CHECK changes, so that table is rebuilt
-- (foreign keys are off while migrations run, and checked before commit).
-- No existing row changes.

CREATE TABLE memories_new (
    id              INTEGER PRIMARY KEY,
    kind            TEXT    NOT NULL CHECK (kind IN ('fact', 'preference', 'constraint', 'strength', 'gap', 'claim_limit', 'feedback', 'practice')),
    content         TEXT    NOT NULL,
    basis           TEXT    NOT NULL CHECK (basis IN ('user_said', 'inferred')),
    evidence        TEXT    NOT NULL DEFAULT '',
    evidence_job_id INTEGER REFERENCES jobs (id),       -- set when inferred from a listing
    status          TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived', 'superseded')),
    supersedes_id   INTEGER REFERENCES memories (id),
    created_at      TEXT    NOT NULL,
    created_by      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    updated_by      TEXT    NOT NULL,
    -- A memory inferred from a listing can never claim the user said it.
    CHECK (evidence_job_id IS NULL OR basis = 'inferred')
) STRICT;

INSERT INTO memories_new (id, kind, content, basis, evidence, evidence_job_id, status, supersedes_id,
                          created_at, created_by, updated_at, updated_by)
SELECT id, kind, content, basis, evidence, evidence_job_id, status, supersedes_id,
       created_at, created_by, updated_at, updated_by
FROM memories;

DROP TABLE memories;
ALTER TABLE memories_new RENAME TO memories;

-- A story: one of the candidate's career stories, written by the agent
-- from the user's words with their OK. claims are what it asserts about
-- the user, checked against the claim limits; tags are lowercase themes.
-- Archived, never deleted by the agent.
CREATE TABLE stories (
    id             INTEGER PRIMARY KEY,
    title          TEXT    NOT NULL,
    summary        TEXT    NOT NULL,
    body           TEXT    NOT NULL,
    claims         TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(claims)),
    tags           TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
    archived_at    TEXT,
    archive_reason TEXT    NOT NULL DEFAULT '',
    created_at     TEXT    NOT NULL,
    created_by     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL,
    updated_by     TEXT    NOT NULL
) STRICT;

-- The stories planned (by prep) or told (from the debrief) in an
-- interview, and how each landed.
CREATE TABLE interview_stories (
    interview_id INTEGER NOT NULL REFERENCES interviews (id) ON DELETE CASCADE,
    story_id     INTEGER NOT NULL REFERENCES stories (id) ON DELETE CASCADE,
    landed       TEXT    NOT NULL DEFAULT 'planned' CHECK (landed IN ('planned', 'well', 'mixed', 'poorly', 'not_used')),
    note         TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (interview_id, story_id)
) STRICT;

CREATE INDEX interview_stories_story ON interview_stories (story_id);
