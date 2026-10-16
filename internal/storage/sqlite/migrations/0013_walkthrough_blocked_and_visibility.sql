-- Migration 13: a fourth agent-reported outcome status, blocked, with an
-- optional reason, and operator visibility (list_walkthroughs,
-- get_walkthrough) over walkthroughs (ADR-0056).
-- Applied migrations are history: never edit this file; add a new one.

-- Widening outcome_status's CHECK constraint and adding reason needs
-- rebuilding this table (SQLite cannot ALTER a CHECK constraint in
-- place). Walkthroughs are explicitly not kept forever (ADR-0053): this
-- discards any in-flight walkthrough across the upgrade, exactly the
-- same outcome as simply letting one expire already has -- no guarantee
-- this schema makes is lost.
DROP TABLE walkthrough_events;

CREATE TABLE walkthrough_events (
    id             INTEGER NOT NULL PRIMARY KEY,
    token          TEXT NOT NULL,
    -- goal/snapshot/repository are set only on a token's first (creating)
    -- event; later events leave them NULL.
    goal           TEXT,
    snapshot       TEXT,
    repository     TEXT,
    -- outcome_key/outcome_status/reason/note are set only on an advance
    -- call that reports an outcome; NULL for the creating event or a peek.
    outcome_key    TEXT,
    outcome_status TEXT CHECK (outcome_status IS NULL OR outcome_status IN ('done', 'skipped', 'escalate', 'blocked')),
    reason         TEXT,
    note           TEXT,
    context        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(context) AND json_type(context) = 'object'),
    created_at     TEXT NOT NULL
) STRICT;

CREATE INDEX walkthrough_events_by_token ON walkthrough_events (token, id);
