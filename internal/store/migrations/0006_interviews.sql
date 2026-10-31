-- Interviews: one row for each interview, about a job, an outreach thread
-- or both. Times are UTC. joining (a meeting link, id and passcode, or an
-- address) and the notes are never copied into events.
CREATE TABLE interviews (
    id               INTEGER PRIMARY KEY,
    job_id           INTEGER REFERENCES jobs (id),
    outreach_id      INTEGER REFERENCES outreach (id),
    stage            TEXT    NOT NULL CHECK (stage IN ('recruiter_screen', 'hiring_manager', 'technical', 'system_design', 'panel', 'product', 'final', 'other')),
    scheduled_at     TEXT    NOT NULL,
    duration_minutes INTEGER CHECK (duration_minutes > 0),
    interviewers     TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(interviewers)), -- [{name, role}]
    medium           TEXT    NOT NULL DEFAULT 'other' CHECK (medium IN ('video', 'phone', 'onsite', 'other')),
    joining          TEXT    NOT NULL DEFAULT '',
    status           TEXT    NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'completed', 'cancelled', 'no_show')),
    outcome          TEXT    NOT NULL DEFAULT 'pending' CHECK (outcome IN ('pending', 'advanced', 'rejected', 'offer')),
    next_step_at     TEXT,
    prep_notes       TEXT    NOT NULL DEFAULT '',
    debrief_notes    TEXT    NOT NULL DEFAULT '',
    recorded         INTEGER NOT NULL DEFAULT 0 CHECK (recorded IN (0, 1)),
    recording_tool   TEXT    NOT NULL DEFAULT '',
    created_at       TEXT    NOT NULL,
    created_by       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL,
    updated_by       TEXT    NOT NULL,
    CHECK (job_id IS NOT NULL OR outreach_id IS NOT NULL)
) STRICT;

CREATE INDEX interviews_job ON interviews (job_id) WHERE job_id IS NOT NULL;
CREATE INDEX interviews_scheduled ON interviews (scheduled_at);

-- Timeline events about an interview name it.
ALTER TABLE events ADD COLUMN interview_id INTEGER REFERENCES interviews (id);
