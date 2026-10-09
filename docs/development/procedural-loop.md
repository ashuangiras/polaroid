# Procedural memory on Polaroid's own development

This page records how Polaroid's development procedures were used through Polaroid itself ([#28](https://github.com/ashuangiras/polaroid/issues/28), [ADR-0017](../architecture/decisions/0017-development-procedures-as-records.md)). It holds two kinds of evidence, which must not be confused:

| | Automated regression evidence | Agent-session evidence |
| --- | --- | --- |
| What | `make demo` replays the loop with scripted outcomes in a temporary store | An agent retrieved procedures over MCP, ran real commands, reasoned about a failure and wrote the records below |
| Proves | Polaroid's lifecycle: loading, MCP calls, conflicts, composition, verification transitions, history, persistence | That the loop works in practice for a real development task |
| Runs | In CI on every push, without an LLM | Once per session; IDs come from one local store |
| Where | [scripts/demo.sh](../../scripts/demo.sh), steps 9 to 18 | This page |

## The procedures

Fixtures in [examples/development](../../examples/development), loaded with [scripts/load-fixtures.sh](../../scripts/load-fixtures.sh):

| Canonical key | Versions | Role |
| --- | --- | --- |
| `go.module.build` | 1 | Build a Go module through the repository's own entry point, checking the toolchain and the artifacts. |
| `go.module.checks` | 1, 2 | Run a Go repository's package tests and gate commands, keeping exit statuses and deterministic excerpts. **Version 1 is a demonstration seed**: its step `unit` is deliberately stale (`go test ./test/...`, with the claim that tests live in a top-level `test/` directory). Its `revision_reason` says it is a seed, without naming the step. Version 2 is the agent's correction. |
| `dev.change.verify` | 1, 2 | Verify a change: establish the commit, working-tree state and environment, then run `build` (→ `go.module.build`) and `checks` (→ `go.module.checks`), both contextual, and record children before the parent. **Version 2** ([#33](https://github.com/ashuangiras/polaroid/issues/33)) resolves at the explicit target (`commit` and `inputs`), keeps selection evidence apart from target verification, always performs a fresh run, records the binding revision, and confirms target verification afterwards; see [below](#target-aware-procedure-33). |

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
| — | `list_verifications`, `resolve_binding` | Two combinations are listed: `{build: 1, checks: 1}` unverified (the failed run) and `{build: 1, checks: 2}` verified, both at `d306ae9`. The binding resolves with root, `build` and `checks` all `selected_by: evidence`, following the verified parent; that evidence is from `d306ae9` only. |
| — | fixture binding | `resolve_binding` for `example.com/fixtures/go-service`: every node `selected_by: latest`, no `verified_by`. The evidence from `github.com/ashuangiras/polaroid` does not transfer. |

Version 1 of `go.module.checks` and the failed executions remain readable. Version 2 was then exported verbatim to [v2.revise.json](../../examples/development/procedures/go-module-checks/v2.revise.json); `scripts/load-fixtures.sh examples/development` against the live store reports that versions 1..2 match and changes nothing.

**What this does and does not show.** The failure, the investigation and the corrected version were produced by the agent from what it observed, and recorded by it through MCP. But the same agent session also wrote the seed, so this was not blind discovery: it shows that the loop and the records work, not that an agent found an unknown defect. The fresh session below is the independent part.

## Semantics checked for #28

**JSON equality in applicability matching.** The only place where Polaroid compares free-form values is the verification combination ([records.md](../architecture/records.md#verification-implemented)). Inputs are compared in canonical form: members sorted, numbers and strings canonicalized as in RFC 8785, integers exact. `environment.attributes` is not part of a combination, and an environment is matched by its name only, also in resolution. Reordered members therefore never split a combination. This was already established by `TestCombinationKey` (domain) and `TestVerificationCombinations` (HTTP), and the demo now shows it end to end with reversed input members (step 14). The one rule that was documented only implicitly, that array elements keep their order, is now stated in records.md and pinned by `TestCombinationKey`. No behavior changed.

**Dependency verification.** A parent is verified only if it succeeded and every reference has a linked, verified child, recursively (`TestVerificationCoverageRule`, `TestExecutionVerification`). Its combination includes the exact child-version tree, so a new child version is a new combination (`TestListCombinations`, `TestVerificationCombinations`). The latest execution of a combination decides, so a later failure withdraws verification (`TestListCombinations`, `TestContextualReferenceSelectsHighestVerifiedVersion`). A verified parent fixes the child versions resolution selects (`TestVerifiedParentFixesItsChildVersions`). The demo adds an end-to-end case: a parent that reports success over a failed child joins the earlier verified combination and makes it unverified (step 15). No defect was found.

## Fresh-session check

**Status: done, by a fresh-context subagent.** The check below was run by a Copilot subagent started from the authoring session. It had no access to that conversation; its only input was the prompt below, which names the goal, the repository, the service and generic operating rules, but no procedure steps. It is not a chat session started by a person. Anyone can repeat the check with the same prompt in a new Copilot chat, and add the evidence here.

Prompt, for a new session in this workspace with `polaroidd -db bin/dogfood/polaroid.db` running on `127.0.0.1:7417`:

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

**Evidence** (2026-10-09, same store; checked afterwards over MCP by the authoring session):
- **Commit verified:** `ebe54c7d89026fe9f332d63b6b6e1149866f1a98`, the head of `issue-28-procedural-loop` at the time, in a detached worktree at `/tmp/polaroid-verify-ebe54c7`, `working_tree: clean` before and after.
- **Resolution before the run** (`darwin-arm64.local`, no target): `dev.change.verify` v1, `go.module.build` v1 and **`go.module.checks` v2**, all `selected_by: evidence`, `verified_by` `01a1216f-564c…`, `01a1216f-1c23…` and `01a1216e-7790…`, the records of the corrected run above. Those executions ran at **`d306ae9`**: they made v2 the candidate, and said nothing about `ebe54c7`, which was unverified until this session ran it. At the time the response did not show that commit; since [#31](https://github.com/ashuangiras/polaroid/issues/31) every such node reports it in `selection_evidence`, and a request with `commit` and `inputs` reports `target_verification` for the commit about to be run. The session read the selected versions from Polaroid; v2 is the corrected one.
- **Executions:** `go.module.build` v1 `01a1217e-2ea6-72e5-869e-3052ac0e71ad` succeeded; `go.module.checks` v2 `01a12181-53c9-7ed4-9f8a-171d1de61b92` succeeded (`go test ./...` 7 packages `ok`; `make check` `0 issues.`, `deps-check: PASS`, no cached results; `make vuln` `No vulnerabilities found.`; `make demo` `demo: PASS`; `make e2e` `passed=192 failed=0`; `make e2e-mcp E2E_INTEROP=0` `passed=81 failed=0`, interop recorded as not run); `dev.change.verify` v1 `01a12181-83fb-73be-96a4-670443fcbe1b` succeeded, linking both.
- **Verification:** `get_verification` on `01a12181-83fb…` is **verified**, combination `{build: 1, checks: 2}` at `ebe54c7`. `list_verifications` for `dev.change.verify` v1 now lists three combinations: `d306ae9` with `checks` v1 (unverified), `d306ae9` with `checks` v2 (verified) and `ebe54c7` with `checks` v2 (verified). The earlier ones are unchanged.
- **Resolution after:** the same versions, all `selected_by: evidence`, now selected by the session's three executions at `ebe54c7`. That is what the next agent's candidates rest on, at whatever commit it works.
- **No instruction was wrong**, so the session appended no version; `go.module.checks` is still at version 2.

## Selection evidence versus target verification (#31)

[#31](https://github.com/ashuangiras/polaroid/issues/31) made explicit what the fresh session above relied on: evidence from one commit selects candidates, and only a run at a commit verifies that commit ([ADR-0018](../architecture/decisions/0018-selection-evidence-and-target-verification.md)). The change was verified with the same procedures, in the same store, served by a `polaroidd` built from the change itself.

**Agent-session evidence** (Copilot, `darwin-arm64.local`, clean worktree at `2b2dea65f7ce039dcb60ba51b7036f154cc04649`, effective inputs = the binding's plus `working_tree: clean`). Resolutions with a target were sent as raw MCP JSON-RPC (`tools/call resolve_binding` with `commit` and `inputs`), because VS Code still had the tool schema from before the change; records were written with the `record_execution` tool.

| Step | Result |
| --- | --- |
| Resolve, no target | All three nodes `selected_by: evidence`, `selection_evidence.commit` = `ebe54c7` (the fresh session's run). No target fields. |
| Resolve at `2b2dea6` | Same candidates, evidence `ebe54c7`; `target_verification` at `2b2dea6`: **unverified, no executions** for the parent and both children. The children's effective inputs were derived through the mappings (build: `artifacts`, `build_command`, `working_tree`; checks: `gate_commands`, `working_tree`). |
| `go.module.build` v1 | `01a121b7-4d81-7d99-940b-6686017880d3` succeeded. |
| `go.module.checks` v2 | `01a121b9-13a4-7d06-8e79-93aa1b9c6680` succeeded: `go test ./...` 7 `ok`; `make check` `0 issues.`, 14 `ok`, none cached, `deps-check: PASS`; `make vuln` `No vulnerabilities found.`; `make demo` `demo: PASS`; `make e2e` `passed=192 failed=0`; `make e2e-mcp E2E_INTEROP=0` `passed=83 failed=0`. |
| Resolve at `2b2dea6`, children only | build and checks **verified** at the target; the parent still **unverified**. |
| `dev.change.verify` v1 | `01a121b9-9776-7f91-89ed-fa3e7d6f9fa7` succeeded, linking both. |
| Resolve at `2b2dea6` | All three **verified** at the target, and selected by this run's evidence. |
| Other targets | `ebe54c7` and `d306ae9` stay verified by their own runs (`01a12181-83fb…`, `01a1216f-564c…`). An unseen commit (`cccc…`) is unverified with evidence from `2b2dea6`. The same commit with `working_tree: modified:demo` is unverified. |

**Automated evidence** for the same rules: `make demo` step 17 (two scripted commits), `TestTargetVerificationIsCommitSpecific`, `TestTargetVerificationNeedsTheExactScope`, `TestTargetVerificationFollowsTheSelectedCombination` (domain), `TestTargetVerificationAcrossCommits`, `TestTargetOnTheGraphEndpoint`, `TestTargetRequestsAreChecked` (real SQLite and HTTP), and the MCP and CLI tests.

## Target-aware procedure (#33)

After #31 the capability existed, but `dev.change.verify` version 1 still resolved without a target and confirmed only with `get_verification`. [#33](https://github.com/ashuangiras/polaroid/issues/33) changed the procedural knowledge, not the code: version 2 was appended through MCP (`revise_procedure`, `base_version: 1`) and exported verbatim to [v2.revise.json](../../examples/development/procedures/dev-change-verify/v2.revise.json). Its `revision_reason` names the capability change and what version 1 got wrong.

**What version 2 asks for.** The full commit; working-tree state and the effective inputs (the binding revision's plus `working_tree`); the environment; `resolve_binding` with `commit` and `inputs`; reading each selected version, mapping, `selection_evidence` and `target_verification`; following `build` then `checks` and judging each against its contract; rechecking the context before each record; recording children, then the parent with `binding_id`, `binding_revision` and `children`; and resolving again at the same target. Its boundaries state that selection evidence never certifies the target, that a missing or `false` `target_verification` is unverified, that a child's success does not establish its parent, that a rebase makes a new, unverified target even with an identical tree, and that Polaroid derives verification from what agents record without checking that the commands ran. The philosophy, the contract inputs, both references and the failure rule are unchanged.

**Existing target evidence.** No reuse policy existed: AGENTS.md, workflow.md, the binding and the procedures say nothing about it, and `go.module.checks` already reports a reused result as reused. Version 2 makes the default explicit: each invocation is a fresh run whose outcome is recorded. An already verified target is reported as context and never as evidence that this invocation ran the checks. Looking up verification (a resolution with a target, or `list_verifications`) is read-only and records nothing.

**Repeated checks.** The parent runs no commands, and its final confirmation is a lookup. The overlap inside the children is kept: `go.module.checks` v2 runs `go test ./...` for fast feedback before `make check`, which runs the tests again with and without `-race`, and `make demo`, `make e2e` and `make e2e-mcp` each run `make build`. That is the established acceptance of those records and of the binding; changing it would be a revision of them, not of this procedure.

**Selection after the append.** Version 2 has no evidence, and version 1 has a verified execution in `darwin-arm64.local`, so contextual resolution keeps selecting version 1, by evidence. That is the resolver working as designed (ADR-0013), not a defect. The resolver, the binding and the evidence were left alone; the first run of version 2 chooses it explicitly, as the version itself allows: `get_graph` for version 2 with the repository, environment, commit and inputs, stated as such in the parent's evidence.

**Automated evidence.** `make demo` step 18 loads version 2 with the loader (version 1 unchanged, references unchanged, the stored instructions resolve with `commit` and `inputs`), shows that a second load changes nothing and that a differing version 2 is refused without writing, and then, over MCP at a new scripted commit: nothing verified; after the children only, the children verified and the parent not; after the parent, all three verified; and at a later commit with the same inputs, nothing verified. The #28 replay in steps 12 to 16 loads the fixtures as they were then, without this version.

## Reproduce

- **Regression replay:** `make demo` (needs `jq`).
- **The live loop, in a store of your own:** run `bin/polaroidd -db /tmp/loop.db` on `127.0.0.1:7417` (stop any other daemon on that port first), load the seed with `scripts/load-fixtures.sh -n 1 examples/development`, and give an agent the prompt above with a commit of your choice. Loading without `-n 1` also appends the correction, as any store loaded from the fixtures has it.
