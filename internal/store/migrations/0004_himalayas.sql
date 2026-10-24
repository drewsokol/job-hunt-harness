-- 0004: Himalayas, a search source read only for what changed
-- (docs/design.md, "Himalayas").

-- When the source stops listing a posting. Himalayas's postings close when
-- it passes, since its searches can't tell a closed job from a quiet one.
ALTER TABLE postings ADD COLUMN expires_at TEXT;

-- jhh's own record of a source's progress, apart from its settings, which
-- people choose: how far each Himalayas query was read, and whether the
-- service last refused the key. Keys never go here.
ALTER TABLE sources ADD COLUMN state TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(state));
