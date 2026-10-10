# Roadmap

Increments are ordered. Issue-ready items are filed as [GitHub issues](https://github.com/ashuangiras/polaroid/issues) using the [work item template](../../.github/ISSUE_TEMPLATE/work-item.yml).

> **Work-item authority.** GitHub Issues are the source of truth for scope and acceptance criteria. This roadmap keeps only order and links for filed items, never a second copy. Items not yet issue-ready stay here until they are refined and filed.

Increments 1 to 6 and the items from agent feedback are implemented. Nothing below them is.

## Increment 1 — Procedure identity and immutable versions (done)

Create, list, retrieve and revise procedures with stable IDs, unique canonical keys, append-only versions, stale-base conflicts, SQLite persistence, health endpoint, CLI. Evidence: [status.md](status.md).

## Increment 2 — Bindings and composition (done)

1. [#1 Repository bindings with guarded revisions](https://github.com/ashuangiras/polaroid/issues/1) — **done**
2. [#2 Named subprocedure references in procedure versions](https://github.com/ashuangiras/polaroid/issues/2) (depends on #1) — **done**
3. [#3 Reference-graph validation and bounded traversal](https://github.com/ashuangiras/polaroid/issues/3) (depends on #2) — **done**

## Increment 3 — Execution evidence and resolution (done)

1. [#7 Execution records](https://github.com/ashuangiras/polaroid/issues/7) — **done**
2. [#9 Subprocedure executions](https://github.com/ashuangiras/polaroid/issues/9) (depends on #7) — **done**
3. [#11 Context-specific verification of executions](https://github.com/ashuangiras/polaroid/issues/11) (depends on #7, #9) — **done**
4. [#13 Resolve contextual references from verification evidence](https://github.com/ashuangiras/polaroid/issues/13) (depends on #11) — **done**

## Increment 4 — Agent integration (done)

1. [#15 MCP transport: tools and resources over streamable HTTP](https://github.com/ashuangiras/polaroid/issues/15) — **done**
2. [#16 Feedback box: agents report problems and suggestions about Polaroid](https://github.com/ashuangiras/polaroid/issues/16) (depends on #15) — **done**

## From agent feedback (done)

Filed from feedback reports that agents recorded with `report_feedback` (#16).

1. [#22 Docs: member order of free-form objects is kept as received; MCP clients may reorder](https://github.com/ashuangiras/polaroid/issues/22) — **done**
2. [#20 MCP: signal tool-catalogue changes](https://github.com/ashuangiras/polaroid/issues/20) — **done**, re-scoped to pinning `ttlMs: 0` and documenting client restarts
3. [#21 MCP: also accept protocol 2025-11-25](https://github.com/ashuangiras/polaroid/issues/21) — **done** ([ADR-0016](../architecture/decisions/0016-mcp-protocol-2025-11-25.md))

## Increment 5 — Procedural memory on Polaroid's own development (done)

No new product capability: the loop that increments 1 to 4 built is used on real development work.

1. [#27 Run the end-to-end scripts' deterministic checks in CI](https://github.com/ashuangiras/polaroid/issues/27) — **done**
2. [#28 Polaroid-guided development: reusable development procedures that improve through execution](https://github.com/ashuangiras/polaroid/issues/28) (depends on #27) — **done** ([ADR-0017](../architecture/decisions/0017-development-procedures-as-records.md), [procedural-loop.md](procedural-loop.md))
3. [#31 Separate selection evidence from target verification in resolution](https://github.com/ashuangiras/polaroid/issues/31) (found while using #28) — **done** ([ADR-0018](../architecture/decisions/0018-selection-evidence-and-target-verification.md))
4. [#33 Teach dev.change.verify explicit target verification, and follow it](https://github.com/ashuangiras/polaroid/issues/33) (depends on #31) — **done**, a record change ([procedural-loop.md](procedural-loop.md#target-aware-procedure-33))

## Increment 6 — Several repositories in one catalog (done)

1. [#35 Organize procedural knowledge, execution history and feedback for use by several repositories](https://github.com/ashuangiras/polaroid/issues/35) — **done** ([ADR-0019](../architecture/decisions/0019-repository-registry.md), [ADR-0020](../architecture/decisions/0020-procedure-origin-and-applicability.md), [ADR-0021](../architecture/decisions/0021-targeted-feedback-and-bounded-lists.md), [procedural-loop.md](procedural-loop.md#several-repositories-35))
2. [#37 Consolidate repository identity in verification, pagination guarantees and applicability discovery](https://github.com/ashuangiras/polaroid/issues/37) (depends on #35) — **done** ([ADR-0022](../architecture/decisions/0022-repository-identity-in-evidence.md), [ADR-0023](../architecture/decisions/0023-pagination-guarantees-and-scope-labels.md), [procedural-loop.md](procedural-loop.md#identity-pages-and-first-runs-of-the-shared-versions-37))

## Correctness fixes (done)

1. [#39 A parent is verified even when a linked child ran with inputs its reference mapping does not produce](https://github.com/ashuangiras/polaroid/issues/39) (fixes #9 and #11) — **done** ([ADR-0024](../architecture/decisions/0024-child-inputs-follow-the-reference-mapping.md), [procedural-loop.md](procedural-loop.md#child-inputs-follow-the-reference-mapping-39))

## Production readiness

Second-repository adoption trials are paused while the existing features are prepared for production use.

1. [#41 Persistent per-user home (~/.polaroid) and safe migration of the shared catalog out of bin/](https://github.com/ashuangiras/polaroid/issues/41) — **done** ([ADR-0025](../architecture/decisions/0025-per-user-default-database.md), [procedural-loop.md](procedural-loop.md#a-persistent-per-user-catalog-41))
2. [#43 Per-user installation and managed service (launchd LaunchAgent, systemd user service)](https://github.com/ashuangiras/polaroid/issues/43) — **done** ([ADR-0026](../architecture/decisions/0026-per-user-installation-and-managed-service.md), [status](status.md))
3. [#45 Recovery from previous/ reinstalls the failed build](https://github.com/ashuangiras/polaroid/issues/45) — **done** ([ADR-0027](../architecture/decisions/0027-install-stages-its-source.md))
4. [#46 Downloadable prerelease verification builds with a repository-independent smoke test](https://github.com/ashuangiras/polaroid/issues/46) — next

## Later (unordered, not yet scoped)

- Onboard a second real repository: register it, bind the shared development procedures with its own commands, and verify one of its commits through them. It can rely on the identity, pagination and discovery contracts of #37.
- Semantic discovery and duplicate suggestions for procedures.
- Merging two registered repositories (identity consolidation); aliases exist since #35, but a merge does not.
- Access control for reads and writes ([ADR-0006](../architecture/decisions/0006-local-unauthenticated-api.md)).
- **Import from the earlier Python/SQLite PoC — blocked:** no source database or schema is available in this environment. Compatibility is not claimed; scope the importer only once a real database or schema is provided.
