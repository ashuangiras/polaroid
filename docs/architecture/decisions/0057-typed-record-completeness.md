# 0057. Mandatory definitions and formal obligations for typed records (Guided Procedures, prerequisite)

**Status:** Accepted. **This is a two-phase decision: the validation rule it defines must not be activated until the authoring backlog it depends on is cleared** (tracked by [#152](https://github.com/ashuangiras/polaroid/issues/152), not re-scoped here). Blocks [ADR-0052](0052-ordered-sibling-steps.md) from being safely enabled against the live corpus, though 0052's own code is independent of this one.
**Date:** 2026-10-15

## Context

Every milestone of the Guided Procedures initiative (ADR-0052 through ADR-0056) assumes a procedure's composed children carry real `requires`/`preconditions`/`postconditions` and real `definition` text — without that, `resolve`'s `plan` has nothing to evaluate and a walkthrough is empty motion. [#152](https://github.com/ashuangiras/polaroid/issues/152), filed the day before this initiative, already found that the live corpus does not: no `definition` on any Domain or Concept record beyond a handful already fixed, and no formal `inputs`/`outputs`/`preconditions`/`postconditions` on any typed `Procedure` record, explicitly left open as needing genuinely new authored content, not extraction, and "explicitly low-value for the procedures currently published" at the time.

Checked against the actual validation code, not assumed: `internal/knowledge/recordcheck.go`'s `scalars` table already tracks exactly this — `definition` is a recognized field for `Domain`, `Concept` and `Procedure`, with its `required` list simply empty (optional everywhere). Turning that into a real requirement is a small code change. But **every publication is validated as a complete catalog** ([internal/knowledge](../../../internal/knowledge)'s own package doc, enforced by `checkCatalog`/`checkRecords` over every record of the resulting snapshot, not only the changed ones) — so activating a stricter rule does not apply only going forward: it is checked against the entire existing corpus on the very next publish, however small and unrelated. Nothing stored is ever re-checked or rewritten (the same guarantee write limits already rely on), but **the next publish of anything would immediately fail** until the backlog is cleared, because the whole catalog is re-validated as a whole, every time.

## Decision

**Two fields become genuinely required, for every future publication, once activated:**

- **`definition`**, non-empty, for every `Domain`, `Concept` and `Procedure` record (`scalars`' existing `required` list, currently `nil`, becomes `[]RecordType{TypeDomain, TypeConcept, TypeProcedure}`).
- **At least one of `requires`, `preconditions`, `postconditions`, or an inherited obligation via `specializes`**, for every `Procedure` record — not all three, since the kernel already treats `requires` alone as sufficient to produce a real obligation (`own_obligation(S, R, S) :- requires(S, R)`, [ADR-0050](0050-corpus-logic.md)'s reasoning kernel); a procedure that specializes another already inherits its obligations, so it need not restate them itself.

A publication that leaves any record without these, once this is active, fails the same way any other invalid-knowledge publish does: `invalid_request`, naming every offending record and field, nothing stored.

**This is explicitly not activated by this ADR.** It is written and reviewable now, against real code, so the rule itself can be agreed before the larger authoring effort begins — but turning it on is a second, separate step, gated on the backlog being actually cleared first: every live Domain, Concept and Procedure record carrying a real, accurate `definition` (no invented claims, the same standard already applied when the `check.*` Concepts' definitions were written), and every Procedure having real formal obligation content or a `specializes` parent that does. This authoring work is #152's, not re-scoped or re-planned here; this ADR only defines what "done" means for it, precisely enough to check mechanically.

**The completion gate is checkable, not a judgment call.** `discover(kind="stats")` ([ADR-0051](0051-agent-mode-discover-kinds.md)) is extended with a completeness dimension: how many `Domain`/`Concept`/`Procedure` records are missing `definition`, and how many `Procedure` records have none of `requires`/`preconditions`/`postconditions`/`specializes`. The backlog is clear exactly when both counts are zero against the live corpus; this ADR's validation rule is activated only then, as a distinct, later, separate code change (not committed to by this ADR, which only defines the rule and the gate).

**Explicitly not decided here:**
- Any similar bar for `Claim`, `Evidence`, `Exception` (their key scalars — `statement`, `program`, etc. — are already required where it matters; this ADR does not add more there).
- Whether `informal_conditions`, `inputs`/`outputs` (with `outputs_complete`) should also become required on `Procedure` — left optional; `requires`/`preconditions`/`postconditions` are the load-bearing fields for obligation resolution, not these.
- The backlog authoring work itself: scope, sequencing and effort remain #152's.

## Consequences

- Once active, authoring a new procedure or typed record without a real definition, or a Procedure with no formal obligation content at all, is refused at publish time — a real, if deliberately deferred, tightening of what Polaroid accepts.
- Nothing already stored is affected or re-checked; the immutability and no-retroactive-rewrite guarantees elsewhere are untouched.
- Until the backlog is cleared and this is actually activated, Guided Procedures (ADR-0052 onward) can still be built and merged, but should not be treated as meaningfully usable against the live corpus: `precedes` and `plan` would mostly resolve against empty obligation sets.
- The stats feature shipped earlier in this initiative ([ADR-0051](0051-agent-mode-discover-kinds.md)) turns out to be exactly the tool needed to measure and announce readiness for this one — a dependency this ADR did not originally anticipate when 0051 was designed, found only by tracing what "complete" would actually require checking.
