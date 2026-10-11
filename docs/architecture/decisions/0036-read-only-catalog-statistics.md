# 0036. Read-only catalog statistics

**Status:** Accepted. Reads what [ADR-0010](0010-execution-records.md), [ADR-0011](0011-subprocedure-executions.md), [ADR-0020](0020-procedure-origin-and-applicability.md), [ADR-0034](0034-toil-items.md) and [ADR-0035](0035-evidence-invalidation-and-procedure-retirement.md) store; changes none of them.
**Date:** 2026-10-11

## Context

Records are kept forever, so the catalog only grows. Before Polaroid can limit payloads or retain less, people and agents need to see what it holds and what takes its space ([#96](https://github.com/ashuangiras/polaroid/issues/96)). Until now that took hand-written SQL against the file.

## Decision

`GET /v1/statistics` (MCP `get_catalog_statistics`, CLI `statistics`) returns one global summary. It takes no parameters (any query parameter is `400`), writes nothing, needs no migration, and exposes no record content, ID or filesystem path.

**Shape.** `statistics_version` (`1`), `generated_at`, then:

- `records`: the count of every stored record kind. It counts identities (`procedures`, `bindings`, `executions`, `toil_items`, `repositories`, `feedback`) apart from their history (`procedure_versions`, `binding_revisions`, `toil_revisions`, `procedure_lifecycle_events`) and from relationship records (`procedure_references`, `procedure_origins`, `repository_aliases`, `execution_children`, `execution_decisions`, `execution_invalidations`, `toil_revision_sources`, `toil_revision_executions`).
- `procedures`: `by_lifecycle` (`active`, `retired`, from each procedure's latest lifecycle event) and `by_scope` (`shared`, `local`, `unspecified`, from its latest version).
- `executions`: `by_outcome` (`succeeded`, `failed`: the recorded outcome of every execution, invalidated ones included), `by_evidence_status` (`valid`, `invalidated`), `invalidated_by_outcome`, and `valid_with_invalidated_child`: valid executions that directly link at least one invalidated child, and so report `child_invalidated`.
- `toil_items`: `by_status`, the status of each item's latest revision (all five statuses).
- `payloads`: for each stored JSON category, `count`, `total_bytes`, `mean_bytes` and `max_bytes`. Categories: `procedure_contracts`, `procedure_instructions`, `reference_inputs`, `binding_revision_inputs`, `execution_inputs`, `execution_evidence`, `execution_environment_attributes`, `decision_evidence`, `feedback_contexts`, `toil_assessments`, `toil_proposals`. Every version and revision counts, not only the latest.
- `storage`: `schema_version`, `page_size`, `page_count`, `freelist_count`, `database_bytes` (page size × page count), and `files`: `database`, `wal` and `shm`, each `{"present": true, "bytes": N}` or `{"present": false}`.

Every breakdown lists all its keys, zero included, so an empty catalog returns zeros everywhere.

**Payload bytes are logical.** A value's size is the length in UTF-8 bytes of its JSON text as stored: compacted on write ([records.md](../records.md)), with member order and string escapes as received. `"é"` counts 2 bytes, its escape `"\u00e9"` 6. Reads return the same text, except that a response writes an escaped character as the character itself, so a value holding escapes can be shorter in a response than here. An empty object `{}` counts as a value of 2 bytes. An absent optional value (a toil revision without `proposal`, a decision without `evidence`) is not counted at all. `mean_bytes` is `total_bytes` divided by `count`, rounded down, and 0 when `count` is 0. Logical bytes exclude SQLite's record headers, indexes, page slack and free pages, so they never add up to a file size. `storage` reports physical bytes, for the whole file only.

**Consistency.** `generated_at` is the server's time just before the read. Everything except `storage.files` is computed by one SQL statement of aggregate queries, so it describes one committed state of the catalog: a concurrent write is either entirely in it or entirely absent, and the breakdowns add up to the `records` counts. `storage.files` are file metadata, read just after that statement, and can disagree with it: a commit grows the WAL, a checkpoint copies the WAL into the database file and resets it, and SQLite deletes the WAL and shared-memory files when its last connection closes. The database file can be smaller than `database_bytes` (committed pages still in the WAL) or larger (it shrinks only on vacuum). A file that does not exist is `present: false`, not an error; any other failure to read a file's metadata is an `internal` error.

**Cost and side effects.** Each count and payload category is one aggregate over its table, so a request is linear in the catalog's size; nothing loads records into memory. The statement is a read: it never writes a row, checkpoints, vacuums or analyzes. Like every read, it may make SQLite create the WAL and shared-memory files when they do not exist. In WAL mode it blocks no writer.

**Left out, on purpose.**

- **Verification counts.** A successful outcome is not verification, and verification is derived per execution, recursively through children, decisions and reference mappings ([ADR-0012](0012-derived-verification.md), [ADR-0029](0029-conditional-references-and-applicability-decisions.md), [ADR-0035](0035-evidence-invalidation-and-procedure-retirement.md)): counting verified executions means walking every execution tree. `valid_with_invalidated_child` is only the direct link; ancestors above such a parent (which report `child_not_verified`) are not counted.
- **Per-table physical size.** It needs SQLite's `dbstat` table, which reads every page and which builds may not include.
- **Prose fields** (philosophy, method, reasons, notes): they are text, not JSON payloads.
- **Percentiles and distributions:** they need every value sorted per category. Count, total, mean and maximum are enough to choose first limits.
- **Anything per procedure or repository, rankings, trends:** out of scope; and nothing interprets `contract`, `instructions`, `inputs` or `evidence`.

**Versioning.** Members may be added within `statistics_version` 1. Removing a member or changing its meaning increments it.

## Consequences

- Payload limits and retention can be decided from numbers that any catalog reproduces with one request.
- A statistics request holds a read snapshot while it runs, which delays the completion of a checkpoint, not writes.
- File sizes are approximate by nature; compare them across requests only as a trend, and use the logical counts for exact comparisons.
