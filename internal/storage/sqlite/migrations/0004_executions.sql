-- Migration 4: immutable execution records.
-- Applied migrations are history: never edit this file; add a new one.

CREATE TABLE executions (
    id                     TEXT    NOT NULL PRIMARY KEY,
    procedure_id           TEXT    NOT NULL,
    version                INTEGER NOT NULL,
    binding_id             TEXT,
    binding_revision       INTEGER,
    repository             TEXT    NOT NULL
                           CHECK (length(repository) BETWEEN 1 AND 255
                                  AND repository NOT GLOB '*[^a-z0-9._/-]*'
                                  AND repository NOT GLOB '/*'
                                  AND repository NOT GLOB '*/'
                                  AND repository NOT GLOB '*//*'),
    commit_hash            TEXT    NOT NULL
                           CHECK (length(commit_hash) IN (40, 64) AND commit_hash NOT GLOB '*[^0-9a-f]*'),
    environment_name       TEXT    NOT NULL
                           CHECK (length(environment_name) BETWEEN 1 AND 128
                                  AND environment_name NOT GLOB '*[^a-z0-9._-]*'),
    environment_attributes TEXT    NOT NULL
                           CHECK (json_valid(environment_attributes) AND json_type(environment_attributes) = 'object'),
    inputs                 TEXT    NOT NULL CHECK (json_valid(inputs) AND json_type(inputs) = 'object'),
    outcome                TEXT    NOT NULL CHECK (outcome IN ('succeeded', 'failed')),
    evidence               TEXT    NOT NULL
                           CHECK (json_valid(evidence) AND json_type(evidence) = 'object' AND length(json(evidence)) > 2),
    created_at             TEXT    NOT NULL,
    FOREIGN KEY (procedure_id, version) REFERENCES procedure_versions (procedure_id, version),
    FOREIGN KEY (binding_id, binding_revision) REFERENCES binding_revisions (binding_id, revision),
    CHECK ((binding_id IS NULL) = (binding_revision IS NULL))
) STRICT;

CREATE INDEX executions_by_procedure ON executions (procedure_id, created_at, id);

-- A binding revision must be for the same procedure and repository, and a
-- pinned revision must pin the version that ran.
CREATE TRIGGER executions_match_binding
BEFORE INSERT ON executions
WHEN NEW.binding_id IS NOT NULL AND NOT EXISTS (
    SELECT 1
    FROM bindings AS b
    JOIN binding_revisions AS r ON r.binding_id = b.id
    WHERE b.id = NEW.binding_id AND r.revision = NEW.binding_revision
      AND b.procedure_id = NEW.procedure_id AND b.repository = NEW.repository
      AND (r.policy = 'contextual' OR r.pinned_version = NEW.version))
BEGIN
    SELECT RAISE(ABORT, 'execution does not match its binding revision');
END;

CREATE TRIGGER executions_immutable
BEFORE UPDATE ON executions
BEGIN
    SELECT RAISE(ABORT, 'executions are immutable');
END;

CREATE TRIGGER executions_undeletable
BEFORE DELETE ON executions
BEGIN
    SELECT RAISE(ABORT, 'executions cannot be deleted');
END;
