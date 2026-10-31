-- Inbound outreach: who wrote first, two new statuses (received and
-- declined), why the user declined, the pay the person mentioned, and an
-- agency's client. The status CHECK changes, so the table is rebuilt
-- (foreign keys are off while migrations run, and checked before commit).
-- Existing threads were messages the user sent: they become outbound.

CREATE TABLE outreach_new (
    id             INTEGER PRIMARY KEY,
    contact_id     INTEGER NOT NULL REFERENCES contacts (id),
    job_id         INTEGER REFERENCES jobs (id),
    direction      TEXT    NOT NULL DEFAULT 'outbound' CHECK (direction IN ('inbound', 'outbound')),
    channel        TEXT    NOT NULL,
    sent_at        TEXT    NOT NULL,                    -- when the thread started, whoever wrote
    status         TEXT    NOT NULL DEFAULT 'sent' CHECK (status IN ('received', 'sent', 'accepted', 'replied', 'scheduled', 'no_reply', 'declined', 'closed')),
    follow_up_at   TEXT,
    note           TEXT    NOT NULL DEFAULT '',
    decline_reason TEXT    CHECK (decline_reason IN ('contract', 'pay_below_floor', 'hybrid_or_onsite', 'wrong_role', 'level_too_low', 'no_details', 'suspicious', 'other')),
    decline_note   TEXT    NOT NULL DEFAULT '',
    pay_min        REAL,                                -- what the person said, not the listing
    pay_max        REAL,
    pay_currency   TEXT,
    pay_period     TEXT    CHECK (pay_period IN ('year', 'hour')),
    pay_note       TEXT    NOT NULL DEFAULT '',
    client_company TEXT,                                -- an agency's client, or 'undisclosed'
    created_at     TEXT    NOT NULL,
    created_by     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL,
    updated_by     TEXT    NOT NULL
) STRICT;

INSERT INTO outreach_new (id, contact_id, job_id, channel, sent_at, status, follow_up_at, note,
                          created_at, created_by, updated_at, updated_by)
SELECT id, contact_id, job_id, channel, sent_at, status, follow_up_at, note,
       created_at, created_by, updated_at, updated_by
FROM outreach;

DROP TABLE outreach;
ALTER TABLE outreach_new RENAME TO outreach;

CREATE INDEX outreach_follow_up ON outreach (follow_up_at) WHERE follow_up_at IS NOT NULL;
CREATE INDEX outreach_sent ON outreach (sent_at);
