# 0054. Multi-level plans: path-addressed leaves (Guided Procedures, Milestone 3)

**Status:** Accepted (Milestone 3 of the initiative; amends [ADR-0053](0053-guided-walkthroughs.md)'s `advance.outcome`, additively).
**Date:** 2026-10-15

## Context

[ADR-0052](0052-ordered-sibling-steps.md) and [ADR-0053](0053-guided-walkthroughs.md) resolve and track exactly one level of a goal's `composes` children, by design — the same scoping `read`'s references already use ("resolved only one level"). That is enough when every step is a leaf, but a composed child can itself be composed, and the whole point of procedure composition is reuse: "a stable identity... so many repositories can reuse it without copying" ([records.md](../records.md)). Once a sub-procedure can appear under more than one parent (or more than once in the same plan, via two different branches), a bare key stops identifying *which occurrence* an `advance` call means.

## Decision

**`plan`'s top-level shape is unchanged**: one level, each entry naming an immediate composed child. What changes is where that child's status comes from: **a composed child's status is always derived, never directly reported** — "done" once every descendant beneath it is done (by the same fold rule ADR-0053 already defined, applied bottom-up), "escalate" if any descendant leaf under it is frozen there. An agent never calls `advance` for a composed key; only for leaves.

**`next` may point several levels down.** When the first not-done immediate child is itself composed, `next` is **not** that child — it recurses into its `plan` (the same `precedes`-ordering rule, applied at every level) to find the actual next leaf, and reports it with a **`path`**: the full chain of keys from the goal down to that leaf. A plan whose first outstanding child is already a leaf degenerates to exactly Milestone 2's behavior: a one-element path.

**`advance`'s `outcome` is amended, additively**: it accepts either the existing bare `key` (shorthand for a one-element `path`, so a one-level plan's `advance` call is unchanged from ADR-0053) or an explicit `path` array, for a leaf found several levels down. A `path` that does not resolve to an actual leaf under the goal — wrong chain, or naming a composed node instead of a leaf — is `invalid_request`. ADR-0053's status line is updated to name this amendment, its own text unchanged, the same way ADR-0049 was amended by ADR-0051 without rewriting it.

**No new size or depth limit.** The underlying `composes` graph is already cycle-checked and bounded at publish time (`internal/knowledge/validate.go`'s `MaxGraphDepth` (32) and `MaxGraphNodes` (2048), the same limits the legacy `/mcp` reference graph already enforces, [ADR-0009](0009-reference-graph-rules.md)): by the time a plan is resolved, its graph is already guaranteed finite, so walking it to find the next leaf needs no new bound or error code.

**`escalate`'s freeze rule (ADR-0053) now applies at the leaf level, unchanged in substance**: an escalated leaf blocks `next` from passing it, however deep it is; the composed ancestors above it report `escalate` too (derived), making the frozen state visible at the top level without the agent needing to already know the path.

**Response size stays bounded by design, not by a new limit.** `plan`'s top level is always exactly the goal's immediate children (never inlining their own descendants); only `next`'s single `path` chain is ever included for the deep case, not the whole expanded tree. An agent that wants to see a composed child's own children explicitly calls `resolve` or `start`/`read` on that key, exactly as it already does for any one-level reference today.

**Explicitly not decided here:**
- Whether `start`/`resolve` should optionally return the *whole* expanded tree on request (an explicit opt-in, for an agent that wants to plan ahead rather than discover depth-first) — left for if it proves needed.
- Any change to `discover`, `read` or `feedback`.

## Consequences

- Multi-level plans are a pure generalization: every one-level plan and every existing `advance` call (by bare `key`) keeps meaning exactly what it meant under ADR-0053.
- Reused sub-procedures are now safely addressable in a nested plan; an unreused one never needs more than its own bare key.
- Nothing about catalog growth, validation limits or compatibility changes elsewhere: this milestone adds no new persisted concept, reusing ADR-0053's walkthrough storage and ADR-0052's `precedes` ordering at every level, and the graph bound that already existed for an unrelated reason (ADR-0009) turns out to already cover this case too.
