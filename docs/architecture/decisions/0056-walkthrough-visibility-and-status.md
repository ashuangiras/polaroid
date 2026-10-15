# 0056. Operator visibility, an agent-reported `blocked` status, and an expand option (Guided Procedures, Milestone 5)

**Status:** Accepted (Milestone 5 of the initiative; amends [ADR-0053](0053-guided-walkthroughs.md)'s `advance` outcome vocabulary, additively, and is the first milestone to touch anything outside `/mcp/agent*`).
**Date:** 2026-10-15

## Context

Three things were left open after Milestone 4: operators have no way to see active walkthroughs at all (every other record kind has one); an agent can only report a leaf `done`, `skipped` or `escalate`, with no way to say "this is actively blocked" as distinct from "I haven't decided yet"; and a plan is always discovered one leaf at a time, with no way to see the whole expanded tree up front.

The second of these ran into a real constraint, checked against the reasoning kernel rather than assumed: Corpus Logic is deliberately **open-world** — "negation is never inferred from absence in domain facts... reported as an unknown, never as absent" ([ADR-0050](0050-corpus-logic.md)), enforced in the kernel itself (`internal/reasoning/datalog/datalog.go`: an unsafe rule using negation fails with "negation is never inferred from absence: absence of knowledge is not evidence"). That means Polaroid **cannot derive** "this precondition is actively violated" the way it derives "this obligation is required" — there is no sound way, by design, to conclude a fact is false merely because it was not asserted. A richer "blocked" status therefore cannot be a new kind of reasoning; it can only be something the agent itself reports, exactly like `done`/`skipped`/`escalate` already are.

## Decision

**A fourth agent-reported outcome status, `blocked`**, alongside `done`/`skipped`/`escalate` on `advance`'s `outcome`, with an optional `reason` (free text, for example "precondition check.vet's required tool is not installed"). Polaroid never evaluates whether a precondition actually holds; `blocked` is the agent's own judgment, stored and folded exactly like every other status. It freezes `next` the same way `escalate` does (ADR-0053), for the same reason — something stops forward progress until corrected — but is kept a distinct value so a human or monitoring tool reading a walkthrough can tell "waiting on a person's judgment" (`escalate`) apart from "waiting on an external, technical precondition" (`blocked`). Both are cleared the same way: a later `advance` reporting `done` or `skipped` for that key.

**Operator visibility, on the legacy `/mcp` and HTTP surfaces, not `/mcp/agent`.** Two new read-only operations, `list_walkthroughs` and `get_walkthrough`, following the existing list/get and pagination conventions ([ADR-0021](0021-targeted-feedback-and-bounded-lists.md), [ADR-0023](0023-pagination-guarantees-and-scope-labels.md)): `list_walkthroughs` filters by `repository`, `goal` and `status`, paginated with `limit`/`after`; `get_walkthrough` returns one walkthrough's `token`, `goal`, `snapshot`, `repository`, `status`, `expires_at`, and its current per-leaf fold, so an operator can see exactly where a stuck walkthrough is waiting. This is the first milestone in the whole initiative to add anything outside `/mcp/agent*`: walkthroughs are SQLite-backed runtime state ([ADR-0053](0053-guided-walkthroughs.md)), and the legacy `/mcp`/HTTP surface, not `/mcp/agent`, is where every other piece of SQLite-backed administrative visibility already lives (executions, feedback, toil items). Agent mode itself gains nothing new here: an agent still only ever sees its own walkthrough, by the token it holds.

**An `expand` argument on `start` and `advance`** (boolean, default false): when true, `plan` entries recursively include their own `children` (the same `{key, status}` shape, nested), instead of the agent discovering depth one leaf at a time. Safe without any new bound, because the underlying `composes` graph is already finite by construction ([ADR-0009](0009-reference-graph-rules.md), reused by [ADR-0054](0054-multi-level-plans.md)). Off by default, so the context-economy goal that started this whole initiative is the default behavior; `expand` is an explicit, informed opt-in to a larger response.

**Explicitly not decided here:**
- Any write/administrative operation on a walkthrough from the operator surface (closing or extending one on an agent's behalf) — visibility only.
- Counting walkthroughs in catalog statistics (ADR-0036) — still explicitly excluded.

## Consequences

- `advance`'s outcome vocabulary grows by one value and one optional field (`reason`); existing `done`/`skipped`/`escalate` callers are unaffected.
- `/mcp`'s tool count grows by two, the first change this initiative makes outside `/mcp/agent*`; `docs/architecture/mcp.md`'s table and the pinned full-mode contract (`testdata/full-mode-tools.json`) are updated together with it, the same discipline every prior tool change in this file has followed.
- `start`/`advance` gain one more optional argument (`expand`); a caller that never sends it sees exactly the one-level, path-addressed behavior Milestones 1 through 4 already defined.
- This remains an open-world system throughout: nothing here adds negation, inference from absence, or any new way for Polaroid to conclude a fact is false. `blocked` is agent testimony, not derived knowledge, the same category `done`/`skipped`/`escalate` already are.
