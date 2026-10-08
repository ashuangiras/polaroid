-- Migration 1: procedure identities and immutable procedure versions.
-- Applied migrations are history: never edit this file; add a new one.

CREATE TABLE procedures (
    id            TEXT NOT NULL PRIMARY KEY,
    canonical_key TEXT NOT NULL UNIQUE
                  CHECK (length(canonical_key) BETWEEN 1 AND 128
                         AND canonical_key NOT GLOB '*[^a-z0-9._-]*'),
    created_at    TEXT NOT NULL
) STRICT;

CREATE TABLE procedure_versions (
    procedure_id    TEXT    NOT NULL REFERENCES procedures (id),
    version         INTEGER NOT NULL CHECK (version >= 1),
    philosophy      TEXT    NOT NULL CHECK (philosophy <> ''),
    method          TEXT    NOT NULL CHECK (method <> ''),
    contract        TEXT    NOT NULL CHECK (json_valid(contract) AND json_type(contract) = 'object'),
    instructions    TEXT    NOT NULL CHECK (json_valid(instructions) AND json_type(instructions) = 'object'),
    revision_reason TEXT    NOT NULL CHECK (revision_reason <> ''),
    created_at      TEXT    NOT NULL,
    PRIMARY KEY (procedure_id, version)
) STRICT;

-- Each procedure's versions are numbered 1, 2, 3, ... without gaps.
CREATE TRIGGER procedure_versions_contiguous
BEFORE INSERT ON procedure_versions
WHEN NEW.version <> 1 + COALESCE(
    (SELECT MAX(version) FROM procedure_versions WHERE procedure_id = NEW.procedure_id), 0)
BEGIN
    SELECT RAISE(ABORT, 'procedure versions must be contiguous');
END;

-- Identities and versions are never changed or removed.
CREATE TRIGGER procedures_immutable
BEFORE UPDATE ON procedures
BEGIN
    SELECT RAISE(ABORT, 'procedures are immutable');
END;

CREATE TRIGGER procedures_undeletable
BEFORE DELETE ON procedures
BEGIN
    SELECT RAISE(ABORT, 'procedures cannot be deleted');
END;

CREATE TRIGGER procedure_versions_immutable
BEFORE UPDATE ON procedure_versions
BEGIN
    SELECT RAISE(ABORT, 'procedure versions are immutable');
END;

CREATE TRIGGER procedure_versions_undeletable
BEFORE DELETE ON procedure_versions
BEGIN
    SELECT RAISE(ABORT, 'procedure versions cannot be deleted');
END;
