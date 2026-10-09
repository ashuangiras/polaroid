-- Migration 3: named subprocedure references of procedure versions.
-- Applied migrations are history: never edit this file; add a new one.

CREATE TABLE procedure_version_references (
    procedure_id        TEXT    NOT NULL,
    version             INTEGER NOT NULL,
    position            INTEGER NOT NULL CHECK (position >= 0),
    name                TEXT    NOT NULL
                        CHECK (length(name) BETWEEN 1 AND 128
                               AND name NOT GLOB '*[^a-z0-9._-]*'),
    target_procedure_id TEXT    NOT NULL REFERENCES procedures (id),
    policy              TEXT    NOT NULL CHECK (policy IN ('pin', 'contextual')),
    pinned_version      INTEGER,
    inputs              TEXT    NOT NULL CHECK (json_valid(inputs) AND json_type(inputs) = 'object'),
    PRIMARY KEY (procedure_id, version, position),
    UNIQUE (procedure_id, version, name),
    -- Deferred: references are inserted before their version, in one transaction.
    FOREIGN KEY (procedure_id, version) REFERENCES procedure_versions (procedure_id, version)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK ((policy = 'pin' AND pinned_version IS NOT NULL AND pinned_version >= 1)
           OR (policy = 'contextual' AND pinned_version IS NULL))
) STRICT;

-- References are written only together with their version: once a version
-- exists, nothing can be added to it.
CREATE TRIGGER procedure_version_references_with_version
BEFORE INSERT ON procedure_version_references
WHEN EXISTS (
    SELECT 1 FROM procedure_versions
    WHERE procedure_id = NEW.procedure_id AND version = NEW.version)
BEGIN
    SELECT RAISE(ABORT, 'references are written only with their version');
END;

-- A pinned version must exist in the target procedure.
CREATE TRIGGER procedure_version_references_pin_exists
BEFORE INSERT ON procedure_version_references
WHEN NEW.policy = 'pin' AND NOT EXISTS (
    SELECT 1 FROM procedure_versions
    WHERE procedure_id = NEW.target_procedure_id AND version = NEW.pinned_version)
BEGIN
    SELECT RAISE(ABORT, 'pinned version does not exist');
END;

CREATE TRIGGER procedure_version_references_immutable
BEFORE UPDATE ON procedure_version_references
BEGIN
    SELECT RAISE(ABORT, 'procedure version references are immutable');
END;

CREATE TRIGGER procedure_version_references_undeletable
BEFORE DELETE ON procedure_version_references
BEGIN
    SELECT RAISE(ABORT, 'procedure version references cannot be deleted');
END;
