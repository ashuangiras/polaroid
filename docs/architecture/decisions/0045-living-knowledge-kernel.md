# 0045. The living knowledge kernel on Git-core

**Status:** Accepted (owner direction, 2026-10-13). Milestone B of [ADR-0044](0044-git-core.md), implemented on branch `git-core`. No daemon, API, CLI, record or schema behaviour changes: the kernel is reachable from no transport until milestone D.
**Date:** 2026-10-13

## Context

[ADR-0044](0044-git-core.md) made a Git repository the target authoritative store of active procedural knowledge, and built Git-core (`internal/gitcore`). Git-core stores files and snapshots and knows no procedures. Milestone B puts real, validated, composable Polaroid knowledge on it, with these requirements:

- stable identities;
- reads fixed to one snapshot;
- references by identity, checked for existence, cycles, limits and applicability;
- publication that validates the whole resulting catalog;
- improvements with provenance;
- rollback;
- deterministic discovery;
- recovery from the repository alone.

The domain must not see Git types, and no second versioning or persistence framework may be built.

Polaroid already has a knowledge model: versioned procedure records in SQLite (`internal/memory`, `internal/storage/sqlite`). That path stays authoritative for everything the daemon serves until milestone E migrates it.

## Decision

- **Two explicit paths.** The *legacy path* is `internal/memory` on SQLite: versioned records, bindings, executions, feedback, verification. Every API, MCP tool and CLI command uses only this path. The *Git-native path* is `internal/knowledge` on Git-core through `internal/knowledge/gitstore`. Nothing writes knowledge to both. `internal/archtest` enforces the separation:
  - no legacy package (memory, storage, transports, recovery, lifecycle, both commands) depends on `internal/knowledge` or `internal/gitcore`;
  - `internal/knowledge` depends on no Git, go-git, SQL, storage, transport, recovery, lifecycle or `memory` package;
  - `internal/knowledge/gitstore` joins only the domain and Git-core.
- **Identity.** A procedure is identified by its canonical key, the same rule as a legacy canonical key: at most 128 lowercase letters and digits joined by single `.`, `-` or `_`. A key never changes and names exactly one procedure. A procedure has one definition per snapshot, not a version number. Its history is the history of its document.
- **Record.** `Procedure{Key, Goal, Philosophy, Method, Applicability, Contract, Instructions, References}`.
  - `Contract` and `Instructions` are free-form JSON objects. The kernel never interprets their members.
  - `Applicability` is unspecified, shared, or local to one repository.
  - A `Reference` has a name (unique within the procedure), a target key, an optional condition (text that Polaroid never evaluates) and an inputs mapping. Each member of the mapping is `{"input": "<parent input>"}` or `{"value": <JSON>}`.
- **Document and layout.** Each procedure is one canonical JSON document at `procedures/<first key segment>/<key>.json`.
  - The member order is fixed. Free-form objects keep their member order and are compacted. The document ends with a newline. Identical content gives identical bytes, so Git diffs and object ids are meaningful.
  - Decoding is strict. A document is refused if it has unknown or duplicate members, trailing data, more than 256 KiB, or bytes that are not its canonical encoding.
- **Validation is the domain's; atomicity and durability are Git-core's.**
  - **Per procedure:** each field's required form and size limits (goal 2 KiB on one line; philosophy, method and condition 8 KiB; reference inputs 16 KiB; contract and instructions 64 KiB objects); reference names; inputs mappings.
  - **Per catalog:** the stored path matches the key; every reference names a key in the same catalog; each child applies wherever its parent applies (a local child is admitted only by a local parent of the same repository); no cycle (the error names the cycle's path); depth at most 32; expansion at most 2,048 procedures; at most 50,000 procedures.
  - **Publication** (`Kernel.Publish`) applies the proposal's changes to its base catalog, refuses unchanged documents and removals of absent keys, and validates the entire resulting catalog. It then commits through the `Store` port with the proposal's base as the expected snapshot. The commit's `Check` runs inside Git-core's publication lock, on the complete proposed tree, and confirms that the tree holds exactly the validated documents.
  - **Git-core** contributes the rest: compare-and-publish against the active ref (a stale base is `ErrStale`, never merged), safe paths and tree limits, fsynced objects and ref, the reflog, the lock across processes, and blob hashing on read.
  - **Reads:** a catalog is loaded from a snapshot only if every file is a procedure document under its own key and the whole catalog validates. A tree written behind the kernel's back (a stray file, a conflicting identity, a non-canonical document, a broken reference) is refused with `ErrInvalid`, and is never served. Damaged content is `ErrCorrupt`.
- **Snapshot consistency.** A `Catalog` is an immutable value for one snapshot. `Resolve` expands a composition completely within it: every node carries that snapshot. Later publications never change an earlier snapshot's catalog. The kernel caches the last eight catalogs by snapshot.
- **Provenance.** A publication's provenance is its Git commit:
  - the author, who authorised it;
  - the subject, a summary;
  - the body, the rationale, which is required;
  - trailers: `Polaroid-Finding` (the feedback or finding the change accepts), `Polaroid-Validation` (how the improvement was validated), `Polaroid-Validated` (the kernel's statement of what it checked, for example `catalog of 3 procedures: documents, references, applicability, cycles, limits`) and `Polaroid-Rollback-To`.

  `Kernel.History` reads all of it back with each publication's added, modified and removed keys.
- **Feedback.** An accepted improvement becomes a publication whose `Polaroid-Finding` names the finding. Unresolved feedback stays an operational record in SQLite, on the legacy path, until it is accepted. Milestone B does not check that a finding id exists in SQLite, because the kernel is not wired to the daemon.
- **Rollback** is a new publication, never a ref move. `Kernel.Rollback(base, to, provenance)` makes the catalog equal to snapshot `to`'s, under the same validation and stale protection, with `Polaroid-Rollback-To`. Rolling back to a catalog that is already active is refused.
- **Discovery** is deterministic term matching over the catalog, with no embeddings. Each query term scores 3 in the key, 2 in the goal and 1 in the method or philosophy. Results are ordered by score, then key. Procedures that do not apply in the given repository are excluded. The index is built once per catalog.
- **The `Store` port** is the domain's only view of storage:
  - `Active`;
  - `Files(snapshot)`, which lists paths and reads them;
  - `Commit` (expected snapshot, file changes, author, subject, body, trailers, `Check`);
  - `Log`.

  Snapshots are opaque strings. `gitstore` implements the port by delegating to Git-core's `Active`, `Tree`, `Publish` (with `Check` as `Validate`) and `History`. It maps Git-core's errors to the domain's as text only, so callers can match knowledge errors and never Git-core's. No other versioning or persistence code exists.
- **Recovery.** Everything the kernel serves is derived from the repository: a fresh process given only the repository directory rebuilds the same catalog, compositions, earlier snapshots and provenance. Git-core's `Verify` and `Recover` remain the integrity tools.

## Consequences

- **Tests.** The acceptance scenario lives once, in `internal/knowledge/knowledgetest`. It runs against an in-memory store, which tests the domain semantics without storage, and against real Git-core. It covers:
  - three related procedures published, discovered and resolved;
  - a child improved, with new reads seeing the improvement while the earlier snapshot keeps the original composition;
  - refused broken references, cycles, invalid contracts, removals that break a parent, applicability violations, key mismatches, unchanged documents and missing rationale;
  - a stale base, and six racing publishers through two kernels with exactly one winner;
  - rollback, with a repeated rollback refused;
  - authorship and rationale in the history;
  - a fresh kernel.

  Git-core integration tests add a fresh *process* recovering from the directory alone, foreign trees refused, and damaged documents reported as corruption. Mutation checks fail the tests without the stale check, publication validation, read validation, cycle and depth checks, applicability admission, the stored-tree check, or canonical decoding.
- **Cost.** A publication validates and encodes the whole catalog, and Git-core rewrites the changed tree path. That is fine for the catalog sizes Polaroid serves, and is measured again in milestone E. A publication costs one Git-core publication (about 120–160 ms on the development Mac, dominated by `F_FULLFSYNC`).
- **Compatibility.** Nothing observable changes. No endpoint, MCP tool, CLI command, record, schema or dependency is added or changed; the daemon does not open a Git repository.
- **Open for later milestones:**
  - C: remotes, transports and their dependencies, credentials, trust and signatures, size caps and quarantine for fetched objects, and divergence handling;
  - D: exposing the kernel to agents, and verifying finding ids against the operational store;
  - E: mapping legacy procedure versions, bindings and execution evidence to keys and snapshots, then retiring the legacy knowledge path;
  - all: retention and garbage collection, and secret detection in published knowledge.
