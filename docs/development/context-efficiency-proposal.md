# Context-efficient interface: a proposal (#131)

**Status: pieces 2 and 3 below are implemented and measured ([ADR-0042](../architecture/decisions/0042-context-efficient-projections.md)); piece 1 (conditional fetch of immutable records) remains open.** This page is the architecture proposal asked for in [#131](https://github.com/ashuangiras/polaroid/issues/131), to be read with its measured baseline, [context-efficiency-baseline.md](context-efficiency-baseline.md), which now reports before/after/cold/warm figures for what shipped.

## Context

The baseline (measured over the real MCP transport, [context-efficiency-baseline.md](context-efficiency-baseline.md)) shows, for one `verify-change` run: 56% of all response tokens are full `contract`/`instructions` text for versions that do not change between runs, and a second `resolve_binding` call (to confirm what a run just verified) repeats 99.45% of the first call's bytes unchanged. Neither is a defect in what is stored or returned — every field documented in [http-api.md](../architecture/http-api.md) is there because some client needs it, at least once. The cost is in always sending everything, every time, to every client, whether or not it already has it or needs it yet.

## Goals

- Make it cheap for an agent that already holds some of Polaroid's (immutable) content to avoid re-fetching it.
- Make it cheap for an agent that only needs a decision (did this verify? what should I run next?) to get just that.
- Never make it harder, slower or less correct for an agent to get the full record, including full contracts and full evidence, whenever it actually needs them.

## Non-goals (explicitly out of scope for this proposal)

- Rewriting any existing procedure's content. The size of `dev.change.verify`'s own contract and instructions is a separate, editorial question; this proposal is about how often that content crosses the wire, not how long it is.
- Any image or binary encoding of payloads.
- A new query language, transport, or framework. Every piece below is a small, additive change to the existing JSON HTTP API and the MCP tools over it ([ADR-0014](../architecture/decisions/0014-mcp-transport.md)).
- Any change to storage semantics, immutability, version/revision conflict rules, write limits, or verification derivation. Every one of those stays exactly as specified in [records.md](../architecture/records.md) and their ADRs.
- **Whether Polaroid should record every successful execution.** The owner is separately reconsidering this; it is a semantic change to what gets recorded, not a context-efficiency projection, and is explicitly out of scope here. This proposal keeps the current execution-recording model exactly as it is (every `record_execution` call unchanged, same contract) and only documents its token cost (below), for that separate decision to use.

## What stays unchanged

A client that sends no new parameter and no new header gets **byte-identical** responses to every existing endpoint, tool and CLI command, in the same shapes documented today. Nothing is removed from any response; fields are only ever added opt-in, by asking for a projection, and omitted fields are simply absent (never replaced with `null` or a placeholder, consistent with how optional fields already work, for example `applicability` or `goal`). Storage, migrations, immutability triggers, pagination guarantees ([ADR-0023](../architecture/decisions/0023-pagination-guarantees-and-scope-labels.md)), and derived verification ([ADR-0012](../architecture/decisions/0012-derived-verification.md), [ADR-0018](../architecture/decisions/0018-selection-evidence-and-target-verification.md), [ADR-0024](../architecture/decisions/0024-child-inputs-follow-the-reference-mapping.md), [ADR-0029](../architecture/decisions/0029-conditional-references-and-applicability-decisions.md)) are untouched: this proposal changes what crosses the wire, never what is true.

## Design

### 1. Minimal repetition of immutable records: conditional fetch by content

**Not implemented by ADR-0042; still open.** A procedure version, a binding revision, an execution and a toil revision are each immutable once written ([ADR-0004](../architecture/decisions/0004-append-only-versions-with-expected-base.md) and its successors). That means a stable content identifier for one is possible, and safe to cache forever: a content hash over its canonical (already-compacted) stored bytes.

- Add an `ETag` (the content hash) to the single-record GET endpoints that return one immutable record by ID: `GET /v1/procedures/{id}/versions/{n}`, `GET /v1/bindings/{id}/revisions/{n}`, `GET /v1/executions/{id}`, `GET /v1/toil-items/{id}/revisions/{n}`.
- Honor standard conditional `If-None-Match`: a match is a `304 Not Modified` with no body. A client (an agent's own session cache, or a longer-lived cache keyed by `(procedure_id, version)`) that already holds the exact content never pays for it again.
- Nothing stored, validated or derived changes: this is a read-path cache-validation header, computed from bytes already produced for the existing response.
- This is why the baseline's **optimized-warm** variant states its assumption explicitly rather than claiming it: without this piece, a client's belief that it already has a version's content is its own bookkeeping, not something Polaroid lets it check cheaply over the wire.

### 2. Selective procedure and response projections

**Implemented ([ADR-0042](../architecture/decisions/0042-context-efficient-projections.md)): `fields=` on `GET /v1/procedures/{id}/versions/{n}` and MCP `get_version`**, a comma-separated list from `philosophy`, `method`, `goal`, `applicability`, `contract`, `instructions`, `references`, `revision_reason` (execution reads are not projected by this increment; see Suggested sequencing).

- Without `fields`, the response is exactly as documented today (default = everything), so every existing client is unaffected.
- With `fields`, only the named members are included, alongside the identity members every response already always carries (`procedure_id`, `version`, `scope`, `created_at`) so the response is still self-describing and still fetchable in full by the same ID.
- An agent that already has `philosophy`/`method`/`goal` cached from an earlier read of the same version (or from discovery) can ask for `fields=instructions` alone; one that only needs to confirm which version it is can ask for no optional fields at all.
- An unknown field name is `400 invalid_request`, naming it, exactly as other unknown-parameter cases already are.

### 3. Compact execution plans

**Implemented ([ADR-0042](../architecture/decisions/0042-context-efficient-projections.md)): `format=plan` on `GET .../graph` and `GET .../resolution` (MCP `get_graph`/`resolve_binding`), and the new `GET /v1/bindings/{id}/preparation` (MCP `prepare_execution`)**, the same projection with its target required. All three compute the identical resolution and target verification `resolve_binding`/`get_graph` already did ([records.md](../architecture/records.md#composition-graph-implemented)); the projection changes only what is reported.

- The default (no `format`, or `format=full`) is the existing nested tree, unchanged.
- `format=plan` returns a flattened, dependency-ordered list of steps, each with `path`, `procedure_id`, `version`, `selected_by`, `decision` (conditional references only), `inputs` (the step's effective inputs at the target, reusing the identical value `target_verification.combination.inputs` already computes, present with a target whatever the step's status) and `status` (`selected`, `decision_required`, `skipped`, `verified` or `pending`) — no `canonical_key`, `scope`, `applicability` or nested nodes. `next_action` names a step still needing attention: any `decision_required` step first, in dependency order (deciding a conditional reference's applicability always precedes running anything, ADR-0029), and only once none remain does the first `pending` step qualify (PR #132 review finding 5 corrected an earlier version that just returned whichever status came first in array order). A client that has already read the full graph once has everything it needs to interpret a later plan response against it; a client that has not can still ask for the full tree, or fetch one node's full record by its `procedure_id`/`version`.
- This directly targets the baseline's second call: a confirmation read made only to learn what just verified can use `format=plan` and carry none of the 99.45%/97.04% that did not change (measured in [context-efficiency-baseline.md](context-efficiency-baseline.md)).
- `prepare_execution` is the same projection with its target (`commit`, `inputs`) required, since preparing to run something always implies an exact one; it is `resolve_binding` plus that requirement and that projection, not a new engine. Its `inputs` per step (review finding 4) is enough to act safely on the next step — what it would run with — without a second call, while never parsing or summarizing `instructions` text to decide *how*: that stays the agent's own judgment, reading the full version (or a `fields` projection of it) when it needs to.

### 4. On-demand access to full contracts and evidence

**Implemented, as a property of 2 and 3, not a separate mechanism.** Nothing above drops anything from the catalog or makes it unreachable: every compact or projected response still carries the identifiers (`procedure_id` + `version`, `execution_id`, `binding_id` + `revision`) needed to fetch the full record, in full, whenever the agent actually needs it — to read a contract before following it for the first time, to quote evidence text in a report, or to audit history. "On demand" means deferred until asked for, never replaced by a summary that cannot be escaped.

### 5. Execution-recording cost (documented, not redesigned)

Out of scope for this change (see the non-goal above), but measured so the owner's separate reconsideration has a number to start from: recording the three executions of one `verify-change` run (two children, one parent) costs about 1,080 response tokens in the benchmark's synthetic evidence, roughly 11-14% of every variant's total, present identically in every variant above (nothing here changes it). That cost scales with how much evidence a call's commands actually produce (excerpts, command lists), not with which projection read the version beforehand.

## Measurement plan

**Done, for pieces 2 and 3.** [scripts/benchmark-context.py](../../scripts/benchmark-context.py) runs the same `verify-change` workflow in four variants — baseline, optimized-cold, optimized-cold-selective and optimized-warm — over the real MCP transport, with `fields`/`format=plan`/`prepare_execution` used as a conforming client would; [scripts/verify-change-real-check.py](../../scripts/verify-change-real-check.py) separately confirms a real (non-synthetic) run of the compact interface against this repository's actual current commit. Results, correctness-checked before being trusted, are in [context-efficiency-baseline.md](context-efficiency-baseline.md):

- **`optimized-warm`: 75.8% fewer response tokens, exceeding the ≥70% target — conditionally** (review finding 1): it assumes the three version reads can be safely skipped, which Polaroid cannot yet let a client check cheaply over the wire (piece 1). Do not cite this as a measured Copilot/agent session saving.
- **`optimized-cold-selective`: 35.1% fewer response tokens, unconditionally**: every read still happens, just with `fields=contract,instructions`; the real-check script additionally confirms a genuine, real-command run stays correct using exactly this narrower read.
- **`optimized-cold`: 19.6% fewer response tokens**, the honest floor when nothing is cached or narrowed yet: this increment does not change how many bytes a first-ever *complete* version read costs (piece 1 would).
- **Decision and verification correctness:** every variant reaches the identical `verified: true` conclusion, for the identical combination and the identical selected `(procedure_id, version)` per reference path as the unoptimized interface; the benchmark asserts this and exits non-zero on any mismatch, before reporting any reduction. The real-check script asserts the same against genuinely executed commands, not synthetic evidence.

Still to measure, once piece 1 ships: a conditional-fetch-aware warm run's further reduction, and whether it changes the cold figure (it should not: a first-ever read still has no `ETag` to send).

## Suggested sequencing for implementation

1. ~~`fields` projection on version reads~~ — **done** (ADR-0042).
2. ~~`format=plan` on `resolve_binding`/`get_graph`, and `prepare_execution`~~ — **done** (ADR-0042).
3. ~~MCP tool surface for both~~ — **done** (ADR-0042): additive optional arguments on `get_version`, `get_graph`, `resolve_binding`, and one new tool, `prepare_execution` (39 tools total, 25 read-only).
4. `fields` projection on `GET /v1/executions/{id}` (`inputs`, `evidence`, `children`, `decisions`) — not yet done; smaller measured value than the version projection, since an execution's own response is already far smaller than a version's.
5. `ETag`/`If-None-Match` on the four single-record GET endpoints (procedure version, binding revision, execution, toil revision) — reuse one content-hash function from `internal/transport/wire` so both transports agree. This is the piece that would let the baseline's `optimized-warm` assumption be checked cheaply over the wire instead of trusted client-side.

Each step ships as its own change, with its own `make check`/`make demo`/`make e2e`/`make e2e-mcp` evidence and its own documentation update (`http-api.md`, `records.md`, `mcp.md` as applicable), per AGENTS.md. Steps 1-3 above are this change (ADR-0042, PR #132); 4 and 5 are proposed, not yet filed as separate work items.
