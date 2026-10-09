-- Migration 6: immutable feedback reports about Polaroid itself (ADR-0015).
-- Applied migrations are history: never edit this file; add a new one.

CREATE TABLE feedback (
    id         TEXT NOT NULL PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('problem', 'suggestion')),
    summary    TEXT NOT NULL
               CHECK (length(trim(summary, char(32, 9, 10, 11, 12, 13))) > 0
                      AND instr(summary, char(10)) = 0 AND instr(summary, char(11)) = 0
                      AND instr(summary, char(12)) = 0 AND instr(summary, char(13)) = 0
                      AND instr(summary, char(133)) = 0 AND instr(summary, char(8232)) = 0
                      AND instr(summary, char(8233)) = 0),
    details    TEXT NOT NULL CHECK (length(trim(details, char(32, 9, 10, 11, 12, 13))) > 0),
    reporter   TEXT NOT NULL
               CHECK (length(reporter) BETWEEN 1 AND 128
                      AND reporter NOT GLOB '*[^a-z0-9._-]*'),
    context    TEXT NOT NULL CHECK (json_valid(context) AND json_type(context) = 'object'),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX feedback_by_time ON feedback (created_at, id);
CREATE INDEX feedback_by_kind ON feedback (kind, created_at, id);

CREATE TRIGGER feedback_immutable
BEFORE UPDATE ON feedback
BEGIN
    SELECT RAISE(ABORT, 'feedback reports are immutable');
END;

CREATE TRIGGER feedback_undeletable
BEFORE DELETE ON feedback
BEGIN
    SELECT RAISE(ABORT, 'feedback reports cannot be deleted');
END;
