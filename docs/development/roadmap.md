# Roadmap

Increments are ordered. Issue-ready items are filed as [GitHub issues](https://github.com/ashuangiras/polaroid/issues) using the [work item template](../../.github/ISSUE_TEMPLATE/work-item.yml).

> **Work-item authority.** GitHub Issues are the source of truth for scope and acceptance criteria. This roadmap keeps only order and links for filed items, never a second copy. Items not yet issue-ready stay here until they are refined and filed.

Increments 1 and 2, and the first two items of increment 3, are implemented. Nothing else below is.

## Increment 1 — Procedure identity and immutable versions (done)

Create, list, retrieve and revise procedures with stable IDs, unique canonical keys, append-only versions, stale-base conflicts, SQLite persistence, health endpoint, CLI. Evidence: [status.md](status.md).

## Increment 2 — Bindings and composition (done)

1. [#1 Repository bindings with guarded revisions](https://github.com/ashuangiras/polaroid/issues/1) — **done**
2. [#2 Named subprocedure references in procedure versions](https://github.com/ashuangiras/polaroid/issues/2) (depends on #1) — **done**
3. [#3 Reference-graph validation and bounded traversal](https://github.com/ashuangiras/polaroid/issues/3) (depends on #2) — **done**

## Increment 3 — Execution evidence and resolution

1. [#7 Execution records](https://github.com/ashuangiras/polaroid/issues/7) — **done**
2. [#9 Subprocedure executions](https://github.com/ashuangiras/polaroid/issues/9) (depends on #7) — **done**

The items below are ordered and scoped, but not yet issue-ready. Refine each one into an issue before starting it.

- **3.3 Context-specific verification.** Verification computed per (repository, commit, environment, effective inputs, child-version combination); a successful parent is validated against its children's evidence; a changed child-version combination requires fresh parent verification while historical evidence stays with its original combination; retrieval of historical combinations.
- **3.4 Contextual resolution from evidence.** Resolve contextual references using verification evidence for the requesting context, always reporting the exact versions selected.

## Later (unordered, not yet scoped)

- Semantic discovery and duplicate suggestions for procedures.
- Aliases and identity consolidation (explicit records; history is never rewritten).
- MCP transport beside the HTTP API.
- Access control for reads and writes ([ADR-0006](../architecture/decisions/0006-local-unauthenticated-api.md)).
- Opt-in pagination for `GET /v1/procedures`.
- **Import from the earlier Python/SQLite PoC — blocked:** no source database or schema is available in this environment. Compatibility is not claimed; scope the importer only once a real database or schema is provided.
