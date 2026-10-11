-- Migration 10: execution-evidence invalidation and procedure retirement (ADR-0035).
-- Applied migrations are history: never edit this file; add a new one.
-- Only new tables and triggers: no existing row changes.

-- At most one invalidation per execution: invalidation is irreversible.
CREATE TABLE execution_invalidations (
    execution_id TEXT NOT NULL PRIMARY KEY REFERENCES executions (id),
    reason       TEXT NOT NULL CHECK (length(trim(reason, char(32, 9, 10, 11, 12, 13))) > 0),
    actor        TEXT NOT NULL
                 CHECK (length(actor) BETWEEN 1 AND 128
                        AND actor NOT GLOB '*[^a-z0-9._-]*'
                        AND actor NOT GLOB '[._-]*'
                        AND actor NOT GLOB '*[._-]'
                        AND actor NOT GLOB '*[._-][._-]*'),
    created_at   TEXT NOT NULL
) STRICT;

-- A procedure is active until its first event; events alternate, starting
-- with a retirement, and are numbered 1, 2, 3, ... without gaps.
CREATE TABLE procedure_lifecycle_events (
    procedure_id TEXT    NOT NULL REFERENCES procedures (id),
    event        INTEGER NOT NULL CHECK (event >= 1),
    status       TEXT    NOT NULL CHECK (status IN ('retired', 'active')),
    reason       TEXT    NOT NULL CHECK (length(trim(reason, char(32, 9, 10, 11, 12, 13))) > 0),
    actor        TEXT    NOT NULL
                 CHECK (length(actor) BETWEEN 1 AND 128
                        AND actor NOT GLOB '*[^a-z0-9._-]*'
                        AND actor NOT GLOB '[._-]*'
                        AND actor NOT GLOB '*[._-]'
                        AND actor NOT GLOB '*[._-][._-]*'),
    created_at   TEXT    NOT NULL,
    PRIMARY KEY (procedure_id, event)
) STRICT;

CREATE TRIGGER procedure_lifecycle_events_contiguous
BEFORE INSERT ON procedure_lifecycle_events
WHEN NEW.event <> 1 + COALESCE(
    (SELECT MAX(event) FROM procedure_lifecycle_events WHERE procedure_id = NEW.procedure_id), 0)
BEGIN
    SELECT RAISE(ABORT, 'procedure lifecycle events must be contiguous');
END;

CREATE TRIGGER procedure_lifecycle_events_alternate
BEFORE INSERT ON procedure_lifecycle_events
WHEN NEW.status IS COALESCE(
    (SELECT status FROM procedure_lifecycle_events
     WHERE procedure_id = NEW.procedure_id AND event = NEW.event - 1), 'active')
BEGIN
    SELECT RAISE(ABORT, 'a procedure lifecycle event must change the status');
END;

-- A retired procedure takes no new version, is the target of no new
-- reference, and gets no new binding or binding revision.
CREATE TRIGGER procedure_versions_not_retired
BEFORE INSERT ON procedure_versions
WHEN (SELECT status FROM procedure_lifecycle_events
      WHERE procedure_id = NEW.procedure_id ORDER BY event DESC LIMIT 1) IS 'retired'
BEGIN
    SELECT RAISE(ABORT, 'the procedure is retired');
END;

CREATE TRIGGER procedure_version_references_not_retired
BEFORE INSERT ON procedure_version_references
WHEN (SELECT status FROM procedure_lifecycle_events
      WHERE procedure_id = NEW.target_procedure_id ORDER BY event DESC LIMIT 1) IS 'retired'
BEGIN
    SELECT RAISE(ABORT, 'the referenced procedure is retired');
END;

CREATE TRIGGER bindings_not_retired
BEFORE INSERT ON bindings
WHEN (SELECT status FROM procedure_lifecycle_events
      WHERE procedure_id = NEW.procedure_id ORDER BY event DESC LIMIT 1) IS 'retired'
BEGIN
    SELECT RAISE(ABORT, 'the bound procedure is retired');
END;

CREATE TRIGGER binding_revisions_not_retired
BEFORE INSERT ON binding_revisions
WHEN (SELECT e.status FROM procedure_lifecycle_events AS e
      JOIN bindings AS b ON b.procedure_id = e.procedure_id
      WHERE b.id = NEW.binding_id ORDER BY e.event DESC LIMIT 1) IS 'retired'
BEGIN
    SELECT RAISE(ABORT, 'the bound procedure is retired');
END;

-- Lifecycle history is never changed or removed.
CREATE TRIGGER execution_invalidations_immutable
BEFORE UPDATE ON execution_invalidations
BEGIN
    SELECT RAISE(ABORT, 'execution invalidations are immutable');
END;

CREATE TRIGGER execution_invalidations_undeletable
BEFORE DELETE ON execution_invalidations
BEGIN
    SELECT RAISE(ABORT, 'execution invalidations cannot be deleted');
END;

CREATE TRIGGER procedure_lifecycle_events_immutable
BEFORE UPDATE ON procedure_lifecycle_events
BEGIN
    SELECT RAISE(ABORT, 'procedure lifecycle events are immutable');
END;

CREATE TRIGGER procedure_lifecycle_events_undeletable
BEFORE DELETE ON procedure_lifecycle_events
BEGIN
    SELECT RAISE(ABORT, 'procedure lifecycle events cannot be deleted');
END;
