-- 0003: sources are turned on and set up per source, not in the filters
-- (docs/decisions/0007-source-settings.md).

-- One row for each source someone turned on or off, set up, or that has
-- run. A source without a row follows its default. enabled is NULL until
-- someone chooses: a source runs then once it has what it needs, so one
-- that needs no setup is on from the start. Keys never go here.
CREATE TABLE sources (
    name          TEXT    PRIMARY KEY,
    enabled       INTEGER CHECK (enabled IN (0, 1)),
    settings      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
    reason        TEXT,                                    -- why it last changed, if given
    last_run_at   TEXT,
    last_postings INTEGER,                                 -- postings its last run found
    last_error    TEXT,                                    -- may quote the service's reply
    updated_at    TEXT    NOT NULL,
    updated_by    TEXT    NOT NULL
) STRICT;

-- Move the active filters' search section over without changing what
-- runs: The Muse's queries keep it on, and Hacker News stays on only if
-- the filters read it.
INSERT INTO sources (name, enabled, settings, updated_at, updated_by)
SELECT 'themuse', NULL, json_object('queries', json(json_extract(filters, '$.search.themuse'))),
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), 'system'
FROM filter_versions
WHERE active = 1 AND json_array_length(filters, '$.search.themuse') > 0;

INSERT INTO sources (name, enabled, settings, updated_at, updated_by)
SELECT 'hackernews',
       CASE WHEN json_type(filters, '$.search.hackernews') = 'object' THEN NULL ELSE 0 END,
       CASE WHEN json_type(filters, '$.search.hackernews.months') = 'integer'
            THEN json_object('months', json_extract(filters, '$.search.hackernews.months')) ELSE '{}' END,
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), 'system'
FROM filter_versions
WHERE active = 1;

-- Then save the filters again without search, as a new version by jhh
-- itself. The old version stays in the history.
INSERT INTO filter_versions (filters, rationale, effect, active, created_at, created_by)
SELECT json_remove(filters, '$.search'),
       'Which sources jhh searches moved out of the filters into each source''s settings.',
       '{}', 0, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), 'system'
FROM filter_versions
WHERE active = 1 AND json_type(filters, '$.search') IS NOT NULL;

UPDATE filter_versions SET active = 0
WHERE active = 1 AND json_type(filters, '$.search') IS NOT NULL;

UPDATE filter_versions SET active = 1
WHERE version = (SELECT max(version) FROM filter_versions)
  AND created_by = 'system' AND json_type(filters, '$.search') IS NULL
  AND NOT EXISTS (SELECT 1 FROM filter_versions WHERE active = 1);
