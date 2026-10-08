# Roadmap

Increments are ordered; each item is written so it can be filed as a GitHub issue with the [work item template](../../.github/ISSUE_TEMPLATE/work-item.yml) unchanged.

> **Work-item authority.** GitHub Issues are the intended source of truth. This repository has no remote yet, so the items below are the issue-ready drafts. When the repository is published, file each item as an issue, then replace its body here with a link — the roadmap keeps only order and links, never a second copy of the work.

Nothing below increment 1 is implemented.

## Increment 1 — Procedure identity and immutable versions (done)

Create, list, retrieve and revise procedures with stable IDs, unique canonical keys, append-only versions, stale-base conflicts, SQLite persistence, health endpoint, CLI. Evidence: [status.md](status.md).

## Increment 2 — Bindings and composition

### 2.1 Repository bindings with guarded revisions — **next**

- **Problem:** A repository cannot record that it uses a shared procedure, with its own inputs and version policy, without copying the procedure's instructions.
- **Scope:** A *binding* record (stable ID; repository identifier; repository-local name; target procedure ID) and immutable *binding revisions* (local inputs as a JSON object; version policy `{"pin": N}` or `{"contextual": {}}`; revision reason). Revisions append with `base_revision`, exactly like procedure versions. Endpoints to create a binding (with revision 1), list bindings for a repository, get a binding with its revision history, and append a revision. Migration `0002` with immutability triggers on the new tables.
- **Contract impact:** Additive `/v1` endpoints and records; records.md and http-api.md gain the binding contract; procedure records unchanged.
- **Dependencies:** Increment 1. Decide first (record in an ADR): the repository identifier format (e.g. normalized remote URL vs. owner-chosen name) and whether `(repository, local name)` is the uniqueness key.
- **Acceptance criteria:**
  1. Two repositories bind the same procedure; both bindings resolve to the same procedure ID and the procedure's history is unchanged (no instruction copies).
  2. Binding to an unknown procedure → `404`; a pinned version that does not exist → `400` naming the field; duplicate `(repository, local name)` → `409`.
  3. A stale `base_revision` → `409` with the latest revision; concurrent revisions from one base → exactly one success (barrier test, no sleeps).
  4. Revisions are immutable: storage triggers reject `UPDATE`/`DELETE`; history survives close/reopen.
  5. Contextual policy is stored and returned verbatim; it is **not** resolved yet (no endpoint claims resolution).
- **Verification:** storage and HTTP tests as above; `make check`; demo extended with a two-repository binding.

### 2.2 Named subprocedure references in procedure versions

- **Problem:** A procedure cannot declare that it composes other procedures, so composition lives in prose instructions that Polaroid cannot validate.
- **Scope:** Optional `references` on a version definition: each has a unique `name`, a target procedure, a version policy (same type as 2.1), and explicit child inputs mapping each child input to a parent input or a literal JSON value. Validation of targets, pinned versions, names and mapping shape.
- **Contract impact:** Version gains `references` (absent = none; existing versions unaffected). Requests that send `references` stop being rejected as unknown — clients must not have relied on that rejection.
- **Dependencies:** 2.1 (shared version-policy type).
- **Acceptance criteria:** create and revise with references; unknown target, unknown pinned version, duplicate name, or malformed mapping → `400` with `fields`; history returns references byte-for-byte; versions created before the migration read back unchanged.
- **Verification:** domain validation tests, storage reopen test, HTTP contract tests, `make check`.

### 2.3 Reference-graph validation and bounded traversal

- **Problem:** References can form cycles or unbounded graphs, which agents cannot execute.
- **Scope:** Reject a version whose references would create a cycle (including self-reference), evaluated against pinned targets and the current latest versions of contextual targets. A read endpoint returning the composition graph of a version with exact versions for pinned references and the policy-selected version for contextual ones, bounded by documented depth and node limits.
- **Contract impact:** New error code (e.g. `reference_cycle`), new read endpoint; ADR on how contextual references are treated before evidence exists (increment 3).
- **Dependencies:** 2.2.
- **Acceptance criteria:** `A→B→A` and `A→A` rejected without storing anything; a graph deeper than the limit returns a defined error instead of partial data; traversal output lists the exact version chosen for every node.
- **Verification:** graph tests including cycles introduced by a later revision of the target; `make check`.

## Increment 3 — Execution evidence and resolution

Each item below needs its acceptance criteria refined after increment 2 lands; they are ordered and scoped, not yet issue-ready.

- **3.1 Execution records.** Immutable record of the exact procedure version (and binding revision) used, effective inputs, repository, commit, environment, outcome and observable evidence.
- **3.2 Subprocedure executions.** Child executions linked to a parent through the named reference they fulfil; the child's procedure and version must satisfy the reference's policy.
- **3.3 Context-specific verification.** Verification computed per (repository, commit, environment, effective inputs, child-version combination); a successful parent is validated against its children's evidence; a changed child-version combination requires fresh parent verification while historical evidence stays with its original combination; retrieval of historical combinations.
- **3.4 Contextual resolution from evidence.** Resolve contextual references using verification evidence for the requesting context, always reporting the exact versions selected.

## Later (unordered, not yet scoped)

- Semantic discovery and duplicate suggestions for procedures.
- Aliases and identity consolidation (explicit records; history is never rewritten).
- MCP transport beside the HTTP API.
- Access control for reads and writes ([ADR-0006](../architecture/decisions/0006-local-unauthenticated-api.md)).
- Opt-in pagination for `GET /v1/procedures`.
- **Import from the earlier Python/SQLite PoC — blocked:** no source database or schema is available in this environment. Compatibility is not claimed; scope the importer only once a real database or schema is provided.
