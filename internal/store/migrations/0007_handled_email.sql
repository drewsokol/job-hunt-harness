-- 0007: email the agent has handled (docs/design.md, "Email the agent has
-- handled"). jhh keeps ids, links and the agent's one-line note of what it
-- did: never a message's text, subject or addresses.

-- Messages the agent already handled, so a scan of a whole label neither
-- misses mail nor handles it twice. message_id is one message, not its
-- thread. A row outlives the job, thread or interview it touched.
CREATE TABLE handled_messages (
    system       TEXT    NOT NULL,
    message_id   TEXT    NOT NULL,
    action       TEXT    NOT NULL,
    job_id       INTEGER REFERENCES jobs (id) ON DELETE SET NULL,
    outreach_id  INTEGER REFERENCES outreach (id) ON DELETE SET NULL,
    interview_id INTEGER REFERENCES interviews (id) ON DELETE SET NULL,
    handled_at   TEXT    NOT NULL,
    handled_by   TEXT    NOT NULL,
    PRIMARY KEY (system, message_id)
) STRICT;

-- How far the agent has read each mail system.
CREATE TABLE mail_checks (
    system          TEXT PRIMARY KEY,
    checked_through TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    updated_by      TEXT NOT NULL
) STRICT;

-- Links from a job (its application), an outreach thread or an interview
-- to the email thread about it. Exactly one target is set.
CREATE TABLE external_refs (
    id           INTEGER PRIMARY KEY,
    job_id       INTEGER REFERENCES jobs (id) ON DELETE CASCADE,
    outreach_id  INTEGER REFERENCES outreach (id) ON DELETE CASCADE,
    interview_id INTEGER REFERENCES interviews (id) ON DELETE CASCADE,
    system       TEXT    NOT NULL,
    ref          TEXT    NOT NULL,
    url          TEXT,
    created_at   TEXT    NOT NULL,
    created_by   TEXT    NOT NULL,
    CHECK ((job_id IS NOT NULL) + (outreach_id IS NOT NULL) + (interview_id IS NOT NULL) = 1)
) STRICT;

CREATE UNIQUE INDEX external_refs_unique ON external_refs
    (coalesce(job_id, 0), coalesce(outreach_id, 0), coalesce(interview_id, 0), system, ref);
CREATE INDEX external_refs_job ON external_refs (job_id) WHERE job_id IS NOT NULL;
CREATE INDEX external_refs_outreach ON external_refs (outreach_id) WHERE outreach_id IS NOT NULL;
CREATE INDEX external_refs_interview ON external_refs (interview_id) WHERE interview_id IS NOT NULL;
