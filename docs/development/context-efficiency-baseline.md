# Context-efficiency baseline (#131)

This page is a measurement, not a work log: it records what today's interface costs an agent, in bytes and estimated tokens, for one representative workflow, before any change. [context-efficiency-proposal.md](context-efficiency-proposal.md) is the design that follows from it.

## Method

[scripts/benchmark-context.py](../../scripts/benchmark-context.py) drives the workflow directly over HTTP, against an isolated `polaroidd` (temporary database, loopback port, started and torn down by the script). No LLM is in the loop, and nothing it does is sent to a model: every request and response is captured verbatim to `bin/benchmark/<timestamp>/` (git-ignored, like `bin/dogfood`), and this document is the durable, human-readable summary of one representative run.

**Token counts are a local, deterministic estimate** (`approx_tokens` in the script): a run of letters or digits counts as one token, and every other non-space character (so every piece of JSON punctuation) counts as one. This is not a real tokenizer and is not meant to predict any model's exact count; it is applied identically to every payload captured, so the *relative* comparison between today's interface and a proposed one is meaningful without ever calling a model.

## Workflow benchmarked

The `verify-change` workflow exactly as bound and documented ([AGENTS.md](../../AGENTS.md), [procedural-loop.md](procedural-loop.md)): `dev.change.verify` version 4 (the version on `main` as of this measurement — none of the unmerged, frozen `#127`/`#128`/`#129`/`#130` work is in this baseline), with its `build` reference conditional (applicable, since the effective inputs name a `build_command`), `checks` pinned at version 4, and `hosted-ci` decided not applicable (this repository's recorded policy is local-only). The store starts empty and is loaded only with `examples/development`'s fixtures, so this is a **cold** run: nothing is yet verified at the target commit.

Nine calls, matching what an agent following the procedure's own `instructions` makes one for one:

| # | Step | Method | Response bytes | Response tokens (est.) |
| --- | --- | --- | ---: | ---: |
| 1 | `resolve_binding_pre` (before anything ran) | GET | 3,452 | 1,262 |
| 2 | `read_version_root` (`dev.change.verify` v4, in full) | GET | 13,903 | 3,387 |
| 3 | `read_version_build` (`go.module.build` v3, in full) | GET | 3,316 | 911 |
| 4 | `read_version_checks` (`go.module.checks` v4, in full) | GET | 4,663 | 1,196 |
| 5 | `record_execution_build` | POST | 649 | 253 |
| 6 | `record_execution_checks` | POST | 904 | 378 |
| 7 | `record_execution_parent` | POST | 1,191 | 449 |
| 8 | `get_verification_parent` | GET | 701 | 262 |
| 9 | `resolve_binding_post` (after recording, to confirm) | GET | 4,777 | 1,673 |

**Totals for this one cold run:** 9 calls, 33,556 response bytes (9,771 estimated tokens), 3,673 request bytes (1,310 estimated tokens). Request cost is already small; the interface's cost to an agent is almost entirely in what it reads back.

## Two concrete sources of repeated or deferrable content

1. **`resolve_binding` is called twice in one workflow (before and after recording), and 99.48% of the first call's bytes reappear, byte-identical, in the second** (3,434 of the first call's 3,452 bytes, measured with a byte-level longest-common-subsequence match). The only information the second call actually adds is which nodes are now verified and by which execution. Today's interface has no way to ask for only that.
2. **The three full version reads are 21,882 bytes, 65% of all response bytes in the workflow**, almost entirely `contract` and `instructions` prose that is immutable for that exact `(procedure_id, version)` ([ADR-0004](../architecture/decisions/0004-append-only-versions-with-expected-base.md)) and already known to any agent (or any earlier run in the same session) that has read that version before. This repository's own [status.md](status.md) records dozens of `verify-change` runs against the same four versions of `dev.change.verify`; a **warm** run (any run after the first against an unchanged version) pays this cost again for no new information.

## Reading this baseline

- **Cold-run floor.** Some of this cost cannot be removed without changing what is verified: the first time an agent (or a fresh session) follows a version, its contract and instructions must cross the wire at least once. A 70% reduction is not expected on a true first-ever cold run.
- **Warm runs are the realistic case**, and are where the two findings above apply almost in full: a client that already holds a version's content, or a binding's already-seen graph shape, needs only confirmation of what changed. [context-efficiency-proposal.md](context-efficiency-proposal.md) targets this case for the ≥70% response-token reduction in [#131](https://github.com/ashuangiras/polaroid/issues/131), and states how to measure both cases honestly after implementing.
- **Scope of this baseline.** Only `verify-change` is measured here. Discovery, feedback and toil workflows are not, and should be benchmarked the same way before generalizing any conclusion to them.

## Reproducing

```sh
make build
python3 scripts/benchmark-context.py   # needs bin/polaroidd, bin/polaroid
```

Prints the path to `summary.json`; per-call request/response bodies and `manifest.json` sit beside it, under `bin/benchmark/<timestamp>/` (git-ignored). The figures above are one such run's `summary.json`, copied in by hand.
