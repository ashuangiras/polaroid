# 0035. Execution-evidence invalidation and procedure retirement

**Status:** Accepted. Refines [ADR-0012](0012-derived-verification.md), [ADR-0013](0013-evidence-based-resolution.md), [ADR-0018](0018-selection-evidence-and-target-verification.md) and [ADR-0032](0032-lexical-discovery-and-duplicate-suggestions.md); follows [ADR-0004](0004-append-only-versions-with-expected-base.md) for concurrent writes.
**Date:** 2026-10-11

## Context

Records are never changed or deleted, so two kinds of record keep acting as ordinary knowledge after people know better ([#91](https://github.com/ashuangiras/polaroid/issues/91)):

- An execution recorded against the wrong version still verifies its combination and still selects that version. Execution `01a12ac5-7fc7` only promoted an earlier assessment, but it is the only `procedure.capability.extract` v2 run in its context, so contextual resolution selects v2 by it. Feedback `01a12ac5-d046` says so, but feedback changes nothing that Polaroid derives.
- A superseded procedure stays in discovery and lists, and stays open to new bindings and references.

## Decision

Lifecycle state is append-only metadata in its own tables, next to the immutable versions and executions, which never change. Each event has a reason (non-blank text), an actor (canonical-key format, as a feedback `reporter`) and the server's `created_at`.

**Execution-evidence invalidation.**

- `POST /v1/executions/{id}/invalidation` `{reason, actor}` records that an execution must no longer be used as evidence. It is **irreversible** here: an execution has at most one invalidation, and a second request, also a concurrent one, is `409 execution_invalidated`. The replacement is a new, genuine execution; there is no reinstatement.
- An execution's **evidence status** is `valid` or `invalidated`. Executions, list entries and verifications report it, and an invalidated execution also reports its `invalidation`. Outcome, children, decisions and evidence read back unchanged.
- **Verification** ([ADR-0012](0012-derived-verification.md)) now asks whether an execution *currently supports* its claim. An invalidated execution is not verified: it reports `invalidated`, plus any structural problem it has anyway, so `invalidated` alone means it satisfied its contract as recorded. A parent whose linked child is invalidated is not verified and reports `child_invalidated`, naming the child, instead of `child_not_verified`. Ancestors follow as for any unverified child. No other child is ever substituted.
- **Combinations.** `execution_ids` still lists every execution. Invalidated ones are listed again in `invalidated_execution_ids` and never decide: the latest *valid* execution does, and it is `latest_execution_id`. A combination with only invalidated executions is unverified, without `latest_execution_id`. Target verification ([ADR-0018](0018-selection-evidence-and-target-verification.md)) has the same shape and rule.
- **Contextual selection** ([ADR-0013](0013-evidence-based-resolution.md)) considers only valid executions: a version is verified in a context when its latest valid execution there is verified. Invalidating that latest execution therefore falls back to the one before it, or to another version, or to no evidence. A parent run linking an invalidated child is still valid, so it remains the latest and is unverified: invalidating a child withdraws its parents' runs instead of bringing older ones back. To use an older parent run instead, invalidate the parent run explicitly.
- Recording an execution, also one that links an invalidated child, is accepted as before; such a parent is never verified.

**Procedure retirement.**

- A procedure identity is `active` until its first lifecycle event. `POST /v1/procedures/{id}/retirement` and `POST /v1/procedures/{id}/reactivation` take `{base_event, reason, actor}` and append event `base_event + 1`. A stale `base_event` is `409 lifecycle_conflict` with `latest_event`; retiring a retired procedure, or reactivating an active one, is `409 lifecycle_unchanged`. Events alternate, the first is a retirement, and `GET /v1/procedures/{id}/lifecycle` returns the state and every event.
- **Reads.** Procedures report `lifecycle_status`, and a procedure's history also `lifecycle_event`. Retrieval by ID, key and exact version, graphs, bindings and executions keep working. The procedure list and discovery take `lifecycle`: `active` (the default), `retired` or `all`. A procedure list snapshot fixes lifecycle state at its first page. Duplicate checks always compare against every procedure, report `lifecycle_status` on suggestions and on `key_collision`, so retirement never invites a second identity for the same knowledge.
- **Resolution and graphs** select exactly what they selected before. A node of a retired procedure carries `retirement` (its latest event): its selection evidence and target verification remain visible, because retirement does not erase evidence that an execution satisfied its contract. Graphs of active procedures are unchanged.
- **Writes refused** with `409 procedure_retired`, naming the reactivation: a version of a retired procedure; a version with a reference, pinned or contextual, to a retired procedure (`fields` name each reference); a binding, or a binding revision, of a retired procedure. Executions, origins, feedback and toil items are still accepted, because they record what happened. Retirement never changes a parent, a reference or a binding.

**Operations.**

| Operation | HTTP | MCP | CLI |
| --- | --- | --- | --- |
| invalidate | `POST /v1/executions/{id}/invalidation` | `invalidate_execution` | `invalidate-execution` |
| invalidation state | `GET /v1/executions/{id}/invalidation` | `get_execution_invalidation` | `get-execution-invalidation` |
| retire, reactivate | `POST /v1/procedures/{id}/retirement`, `…/reactivation` | `retire_procedure`, `reactivate_procedure` | `retire-procedure`, `reactivate-procedure` |
| lifecycle history | `GET /v1/procedures/{id}/lifecycle` | `get_procedure_lifecycle` | `get-procedure-lifecycle` |

The execution list also filters by `evidence_status`.

**Storage.** Migration 10 adds `execution_invalidations` (one row per execution at most) and `procedure_lifecycle_events` (numbered without gaps, alternating, first a retirement). Triggers reject UPDATE and DELETE of both, and inserts of versions, references, bindings and binding revisions that a retired procedure forbids; the service checks the same rules in the write transaction to report them precisely. No existing row changes.

## Consequences

- An agent can withdraw wrong evidence without rewriting history, and retire knowledge without breaking anything that already depends on it. Both are visible wherever the record is read.
- Verification, combination statuses, selection and target verification change only where an execution is invalidated. Without invalidations and retirements, every existing response is unchanged apart from the added `evidence_status` and `lifecycle_status` fields.
- Invalidation is permanent until a later ADR adds reinstatement for a concrete need. A mistaken invalidation is answered by recording a new genuine execution.
- Schema 10 cannot be opened by earlier builds. The live correction of `01a12ac5-7fc7` waits for a release.
