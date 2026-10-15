# 0055. Walkthrough completion semantics and an explicit early close (Guided Procedures, Milestone 4)

**Status:** Accepted (Milestone 4 of the initiative; amends [ADR-0053](0053-guided-walkthroughs.md)'s `advance`, additively, the same way [ADR-0054](0054-multi-level-plans.md) already did).
**Date:** 2026-10-15

## Context

Neither [ADR-0053](0053-guided-walkthroughs.md) nor [ADR-0054](0054-multi-level-plans.md) says what happens once a walkthrough actually finishes, or gives an agent any way to end one before its TTL would. Two things need settling: how completion is signaled, without colliding with `resolve`'s own, differently-meant `complete`/`incomplete` pair (ADR-0050: "whether every conclusion the missing context could add was examined" — reasoning completeness, not task completeness); and whether ending one early is worth adding at all, given the TTL (ADR-0053) already bounds every walkthrough's lifetime regardless.

## Decision

**A new response member, `status`**, on `start` and `advance`: `"in_progress"` or `"complete"`, derived purely from whether a `next` leaf remains (the same derivation ADR-0053/0054 already define; nothing new is computed). Deliberately not named `complete`/`incomplete`, to avoid colliding with `resolve`'s existing, unrelated pair on the same endpoint family.

**Completion is not a terminal lock.** Because current status folds the latest report per leaf key ([ADR-0053](0053-guided-walkthroughs.md)), a later `advance` correcting an already-`done` leaf back to `escalate` simply moves `status` back to `in_progress`. No new state machine, no special case: this falls directly out of the existing fold rule.

**No special-cased cleanup on completion.** A `"complete"` walkthrough is not deleted, closed, or treated differently by the TTL; it keeps sliding exactly like any other, peekable the same way, until it expires or is closed (below). One cleanup path, not two.

**`advance` gains one new optional argument, `close`** (boolean). When true, the walkthrough is ended immediately, whether or not every leaf is done (an explicit abandon, not only a completion shortcut), and `outcome` may still be given in the same call (report a final leaf and close in one round trip). A closed walkthrough behaves, from that point on, exactly like an expired one: `not_found` on every later call, and Polaroid keeps no trace that it ever existed — the same "expiry means gone, not gone-but-recorded" stance ADR-0053 already took, applied uniformly to both ways a walkthrough can end. `close` on an already-closed or already-expired token is `not_found`, the same as any other operation on one.

**Why add `close` at all, given the TTL already bounds everything:** an agent that knows it is finished (or knows it is abandoning the attempt) can free the row immediately rather than leaving it to expire on its own; this is a courtesy for the common case, not a replacement for the TTL, which remains the only *required* cleanup path — nothing depends on an agent remembering to call `close`.

**Explicitly not decided here:**
- Any operator-facing count of how many walkthroughs are open, completed or closed (still deferred, as ADR-0053 already said).
- Whether completion or closing should ever surface to catalog statistics (ADR-0036) — walkthroughs remain outside it, as ADR-0053 already decided.

## Consequences

- `start`/`advance`'s result gains one new optional member (`status`) and `advance` gains one new optional argument (`close`); a caller using neither sees no change, the same additive pattern every prior milestone in this initiative has used.
- An agent can now always tell, without inferring it from `next`'s absence, whether a walkthrough is done — and can choose to tidy up after itself without waiting on the TTL, without that choice being required for correctness.
- This is the last of the milestones currently scoped for this initiative's first phase (1 through 4); further milestones (operator visibility, richer per-step statuses, the whole-tree opt-in) remain explicitly open, not committed to by any ADR so far.
