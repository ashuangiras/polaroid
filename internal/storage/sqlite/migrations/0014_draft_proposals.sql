-- Migration 14: interactive, field-by-field proposal drafts (ADR-0060):
-- per-agent runtime progress through one Procedure's still-undecided
-- content fields, in an append-only event log whose current state is
-- folded per field, the same shape as walkthrough_events.
-- Applied migrations are history: never edit this file; add a new one.

-- Like walkthrough_events (migration 12), these rows are not kept
-- forever: an expired token's rows are deleted outright (internal/memory
-- and internal/storage/sqlite's DeleteDraft), the same "not kept
-- forever" exception ADR-0053 first described, now shared by a second
-- kind of runtime state. No immutability or undeletable trigger here,
-- unlike every other table.
CREATE TABLE draft_events (
    id              INTEGER NOT NULL PRIMARY KEY,
    token           TEXT NOT NULL,
    -- key/snapshot/proposer/rationale are set only on a token's first
    -- (creating) event; later events leave them NULL.
    key             TEXT,
    snapshot        TEXT,
    proposer        TEXT,
    rationale       TEXT,
    -- answer_field/answer_values/answer_citations/confirmed_absent/reason
    -- are set only on an advance call that answers a field; NULL for the
    -- creating event or a peek.
    answer_field       TEXT CHECK (answer_field IS NULL OR answer_field IN
                           ('preconditions', 'postconditions', 'invariants', 'informal_conditions')),
    answer_values      TEXT CHECK (answer_values IS NULL OR (json_valid(answer_values) AND json_type(answer_values) = 'array')),
    answer_citations   TEXT CHECK (answer_citations IS NULL OR (json_valid(answer_citations) AND json_type(answer_citations) = 'array')),
    confirmed_absent   INTEGER CHECK (confirmed_absent IS NULL OR confirmed_absent IN (0, 1)),
    reason             TEXT,
    created_at      TEXT NOT NULL
) STRICT;

CREATE INDEX draft_events_by_token ON draft_events (token, id);
