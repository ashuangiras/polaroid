# Context-efficiency baseline (#131)

This page is a measurement, not a work log: it records what the interface costs an agent, in tokens, for one representative workflow, both before and after [ADR-0042](../architecture/decisions/0042-context-efficient-projections.md). [context-efficiency-proposal.md](context-efficiency-proposal.md) is the design; this page is the evidence. Updated for the [PR #132](https://github.com/ashuangiras/polaroid/pull/132) review round: a fourth variant (selective field retrieval), the `optimized-warm` figure relabelled explicitly as conditional, and a real (non-synthetic) correctness check alongside the token benchmark.

## Method

[scripts/benchmark-context.py](../../scripts/benchmark-context.py) drives the workflow over the **real MCP transport** (`tools/call` JSON-RPC, the exact envelope, headers and `_meta` an MCP client sends), against an isolated `polaroidd` (temporary database, loopback port, started and torn down by the script). No LLM is in the loop, and nothing it does is sent to a model: every request and response is captured verbatim to `bin/benchmark/<timestamp>/` (git-ignored, like `bin/dogfood`), and this document is the durable, human-readable summary of one representative run. It uses **synthetic, pre-written evidence** (fixed exit codes and excerpts) to keep the token measurement fast and deterministic; [scripts/verify-change-real-check.py](../../scripts/verify-change-real-check.py) is the separate, real counterpart (below).

**Tokens:** the script prefers a real tokenizer (`tiktoken`'s `o200k_base`); this environment has no network access to install it, so the figures below use its documented, deterministic fallback estimate (a run of letters/digits counts as one token, every other non-space character as one), applied identically to every call and every variant, so the *relative* comparison is meaningful without ever calling a model. **What is counted** is the tool result text (`content[0].text`), what a conforming agent client forwards to the model — not the outer JSON-RPC envelope, which the script also records separately as a measure of MCP's own transport overhead.

## Workflow and variants

The `verify-change` workflow exactly as bound and documented ([AGENTS.md](../../AGENTS.md), [procedural-loop.md](procedural-loop.md)): `dev.change.verify` version 4, `build` applicable, `checks` pinned at version 4, `hosted-ci` decided not applicable (this repository's recorded policy is local-only). Four variants, each at its own fresh target commit so none can rely on another's recorded evidence:

- **baseline**: today's full interface before [ADR-0042](../architecture/decisions/0042-context-efficient-projections.md) — `resolve_binding` (full graph) before and after recording, `get_version` in full for every node.
- **optimized-cold**: `prepare_execution` (compact) instead of `resolve_binding` before running, `format=plan` instead of the full graph to confirm; `get_version` is still read in full for every node, because a cold agent (or a fresh session) has no cached content yet.
- **optimized-cold-selective** (new, review finding 3): the same, but each `get_version` read names `fields=contract,instructions` instead of the complete version. Still cold — nothing is cached, nothing is skipped — it just asks for less. The correctness check below is identical to every other variant's, so a pass is evidence the workflow completes correctly with less information, not only a smaller number.
- **optimized-warm** — **CONDITIONAL, not a measurement of real agent/session context savings** (review finding 1): the three `get_version` calls are not made at all, on the stated assumption that the caller already holds this exact, immutable `(procedure_id, version)` content from an earlier read ([ADR-0004](../architecture/decisions/0004-append-only-versions-with-expected-base.md) makes that content cacheable forever in principle). Polaroid does not yet let a client check that assumption cheaply over the wire (no `ETag`/`If-None-Match`, open in the proposal): this script cannot, and does not, verify that a real session actually skipped those reads — only what the token cost would be *if* it safely could.

**Correctness, checked before any number is trusted:** the script asserts every variant's parent execution is `verified: true`, with the identical `outcome` and the identical selected `(procedure_id, version)` per reference path as the baseline, and that each variant's own pre- and post-run reads agree with each other. A variant that is cheaper but reaches a different answer fails the script, not just the comparison.

## Results

| Variant | Calls | Response tokens (tool result) | Reduction vs. baseline |
| --- | ---: | ---: | ---: |
| baseline | 9 | 9,771 | — |
| optimized-cold | 9 | 7,858 | 19.6% |
| optimized-cold-selective | 9 | 6,345 | 35.1% |
| optimized-warm (*conditional*) | 6 | 2,364 | 75.8% |

`optimized-warm`'s 75.8% exceeds the ≥70% target in [#131](https://github.com/ashuangiras/polaroid/issues/131) **conditionally**: it is a real token count for a real call sequence, but the premise that those three reads could be skipped is asserted, not independently verified over the wire. `optimized-cold-selective` is the strongest *unconditional* result: 35.1% fewer tokens with no assumption beyond what `fields=` already lets any client ask for today, and the real-check below additionally confirms a real, non-synthetic workflow still completes correctly while using exactly this selective content.

Tool schema cost (review finding 6, reported separately, never added to any total above): `tools/list` is 14,433 tokens, 46,650 bytes — a one-time, per-connection cost, not paid per workflow call.

## Real executable workflow check (review finding 2)

[scripts/verify-change-real-check.py](../../scripts/verify-change-real-check.py) does not use synthetic evidence: it resolves via `prepare_execution` against this repository's **real current commit and working-tree state**, decides `build`/`hosted-ci` not applicable (the effective inputs name no `build_command`; the policy is local-only), and then **actually runs** `gofmt -l .` and `go vet ./...` as real subprocesses, with their real exit codes and real (trimmed) output as the recorded evidence. It also reads the `checks` version with `fields=instructions` (not in full) and reports the size difference, making explicit that the two commands it ran came entirely from `prepare_execution`'s own `inputs` — never from parsing `instructions` text, which stays opaque (AGENTS.md). A real command failure is reported honestly (exit 1, verification correctly absent), never masked.

One representative run (your numbers will vary with the real commit and tree state):

```
real commit: 9cb43e2aa20c8d8c791852467e5129d12259e254
working tree: modified
prepare_execution: next_action=checks, effective gate_commands={'gate_commands': ['gofmt -l .', 'go vet ./...'], 'working_tree': 'modified'}
selective fields=instructions read: 1553 bytes vs 4720 bytes in full (67% smaller)
  ran: 'gofmt -l .' -> exit 0, ok: (no output)
  ran: 'go vet ./...' -> exit 0, ok: (no output)
REAL RESULT: verified=true, 2 real command(s) run, outcome=succeeded, next_action=none
```

This is deliberately a small, fast gate (two commands, not the full `make check`/`demo`/`e2e`), so it stays a seconds-long check: it demonstrates correctness of the compact interface for a real run, it does not replace the repository's full verification gate.

## Reading this baseline

- **Cold-run floor.** The first time an agent (or a fresh session) reads a version's content, it must cross the wire; `optimized-cold`'s 19.6% is the honest, unconditional ceiling for a *complete* read with this increment alone. `optimized-cold-selective`'s 35.1% shows that ceiling is lower once selective retrieval is used, and real-checked.
- **`optimized-warm` is a conditional projection**, not a verified session saving, until conditional fetch (`ETag`/`If-None-Match`) exists to make the underlying assumption checkable. Treat 75.8% as "if a client can safely skip these reads, this is what it would cost", not as a reported fact about any actual Copilot session.
- **Scope of this baseline.** Only `verify-change` is measured here. Discovery, feedback and toil workflows are not, and should be benchmarked the same way before generalizing any conclusion to them.

## Reproducing

```sh
make build
python3 scripts/benchmark-context.py        # token benchmark, synthetic evidence, four variants
python3 scripts/verify-change-real-check.py # real, small, fast correctness check
```

The first prints a compact table and the path to `summary.json`; per-call request/response bodies, `manifest.json` and `tools_list.json` sit beside it, under `bin/benchmark/<timestamp>/` (git-ignored). The figures above are one such run's `summary.json`, copied in by hand.
