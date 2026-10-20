-- 0002: the dashboard (M5).

-- The user sent a job the prefilter dropped to screening anyway. The
-- prefilter still records its decision but no longer moves the job.
ALTER TABLE jobs ADD COLUMN prefilter_override INTEGER NOT NULL DEFAULT 0 CHECK (prefilter_override IN (0, 1));

-- Dashboard sign-in. A login token is the one-time token in the link
-- open_dashboard returns; a session is the cookie the browser gets for it.
-- Only a hash of each is stored. Every jhh process shares them, so any
-- process can hand out a link to the one serving the dashboard.
CREATE TABLE ui_tokens (
    hash       TEXT PRIMARY KEY,                         -- hex SHA-256 of the token
    kind       TEXT NOT NULL CHECK (kind IN ('login', 'session')),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
) STRICT;
