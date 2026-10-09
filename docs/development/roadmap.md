# Roadmap

Increments are ordered. Issue-ready items are filed as [GitHub issues](https://github.com/ashuangiras/polaroid/issues) using the [work item template](../../.github/ISSUE_TEMPLATE/work-item.yml).

> **Work-item authority.** GitHub Issues are the source of truth for scope and acceptance criteria. This roadmap keeps only order and links for filed items, never a second copy. Items not yet issue-ready stay here until they are refined and filed.

Increments 1 to 4 are implemented. Nothing below them is.

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

## Later (unordered, not yet scoped)

- Semantic discovery and duplicate suggestions for procedures.
- Aliases and identity consolidation (explicit records; history is never rewritten).
- Access control for reads and writes ([ADR-0006](../architecture/decisions/0006-local-unauthenticated-api.md)).
- Opt-in pagination for `GET /v1/procedures`.
- **Import from the earlier Python/SQLite PoC — blocked:** no source database or schema is available in this environment. Compatibility is not claimed; scope the importer only once a real database or schema is provided.
