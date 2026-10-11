-- Migration 9: toil items, versioned assessments of repetitive work (ADR-0034).
-- Applied migrations are history: never edit this file; add a new one.
-- Only new tables: no existing row changes.

CREATE TABLE toil_items (
    id            TEXT NOT NULL PRIMARY KEY,
    canonical_key TEXT NOT NULL UNIQUE
                  CHECK (length(canonical_key) BETWEEN 1 AND 128
                         AND canonical_key NOT GLOB '*[^a-z0-9._-]*'
                         AND canonical_key NOT GLOB '[._-]*'
                         AND canonical_key NOT GLOB '*[._-]'
                         AND canonical_key NOT GLOB '*[._-][._-]*'),
    created_at    TEXT NOT NULL
) STRICT;

CREATE TABLE toil_revisions (
    toil_item_id           TEXT    NOT NULL REFERENCES toil_items (id),
    revision               INTEGER NOT NULL CHECK (revision >= 1),
    activity               TEXT    NOT NULL
                           CHECK (length(trim(activity, char(32, 9))) > 0
                                  AND instr(activity, char(10)) = 0 AND instr(activity, char(11)) = 0
                                  AND instr(activity, char(12)) = 0 AND instr(activity, char(13)) = 0
                                  AND instr(activity, char(133)) = 0 AND instr(activity, char(8232)) = 0
                                  AND instr(activity, char(8233)) = 0),
    scope                  TEXT    NOT NULL CHECK (scope IN ('shared', 'local')),
    scope_repository_id    TEXT    REFERENCES repositories (id),
    assessment             TEXT    NOT NULL CHECK (json_valid(assessment) AND json_type(assessment) = 'object'
                                                   AND json_type(assessment, '$.repetition.status') IS 'text'
                                                   AND json_type(assessment, '$.burden.status') IS 'text'),
    proposal               TEXT    CHECK (proposal IS NULL
                                          OR (json_valid(proposal) AND json_type(proposal) = 'object'
                                              AND json_type(proposal, '$.acceptance_criteria') IS 'array'
                                              AND json_array_length(proposal, '$.acceptance_criteria') > 0)),
    status                 TEXT    NOT NULL
                           CHECK (status IN ('observed', 'candidate', 'implemented', 'validated', 'retired')),
    status_rationale       TEXT    NOT NULL CHECK (length(trim(status_rationale, char(32, 9, 10, 11, 12, 13))) > 0),
    replacement_summary    TEXT    CHECK (replacement_summary IS NULL
                                          OR length(trim(replacement_summary, char(32, 9, 10, 11, 12, 13))) > 0),
    replacement_references TEXT    CHECK (replacement_references IS NULL
                                          OR (json_valid(replacement_references) AND json_type(replacement_references) = 'array'
                                              AND json_array_length(replacement_references) > 0)),
    validation_rationale   TEXT    CHECK (validation_rationale IS NULL
                                          OR length(trim(validation_rationale, char(32, 9, 10, 11, 12, 13))) > 0),
    validation_references  TEXT    CHECK (validation_references IS NULL
                                          OR (json_valid(validation_references) AND json_type(validation_references) = 'array')),
    revision_reason        TEXT    NOT NULL CHECK (length(trim(revision_reason, char(32, 9, 10, 11, 12, 13))) > 0),
    author                 TEXT    NOT NULL
                           CHECK (length(author) BETWEEN 1 AND 128 AND author NOT GLOB '*[^a-z0-9._-]*'),
    created_at             TEXT    NOT NULL,
    PRIMARY KEY (toil_item_id, revision),
    CHECK ((scope = 'local') = (scope_repository_id IS NOT NULL)),
    CHECK ((replacement_summary IS NULL) = (replacement_references IS NULL)),
    CHECK (validation_rationale IS NOT NULL OR validation_references IS NULL),
    -- What each status requires and allows; retired allows everything.
    CHECK (CASE status
        WHEN 'observed' THEN replacement_summary IS NULL AND validation_rationale IS NULL
        WHEN 'candidate' THEN proposal IS NOT NULL AND replacement_summary IS NULL AND validation_rationale IS NULL
        WHEN 'implemented' THEN proposal IS NOT NULL AND replacement_summary IS NOT NULL AND validation_rationale IS NULL
        WHEN 'validated' THEN proposal IS NOT NULL AND replacement_summary IS NOT NULL AND validation_rationale IS NOT NULL
        ELSE 1 END)
) STRICT;

CREATE INDEX toil_revisions_by_status ON toil_revisions (status);

-- The exact procedure versions a revision cites, with declared step
-- identifiers (a JSON array of strings, or NULL).
CREATE TABLE toil_revision_sources (
    toil_item_id TEXT    NOT NULL,
    revision     INTEGER NOT NULL,
    position     INTEGER NOT NULL CHECK (position >= 0),
    procedure_id TEXT    NOT NULL,
    version      INTEGER NOT NULL CHECK (version >= 1),
    steps        TEXT    CHECK (steps IS NULL
                                OR (json_valid(steps) AND json_type(steps) = 'array' AND json_array_length(steps) > 0)),
    PRIMARY KEY (toil_item_id, revision, position),
    UNIQUE (toil_item_id, revision, procedure_id, version),
    FOREIGN KEY (procedure_id, version) REFERENCES procedure_versions (procedure_id, version),
    -- Deferred: links are inserted before their revision, in one transaction.
    FOREIGN KEY (toil_item_id, revision) REFERENCES toil_revisions (toil_item_id, revision)
        DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE INDEX toil_revision_sources_by_procedure ON toil_revision_sources (procedure_id, toil_item_id, revision);

-- The executions a revision cites: observations support the assessment;
-- validations are evidence offered for a replacement.
CREATE TABLE toil_revision_executions (
    toil_item_id TEXT    NOT NULL,
    revision     INTEGER NOT NULL,
    role         TEXT    NOT NULL CHECK (role IN ('observation', 'validation')),
    position     INTEGER NOT NULL CHECK (position >= 0),
    execution_id TEXT    NOT NULL REFERENCES executions (id),
    note         TEXT    NOT NULL CHECK (length(trim(note, char(32, 9, 10, 11, 12, 13))) > 0),
    PRIMARY KEY (toil_item_id, revision, role, position),
    UNIQUE (toil_item_id, revision, role, execution_id),
    FOREIGN KEY (toil_item_id, revision) REFERENCES toil_revisions (toil_item_id, revision)
        DEFERRABLE INITIALLY DEFERRED
) STRICT;

-- Each item's revisions are numbered 1, 2, 3, ... without gaps.
CREATE TRIGGER toil_revisions_contiguous
BEFORE INSERT ON toil_revisions
WHEN NEW.revision <> 1 + COALESCE(
    (SELECT MAX(revision) FROM toil_revisions WHERE toil_item_id = NEW.toil_item_id), 0)
BEGIN
    SELECT RAISE(ABORT, 'toil revisions must be contiguous');
END;

-- Revision 1 is observed or candidate; later revisions follow the lifecycle.
CREATE TRIGGER toil_revisions_lifecycle
BEFORE INSERT ON toil_revisions
WHEN NOT (
    (NEW.revision = 1 AND NEW.status IN ('observed', 'candidate'))
    OR (NEW.revision > 1 AND EXISTS (
        SELECT 1 FROM toil_revisions AS prev
        WHERE prev.toil_item_id = NEW.toil_item_id AND prev.revision = NEW.revision - 1
          AND (prev.status = NEW.status
               OR (prev.status = 'observed' AND NEW.status IN ('candidate', 'retired'))
               OR (prev.status = 'candidate' AND NEW.status IN ('observed', 'implemented', 'retired'))
               OR (prev.status = 'implemented' AND NEW.status IN ('candidate', 'validated', 'retired'))
               OR (prev.status = 'validated' AND NEW.status IN ('candidate', 'retired'))
               OR (prev.status = 'retired' AND NEW.status = 'observed')))))
BEGIN
    SELECT RAISE(ABORT, 'toil status transition is not allowed');
END;

-- A revision cites at least one source, and a validation at least one
-- execution or reference; links are inserted before the revision.
CREATE TRIGGER toil_revisions_links
BEFORE INSERT ON toil_revisions
WHEN NOT EXISTS (SELECT 1 FROM toil_revision_sources
                 WHERE toil_item_id = NEW.toil_item_id AND revision = NEW.revision)
  OR (NEW.validation_rationale IS NOT NULL
      AND COALESCE(json_array_length(NEW.validation_references), 0) = 0
      AND NOT EXISTS (SELECT 1 FROM toil_revision_executions
                      WHERE toil_item_id = NEW.toil_item_id AND revision = NEW.revision AND role = 'validation'))
  OR (NEW.validation_rationale IS NULL
      AND EXISTS (SELECT 1 FROM toil_revision_executions
                  WHERE toil_item_id = NEW.toil_item_id AND revision = NEW.revision AND role = 'validation'))
BEGIN
    SELECT RAISE(ABORT, 'a toil revision needs a source, and validation evidence exactly when it has a validation');
END;

-- Links are written only together with their revision.
CREATE TRIGGER toil_revision_sources_with_revision
BEFORE INSERT ON toil_revision_sources
WHEN EXISTS (SELECT 1 FROM toil_revisions WHERE toil_item_id = NEW.toil_item_id AND revision = NEW.revision)
BEGIN
    SELECT RAISE(ABORT, 'toil sources are written only with their revision');
END;

CREATE TRIGGER toil_revision_executions_with_revision
BEFORE INSERT ON toil_revision_executions
WHEN EXISTS (SELECT 1 FROM toil_revisions WHERE toil_item_id = NEW.toil_item_id AND revision = NEW.revision)
BEGIN
    SELECT RAISE(ABORT, 'toil executions are written only with their revision');
END;

-- Toil items, revisions and their links are never changed or removed.
CREATE TRIGGER toil_items_immutable
BEFORE UPDATE ON toil_items
BEGIN
    SELECT RAISE(ABORT, 'toil items are immutable');
END;

CREATE TRIGGER toil_items_undeletable
BEFORE DELETE ON toil_items
BEGIN
    SELECT RAISE(ABORT, 'toil items cannot be deleted');
END;

CREATE TRIGGER toil_revisions_immutable
BEFORE UPDATE ON toil_revisions
BEGIN
    SELECT RAISE(ABORT, 'toil revisions are immutable');
END;

CREATE TRIGGER toil_revisions_undeletable
BEFORE DELETE ON toil_revisions
BEGIN
    SELECT RAISE(ABORT, 'toil revisions cannot be deleted');
END;

CREATE TRIGGER toil_revision_sources_immutable
BEFORE UPDATE ON toil_revision_sources
BEGIN
    SELECT RAISE(ABORT, 'toil sources are immutable');
END;

CREATE TRIGGER toil_revision_sources_undeletable
BEFORE DELETE ON toil_revision_sources
BEGIN
    SELECT RAISE(ABORT, 'toil sources cannot be deleted');
END;

CREATE TRIGGER toil_revision_executions_immutable
BEFORE UPDATE ON toil_revision_executions
BEGIN
    SELECT RAISE(ABORT, 'toil executions are immutable');
END;

CREATE TRIGGER toil_revision_executions_undeletable
BEFORE DELETE ON toil_revision_executions
BEGIN
    SELECT RAISE(ABORT, 'toil executions cannot be deleted');
END;
