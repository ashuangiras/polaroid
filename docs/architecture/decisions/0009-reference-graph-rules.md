# 0009. Reference graphs are acyclic per procedure, bounded, and resolve contextual references to the latest version

**Status:** Accepted
**Date:** 2026-10-09

## Context

Since [#2](https://github.com/ashuangiras/polaroid/issues/2), versions reference other procedures. Agents execute a version by walking its references, so the graph must be finite and acyclic, and an agent must learn exactly which version each node uses ([#3](https://github.com/ashuangiras/polaroid/issues/3)). Contextual policies are meant to be resolved from execution evidence (increment 3), and that evidence does not exist yet. A contextual reference follows whatever its target's latest version is, so a graph that was acyclic can become cyclic when a target is revised.

## Decision

- **Version selection.** A pinned reference selects its pinned version. Until execution evidence exists, a contextual reference selects the target's **latest version at the moment of the check or read**. Increment 3 may change this rule, through a new ADR. Contextual binding policies are still not resolved.
- **Cycles are per procedure.** A path that reaches a procedure already on the path, at any version, is a cycle. So `A → A` is always a cycle, and so is `A v2 → B → A v1`. This is stricter than a per-version rule and can be relaxed later without breaking clients. Tightening it later could not be done compatibly.
- **Checked on every write.** When a version with references is stored, its graph is expanded from the new version, inside the same write transaction and after the version is inserted. A cycle or an exceeded limit rolls everything back. The write lock serializes writers, so two concurrent revisions cannot each add half of a cycle. A revision that closes a cycle through a contextual reference, such as revising `B` to reference `A` while `A` references `B` contextually, is rejected.
- **Limits.** A walk stops with an error, never with partial data, when it exceeds a **depth of 32** reference hops from the root or **2048 nodes** in the expanded tree. The same limits apply on writes and reads.
- **Errors.**
  - `409 reference_cycle` carries the cycle as a list of `{procedure_id, version, reference}` steps. It ends with the repeated procedure, which has no `reference`.
  - `422 graph_too_large` names the limit that was exceeded.
- **Read endpoint.** `GET /v1/procedures/{id}/versions/{n}/graph` returns a nested tree. Each node has `procedure_id`, `canonical_key`, the exact `version`, and `references`. Each reference has the stored `name`, `version_policy` and `inputs`, plus the selected child `node`. A target that two references share appears once under each. The walk runs in one transaction, so it reads one consistent snapshot.

## Consequences

- Every stored version written after this change has an acyclic graph at the moment it is written.
- A later revision of a contextually referenced target can grow another version's graph beyond the limits. A read then returns `422` for that root. Such a revision is not rejected, because the check runs only from the version being written.
- Versions stored under [#2](https://github.com/ashuangiras/polaroid/issues/2) before this check may contain cycles, including self-references. Reading their graph returns `409 reference_cycle`. They are never rewritten.
- Graph reads take the write lock for their snapshot (`_txlock=immediate`), so they briefly queue with writers.
