-- 0001: the MVP schema. See docs/design.md section 4.
--
-- Conventions:
--   * Timestamps are TEXT in UTC, formatted as utc.Layout ("2026-10-18T14:05:09.120Z").
--   * *_by columns and events.actor hold an Actor: 'user', 'system' or 'agent:<client name>'.
--   * JSON columns are TEXT checked with json_valid().
--   * Released migrations are never edited; changes go in a new file.

CREATE TABLE companies (
    id              INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL,
    source          TEXT    NOT NULL,                  -- greenhouse | lever | ashby | none
    board_key       TEXT    UNIQUE,                    -- e.g. 'greenhouse:exampleco'; NULL when there is no board
    source_config   TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(source_config)),
    careers_url     TEXT,
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    origin          TEXT    NOT NULL CHECK (origin IN ('registry', 'user', 'agent')),
    etag            TEXT,
    last_fetch_at   TEXT,
    last_fetch_ok   INTEGER CHECK (last_fetch_ok IN (0, 1)),
    last_error      TEXT,
    open_postings   INTEGER NOT NULL DEFAULT 0,
    suspect         INTEGER NOT NULL DEFAULT 0 CHECK (suspect IN (0, 1)),
    suspect_reason  TEXT,
    created_at      TEXT    NOT NULL,
    created_by      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    updated_by      TEXT    NOT NULL
) STRICT;

-- A job is what the user triages. Its id doubles as the short number shown
-- to people and agents (#482); AUTOINCREMENT means a number is never reused.
CREATE TABLE jobs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    company_id          INTEGER NOT NULL REFERENCES companies (id),
    title               TEXT    NOT NULL,
    workplace           TEXT    NOT NULL DEFAULT 'unknown' CHECK (workplace IN ('remote', 'hybrid', 'onsite', 'unknown')),
    locations           TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(locations)),
    pay_raw             TEXT,
    pay_ranges          TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(pay_ranges)),
    url                 TEXT,
    description         TEXT,
    posted_at           TEXT,
    first_seen_at       TEXT    NOT NULL,
    closed_at           TEXT,
    reposted_at         TEXT,
    hidden_at           TEXT,
    origin              TEXT    NOT NULL CHECK (origin IN ('feed', 'manual', 'agent')),
    prefilter_status    TEXT    CHECK (prefilter_status IN ('pass', 'drop')),
    prefilter_reasons   TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(prefilter_reasons)),
    prefilter_score     REAL,
    filters_version     INTEGER,
    status              TEXT    NOT NULL DEFAULT 'new' CHECK (status IN (
                            'new', 'filtered_out', 'unscreened', 'screened_out', 'review', 'skipped',
                            'applied', 'interviewing', 'offer', 'rejected', 'withdrawn')),
    applied_at          TEXT,
    applied_unconfirmed INTEGER NOT NULL DEFAULT 0 CHECK (applied_unconfirmed IN (0, 1)),
    notes               TEXT    NOT NULL DEFAULT '',
    created_at          TEXT    NOT NULL,
    created_by          TEXT    NOT NULL,
    updated_at          TEXT    NOT NULL,
    updated_by          TEXT    NOT NULL
) STRICT;

CREATE INDEX jobs_status ON jobs (status);
CREATE INDEX jobs_company ON jobs (company_id);

-- A posting is one listing on one board. Postings are grouped into jobs by the
-- dedupe ladder (docs/decisions/0003); job_id is that link, never a merge.
CREATE TABLE postings (
    id                   TEXT    PRIMARY KEY,           -- 'gh:exampleco:123', 'agent:<uuid>'
    company_id           INTEGER NOT NULL REFERENCES companies (id),
    job_id               INTEGER REFERENCES jobs (id),
    source               TEXT    NOT NULL,
    external_id          TEXT    NOT NULL,
    opening_key          TEXT,                          -- the board's own opening id, e.g. Greenhouse internal_job_id
    canonical_url        TEXT,
    fingerprint          TEXT,
    title                TEXT    NOT NULL,
    location_raw         TEXT,
    workplace            TEXT    NOT NULL DEFAULT 'unknown' CHECK (workplace IN ('remote', 'hybrid', 'onsite', 'unknown')),
    locations            TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(locations)),
    url                  TEXT,
    posted_at            TEXT,
    source_updated_at    TEXT,
    pay_raw              TEXT,
    employment_type      TEXT,
    description          TEXT,
    description_html_raw TEXT,
    detail_fetched_at    TEXT,
    first_seen_at        TEXT    NOT NULL,
    last_seen_at         TEXT    NOT NULL,
    closed_at            TEXT
) STRICT;

CREATE INDEX postings_job ON postings (job_id);
CREATE INDEX postings_company ON postings (company_id);
CREATE INDEX postings_opening ON postings (company_id, opening_key) WHERE opening_key IS NOT NULL;
CREATE INDEX postings_url ON postings (canonical_url) WHERE canonical_url IS NOT NULL;
CREATE INDEX postings_fingerprint ON postings (company_id, fingerprint) WHERE fingerprint IS NOT NULL;

CREATE TABLE screenings (
    id              INTEGER PRIMARY KEY,
    job_id          INTEGER NOT NULL REFERENCES jobs (id),
    fit             TEXT    NOT NULL CHECK (fit IN ('strong', 'good', 'stretch', 'no')),
    reason          TEXT    NOT NULL,
    gaps            TEXT    NOT NULL DEFAULT '',
    flags           TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(flags)),
    profile_version INTEGER,
    screened_by     TEXT    NOT NULL,
    screened_at     TEXT    NOT NULL
) STRICT;

CREATE INDEX screenings_job ON screenings (job_id, screened_at);

-- Who a contact is and why they matter to the search.
CREATE TABLE contacts (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL,
    title        TEXT,                                  -- their job title
    company_id   INTEGER REFERENCES companies (id),
    company_name TEXT,
    relationship TEXT,                                  -- connection, alumni, former coworker, recruiter, hiring manager, referral, friend, other
    source       TEXT,                                  -- where you found them: linkedin, referral, event, email, other
    why          TEXT    NOT NULL DEFAULT '',           -- why they matter, in a sentence or two
    profile_url  TEXT,
    email        TEXT,
    notes        TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    created_by   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL,
    updated_by   TEXT    NOT NULL
) STRICT;

CREATE TABLE outreach (
    id           INTEGER PRIMARY KEY,
    contact_id   INTEGER NOT NULL REFERENCES contacts (id),
    job_id       INTEGER REFERENCES jobs (id),
    channel      TEXT    NOT NULL,
    sent_at      TEXT    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'sent' CHECK (status IN ('sent', 'accepted', 'replied', 'scheduled', 'no_reply', 'closed')),
    follow_up_at TEXT,
    note         TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    created_by   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL,
    updated_by   TEXT    NOT NULL
) STRICT;

CREATE INDEX outreach_follow_up ON outreach (follow_up_at) WHERE follow_up_at IS NOT NULL;

-- The timeline. Every change to a job's status writes one of these in the
-- same transaction.
CREATE TABLE events (
    id          INTEGER PRIMARY KEY,
    at          TEXT    NOT NULL,
    actor       TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    job_id      INTEGER REFERENCES jobs (id),
    outreach_id INTEGER REFERENCES outreach (id),
    contact_id  INTEGER REFERENCES contacts (id),
    company_id  INTEGER REFERENCES companies (id),
    data        TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(data))
) STRICT;

CREATE INDEX events_job ON events (job_id, at) WHERE job_id IS NOT NULL;

CREATE TABLE profile_versions (
    version        INTEGER PRIMARY KEY AUTOINCREMENT,
    markdown       TEXT    NOT NULL,
    change_summary TEXT    NOT NULL,
    created_at     TEXT    NOT NULL,
    created_by     TEXT    NOT NULL
) STRICT;

CREATE TABLE resume_versions (
    version    INTEGER PRIMARY KEY AUTOINCREMENT,
    text       TEXT    NOT NULL,
    source     TEXT    NOT NULL CHECK (source IN ('pasted', 'file')),
    created_at TEXT    NOT NULL,
    created_by TEXT    NOT NULL
) STRICT;

CREATE TABLE filter_versions (
    version    INTEGER PRIMARY KEY AUTOINCREMENT,
    filters    TEXT    NOT NULL CHECK (json_valid(filters)),
    rationale  TEXT    NOT NULL,
    effect     TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(effect)),
    active     INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
    created_at TEXT    NOT NULL,
    created_by TEXT    NOT NULL
) STRICT;

CREATE UNIQUE INDEX filter_versions_one_active ON filter_versions (active) WHERE active = 1;

CREATE TABLE memories (
    id              INTEGER PRIMARY KEY,
    kind            TEXT    NOT NULL CHECK (kind IN ('fact', 'preference', 'constraint', 'strength', 'gap', 'claim_limit', 'feedback')),
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

CREATE TABLE content_flags (
    id          INTEGER PRIMARY KEY,
    posting_id  TEXT    NOT NULL REFERENCES postings (id),
    detector    TEXT    NOT NULL,                       -- 'rule:<name>' or 'agent:<name>'
    kind        TEXT    NOT NULL CHECK (kind IN ('canary', 'hostile', 'hidden_text', 'unclear')),
    excerpt     TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'kept', 'hidden')),
    created_at  TEXT    NOT NULL,
    reviewed_at TEXT,
    reviewed_by TEXT
) STRICT;

CREATE INDEX content_flags_open ON content_flags (posting_id) WHERE status = 'open';

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE fetch_runs (
    id          INTEGER PRIMARY KEY,
    started_at  TEXT    NOT NULL,
    finished_at TEXT,
    companies   INTEGER NOT NULL DEFAULT 0,
    postings    INTEGER NOT NULL DEFAULT 0,
    new_jobs    INTEGER NOT NULL DEFAULT 0,
    passed      INTEGER NOT NULL DEFAULT 0,
    errors      TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(errors))
) STRICT;
