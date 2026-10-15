# 0052. Ordered sibling steps: a `precedes` relation and `resolve`'s stateless `plan` (Guided Procedures, Milestone 1)

**Status:** Accepted (Milestone 1 of a multi-milestone initiative; later milestones are named in Consequences and decided by later ADRs, not here, the same way [ADR-0044](0044-git-core.md) opened a multi-milestone roadmap that [ADR-0045](0045-living-knowledge-kernel.md) through [ADR-0049](0049-agent-mode-finalized-no-migration.md) then decided one at a time). [ADR-0057](0057-typed-record-completeness.md) names this ADR's `precedes`/`plan` as meaningfully usable against the live corpus only once its completeness bar is met; this ADR's own code is independent of that gate.
**Date:** 2026-10-15

## Context

An agent reading a procedure's guidance today gets `instructions.steps[]` whole: free-form prose the agent alone judges (`when`, `satisfied_when`, `escalate_when`), onto which Polaroid "derives nothing" ([records.md](../records.md#instruction-steps-recommended-convention)). Separately, Corpus Logic ([ADR-0050](0050-corpus-logic.md)) already resolves formal **obligations** (which Concepts a procedure requires, from Rules and `context`) and formal **composition** (`composes`, plus `Preconditions`/`Postconditions`/`Invariants`/`Inputs`/`Outputs` on `Procedure` records, checked pairwise by `resolve`'s `then`). Asked whether `discover` could serve a procedure's steps one at a time, the real gap turned out not to be delivery mechanics but a missing primitive: **every relation Corpus Logic has today is an unordered, canonicalized set** ([recordcheck.go](../../../internal/knowledge/recordcheck.go)'s `relations` table; encoding sorts every target list, and a test rejects an unsorted one) — nothing establishes that one composed step must happen before another. `composes` says what a procedure is made of, never in what order.

This ADR is Milestone 1 of a larger idea (an agent being guided through a procedure's steps, with Polaroid itself resolving which is next): the smallest useful, fully stateless slice — ordering sibling steps and reporting which is next, given facts the caller supplies on every call, exactly as `resolve` already works. It adds no persistence, no new identity, and no record type.

## Decision

**A new relation, `precedes`**, alongside `composes` in the relations table: `{Procedure -> Procedure, acyclic}`, in record-field order next to `composes`. `A.precedes = [B]` means A must be reported done before B is offered as next. Unlike every other relation, `precedes` carries one additional rule the generic `sources`/`targets`/`acyclic` check does not express: **every target must be a sibling** — another key in the same parent's `composes` set as the record declaring `precedes` — checked where `composes` cycles and admission already are (`checkCatalog`/`checkRecords`-adjacent logic), not by the generic relation table alone. A `precedes` target outside the same parent's `composes`, or a cycle among sibling `precedes` edges, is `invalid_request` at publish time, the same category of error as an existing reference cycle.

**`resolve` gains one new, optional result member, `plan`**, populated only when the goal record `composes`. It lists the goal's immediate composed children (one level, not recursive — a composed child that is itself composed is resolved separately when the agent reaches it, exactly as `read`'s references resolve only one level), ordered by a topological sort of their `precedes` edges, each `{key, status}`:

- `"done"`: named in the new `completed` argument.
- `"next"`: the first not-done child in topological order.
- the rest are listed but not specially marked (a future milestone may add richer statuses; this one does not).

A child whose own obligations are not decidable from `context` reports through the **existing** `unknowns` mechanism; `plan` adds no new "blocked" concept. Two children with no `precedes` edge between them (parallel, order-insensitive today) are listed in key order, deterministically.

**A new optional argument, `completed`**, a list of keys: which of the goal's composed children the caller already considers done. Deliberately separate from `context` (governance facts like `concurrency: true`): "I did this" is not the same kind of fact `resolve` already consumes, and conflating them would make `context`'s meaning depend on reading key names, which nothing else in `resolve` does. A `completed` key that is not one of the goal's immediate composed children is `invalid_request`.

**Fully stateless, on purpose.** The caller resends `completed` on every call, exactly as it already resends `context`; `resolve`'s determinism (same snapshot, `context`, `assertions`, `at` and now `completed`, give the same result) is unchanged, just over one more input. No walkthrough, session or progress is stored anywhere. This lets the ordering and resolution idea be validated cheaply before any later milestone pays for persistence, identity or lifecycle.

**Explicitly not decided here, left to later milestones:**
- Whether and how Polaroid should ever remember an agent's progress across calls (a stateful "walkthrough"), including its identity, storage (SQLite, not Git-native knowledge, since it would be per-agent runtime state, not published content), concurrency and retention — a materially bigger decision (new persisted record, new identity scheme, new lifecycle), deliberately deferred.
- Any new tool. `discover` and `read` are unchanged; this milestone only changes `resolve`.
- Multi-level planning (a composed child that is itself composed resolving its own plan as part of the parent's).
- Richer per-child statuses (for example a child whose precondition is violated by what the agent reported, not merely undecided).

## Consequences

- `resolve`'s result grows one more optional member and one more optional argument; a caller that sends neither sees no change, consistent with [ADR-0038](0038-stable-releases-and-compatibility.md)'s additive-minor-release allowance, the same basis [ADR-0051](0051-agent-mode-discover-kinds.md) used for `discover`.
- A procedure author now has a real way to say "do these composed steps in this order," distinct from the unordered `composes` fact that already existed; omitting `precedes` changes nothing (an unordered `plan` in key order, as today's `composes` already implies).
- This does not reverse "Polaroid never evaluates conditions" ([ADR-0029](0029-conditional-references-and-applicability-decisions.md)): that principle is about the legacy `/mcp` subprocedure references, which the agent alone evaluates. Corpus Logic's obligations are already Polaroid-evaluated, by design, since [ADR-0050](0050-corpus-logic.md); `plan` reuses that same evaluation, it does not newly introduce it.
- The next milestone (stateful progress) is a separate, materially larger decision; nothing here commits to it, and this milestone is useful on its own (an agent can already avoid re-deriving step order itself, and avoid resending full prior output, by sending back only the compact `completed` key list).
