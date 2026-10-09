# Procedural memory on Polaroid's own development

This page records how Polaroid's development procedures were used through Polaroid itself ([#28](https://github.com/ashuangiras/polaroid/issues/28), [ADR-0017](../architecture/decisions/0017-development-procedures-as-records.md)). It holds two kinds of evidence, which must not be confused:

| | Automated regression evidence | Agent-session evidence |
| --- | --- | --- |
| What | `make demo` replays the loop with scripted outcomes in a temporary store | An agent retrieved procedures over MCP, ran real commands, reasoned about a failure and wrote the records below |
| Proves | Polaroid's lifecycle: loading, MCP calls, conflicts, composition, verification transitions, history, persistence | That the loop works in practice for a real development task |
| Runs | In CI on every push, without an LLM | Once per session; IDs come from one local store |
| Where | [scripts/demo.sh](../../scripts/demo.sh), steps 9 to 17 | This page |

## The procedures

Fixtures in [examples/development](../../examples/development), loaded with [scripts/load-fixtures.sh](../../scripts/load-fixtures.sh):

| Canonical key | Versions | Role |
| --- | --- | --- |
| `go.module.build` | 1 | Build a Go module through the repository's own entry point, checking the toolchain and the artifacts. |
| `go.module.checks` | 1, 2 | Run a Go repository's package tests and gate commands, keeping exit statuses and deterministic excerpts. **Version 1 is a demonstration seed**: its step `unit` is deliberately stale (`go test ./test/...`, with the claim that tests live in a top-level `test/` directory). Its `revision_reason` says it is a seed, without naming the step. Version 2 is the agent's correction. |
| `dev.change.verify` | 1 | Verify a change: establish the commit, working-tree state and environment, then run `build` (→ `go.module.build`) and `checks` (→ `go.module.checks`), both contextual, and record children before the parent. |

Bindings of `dev.change.verify`, by the same procedure ID:
- `github.com/ashuangiras/polaroid`, name `verify-change`: `make build`, artifacts `bin/polaroidd` and `bin/polaroid`, and the gate `make check`, `make vuln`, `make demo`, `make e2e`, `make e2e-mcp E2E_INTEROP=0`.
- `example.com/fixtures/go-service`, name `verify-change`: a **fixture only**. No such repository exists and nothing has run there.

## Live session: verifying #27 with the procedures

**Store.** An isolated store, `bin/dogfood/polaroid.db` (ignored by git), served by `polaroidd` on `127.0.0.1:7417`, which `.vscode/mcp.json` points Copilot at. It was seeded with `scripts/load-fixtures.sh -n 1 examples/development`, that is, with version 1 of each procedure. No other data is in it. The IDs below exist only in that store.

**Agent.** GitHub Copilot in VS Code chat, using the `mcp_polaroid_proc_*` tools for every read and write, and its terminal for every command.

**Task.** Verify the change for [#27](https://github.com/ashuangiras/polaroid/issues/27) at commit `d306ae907dd3aeb6ba032d35aca55b191ddd2258` (PR #29; merged to `main` as `ee00697` by a rebase merge, with the identical tree `13d70af`).

**Context recorded.** Repository `github.com/ashuangiras/polaroid` (from `git remote get-url origin`); a git worktree at `/tmp/polaroid-wt-27`, detached at the commit, with an empty `git status --porcelain`, so `working_tree` is `clean`; environment `darwin-arm64.local` with Go 1.27.2, bash 5.2.37, jq 1.8.1, sqlite3 3.51.0, and golangci-lint 2.14.0 passed as `GOLANGCI_LINT` because `PATH` has 2.12.2.

| Time (UTC) | Record | What happened |
| --- | --- | --- |
| 16:05:12 | procedures, bindings | Fixtures loaded: `dev.change.verify` `01a12169-510a-7c14-9351-432f7866d8f1`, `go.module.build` `01a12169-4f6f-7758-ba08-43b1463dcca4`, `go.module.checks` `01a12169-5004-724d-bbef-b8b28a4f0417`; Polaroid binding `01a12169-52ff-7804-a50d-6719c951301c`, fixture binding `01a12169-5278-778d-b5d9-05d938908a06`. |
| — | `resolve_binding` | Nothing verified: root, `build` v1 and `checks` v1 all `selected_by: latest`. The agent read all three versions in full. |
| 16:06:48 | execution `01a1216a-c81a-785e-8fa5-0a45eab46cdc` | `go.module.build` v1 **succeeded**: toolchain line `go1.27.2` matched `go version`; `make build` exit 0; both artifacts rewritten; tree unchanged. |
| 16:07:20 | execution `01a1216b-4377-7c2d-b5a8-7237c39e9aa0` | `go.module.checks` v1 **failed** at step `unit`: `GOFLAGS=-count=1 go test ./test/...` exited 1 with `pattern ./test/...: lstat ./test/: no such file or directory`. The gate commands were recorded as not run. The evidence labels the version as the demonstration seed. |
| 16:07:32 | execution `01a1216b-7272-7f74-8c36-5d6d07432177` | `dev.change.verify` v1 **failed**, linking both children. |
| — | investigation | No `test/` or `tests/` directory exists at the commit or anywhere in history (`git log --all -- test/ tests/` is empty). The 36 `*_test.go` files sit beside their packages in 7 directories. The Makefile's `test` target is `go test ./...`, and `GOFLAGS=-count=1 go test ./...` passed all 7 test packages with the same toolchain. Conclusion: not an environment blocker (the toolchain works) and not a product defect (the tests pass); the instruction was wrong. |
| 16:08:29 | `go.module.checks` **version 2** | Appended with `base_version: 1`, the version read. Only step `unit` changed: it now runs `GOFLAGS=-count=1 go test ./...` and says that Go tests live beside their packages. The `revision_reason` gives the failing execution, the investigation and the conclusion. |
| — | `revise_procedure` with `base_version: 1` again | Refused: `version_conflict`, `latest_version: 2`. Nothing stored. |
| — | `resolve_binding` | `build` v1 `selected_by: evidence` (`01a1216a…`); `checks` **v2** `selected_by: latest`; root unverified. |
| 16:10:50 | execution `01a1216e-7790-77ef-818c-1bead22f3e83` | `go.module.checks` v2 **succeeded**: `go test ./...` 7 packages `ok`; `make check` `0 issues.`, 14 `ok` lines, `deps-check: PASS`; `make vuln` `No vulnerabilities found.`; `make demo` `demo: PASS`; `make e2e` `passed=192 failed=0`; `make e2e-mcp E2E_INTEROP=0` `passed=81 failed=0`. |
| — | `resolve_binding`, `list_verifications` | `checks` v2 now `selected_by: evidence` (`01a1216e…`), but the parent is still unverified: its only combination, `{build: 1, checks: 1}`, is `verified: false`. |
| 16:11:32 | execution `01a1216f-1c23-79d2-9de1-fe55d64edebe` | `go.module.build` v1 **succeeded** again. The first build execution is already the child of the failed parent, and an execution has at most one parent. |
| 16:11:47 | execution `01a1216f-564c-722d-a6df-2680d6e87be2` | `dev.change.verify` v1 **succeeded**, linking build `01a1216f-1c23…` and checks `01a1216e-7790…`. `get_verification`: **verified**, combination `{build: 1, checks: 2}` at `d306ae9`, `darwin-arm64.local`, inputs with `working_tree: clean`. |
| — | `list_verifications`, `resolve_binding` | Two combinations are listed: `{build: 1, checks: 1}` unverified (the failed run) and `{build: 1, checks: 2}` verified. The binding resolves with root, `build` and `checks` all `selected_by: evidence`, following the verified parent. |
| — | fixture binding | `resolve_binding` for `example.com/fixtures/go-service`: every node `selected_by: latest`, no `verified_by`. The evidence from `github.com/ashuangiras/polaroid` does not transfer. |

Version 1 of `go.module.checks` and the failed executions remain readable. Version 2 was then exported verbatim to [v2.revise.json](../../examples/development/procedures/go-module-checks/v2.revise.json); `scripts/load-fixtures.sh examples/development` against the live store reports that versions 1..2 match and changes nothing.

**What this does and does not show.** The failure, the investigation and the corrected version were produced by the agent from what it observed, and recorded by it through MCP. But the same agent session also wrote the seed, so this was not blind discovery: it shows that the loop and the records work, not that an agent found an unknown defect. The fresh session below is the independent part.

## Semantics checked for #28

**JSON equality in applicability matching.** The only place where Polaroid compares free-form values is the verification combination ([records.md](../architecture/records.md#verification-implemented)). Inputs are compared in canonical form: members sorted, numbers and strings canonicalized as in RFC 8785, integers exact. `environment.attributes` is not part of a combination, and an environment is matched by its name only, also in resolution. Reordered members therefore never split a combination. This was already established by `TestCombinationKey` (domain) and `TestVerificationCombinations` (HTTP), and the demo now shows it end to end with reversed input members (step 14). The one rule that was documented only implicitly, that array elements keep their order, is now stated in records.md and pinned by `TestCombinationKey`. No behavior changed.

**Dependency verification.** A parent is verified only if it succeeded and every reference has a linked, verified child, recursively (`TestVerificationCoverageRule`, `TestExecutionVerification`). Its combination includes the exact child-version tree, so a new child version is a new combination (`TestListCombinations`, `TestVerificationCombinations`). The latest execution of a combination decides, so a later failure withdraws verification (`TestListCombinations`, `TestContextualReferenceSelectsHighestVerifiedVersion`). A verified parent fixes the child versions resolution selects (`TestVerifiedParentFixesItsChildVersions`). The demo adds an end-to-end case: a parent that reports success over a failed child joins the earlier verified combination and makes it unverified (step 15). No defect was found.

## Fresh-session check

**Status: pending.** A session that has not seen this conversation must retrieve the corrected procedure from Polaroid, follow it, record new evidence and confirm the result. Its evidence goes here and on #28.

Prompt to give a new Copilot chat session in this workspace, with `polaroidd -db bin/dogfood/polaroid.db` running on `127.0.0.1:7417`:

```text
Goal: verify the change on branch issue-28-procedural-loop of this repository
(github.com/ashuangiras/polaroid, work item
https://github.com/ashuangiras/polaroid/issues/28) at the branch's current
head commit, using the procedures stored in Polaroid, and record what you did
in Polaroid.

Polaroid is a procedural-memory service running at http://127.0.0.1:7417.
Its MCP tools are available to you as mcp_polaroid_proc_*. It stores
procedures, bindings and execution evidence; you interpret the procedures and
do the work with your own tools.

Operating rules:
- Find this repository's binding for verifying a change, resolve it in an
  environment name that identifies this machine (earlier runs here used
  darwin-arm64.local), and read every selected procedure version in full
  before acting. The stored versions are the only instructions to follow; do
  not use steps from memory or from elsewhere.
- Verify in a clean checkout of exactly that commit (for example a git
  worktree outside the repository), so the commit and working-tree state you
  record are true.
- Record each execution in Polaroid as soon as it finishes, children before
  their parent, with the exact versions you followed, the effective inputs,
  the commit and the environment. Report failures and skipped commands as
  such, never as passed.
- If an instruction turns out to be wrong, establish whether the instruction,
  the environment or the product is at fault before changing anything. Only a
  wrong instruction justifies appending a new version, with base_version set
  to the version you read.
- Environment facts: go is Go 1.27.2. The pinned golangci-lint 2.14.0 is at
  /tmp/gcl/golangci-lint-2.14.0-darwin-arm64/golangci-lint; the one on PATH
  is 2.12.2. jq, sqlite3 and bash 5 are installed.
- Do not edit repository files, push, merge or comment on issues.

Finish with: the commit verified; the binding resolution before you ran
(versions, selected_by, verified_by); each execution ID with its procedure,
version and outcome; the parent's verification; and the resolution after.
```

Evidence: _pending._

## Reproduce

- **Regression replay:** `make demo` (needs `jq`).
- **The live loop, in a store of your own:** run `bin/polaroidd -db /tmp/loop.db` on `127.0.0.1:7417` (stop any other daemon on that port first), load the seed with `scripts/load-fixtures.sh -n 1 examples/development`, and give an agent the prompt above with a commit of your choice. Loading without `-n 1` also appends the correction, as any store loaded from the fixtures has it.
