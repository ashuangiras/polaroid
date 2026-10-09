-- Migration 2: repository bindings and immutable binding revisions.
-- Applied migrations are history: never edit this file; add a new one.

CREATE TABLE bindings (
    id           TEXT NOT NULL PRIMARY KEY,
    repository   TEXT NOT NULL
                 CHECK (length(repository) BETWEEN 1 AND 255
                        AND repository NOT GLOB '*[^a-z0-9._/-]*'
                        AND repository NOT GLOB '/*'
                        AND repository NOT GLOB '*/'
                        AND repository NOT GLOB '*//*'),
    name         TEXT NOT NULL
                 CHECK (length(name) BETWEEN 1 AND 128
                        AND name NOT GLOB '*[^a-z0-9._-]*'),
    procedure_id TEXT NOT NULL REFERENCES procedures (id),
    created_at   TEXT NOT NULL,
    UNIQUE (repository, name)
) STRICT;

CREATE TABLE binding_revisions (
    binding_id      TEXT    NOT NULL REFERENCES bindings (id),
    revision        INTEGER NOT NULL CHECK (revision >= 1),
    inputs          TEXT    NOT NULL CHECK (json_valid(inputs) AND json_type(inputs) = 'object'),
    policy          TEXT    NOT NULL CHECK (policy IN ('pin', 'contextual')),
    pinned_version  INTEGER,
    revision_reason TEXT    NOT NULL CHECK (revision_reason <> ''),
    created_at      TEXT    NOT NULL,
    PRIMARY KEY (binding_id, revision),
    -- A pin names a version; a contextual policy names none.
    CHECK ((policy = 'pin' AND pinned_version IS NOT NULL AND pinned_version >= 1)
           OR (policy = 'contextual' AND pinned_version IS NULL))
) STRICT;

-- Each binding's revisions are numbered 1, 2, 3, ... without gaps.
CREATE TRIGGER binding_revisions_contiguous
BEFORE INSERT ON binding_revisions
WHEN NEW.revision <> 1 + COALESCE(
    (SELECT MAX(revision) FROM binding_revisions WHERE binding_id = NEW.binding_id), 0)
BEGIN
    SELECT RAISE(ABORT, 'binding revisions must be contiguous');
END;

-- A pinned version must exist in the bound procedure.
CREATE TRIGGER binding_revisions_pin_exists
BEFORE INSERT ON binding_revisions
WHEN NEW.policy = 'pin' AND NOT EXISTS (
    SELECT 1
    FROM bindings AS b
    JOIN procedure_versions AS v ON v.procedure_id = b.procedure_id
    WHERE b.id = NEW.binding_id AND v.version = NEW.pinned_version)
BEGIN
    SELECT RAISE(ABORT, 'pinned version does not exist');
END;

-- Bindings and their revisions are never changed or removed.
CREATE TRIGGER bindings_immutable
BEFORE UPDATE ON bindings
BEGIN
    SELECT RAISE(ABORT, 'bindings are immutable');
END;

CREATE TRIGGER bindings_undeletable
BEFORE DELETE ON bindings
BEGIN
    SELECT RAISE(ABORT, 'bindings cannot be deleted');
END;

CREATE TRIGGER binding_revisions_immutable
BEFORE UPDATE ON binding_revisions
BEGIN
    SELECT RAISE(ABORT, 'binding revisions are immutable');
END;

CREATE TRIGGER binding_revisions_undeletable
BEFORE DELETE ON binding_revisions
BEGIN
    SELECT RAISE(ABORT, 'binding revisions cannot be deleted');
END;
