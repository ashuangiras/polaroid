# 0063. Dynamic child procedures in stateful parent walkthroughs

**Status:** Accepted.
**Date:** 2026-10-18

## Context

[ADR-0053](0053-guided-walkthroughs.md) pins a procedure record and derives a walkthrough plan exclusively from that record's static `composes` edges. A procedure with no composed children is therefore immediately `complete`. This is unsuitable for the shared `polaroid.using` workflow: the agent must discover the task-specific procedure at runtime, execute that child's steps, and only then finish the parent. Adding every possible task procedure to the root's static `composes` list would put unrelated work into every plan and would turn a runtime choice into a knowledge relationship.

The live Copilot Chat trials for issue #190 confirmed both sides of the gap: an individual child walkthrough can complete, but `start(polaroid.using)` returns an empty, trivially complete plan and has no relation to that dynamic child.

## Decision

**Add agent mode v7 at `/mcp/agent/v7`.** It serves the v6 tools and their contracts, plus a v7 `start` request and `attach_child`. The schemas and behavior of `/mcp/agent/v1` through `/mcp/agent/v6` remain unchanged.

**A v7 parent can opt into dynamic children.** `start` accepts `allow_dynamic_children` and an optional `snapshot`. When the flag is true, the goal must be a Procedure; `start` pins the supplied snapshot (or the active snapshot when omitted) and returns `in_progress` even if the plan is empty. This represents a parent waiting for the agent to discover a suitable child, rather than a completed no-op.

**`attach_child` appends one runtime child to an open dynamic-parent walkthrough.** The request is `{token, key}`. The key must identify an applicable Procedure in the parent's pinned snapshot. An unknown, non-Procedure, inapplicable, duplicate, or cyclic child is rejected. The candidate's entire composed subtree must also be disjoint from every node already present in the parent plan; otherwise the same leaf could appear at multiple paths and progress would be ambiguous. The relationship is stored only in that walkthrough's ephemeral SQLite event log; it does not publish or revise a knowledge record and does not add a `composes` edge to the corpus.

Attachments are allowed at any time before the walkthrough is closed or expires, including after `status` was `complete`. Adding a child to a previously complete but still open parent makes it `in_progress` again. The token's existing sliding TTL and `close` semantics remain in force.

**Concurrent attachments resolve as an explicit conflict.** Each attachment is based on the dynamic-child set the caller observed. Storage compares that set and appends the child in one transaction. If another attachment changed the set first, Polaroid returns HTTP 409 `walkthrough_conflict`; the caller must peek the current plan and retry. It does not silently merge competing plans whose composed subtrees may overlap.

**The runtime plan includes static and dynamic children.** Static `composes` edges retain their existing semantics; dynamically attached Procedures join the root's runtime sibling set. The combined plan uses the existing `precedes` topological ordering and deterministic key tie-break. A dynamic child may itself have static composed descendants, addressed by the existing full `path` form in `advance`. The parent is `complete` only after at least one dynamic child has been attached and every leaf in the combined plan is `done` or `skipped`; `escalate`, `blocked`, or an outstanding leaf keeps it in progress. In dynamic-parent mode, an empty plan alone is never enough to complete the parent.

**The pinned snapshot is authoritative for late additions.** A dynamically attached child must exist and apply in the snapshot pinned by `start`. A procedure published after that snapshot cannot be attached to the existing walk; the agent must start a new parent at a newer snapshot. This avoids mixing knowledge states inside one plan.

**Persistence stays runtime-only.** Migration 0016 adds `allow_dynamic_children` to the walkthrough's creating event and `dynamic_child_key` to later attachment events, with uniqueness per token. Pre-v7 events default to static mode with no dynamic children. Expiry and `close` still delete every event for the token.

**The agent selects the child; Polaroid does not rank or execute it for the walkthrough.** The prescribed agent loop remains `discover`/`read`, then v7 `start` with `allow_dynamic_children: true`, then task-specific `discover`/`read`, `attach_child`, execution with the agent's normal tools, and `advance` for each child leaf. A later applicable procedure can be attached without restarting the parent. The v7 server tracks completion; it does not enforce that every agent in every host calls Polaroid.

## Consequences

- The v7 `start`/`attach_child`/`advance` sequence can report a parent complete only after its dynamically selected child work completes, and can reopen it when new child work is added.
- Concurrent stale child attachments return `walkthrough_conflict`, never a silently merged plan.
- A selected child is not a permanent corpus relationship and will not appear in later `resolve` plans unless it is separately published as `composes`.
- The walkthrough table gains runtime columns. A database opened by v7 migrates from schema 15 to 16; an older binary refuses the newer schema and cannot be used to roll back the database. Recover by upgrading to a compatible build, not by editing migration history.
- No dependency, task-specific server branch, or change to typed record contracts is introduced.
