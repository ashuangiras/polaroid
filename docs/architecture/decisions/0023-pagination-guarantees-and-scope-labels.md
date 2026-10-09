# 0023. Lists are live traversals with bound cursors; discovery can page a snapshot; every version names its scope

**Status:** Accepted. Refines [ADR-0021](0021-targeted-feedback-and-bounded-lists.md) and [ADR-0020](0020-procedure-origin-and-applicability.md).
**Date:** 2026-10-09

## Context

ADR-0021 added opt-in keyset pagination, but its guarantee differs between lists, and it was not stated for changes of filter membership ([#37](https://github.com/ashuangiras/polaroid/issues/37)):

- In the key-ordered lists (procedures, bindings), a record created while a client pages, with a key before the cursor, is never returned. Each page also reads the latest versions as they are then, so a procedure can enter or leave a filter mid-traversal.
- A cursor only records a position, so it can be reused with other filters or another list, and the result is silently wrong.
- `scope` is reported per procedure, from its latest version. Resolution may select an older version with another declaration. Versions and graph nodes omit applicability when it is unspecified, so "unspecified" is visible only by absence.

## Decision

- **Live traversal stays the default** on every list. A cursor is a position: after the last item returned, in the list's order. Every page is a fresh read.
  - **Order:** procedures by `canonical_key` (unique); bindings by `name`, then `id`; repositories, executions and feedback by `created_at`, then `id`.
  - **Inserts:** in the time-ordered lists, a record committed during traversal sorts after every cursor, so a later page returns it ([ADR-0021](0021-targeted-feedback-and-bounded-lists.md)). In the key-ordered lists, it is returned only if its key sorts after the cursor.
  - **Membership:** filters are evaluated per page against current state: the latest version for procedure filters, and the registry for repository filters. A record whose membership changes between pages may be missed or appear late, and item contents (for example `latest_version`) are as of the page that returned them.
  - **Restart:** to see records a traversal could not return, request the first page again, without `after`.
- **Cursors are bound to their list and parameters.** A new cursor records the list and a digest of every filter, including `snapshot`. A cursor used on another list or with other filters gets `400` on `after`, and so does a malformed one. `limit` may change between pages. Cursors issued before this decision are still accepted as positions, without the check.
- **Snapshot traversal, opt-in, for discovery.** `snapshot=true`, with `limit`, on `GET /v1/procedures` and `GET /v1/bindings` (MCP `list_procedures` and `list_bindings`):
  - **Boundary.** The first page records a boundary for each table the list reads: procedures, versions, origins and repository identifiers, or bindings, binding revisions and repository identifiers. The boundary is SQLite's row ID high-water mark. These tables are append-only, rows are never updated or deleted, and writes are serialized, so row IDs grow in commit order.
  - **Pages.** Every page, and the next cursor, reads only rows at or below the boundary. Membership and item contents are therefore fixed as of the first page, including each procedure's latest version, scope, goal and origin as of then, and repository identity for the `repository` filter. A record committed after the boundary is excluded. No record within the boundary is skipped or repeated, wherever its key sorts.
  - **Scope.** Nothing else is a snapshot: fetching a record by ID, or a list without `snapshot`, reads current state. No transaction spans requests. Time-ordered lists offer no snapshot, because live traversal already returns every record committed during it. A snapshot cursor assumes the database file is not rebuilt between pages.
- **Every version names its own scope.** Versions (in histories, `get_version`, create and revise responses) and graph nodes gain `scope`: `shared`, `local` or `unspecified`. `applicability` keeps its form, and stays omitted when unspecified. `scope`, `goal` and `applicability` on a procedure (list items, histories) describe version `latest_version`, and only that version.
- **Declaration, admissibility and verification stay separate.**
  - `shared` declares a contract intended for reuse; `local` declares one for a single repository.
  - `unspecified` declares nothing. Those versions remain admissible everywhere for compatibility, and are never a claim of general reuse.
  - None of the three says the version was verified anywhere, which only evidence at a target does ([ADR-0018](0018-selection-evidence-and-target-verification.md)).

## Consequences

- An agent discovering procedures pages with `snapshot=true` and gets each procedure that existed at the first page exactly once. To reuse one, it resolves a binding or graph, reads the selected node's `version` and `scope`, and reads that version's contract before following it.
- Existing requests behave as before. Responses only add fields, and old cursors still work. Reusing a new cursor with different parameters, which used to return an arbitrary page, is now `400`.
- Snapshots cost no storage and no migration; their boundary is read in one statement.
