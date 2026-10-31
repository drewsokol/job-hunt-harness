-- A job the user applied to outside jhh, recorded with add_jobs and a
-- status. Its origin stays the agent's: it's the application that
-- happened elsewhere, not the finding.
ALTER TABLE jobs ADD COLUMN applied_elsewhere INTEGER NOT NULL DEFAULT 0 CHECK (applied_elsewhere IN (0, 1));
