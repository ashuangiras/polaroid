-- Migration 12: guided-procedure walkthroughs (ADR-0053): per-agent
-- runtime progress through a procedure's composed steps, in an
-- append-only event log whose current state is folded per key, not
-- "latest row wins" the way other append-only tables here work.
-- Applied migrations are history: never edit this file; add a new one.

-- Unlike every other table in this schema, these rows are not kept
-- forever: an expired token's rows are deleted outright (internal/memory
-- and internal/storage/sqlite's DeleteWalkthrough), the deliberate,
-- narrow exception ADR-0053 describes as "the first record in Polaroid
-- that is not kept forever". There is therefore no immutability or
-- undeletable trigger here, unlike every other table.
CREATE TABLE walkthrough_events (
    id             INTEGER NOT NULL PRIMARY KEY,
    token          TEXT NOT NULL,
    -- goal/snapshot/repository are set only on a token's first (creating)
    -- event; later events leave them NULL.
    goal           TEXT,
    snapshot       TEXT,
    repository     TEXT,
    -- outcome_key/outcome_status/note are set only on an advance call that
    -- reports an outcome; NULL for the creating event or a peek.
    outcome_key    TEXT,
    outcome_status TEXT CHECK (outcome_status IS NULL OR outcome_status IN ('done', 'skipped', 'escalate')),
    note           TEXT,
    context        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(context) AND json_type(context) = 'object'),
    created_at     TEXT NOT NULL
) STRICT;

CREATE INDEX walkthrough_events_by_token ON walkthrough_events (token, id);
