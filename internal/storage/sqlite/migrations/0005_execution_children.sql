-- Migration 5: links from a parent execution to the child executions that
-- fulfilled its references (ADR-0011).
-- Applied migrations are history: never edit this file; add a new one.

-- Lets a link name the parent's run as one foreign key.
CREATE UNIQUE INDEX executions_run ON executions (id, procedure_id, version, repository, commit_hash);

CREATE TABLE execution_children (
    parent_execution_id TEXT    NOT NULL,
    parent_procedure_id TEXT    NOT NULL,
    parent_version      INTEGER NOT NULL,
    parent_repository   TEXT    NOT NULL,
    parent_commit_hash  TEXT    NOT NULL,
    position            INTEGER NOT NULL CHECK (position >= 0),
    reference           TEXT    NOT NULL,
    child_execution_id  TEXT    NOT NULL UNIQUE REFERENCES executions (id),
    PRIMARY KEY (parent_execution_id, position),
    UNIQUE (parent_execution_id, reference),
    -- Deferred: links are inserted before their parent, in one transaction.
    FOREIGN KEY (parent_execution_id, parent_procedure_id, parent_version, parent_repository, parent_commit_hash)
        REFERENCES executions (id, procedure_id, version, repository, commit_hash)
        DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (parent_procedure_id, parent_version, reference)
        REFERENCES procedure_version_references (procedure_id, version, name)
) STRICT;

-- Links are written only together with their parent.
CREATE TRIGGER execution_children_with_parent
BEFORE INSERT ON execution_children
WHEN EXISTS (SELECT 1 FROM executions WHERE id = NEW.parent_execution_id)
BEGIN
    SELECT RAISE(ABORT, 'children are linked only when their parent is recorded');
END;

-- The child ran the reference's target (at the pinned version, if pinned)
-- in the parent's repository and commit.
CREATE TRIGGER execution_children_match_reference
BEFORE INSERT ON execution_children
WHEN NOT EXISTS (
    SELECT 1
    FROM executions AS child
    JOIN procedure_version_references AS r
      ON r.procedure_id = NEW.parent_procedure_id AND r.version = NEW.parent_version AND r.name = NEW.reference
    WHERE child.id = NEW.child_execution_id
      AND child.procedure_id = r.target_procedure_id
      AND (r.policy = 'contextual' OR r.pinned_version = child.version)
      AND child.repository = NEW.parent_repository
      AND child.commit_hash = NEW.parent_commit_hash)
BEGIN
    SELECT RAISE(ABORT, 'child execution does not fulfil the reference');
END;

CREATE TRIGGER execution_children_immutable
BEFORE UPDATE ON execution_children
BEGIN
    SELECT RAISE(ABORT, 'execution children are immutable');
END;

CREATE TRIGGER execution_children_undeletable
BEFORE DELETE ON execution_children
BEGIN
    SELECT RAISE(ABORT, 'execution children cannot be deleted');
END;
