-- Migration 15: field-complete proposal drafts (ADR-0062). The v5
-- draft_events table is frozen at four condition-list fields; v6 drafts
-- use this generic event log for every applicable Procedure-document and
-- typed-record field.
CREATE TABLE field_draft_events (
    id                 INTEGER NOT NULL PRIMARY KEY,
    token              TEXT NOT NULL,
    key                TEXT,
    snapshot           TEXT,
    record_type        TEXT,
    is_new             INTEGER CHECK (is_new IS NULL OR is_new IN (0, 1)),
    proposer           TEXT,
    rationale          TEXT,
    answer_entity      TEXT CHECK (answer_entity IS NULL OR answer_entity IN ('procedure', 'record')),
    answer_field       TEXT,
    answer_operation   TEXT CHECK (answer_operation IS NULL OR answer_operation IN ('keep', 'add', 'remove', 'replace', 'clear')),
    answer_value       TEXT CHECK (answer_value IS NULL OR json_valid(answer_value)),
    answer_citations   TEXT CHECK (answer_citations IS NULL OR (json_valid(answer_citations) AND json_type(answer_citations) = 'array')),
    created_at         TEXT NOT NULL
) STRICT;

CREATE INDEX field_draft_events_by_token ON field_draft_events (token, id);
