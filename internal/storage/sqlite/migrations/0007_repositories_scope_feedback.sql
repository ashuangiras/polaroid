-- Migration 7: the repository registry, procedure origin and applicability,
-- and feedback subjects (ADR-0019, ADR-0020, ADR-0021).
-- Applied migrations are history: never edit this file; add a new one.
-- Existing rows are never updated: every new column is NULL for them, which
-- means "absent" (unspecified applicability, no goal, no subject).

CREATE TABLE repositories (
    id         TEXT NOT NULL PRIMARY KEY,
    name       TEXT NOT NULL
               CHECK (length(trim(name, char(32, 9))) > 0
                      AND instr(name, char(10)) = 0 AND instr(name, char(11)) = 0
                      AND instr(name, char(12)) = 0 AND instr(name, char(13)) = 0
                      AND instr(name, char(133)) = 0 AND instr(name, char(8232)) = 0
                      AND instr(name, char(8233)) = 0),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX repositories_by_time ON repositories (created_at, id);

-- Every identifier belongs to exactly one repository, forever. Each
-- repository has one canonical identifier; aliases carry a reason.
CREATE TABLE repository_identifiers (
    identifier    TEXT    NOT NULL PRIMARY KEY
                  CHECK (length(identifier) BETWEEN 1 AND 255
                         AND identifier NOT GLOB '*[^a-z0-9._/-]*'
                         AND identifier NOT GLOB '/*'
                         AND identifier NOT GLOB '*/'
                         AND identifier NOT GLOB '*//*'
                         AND identifier NOT GLOB '*.git'),
    repository_id TEXT    NOT NULL REFERENCES repositories (id),
    canonical     INTEGER NOT NULL CHECK (canonical IN (0, 1)),
    reason        TEXT,
    created_at    TEXT    NOT NULL,
    CHECK ((canonical = 1 AND reason IS NULL)
           OR (canonical = 0 AND reason IS NOT NULL AND length(trim(reason, char(32, 9, 10, 11, 12, 13))) > 0))
) STRICT;

CREATE UNIQUE INDEX repository_identifiers_canonical ON repository_identifiers (repository_id) WHERE canonical = 1;
CREATE INDEX repository_identifiers_by_repository ON repository_identifiers (repository_id, created_at);

CREATE TRIGGER repositories_immutable
BEFORE UPDATE ON repositories
BEGIN
    SELECT RAISE(ABORT, 'repositories are immutable');
END;

CREATE TRIGGER repositories_undeletable
BEFORE DELETE ON repositories
BEGIN
    SELECT RAISE(ABORT, 'repositories cannot be deleted');
END;

CREATE TRIGGER repository_identifiers_immutable
BEFORE UPDATE ON repository_identifiers
BEGIN
    SELECT RAISE(ABORT, 'repository identifiers are immutable');
END;

CREATE TRIGGER repository_identifiers_undeletable
BEFORE DELETE ON repository_identifiers
BEGIN
    SELECT RAISE(ABORT, 'repository identifiers cannot be deleted');
END;

-- Where and why a procedure was first created, recorded at most once.
CREATE TABLE procedure_origins (
    procedure_id  TEXT NOT NULL PRIMARY KEY REFERENCES procedures (id),
    repository_id TEXT NOT NULL REFERENCES repositories (id),
    reason        TEXT NOT NULL CHECK (length(trim(reason, char(32, 9, 10, 11, 12, 13))) > 0),
    created_at    TEXT NOT NULL
) STRICT;

CREATE TRIGGER procedure_origins_immutable
BEFORE UPDATE ON procedure_origins
BEGIN
    SELECT RAISE(ABORT, 'procedure origins are immutable');
END;

CREATE TRIGGER procedure_origins_undeletable
BEFORE DELETE ON procedure_origins
BEGIN
    SELECT RAISE(ABORT, 'procedure origins cannot be deleted');
END;

-- A version's goal and applicability. NULL scope is "unspecified".
ALTER TABLE procedure_versions ADD COLUMN goal TEXT
    CHECK (goal IS NULL
           OR (length(trim(goal, char(32, 9))) > 0
               AND instr(goal, char(10)) = 0 AND instr(goal, char(11)) = 0
               AND instr(goal, char(12)) = 0 AND instr(goal, char(13)) = 0
               AND instr(goal, char(133)) = 0 AND instr(goal, char(8232)) = 0
               AND instr(goal, char(8233)) = 0));
ALTER TABLE procedure_versions ADD COLUMN scope TEXT CHECK (scope IS NULL OR scope IN ('shared', 'local'));
ALTER TABLE procedure_versions ADD COLUMN scope_repository_id TEXT REFERENCES repositories (id);

CREATE TRIGGER procedure_versions_scope_consistent
BEFORE INSERT ON procedure_versions
WHEN (NEW.scope IS 'local') <> (NEW.scope_repository_id IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'a local scope names exactly one repository, and only a local scope does');
END;

-- A report's subject, and the repository and execution it was made in.
ALTER TABLE feedback ADD COLUMN subject_type TEXT
    CHECK (subject_type IS NULL OR subject_type IN ('service', 'repository', 'procedure', 'binding', 'execution'));
ALTER TABLE feedback ADD COLUMN subject_id TEXT;
ALTER TABLE feedback ADD COLUMN subject_version INTEGER CHECK (subject_version IS NULL OR subject_version >= 1);
ALTER TABLE feedback ADD COLUMN repository TEXT
    CHECK (repository IS NULL
           OR (length(repository) BETWEEN 1 AND 255
               AND repository NOT GLOB '*[^a-z0-9._/-]*'
               AND repository NOT GLOB '/*'
               AND repository NOT GLOB '*/'
               AND repository NOT GLOB '*//*'));
ALTER TABLE feedback ADD COLUMN execution_id TEXT REFERENCES executions (id);

CREATE TRIGGER feedback_subject_exists
BEFORE INSERT ON feedback
WHEN NOT (
    (NEW.subject_type IS NULL AND NEW.subject_id IS NULL AND NEW.subject_version IS NULL)
    OR (NEW.subject_type = 'service' AND NEW.subject_id IS NULL AND NEW.subject_version IS NULL)
    OR (NEW.subject_type = 'repository' AND NEW.subject_version IS NULL
        AND EXISTS (SELECT 1 FROM repositories WHERE id = NEW.subject_id))
    OR (NEW.subject_type = 'procedure'
        AND EXISTS (SELECT 1 FROM procedure_versions
                    WHERE procedure_id = NEW.subject_id AND version = COALESCE(NEW.subject_version, 1)))
    OR (NEW.subject_type = 'binding'
        AND EXISTS (SELECT 1 FROM binding_revisions
                    WHERE binding_id = NEW.subject_id AND revision = COALESCE(NEW.subject_version, 1)))
    OR (NEW.subject_type = 'execution' AND NEW.subject_version IS NULL
        AND EXISTS (SELECT 1 FROM executions WHERE id = NEW.subject_id)))
BEGIN
    SELECT RAISE(ABORT, 'feedback subject does not exist');
END;

CREATE INDEX feedback_by_subject ON feedback (subject_type, subject_id, created_at, id);
CREATE INDEX executions_by_time ON executions (created_at, id);
CREATE INDEX bindings_by_name ON bindings (name, id);
