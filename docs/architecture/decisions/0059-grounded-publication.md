# 0059. Grounded publication: non-vacuous composition, and agent-proposed knowledge by citation and evidence

**Status:** Accepted (Milestone 1 of a new initiative; Part 1 applies to every publish path unconditionally; Parts 2–4 open `/mcp/agent`'s first write capability, a deliberate, narrowly bounded reversal of the stance ADR-0049 through ADR-0058 held).
**Date:** 2026-10-17

## Context

Running the live corpus's own composition check (`resolve` with `then`, [ADR-0050](0050-corpus-logic.md)) between every `precedes`-adjacent pair under `dev.change.lifecycle` returns `"verdict":"undetermined"` with `"reason":"nothing formal establishes..."` on every single finding, for every pair checked. [ADR-0057](0057-typed-record-completeness.md)'s completeness bar (cleared by #164) only required *presence* — at least one of `requires`/`preconditions`/`postconditions`/`specializes` — and every live Procedure satisfies it entirely on the *needs* side: none declares a formal `postcondition`, `output` or `invariant` at all. Presence without teeth, demonstrated directly, not assumed.

The direct fix — require every publish to also carry real exceptions, conditional obligations and detected conflicts — was considered and rejected: a `Conflict` is something Corpus Logic *finds*, never something an author can truthfully assert exists; many real procedures (`gate.check.fmt-check`: *"Fail if any Go source is not gofmt-formatted"*) are genuinely unconditional and exception-free, and forcing one onto them would mean inventing an untrue claim in an immutable, audit-grade store — directly against the standard ADR-0057 itself already set clearing its own backlog: *"no invented claims, the same standard already applied when the `check.*` Concepts' definitions were written."* What's actually missing is narrower and fully checkable without fabrication: producers never declare what they formally produce, so a formally sound handoff can never be decided either way.

A second question followed: who authors the correction, and how is it trusted without inventing content. Mandating every record carry fabricated richness was rejected above; the alternative settled on is an agent proposing grounded content directly, with no operator step — a deliberate reversal of this project's repeated stance (*"storing is an explicit, approved operator action, never something an agent triggers on its own,"* [knowledge-guide.md](../knowledge-guide.md); restated as recently as [ADR-0056](0056-walkthrough-visibility-and-status.md), which drew this line even for read-only visibility). Removing the human reviewer means whatever substitutes for review has to happen entirely between the agent and Polaroid.

## Decision

### Part 1 — non-vacuous composition, a new publish-time gate (every path, unconditionally)

Extended into `Kernel.publish`'s existing validation (the same call site ADR-0057's completeness check already occupies): for every `precedes`-ordered adjacent pair of Procedures reachable through a `composes` parent in the resulting candidate catalog, `Compose(producer, consumer)` must yield **at least one finding whose status is `established` or `contradicted`** — never all `undetermined`. This forces a producer to formally declare enough of what it actually produces (`postconditions`/`outputs`) to decide at least one of the next step's stated needs, one way or the other. It never requires a contradiction to exist, and it touches no `Exception`/conditional-obligation content at all — exactly the part of the original ask this ADR declines, for the reason above. A Procedure with no composed or `precedes` neighbor (`gate.check.fmt-check`, most of the Terraform procedures) has nothing to check and is unaffected.

### Part 2 — `propose`, a new, standalone `/mcp/agent` tool

A new tool, deliberately independent of `feedback` (a different purpose, append-only and never itself changing served knowledge): an agent submits a candidate addition — new formal fields on an *existing* Procedure (`definition`, `requires`, `preconditions`, `postconditions`, `invariants`), a new supporting `Concept` or `Claim`, or a wholly new Procedure (Parts 3–4). Three checks, all computed by Polaroid alone, no external I/O and no human:

- **Citation self-consistency.** Every new formal-field or Concept/Claim submission carries a required citation, `{source, quote}` (`source` a file path or an existing record's key, for provenance only; `quote` the exact text). Polaroid's only enforceable check is that `quote` appears verbatim inside the new content being proposed — it cannot confirm `source` actually says this in the real repository (Polaroid has no filesystem access to a consuming repository anywhere in this architecture; `repository` is already documented as "just a string," [knowledge-guide.md](../knowledge-guide.md)). This does not prove truth; it proves the agent is not inventing content disconnected from what it claims grounds it, and the citation is retained permanently in the published record for later, independent audit.
- **Non-vacuousness and completeness** (Part 1, ADR-0057): the candidate must still leave the whole catalog passing both.
- **Novelty, by resolving the difference.** The candidate is merged into a scratch candidate catalog (the same candidate-construction `Kernel.publish` already does before committing); every goal whose relevant set could include it is resolved before and after. No change in any resolution's result means the candidate is redundant and is refused as such — not because it's wrong, but because it adds nothing a correct resolution didn't already conclude.

Passing all three, `propose` commits immediately through the same `Kernel.publish` call the `knowledge-publish` CLI already uses. No draft, no queue, no operator: agent and Polaroid only, exactly as scoped.

### Part 3 — Tier 1: orchestration Procedures

A wholly new Procedure may be proposed if its entire `instructions` is exactly an ordered `composes` of Procedures that **already exist** in the active catalog — nothing else, no new freeform steps. This asserts no new operational claim, only a grouping and ordering of already-trusted steps (the same shape `dev.change.lifecycle` already has: *"Follow dev.change.implement, then dev.change.verify, then dev.change.review"*). It needs no citation or evidence beyond the composition graph itself already being valid (acyclic and bounded, [ADR-0009](0009-reference-graph-rules.md)) and Part 1's non-vacuousness applying to its own new edges exactly as to any other.

### Part 4 — Tier 2: leaf Procedures with genuinely new instructions, evidence-backed

A wholly new Procedure with genuinely new `instructions.steps` — content citation-grounding cannot help with, since it is synthesis, not quotation, and Polaroid "derives nothing" from free-form steps even today ([ADR-0052](0052-ordered-sibling-steps.md)) — may be proposed only together with:
- a new `Claim` stating its outcome ("following these steps produces X"), and
- at least one new `Evidence` record, `supports`-linked to that Claim, whose `Source` names something independently inspectable (a Dispatch run identifier, a retained command transcript, a specific file and line) describing a concrete run the agent actually performed, this session, of exactly these steps.

**Correction found while designing this, not assumed going in:** this needs no new record type or schema. `Evidence`, `Claim` and the `supports` relation already exist in `internal/knowledge/record.go` and are already part of Corpus Logic's resolution (`ClaimStatus`: `supported`/`contradicted`/`contested`/`unsupported`) — the live corpus simply has zero `Evidence` records today. Tier 2 adds one new *requirement* `propose` enforces for this specific case, not new plumbing: a Claim that `propose`'s own resolution would already report as `unsupported` or `contested` is refused outright; only one resolving `supported`, with nothing attached that `contradicts` it, may be published this way.

**Explicitly acknowledged, not solved:** one supporting run proves the steps worked once, for the one agent that ran them; it does not prove they generalize to every future case. This is the strongest anchor available without a human reviewer, stated as exactly that, not as a claim of certainty.

**Explicitly not decided here:**
- Verifying a citation's `source` actually exists and says what's claimed, in the real repository, at proposal time — would require Polaroid to read an arbitrary consuming repository's filesystem, a capability it has nowhere else in this architecture; citations are retained for later, independent audit instead.
- Correcting a `propose`-published record found wrong afterward — the existing immutability and no-retroactive-rewrite rules apply exactly as to any other publish; a correction is a new, superseding revision, same as always, never a rewrite.
- Any rate limit or budget on `propose` calls in one session.
- Extending this mechanism to retiring or superseding existing records, or to `Rule` records' Mangle programs.

## Consequences

- A future publish — by any path — can no longer leave a `precedes`-adjacent pair of Procedures 100% `undetermined`; closing that gap requires real, formally declared postconditions/outputs, never a fabricated exception or an invented conflict.
- `/mcp/agent` gains its first write capability, a deliberate, narrow reversal of every prior milestone's stance (ADR-0049 through ADR-0058): the boundary moves from "agent mode never writes" to "agent mode writes only what it can show its work for" — a verbatim citation for existing formal content, pure recomposition of already-trusted steps for orchestration, and run evidence through the corpus's own already-defined (if previously unused) Claim/Evidence apparatus for new leaf behavior.
- The existing `Evidence`/`Claim`/`supports` schema gets real, first use.
- This ADR defines the mechanism precisely enough to build and use, the same way ADR-0057 defined completeness before #152/#164 did the actual authoring; using `propose` to close the live corpus's own composition gaps across its 31 Procedures is the corresponding follow-on work, not performed by this ADR itself.
