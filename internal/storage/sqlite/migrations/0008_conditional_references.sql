-- Migration 8: conditional references and the applicability decisions of
-- executions (ADR-0029).
-- Applied migrations are history: never edit this file; add a new one.

-- NULL means required, so every reference stored before this migration stays
-- required. The existing triggers keep references immutable.
ALTER TABLE procedure_version_references ADD COLUMN condition TEXT
    CHECK (condition IS NULL OR length(trim(condition)) > 0);

CREATE TABLE execution_decisions (
    parent_execution_id TEXT    NOT NULL,
    parent_procedure_id TEXT    NOT NULL,
    parent_version      INTEGER NOT NULL,
    parent_repository   TEXT    NOT NULL,
    parent_commit_hash  TEXT    NOT NULL,
    position            INTEGER NOT NULL CHECK (position >= 0),
    reference           TEXT    NOT NULL,
    applicable          INTEGER NOT NULL CHECK (applicable IN (0, 1)),
    rationale           TEXT    NOT NULL CHECK (length(trim(rationale)) > 0),
    evidence            TEXT    CHECK (evidence IS NULL OR (json_valid(evidence) AND json_type(evidence) = 'object')),
    PRIMARY KEY (parent_execution_id, position),
    UNIQUE (parent_execution_id, reference),
    -- Deferred: decisions are inserted before their parent, in one transaction.
    FOREIGN KEY (parent_execution_id, parent_procedure_id, parent_version, parent_repository, parent_commit_hash)
        REFERENCES executions (id, procedure_id, version, repository, commit_hash)
        DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (parent_procedure_id, parent_version, reference)
        REFERENCES procedure_version_references (procedure_id, version, name)
) STRICT;

-- Decisions are written only together with their parent.
CREATE TRIGGER execution_decisions_with_parent
BEFORE INSERT ON execution_decisions
WHEN EXISTS (SELECT 1 FROM executions WHERE id = NEW.parent_execution_id)
BEGIN
    SELECT RAISE(ABORT, 'decisions are recorded only when their parent is recorded');
END;

-- Only a conditional reference takes a decision; a required one cannot be
-- skipped.
CREATE TRIGGER execution_decisions_conditional
BEFORE INSERT ON execution_decisions
WHEN NOT EXISTS (
    SELECT 1 FROM procedure_version_references
    WHERE procedure_id = NEW.parent_procedure_id AND version = NEW.parent_version
      AND name = NEW.reference AND condition IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'only a conditional reference takes an applicability decision');
END;

-- A reference that does not apply has no child, whichever is inserted first.
CREATE TRIGGER execution_decisions_not_applicable_unlinked
BEFORE INSERT ON execution_decisions
WHEN NEW.applicable = 0 AND EXISTS (
    SELECT 1 FROM execution_children
    WHERE parent_execution_id = NEW.parent_execution_id AND reference = NEW.reference)
BEGIN
    SELECT RAISE(ABORT, 'a reference decided not applicable has no child');
END;

CREATE TRIGGER execution_children_not_skipped
BEFORE INSERT ON execution_children
WHEN EXISTS (
    SELECT 1 FROM execution_decisions
    WHERE parent_execution_id = NEW.parent_execution_id AND reference = NEW.reference AND applicable = 0)
BEGIN
    SELECT RAISE(ABORT, 'a reference decided not applicable has no child');
END;

CREATE TRIGGER execution_decisions_immutable
BEFORE UPDATE ON execution_decisions
BEGIN
    SELECT RAISE(ABORT, 'execution decisions are immutable');
END;

CREATE TRIGGER execution_decisions_undeletable
BEFORE DELETE ON execution_decisions
BEGIN
    SELECT RAISE(ABORT, 'execution decisions cannot be deleted');
END;
