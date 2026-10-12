-- Migration 11: feedback resolution, an append-only disposition record next
-- to an immutable report (ADR-0040).
-- Applied migrations are history: never edit this file; add a new one.
-- Only a new table: no existing row changes.

-- At most one resolution per report: resolving is irreversible.
CREATE TABLE feedback_resolutions (
    feedback_id    TEXT NOT NULL PRIMARY KEY REFERENCES feedback (id),
    outcome        TEXT NOT NULL CHECK (outcome IN ('addressed', 'declined', 'not_reproduced', 'duplicate')),
    reason         TEXT NOT NULL CHECK (length(trim(reason, char(32, 9, 10, 11, 12, 13))) > 0),
    actor          TEXT NOT NULL
                   CHECK (length(actor) BETWEEN 1 AND 128
                          AND actor NOT GLOB '*[^a-z0-9._-]*'
                          AND actor NOT GLOB '[._-]*'
                          AND actor NOT GLOB '*[._-]'
                          AND actor NOT GLOB '*[._-][._-]*'),
    references_json TEXT NOT NULL DEFAULT '[]'
                   CHECK (json_valid(references_json) AND json_type(references_json) = 'array'),
    created_at     TEXT NOT NULL
) STRICT;

-- Feedback resolutions are never changed or removed.
CREATE TRIGGER feedback_resolutions_immutable
BEFORE UPDATE ON feedback_resolutions
BEGIN
    SELECT RAISE(ABORT, 'feedback resolutions are immutable');
END;

CREATE TRIGGER feedback_resolutions_undeletable
BEFORE DELETE ON feedback_resolutions
BEGIN
    SELECT RAISE(ABORT, 'feedback resolutions cannot be deleted');
END;
