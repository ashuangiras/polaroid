# Context-efficient interface: a proposal (#131)

**Status: proposed, not yet implemented.** This page is the architecture proposal asked for in [#131](https://github.com/ashuangiras/polaroid/issues/131), to be read with its measured baseline, [context-efficiency-baseline.md](context-efficiency-baseline.md). No code here has shipped; a follow-up work item implements it, each piece as its own change with its own contract-impact statement and tests, per [AGENTS.md](../../AGENTS.md).

## Context

The baseline shows, for one cold `verify-change` run: 65% of all response bytes are full `contract`/`instructions` text for versions that do not change between runs, and a second `resolve_binding` call (to confirm what a run just verified) repeats 99.48% of the first call's bytes unchanged. Neither is a defect in what is stored or returned — every field documented in [http-api.md](../architecture/http-api.md) is there because some client needs it, at least once. The cost is in always sending everything, every time, to every client, whether or not it already has it or needs it yet.

## Goals

- Make it cheap for an agent that already holds some of Polaroid's (immutable) content to avoid re-fetching it.
- Make it cheap for an agent that only needs a decision (did this verify? what should I run next?) to get just that.
- Never make it harder, slower or less correct for an agent to get the full record, including full contracts and full evidence, whenever it actually needs them.

## Non-goals (explicitly out of scope for this proposal)

- Rewriting any existing procedure's content. The size of `dev.change.verify`'s own contract and instructions is a separate, editorial question; this proposal is about how often that content crosses the wire, not how long it is.
- Any image or binary encoding of payloads.
- A new query language, transport, or framework. Every piece below is a small, additive change to the existing JSON HTTP API and the MCP tools over it ([ADR-0014](../architecture/decisions/0014-mcp-transport.md)).
- Any change to storage semantics, immutability, version/revision conflict rules, write limits, or verification derivation. Every one of those stays exactly as specified in [records.md](../architecture/records.md) and their ADRs.

## What stays unchanged

A client that sends no new parameter and no new header gets **byte-identical** responses to every existing endpoint, tool and CLI command, in the same shapes documented today. Nothing is removed from any response; fields are only ever added opt-in, by asking for a projection, and omitted fields are simply absent (never replaced with `null` or a placeholder, consistent with how optional fields already work, for example `applicability` or `goal`). Storage, migrations, immutability triggers, pagination guarantees ([ADR-0023](../architecture/decisions/0023-pagination-guarantees-and-scope-labels.md)), and derived verification ([ADR-0012](../architecture/decisions/0012-derived-verification.md), [ADR-0018](../architecture/decisions/0018-selection-evidence-and-target-verification.md), [ADR-0024](../architecture/decisions/0024-child-inputs-follow-the-reference-mapping.md), [ADR-0029](../architecture/decisions/0029-conditional-references-and-applicability-decisions.md)) are untouched: this proposal changes what crosses the wire, never what is true.

## Design

### 1. Minimal repetition of immutable records: conditional fetch by content

A procedure version, a binding revision, an execution and a toil revision are each immutable once written ([ADR-0004](../architecture/decisions/0004-append-only-versions-with-expected-base.md) and its successors). That means a stable content identifier for one is possible, and safe to cache forever: a content hash over its canonical (already-compacted) stored bytes.

- Add an `ETag` (the content hash) to the single-record GET endpoints that return one immutable record by ID: `GET /v1/procedures/{id}/versions/{n}`, `GET /v1/bindings/{id}/revisions/{n}`, `GET /v1/executions/{id}`, `GET /v1/toil-items/{id}/revisions/{n}`.
- Honor standard conditional `If-None-Match`: a match is a `304 Not Modified` with no body. A client (an agent's own session cache, or a longer-lived cache keyed by `(procedure_id, version)`) that already holds the exact content never pays for it again.
- Nothing stored, validated or derived changes: this is a read-path cache-validation header, computed from bytes already produced for the existing response.

### 2. Selective procedure and response projections

Add an optional `fields` query parameter (a comma-separated list, repeated-member rules as elsewhere in the API) to the endpoints that return a full version or a full execution: `GET /v1/procedures/{id}/versions/{n}` (`philosophy`, `method`, `goal`, `contract`, `instructions`, `references`), `GET /v1/executions/{id}` (`inputs`, `evidence`, `children`, `decisions`).

- Without `fields`, the response is exactly as documented today (default = everything), so every existing client is unaffected.
- With `fields`, only the named members are included, alongside the identity members every response already always carries (`procedure_id`, `version`, `created_at`, and so on) so the response is still self-describing and still fetchable in full by the same ID.
- An agent that already has `philosophy`/`method`/`goal` cached from an earlier read of the same version (or from discovery) can ask for `fields=instructions` alone; one that only needs to confirm which version it is can ask for no optional fields at all.
- An unknown field name is `400 invalid_request`, naming it, exactly as other unknown-parameter cases already are.

### 3. Compact execution plans

`resolve_binding` and `get_graph` already compute, in one read-only snapshot, exactly what a calling agent needs to decide what to run next ([records.md](../architecture/records.md#composition-graph-implemented)). Today that computation is reported as a full nested tree, every time, including the parts that answer "what does this binding compose" (stable, changes only when a version or binding revision is appended) mixed with the parts that answer "is it verified here, right now" (the only thing a repeated call after recording usually wants).

- Add `format=plan` to both endpoints. The default (no `format`, or `format=full`) is the existing nested tree, unchanged.
- `format=plan` returns, per node: `procedure_id`, `version`, `selected_by`, `decision`/`skipped_by` (for conditional references), and `target_verification` when a target was given — no `canonical_key`, `scope`, `applicability` or nested nodes beyond what identifies them. A client that has already read the full graph once (for example at `resolve_binding_pre` in the baseline) has everything it needs to interpret a later plan response against it; a client that has not can still ask for the full tree, or fetch one node's full record by its `procedure_id`/`version`.
- This directly targets the baseline's second call: a `resolve_binding_post` made only to confirm what just verified can use `format=plan` and carry none of the 99.48% that did not change.

### 4. On-demand access to full contracts and evidence

Nothing above drops anything from the catalog or makes it unreachable: every compact or projected response still carries the identifiers (`procedure_id` + `version`, `execution_id`, `binding_id` + `revision`) needed to fetch the full record, in full, whenever the agent actually needs it — to read a contract before following it for the first time, to quote evidence text in a report, or to audit history. "On demand" means deferred until asked for, never replaced by a summary that cannot be escaped.

## Measurement plan

Extend [scripts/benchmark-context.py](../../scripts/benchmark-context.py) to run the same `verify-change` workflow twice: once cold (as the baseline does), and once **warm**, with `ETag`/`fields`/`format=plan` used as a conforming client would (conditional fetch on the three version reads, `format=plan` on `resolve_binding_post`). Compare `summary.json` totals to [context-efficiency-baseline.md](context-efficiency-baseline.md) before reporting any reduction:

- Warm `verify-change`: target **≥70% fewer response tokens** (the estimate defined in the baseline), the measure asked for in [#131](https://github.com/ashuangiras/polaroid/issues/131).
- Cold `verify-change`: report the actual reduction honestly; it is expected to be smaller, bounded by content that must cross the wire at least once.
- Decision and verification correctness: the warm run must reach the identical `verified: true` conclusion, for the identical combination, as the cold run and as today's interface: the benchmark harness itself asserts this before any token count is trusted, exactly as the baseline's harness already records a real `succeeded` outcome and a real `get_verification` before reporting sizes.

## Suggested sequencing for implementation (a later item, not this one)

1. `fields` projection on version and execution reads — smallest change, highest measured value (65% of baseline bytes).
2. `ETag`/`If-None-Match` on the four single-record GET endpoints — reuse one content-hash function from `internal/transport/wire` so both transports agree.
3. `format=plan` on `resolve_binding`/`get_graph` — addresses the pre/post repetition directly.
4. MCP tool surface: expose the same parameters on the corresponding tools, additively (new optional arguments on existing tools, not new tools), being mindful that MCP clients can cache a stale tool schema after an upgrade (noted in [mcp.md](../architecture/mcp.md) and in this repository's own operating notes) — ship with a clear restart/verification step, as every earlier MCP-visible change here has.

Each step above ships as its own change, with its own `make check`/`make demo`/`make e2e`/`make e2e-mcp` evidence and its own documentation update (`http-api.md`, `records.md`, `mcp.md` as applicable), per AGENTS.md. None of them is implemented by this proposal.
