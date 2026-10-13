# 0047. Knowledge identities and origin, the foundations of the Git-backed agent interface

**Status:** Accepted (owner approval of the milestone D integration plan, 2026-10-13). These are the foundations only. Two parts of milestone D wait until PRs #128 and #132 are merged and `main` is integrated into `git-core`: wiring `/mcp/agent` to the kernel, and reconciling feedback resolutions.
**Date:** 2026-10-13

## Context

Milestone D puts the agent interface on Git-backed knowledge ([ADR-0044](0044-git-core.md)). Agents see no versions, bindings or selection; they read by key from one snapshot. The owner approved these points of the plan:

- Feedback has a typed knowledge identity.
- Resolving feedback after a publication is retry-safe and refers to the publication by an immutable reference.
- A finding that arrives with imported knowledge is a claim, never local evidence.
- Commit authors and trailers authenticate nothing.

The parts that do not depend on the pending PRs come first.

## Decision

- **`knowledge.FeedbackRef{Key, Snapshot}`** is the identity of knowledge feedback: the procedure's stable key and the snapshot that served it.
  - Its JSON form is exactly `{"key": ..., "snapshot": ...}`, and it is parsed strictly. Unknown members, missing members, wrong types, a non-canonical key, and a snapshot token that is not bounded and printable are all `ErrInvalid`.
  - `Kernel.CheckFeedbackRef` confirms that the snapshot is published here and that it holds the key. An earlier snapshot stays valid after later publications change or remove the procedure.
  - Until a first-class representation is added by migration (milestone E), it is stored in a feedback report's existing context under one reserved member, `knowledge`. No other ad hoc members are added.
- **`knowledge.PublicationRef{Instance, Snapshot, Key}`**, written `polaroid-knowledge:<instance>/<snapshot>#<key>`, names one procedure as one publication on one instance left it. It never changes, so a feedback resolution can cite it, and reconciliation can check that citation and redo it idempotently.
- **Instance identity.**
  - Each Git-core repository has an identity, `pi-` followed by 32 hex digits. It is created once, durably, under the writer lock (racing opens agree), and kept in the local file `polaroid-instance`.
  - No ref or object holds it, so no push carries it. A repository rebuilt from a remote is a new instance, and a damaged identity file is `ErrCorrupt`.
  - It names where a publication was made; it authenticates nothing.
- **Origin.** `Kernel.Origin(snapshot)` classifies a published snapshot:
  - `imported` if an activation introduced it;
  - `local` if it was published here, rollbacks included.

  The classification comes only from the activation journal: each entry's `Activated` snapshot back along first parents to its `Previous` one. A commit's author, its `Polaroid-Instance` trailer and its other trailers are never consulted, so a commit claiming this instance or a local operator is still `imported` when it arrived by activation. Only local snapshots can carry local evidence, such as a finding that names a local feedback report.
- **Journal before the ref.** This amends [ADR-0046](0046-git-knowledge-synchronisation.md).
  - `Advance` now writes the journal entry before moving the active ref. If an activation stops in between, the old snapshot stays active, the entry names a target that was never published, and `Verify` warns that the last recorded activation did not complete. Nothing imported is ever classified as local.
  - Callers can no longer set the journal's own trailers, `Activated` and `Previous`.
- **Input mapping.** `knowledge.MapInputs` applies a reference's mapping to a parent's inputs: `{"input": name}` or `{"value": v}`, in the mapping's order, and an absent parent input leaves the child's input absent ([ADR-0024](0024-child-inputs-follow-the-reference-mapping.md)'s semantics on the knowledge path). It refuses a malformed mapping.
- **The Store port** gains `Instance` and `Imported`.

## Consequences

- **Tests.**
  - Identities: valid and invalid feedback identities, publication references and mappings.
  - Snapshot-consistent child reads with a discovery's token.
  - Origin across forged instance and author claims, imported history, rollback, recovery from a damaged local publication, a fresh process (a daemon restart), and an instance rebuilt from the remote.
  - An interrupted activation, and identity creation by racing opens.
  - Mutation checks fail the tests without:
    - the activation range walk;
    - the classification;
    - journal-first ordering;
    - the reserved trailers;
    - the identity check;
    - the key check of `CheckFeedbackRef`;
    - the snapshot-token check;
    - the absent-input rule.
- **Compatibility.** No API, MCP, CLI, record or schema changes, and the daemon still does not link Git-core.
- **Still to come in milestone D:**
  - `/mcp/agent` on the kernel;
  - feedback storage with `FeedbackRef`;
  - publication through the operator control plane;
  - reconciliation of #128's resolutions, by `PublicationRef` and for local snapshots only;
  - the end-to-end and full-mode contract tests on the integrated branch.
