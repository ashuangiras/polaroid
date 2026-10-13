# Context-efficiency baseline (#131)

This page is a measurement, not a work log: it records what the interface costs an agent, in tokens, for one representative workflow, both before and after [ADR-0042](../architecture/decisions/0042-context-efficient-projections.md). [context-efficiency-proposal.md](context-efficiency-proposal.md) is the design; this page is the evidence.

## Method

[scripts/benchmark-context.py](../../scripts/benchmark-context.py) drives the workflow over the **real MCP transport** (`tools/call` JSON-RPC, the exact envelope, headers and `_meta` an MCP client sends), against an isolated `polaroidd` (temporary database, loopback port, started and torn down by the script). No LLM is in the loop, and nothing it does is sent to a model: every request and response is captured verbatim to `bin/benchmark/<timestamp>/` (git-ignored, like `bin/dogfood`), and this document is the durable, human-readable summary of one representative run.

**Tokens:** the script prefers a real tokenizer (`tiktoken`'s `o200k_base`); this environment has no network access to install it, so the figures below use its documented, deterministic fallback estimate (a run of letters/digits counts as one token, every other non-space character as one), applied identically to every call and every variant, so the *relative* comparison is meaningful without ever calling a model. **What is counted** is the tool result text (`content[0].text`), what a conforming agent client forwards to the model — not the outer JSON-RPC envelope, which the script also records separately as a measure of MCP's own transport overhead.

## Workflow and variants

The `verify-change` workflow exactly as bound and documented ([AGENTS.md](../../AGENTS.md), [procedural-loop.md](procedural-loop.md)): `dev.change.verify` version 4 (the version on `main`; none of the unmerged, frozen `#127`/`#128`/`#129`/`#130` work is in this measurement), `build` applicable, `checks` pinned at version 4, `hosted-ci` decided not applicable (this repository's recorded policy is local-only). Three variants, each at its own fresh target commit so none can rely on another's recorded evidence:

- **baseline**: today's full interface before [ADR-0042](../architecture/decisions/0042-context-efficient-projections.md) — `resolve_binding` (full graph) before and after recording, `get_version` in full for every node.
- **optimized-cold**: `prepare_execution` (compact) instead of `resolve_binding` before running, `format=plan` instead of the full graph to confirm; `get_version` is still read in full for every node, because a cold agent (or a fresh session) has no cached content yet.
- **optimized-warm**: the same, but the three `get_version` calls are **not made at all**: this variant states, explicitly, that it simulates a session that has already read these exact, immutable `(procedure_id, version)` pairs before, which [ADR-0004](../architecture/decisions/0004-append-only-versions-with-expected-base.md) makes safe to assume. It is **not** a claim that conditional fetch (`ETag`/`If-None-Match`) is implemented over the wire to make that assumption checkable at low cost: that remains open, named in the proposal.

**Correctness, checked before any number is trusted:** the script asserts every variant's parent execution is `verified: true`, with the identical `outcome` and the identical selected `(procedure_id, version)` per reference path as the baseline, and that each variant's own pre- and post-run reads agree with each other. A variant that is cheaper but reaches a different answer fails the script, not just the comparison.

## Results

| Variant | Calls | Response tokens (tool result) | Reduction vs. baseline |
| --- | ---: | ---: | ---: |
| baseline | 9 | 9,771 | — |
| optimized-cold | 9 | 7,486 | 23.4% |
| optimized-warm | 6 | 1,992 | **79.6%** |

The **warm** figure exceeds the ≥70% target in [#131](https://github.com/ashuangiras/polaroid/issues/131) for the realistic, repeated case this repository's own [status.md](status.md) history shows: the same few versions of `dev.change.verify` run dozens of times. The **cold** figure is smaller, as expected: a first-ever read of a version's content must cross the wire at least once, and `optimized-cold` only removes the resolution-side repetition (`prepare_execution` and `format=plan`), not the three full version reads.

Per-call detail (response tokens, tool result only):

| Step | baseline | optimized-cold | optimized-warm |
| --- | ---: | ---: | ---: |
| resolve/prepare (before running) | 1,262 (`resolve_binding`) | 300 (`prepare_execution`) | 300 (`prepare_execution`) |
| `get_version` × 3 (root, build, checks) | 3,387 + 911 + 1,196 = 5,494 | 5,494 (unchanged: cold) | *not read* |
| `record_execution` × 3 | 253 + 378 + 449 = 1,080 | 1,080 | 1,080 |
| `get_verification` | 262 | 262 | 262 |
| resolve/confirm (after running) | 1,673 (`resolve_binding`) | 350 (`format=plan`) | 350 (`format=plan`) |
| **Total** | **9,771** | **7,486** | **1,992** |

**Repeated content**, measured as the byte-level overlap between each variant's first and last call: baseline's two `resolve_binding` calls share 99.45% of their bytes unchanged; `optimized-cold`/`optimized-warm`'s `prepare_execution` and `format=plan` confirmation share 97.04%. `format=plan` already removed the large, repeated graph structure (`canonical_key`, `scope`, nested nodes); what remains shared is mostly identifiers and the step shape itself, a smaller further opportunity this page does not claim to have closed.

**Tool schema list** (`tools/list`): 14,433 tokens, 46,650 bytes. This is a one-time, per-connection cost, not paid per workflow call: a conforming client fetches it once per session, not on every turn, so it is reported separately here, never added into a per-workflow total.

## Reading this baseline

- **Cold-run floor.** The first time an agent (or a fresh session) reads a version's content, it must cross the wire; `optimized-cold`'s 23.4% is the honest ceiling for that case with this increment alone.
- **Warm runs are the realistic case**: a client that already holds a version's content, or has already seen a binding's graph shape, needs only confirmation of what changed. 79.6% is measured, not projected, with the correctness assertions above holding throughout.
- **Scope of this baseline.** Only `verify-change` is measured here. Discovery, feedback and toil workflows are not, and should be benchmarked the same way before generalizing any conclusion to them.

## Reproducing

```sh
make build
python3 scripts/benchmark-context.py   # needs bin/polaroidd, bin/polaroid
```

Prints a compact table and the path to `summary.json`; per-call request/response bodies, `manifest.json` and `tools_list.json` sit beside it, under `bin/benchmark/<timestamp>/` (git-ignored). The figures above are one such run's `summary.json`, copied in by hand.
