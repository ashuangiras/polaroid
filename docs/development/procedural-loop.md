# Procedural memory on Polaroid's own development

This page records how Polaroid's development procedures were used through Polaroid itself ([#28](https://github.com/ashuangiras/polaroid/issues/28), [ADR-0017](../architecture/decisions/0017-development-procedures-as-records.md)). It holds two kinds of evidence, which must not be confused:

| | Automated regression evidence | Agent-session evidence |
| --- | --- | --- |
| What | `make demo` replays the loop with scripted outcomes in a temporary store | An agent retrieved procedures over MCP, ran real commands, reasoned about a failure and wrote the records below |
| Proves | Polaroid's lifecycle: loading, MCP calls, conflicts, composition, verification transitions, history, persistence | That the loop works in practice for a real development task |
| Runs | In every full verification (`make demo`) and `make ci`, without an LLM | Once per session; IDs come from one local store |
| Where | [scripts/demo.sh](../../scripts/demo.sh), steps 9 to 18, 20 and 21 | This page |

## The procedures

Fixtures in [examples/development](../../examples/development), loaded with [scripts/load-fixtures.sh](../../scripts/load-fixtures.sh):

| Canonical key | Versions | Role |
| --- | --- | --- |
| `go.module.build` | 1, 2 | Build a Go module through the repository's own entry point, checking the toolchain and the artifacts. |
| `go.module.checks` | 1, 2, 3 | Run a Go repository's package tests and gate commands, keeping exit statuses and deterministic excerpts. **Version 1 is a demonstration seed**: its step `unit` is deliberately stale (`go test ./test/...`, with the claim that tests live in a top-level `test/` directory). Its `revision_reason` says it is a seed, without naming the step. Version 2 is the agent's correction. |
| `dev.change.verify` | 1, 2, 3 | Verify a change: establish the commit, working-tree state and environment, then run `build` (→ `go.module.build`) and `checks` (→ `go.module.checks`), both contextual, and record children before the parent. **Version 2** ([#33](https://github.com/ashuangiras/polaroid/issues/33)) resolves at the explicit target (`commit` and `inputs`), keeps selection evidence apart from target verification, always performs a fresh run, records the binding revision, and confirms target verification afterwards; see [below](#target-aware-procedure-33). |
| `polaroid.record-model.change` | 1, 2, 3 | Change Polaroid's stored records, schema or their requests without changing what a stored record means, then verify through `verify` (→ `dev.change.verify`). Created during [#35](https://github.com/ashuangiras/polaroid/issues/35); version 2 is local to Polaroid; version 3 ([#37](#identity-pages-and-first-runs-of-the-shared-versions-37)) adds a migration only when the schema must change. See [below](#several-repositories-35). |

The latest versions of the first three (#35) repeat their predecessors' definitions with a goal and shared applicability. Every procedure records its origin in `github.com/ashuangiras/polaroid`, which the fixtures register.

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

Prompt, for a new session in this workspace with `polaroidd` running on `127.0.0.1:7417` (it was `-db bin/dogfood/polaroid.db` then; since #41 the shared catalog is the per-user default, `~/.polaroid/data/polaroid.db`, so no `-db` is needed):

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

**Live run of version 2** (Copilot, the authoring session, 2026-10-09, `bin/dogfood/polaroid.db`). Target: commit `827aff26ba79c070b28299c16a97adb2e2e5f35e`, the implementation commit of #33 with the fixture and the demo, in a detached worktree `/tmp/polaroid-verify-827aff2` with an empty `git status --porcelain`; `darwin-arm64.local` with Go 1.27.2, bash 5.2.37, jq 1.8.1, sqlite3 3.51.0 and golangci-lint 2.14.0 passed as `GOLANGCI_LINT`; binding `01a12169-52ff…` revision 1, so the effective inputs are its inputs plus `working_tree: clean`.

| Step | Result |
| --- | --- |
| `resolve_binding` at `827aff2` with the inputs | Root **v1**, build v1 and checks v2, all `selected_by: evidence` with `selection_evidence.commit` `2b2dea6`; `target_verification` **false, no executions** for all three. |
| `get_graph` for **version 2** at the same target | Chosen explicitly for its first run (the `selection` step). build v1 and checks v2 by evidence from `2b2dea6`; all three unverified at the target. |
| `go.module.build` v1 | `01a121de-5a0d-7774-a54c-26d670ba48a3` succeeded: `toolchain go1.27.2` matched `go version`; `make build` exit 0; both artifacts written by the run; tree unchanged. |
| `go.module.checks` v2 | `01a121e0-f85c-7083-b2bf-e0eed7808cf1` succeeded: `go test ./...` 7 `ok`; `make check` `0 issues.`, 7 `ok` lines in each test run, none cached, `deps-check: PASS`; `make vuln` `No vulnerabilities found.`; `make demo` `demo: PASS` (with step 18); `make e2e` `passed=192 failed=0`; `make e2e-mcp E2E_INTEROP=0` `passed=83 failed=0`; interop recorded as not run. |
| `get_graph` v2 at the target, children only | build and checks **verified**, with those executions as latest; the root still **unverified, no executions**. |
| `dev.change.verify` v2 | `01a121e1-6f23-7999-8e69-d02afa3e0803` succeeded, with `binding_id`, `binding_revision: 1` and both children. Its evidence says that version 2 was chosen explicitly and what resolution selected. `get_verification`: **verified**. HEAD and the porcelain status were rechecked before each record. |
| `resolve_binding` at `827aff2` | Root **v2** now `selected_by: evidence` from `827aff2`; all three **verified** at the target, latest = the three executions above. |
| Other targets | `bfcbb3a` (an unseen commit) and `827aff2` with `working_tree: modified:x`: all unverified. `2b2dea6`: the children verified by their #31 runs, but root v2 unverified, because version 2 never ran there. |

Resolutions with a target were sent as raw MCP JSON-RPC, because the chat client rejected them (see below); `get_graph`, `record_execution` and every other call went through the normal MCP tools.

**Fresh session.** A Copilot subagent started from the authoring session, with no access to its conversation; its only input was the prompt below, which gives the goal, the commit, the service and environment facts, and no procedure steps or call sequence. It shares the workspace and the VS Code MCP client. It is not a chat session started by a person. It worked in its own worktree `/tmp/polaroid-verify-827aff2-r2`, at the same commit, which the run above had already verified:

- it found the binding, and resolution gave it **version 2 by evidence** (from `827aff2`); it followed what was selected;
- before its run, target verification was already **true** (latest `01a121e1-6f23…`). Following the `existing-evidence` step, it reported that as context and ran again;
- it recorded `go.module.build` v1 `01a121e3-e3b8-727e-99e2-55994198851a`, `go.module.checks` v2 `01a121e6-0b33-7fce-a182-a03f36de2b4c` (the same gate results: 7 `ok`, `0 issues.`, `deps-check: PASS`, `No vulnerabilities found.`, `demo: PASS`, `passed=192 failed=0`, `passed=83 failed=0`), and `dev.change.verify` v2 `01a121e6-38ad-7e1c-bddf-fb0fb86d35fa` with binding revision 1 and both children; verified;
- resolving again at the target reported all three verified with **its** executions as latest; `list_verifications` for version 2 shows one combination at `827aff2` with two executions;
- its `resolve_binding` calls with `commit` and `inputs` were rejected by the client in the same way, so it used `bin/polaroid resolve` and said so in the parent's evidence. No procedure step was wrong, and it appended no version.

```text
Goal: verify Polaroid (this repository, github.com/ashuangiras/polaroid, work
item https://github.com/ashuangiras/polaroid/issues/33) at commit
827aff26ba79c070b28299c16a97adb2e2e5f35e, using the procedure that Polaroid
holds for this repository, and record what you did in Polaroid.

Polaroid is a procedural-memory service running at http://127.0.0.1:7417.
Its MCP tools are available to you as mcp_polaroid_proc_*; the same
operations are available over its HTTP API at that address, and through the
CLI bin/polaroid in this repository (POLAROID_URL=http://127.0.0.1:7417).
Polaroid stores procedures, bindings and execution evidence; you interpret
the procedures and do the work with your own tools.

- Discover the binding this repository has for verifying a change, and
  retrieve the procedure it gives you. The stored procedure versions are the
  only instructions to follow: do not use steps from memory, from this
  repository's documentation about procedures, or from elsewhere.
- Work in a clean checkout of exactly that commit outside this working tree,
  for example a git worktree under /tmp.
- If a tool call fails, report the error verbatim; you may use an
  equivalent operation over HTTP or the CLI, and say that you did.
- Environment facts: this machine has been called darwin-arm64.local in
  Polaroid. go is Go 1.27.2. The pinned golangci-lint 2.14.0 is at
  /tmp/gcl/golangci-lint-2.14.0-darwin-arm64/golangci-lint; the one on PATH
  is 2.12.2. jq, sqlite3 and bash 5 are installed. Network is available.
- Do not edit files in this repository, push, merge, or comment on issues.

Finish with: the commit verified; the procedure and versions you followed
and how they were chosen; what Polaroid said about this commit before and
after your run; each execution you recorded, with its ID, procedure, version
and outcome; and anything that did not work as the procedure described.
```

A chat session started by a person can repeat it with the same prompt (for a later commit, change the commit).

**MCP client refresh.** VS Code's connection to `polaroid` (`mcp.config.ws0.polaroid`, pointing at the daemon above; the daemon itself was not restarted) was restarted with `workbench.mcp.restartServer`. Its log shows `Discovered 20 tools` at the restart, and the tool definitions the agent sees list `commit` and `inputs` for both `resolve_binding` and `get_graph`. Through the normal tools, `get_graph` with `commit` and `inputs` works. `resolve_binding` with them is still rejected before any request reaches the server: `Your input to the tool was invalid (must NOT have additional properties)`. The server's `tools/list` advertises both arguments, and the same call over raw JSON-RPC or the CLI works, so the cause is on the client side; that it keeps a validator from the schema it first saw in a long session is a guess, not established. Recorded as feedback report `01a121e7-a0f3-7214-8e4b-9a6bb07bac6d`. Whether a newly started chat accepts the arguments is not yet checked.

## Several repositories (#35)

[#35](https://github.com/ashuangiras/polaroid/issues/35) added a repository registry, procedure origin and applicability, typed feedback subjects and bounded lists ([ADR-0019](../architecture/decisions/0019-repository-registry.md) to [ADR-0021](../architecture/decisions/0021-targeted-feedback-and-bounded-lists.md)). It was developed with Polaroid, in the store `bin/dogfood/polaroid.db`, by Copilot in the authoring session on 2026-10-09.

**Retrieved first.** Over MCP, before any code: `list_procedures` returned only `dev.change.verify`, `go.module.build` and `go.module.checks`; `list_feedback` returned one report (`01a121e7…`, about the VS Code client). No procedure covered changing Polaroid's record model.

**Created.** `polaroid.record-model.change` version 1 (`01a12205-d4f3-792b-a539-0fe71a4f18d6`), through `create_procedure`, before the implementation: contracts, compatibility (ADR first), migration, upgrade test, domain rules with concurrency and mutation checks, transport parity, docs, then the `verify` reference (`dev.change.verify`, contextual) at the implementation commit, and the record. Applicability could not be declared yet, which its `revision_reason` says.

**Live upgrade.** The store was at schema 6. Before starting the new build: every procedure history, the Polaroid bindings, the `dev.change.verify` executions and the feedback list were read over HTTP into `bin/dogfood/pre35/`, and the database was copied with `sqlite3 .backup` (`pre35/polaroid-schema6.db`). The old daemon stopped cleanly on SIGTERM; the new build opened the same file at schema 7. Every snapshot compared equal apart from the added fields (`scope`, `goal`, `applicability`, `origin`; the old report has no `subject`), and nothing was registered. The VS Code client log then showed `Discovered 25 tools`.

**Records written after the upgrade** (over MCP; this chat session never received the five new tools, so those calls were raw JSON-RPC, see below):

| Record | Result |
| --- | --- |
| `register_repository` `github.com/ashuangiras/polaroid`, "Polaroid" | `01a12236-dcb3-73ba-bba1-fc4933800e25`; its existing bindings and executions are associated through the identifier, not rewritten. |
| `record_procedure_origin` ×4 | The three development procedures: seeded in Polaroid (#28). `polaroid.record-model.change`: created in Polaroid during #35. |
| `go.module.build` v2, `go.module.checks` v3, `dev.change.verify` v3 | The previous definitions, byte-identical (copied with jq from the stored version), plus a goal and `{"shared": {}}`: each contract names no repository, and each is bound by two repositories already. |
| `polaroid.record-model.change` v2 | Local to Polaroid, with a goal, a new `live-upgrade` step and three refinements learned while following version 1 (discriminating mutation scenarios, storage-layer tests for races HTTP cannot reach, a demo step in parity). |

`list_procedures` now offers the fixture repository only the three shared procedures, `scope=local` in Polaroid lists only `polaroid.record-model.change`, and binding it in `example.com/fixtures/go-service` gets `400` on `procedure_id`. All of this is exported to `examples/development` (`repositories/`, `origin.json`, the new version files); the loader reports every version matching against the live store and writes nothing, and `make demo` step 20 replays it.

**Live verification at `8d5cc70`** (the fixture commit, which contains the implementation `74d6deb`), worktree `/tmp/polaroid-verify-8d5cc70`, empty porcelain, `darwin-arm64.local`, binding `01a12169-52ff…` revision 1:

| Step | Result |
| --- | --- |
| `resolve_binding` with commit and inputs | Root **v2**, build v1, checks v2, all by evidence from `827aff2`; the new v3/v2/v3 have no evidence, so resolution kept the evidenced versions, as designed. Nothing verified at `8d5cc70`. |
| `go.module.build` v1 | `01a1223c-6d5e-7581-a802-4f7324c0a949` succeeded: toolchain matched; `bin/` absent before, both artifacts written by the run. |
| `go.module.checks` v2 | `01a1224b-1c92-7449-a689-bc5aa6910c67` succeeded: 7 `ok`; `0 issues.`; none cached; `deps-check: PASS`; `No vulnerabilities found.`; `demo: PASS` (steps 20 to 22 included); `passed=203 failed=0`; `passed=93 failed=0`. |
| `dev.change.verify` v2 | `01a1224b-8a3e-71ce-a6d5-7ee65cbad5d6`, with both children: **verified**; resolving again reports root, build and checks verified at `8d5cc70` with these executions as latest. |
| `polaroid.record-model.change` v1 | `01a1224e-c5b0-7a56-a349-8f46d6edfa15`, the version followed during the work (version 2 was appended after it), with the `dev.change.verify` execution as its `verify` child: **verified**. Its evidence lists the ADRs, migration 7, the upgrade and concurrency tests, eleven mutation checks with what each broke, the live upgrade, and one deviation (a `sed` rename in a test file). |

Feedback `01a1224e-fe89-709f-a584-7830ab16fab5` (subject: the service; context: this repository and the execution above) records the client problem: after the upgrade and a client restart the log showed 25 tools, but this long-running chat kept its original 20 and rejected `list_procedures` with `repository` or `scope`, and `resolve_binding` with `commit` and `inputs`, client-side. Raw JSON-RPC to `/mcp` worked.

**Not shown live.** No second real repository uses the catalog yet: repositories A and B are fixtures, exercised by `make demo` step 19 and the end-to-end scripts as scripted regression evidence. Version 2 of `polaroid.record-model.change` and the shared versions of the development procedures have not been followed yet (they were in #37, [below](#identity-pages-and-first-runs-of-the-shared-versions-37)).

## Identity, pages and first runs of the shared versions (#37)

[#37](https://github.com/ashuangiras/polaroid/issues/37) made evidence match by registered repository identity ([ADR-0022](../architecture/decisions/0022-repository-identity-in-evidence.md)), bound cursors to their parameters and added snapshot pages, and gave every version its own `scope` ([ADR-0023](../architecture/decisions/0023-pagination-guarantees-and-scope-labels.md)). It was developed with Polaroid, in `bin/dogfood/polaroid.db`, by Copilot in the authoring session on 2026-10-09.

**Retrieved first.** `list_procedures` returned the four procedures; the one that applies to this change, local to Polaroid, is `polaroid.record-model.change` version 2, which references `dev.change.verify`. `list_feedback` returned the two client reports.

**A procedure defect, found by following it.** Version 2 says every record-model change adds a migration, in its method, its expected outcome, and its `migration` and `upgrade-test` steps. #37 needed none: identity is derived on read from the append-only registry, snapshot boundaries are row-ID marks of existing append-only tables, and the new response fields are derived. That is wrong instructions, not a product defect or an environment blocker. So:

- the run of version 2 at the implementation commit `74485e4` was recorded as **failed** at `migration` (`01a12286-c9e9-709f-8fbd-bcba70a573a2`), with the steps that held;
- version 3 was appended through MCP (`base_version: 2`). It makes the migration conditional on the schema having to change, says how to show compatibility without one, and asks the compatibility step to decide and test changes to what existing records derive;
- version 3 was exported to [v3.revise.json](../../examples/development/procedures/polaroid-record-model-change/v3.revise.json) (commit `2639d70`); the loader reports versions 1 to 3 matching the live store.

**Live upgrade.** Before the new build opened the store, every procedure history, execution, verification, binding history, repository and report was read over HTTP into 54 files, and the database was backed up with `sqlite3 .backup` (`bin/dogfood/pre37/`). Afterwards, all 54 compared equal apart from the added `scope` and `repository_id`; the schema is still 7. The VS Code client log showed `Discovered 25 tools`.

**First runs of the classified versions, at `2639d70`** (worktree `/tmp/polaroid-verify-2639d70`, empty porcelain, `darwin-arm64.local`, binding `01a12169-52ff…` revision 1):

| Step | Result |
| --- | --- |
| `resolve_binding` at the target, before | Root `dev.change.verify` **v2**, build v1, checks v2, all `unspecified`, all by evidence from `8d5cc70`; nothing verified at `2639d70`. The shared versions had no evidence, so the resolver kept the evidenced ones, as designed. |
| `get_graph` for `dev.change.verify` **v3** at the target | Chosen explicitly for its first run. Its contextual children still selected build v1 and checks v2 by evidence. |
| `go.module.build` **v2**, chosen explicitly | `01a12289-5b97-75fb-a8b0-2f63bfeb1b00` succeeded: toolchain matched; `bin/` absent before, both artifacts written by the run. |
| `go.module.checks` **v3**, chosen explicitly | `01a1228c-05a9-7ed9-b41c-b8b2543355e9` succeeded: 7 `ok`; `0 issues.`; none cached; `deps-check: PASS`; `No vulnerabilities found.`; `demo: PASS`; `passed=209 failed=0`; `passed=97 failed=0`. |
| `get_graph` v3 after the children | Its contextual references now selected **build v2 and checks v3 by evidence** from `2639d70`, with no change to any policy; root still unverified. |
| `dev.change.verify` **v3** | `01a1228c-a6b4-7564-890a-16b422f6418a`, with both children and binding revision 1: **verified**. |
| `polaroid.record-model.change` **v3** | `01a1228d-e415-7b78-b519-ab986f404d70`, with that execution as its `verify` child: **verified**. Its evidence lists the ADRs, why no migration was needed, the concurrency tests, seven mutation checks with what each broke, the live upgrade and the earlier failed run. |
| `resolve_binding` at the target, after | Root **v3**, build **v2**, checks **v3**, all `shared`, all by evidence from `2639d70`, all **verified** there with these executions as latest. |
| `resolve_binding` at `8d5cc70` | Root v3 is now selected there too (by evidence from `2639d70`), but **unverified** at `8d5cc70`: nothing transferred between commits. |

Contextual resolution moved to the new versions only after each of them had its own verified run. Recording the parent did not create evidence for its children; each child was run and recorded.

**Client.** This long-running chat still rejected the new `snapshot` argument, and `commit` and `inputs` on `resolve_binding`, client-side (`must NOT have additional properties`), although the client log reported 25 tools. Those calls, the registration-dependent reads and the version 3 revision went through raw JSON-RPC to `/mcp`. `get_graph`, `get_version`, `record_execution`, `get_verification`, `list_procedures` without new arguments and `list_feedback` went through the normal client. This is the problem already recorded in feedback `01a1224e-fe89…`.

## Child inputs follow the reference mapping (#39)

[#39](https://github.com/ashuangiras/polaroid/issues/39) closed a verification gap: a parent could be verified although a linked child ran with inputs other than its reference maps ([ADR-0024](../architecture/decisions/0024-child-inputs-follow-the-reference-mapping.md)). It was developed with Polaroid, in `bin/dogfood/polaroid.db`, by Copilot in the authoring session on 2026-10-09 and 2026-10-10.

**Retrieved first.** `list_procedures` for `github.com/ashuangiras/polaroid` returned the four procedures. The one for this change is `polaroid.record-model.change` version 3: the change alters a rule on stored records and what existing records derive. No new procedure was needed, and no step proved wrong, so no version was appended. Its `parity` step asked for a `make demo` step, which was added (step 22) before verification.

**Reproduced before the fix.** Binaries built from the code of `fe6c34a`, on a throwaway store: a parent whose reference maps `service` linked a successful build of service `b` while it ran for service `a`. It was accepted, verified (`{"verified":true}`), and at its commit target verification reported the parent verified while the build for service `a` was unverified. With the fix, the same request is refused: `children[0].execution_id: ran with inputs {"service":"b"}, but reference "build" maps the parent's inputs to {"service":"a"} (ADR-0024)`, and the target parent is unverified.

**The development store was checked before deciding.** All 18 of its child links matched their mappings, so the rule changes none of its derived verification. The live upgrade confirmed it: 64 files read over HTTP before (`bin/dogfood/pre39/`, backup `bin/dogfood/pre39.db`) and after the new build opened the store were identical, verifications included; the schema is still 7. No migration: a trigger cannot reproduce the canonical comparison, so the database does not enforce the rule, and derived verification refuses to count a mismatched link.

**Run at `0829a4c`** (the implementation `51f23a2` plus the demo step; worktree `/tmp/polaroid-verify-0829a4c`, empty porcelain, `darwin-arm64.local`, binding `01a12169-52ff…` revision 1):

| Step | Result |
| --- | --- |
| `resolve_binding` at the target, before | Root `dev.change.verify` v3, build v2, checks v3, all by evidence from `2639d70`; nothing verified at `0829a4c`. Each child's `target_verification.combination.inputs` gave the inputs it had to run with. |
| `go.module.build` v2 | `01a122bd-e703-7ad9-8bc8-034b4ccc2417` succeeded, with the build node's mapped inputs. |
| `go.module.checks` v3 | `01a122c0-1974-7fdf-944f-ddd63b38a3f6` succeeded, with the checks node's mapped inputs: 7 `ok`, none cached; `0 issues.`; `deps-check: PASS`; `No vulnerabilities found.`; `demo: PASS`; `passed=210 failed=0`; `passed=98 failed=0`. |
| `dev.change.verify` v3 | `01a122c0-55aa-7e14-8282-1edba016622b`, both children linked under the new rule: **verified**. |
| `resolve_binding` at the target, after | Root, build and checks **verified** at `0829a4c`, with these executions as latest. |
| `polaroid.record-model.change` v3 | `01a122c1-0a76-757c-ab28-00beba62ea20`, with that execution as its `verify` child: **verified**. Its evidence lists the reproduction, ADR-0024, why no migration, the tests, four mutation checks with what each broke, and the live upgrade. |

The merge rebases these commits onto `main`, which gives them new SHAs. `0829a4c` is the verified commit; the merged commit with the same tree is unverified until a run there is recorded.

## A persistent per-user catalog (#41)

[#41](https://github.com/ashuangiras/polaroid/issues/41) made `~/.polaroid/data/polaroid.db` the default database ([ADR-0025](../architecture/decisions/0025-per-user-default-database.md)) and moved the shared catalog there from `bin/dogfood/`, where `make clean` would have deleted it. The Genesis adoption trial had reported that risk as feedback `01a122ec…` and deliberately left the live catalog alone. Copilot did the work in the authoring session on 2026-10-10.

**Retrieved first.** No stored procedure covered a storage-location change or moving a live catalog. `polaroid.record-model.change` is for records, schema and requests, and this change touches none of them; verification follows `dev.change.verify`. So a new local procedure, `polaroid.catalog.migrate` v1 (`01a12338-d827…`), was created in the live catalog before the cutover, so that it migrated with everything else, and exported to [examples/development](../../examples/development/procedures/polaroid-catalog-migrate/v1.create.json). No existing procedure proved wrong.

**Cutover, following `polaroid.catalog.migrate` v1** (execution `01a1233c-8caf-7591-9fd5-dd47a7149852`, script and build at `acb3f2e`):

| Step | Result |
| --- | --- |
| identify | The report named `bin/dogfood/polaroid.db`, but the running process differed from the one last recorded: pid 2842, restarted at 01:07 from an interactive shell with the same binary and `-db bin/dogfood/polaroid.db`, holding the database and its `-wal` and `-shm`. No service manager or other repository launched it. `~/.polaroid` did not exist. |
| active-work | No established connections; the last write by another client was at 23:08Z (the paused Genesis trial). A `genesis serve` process listens on another port and never connected. Announced on #41. |
| inventory | 94 files over HTTP: every procedure history, both repositories, 3 bindings, 35 executions with their verifications, 6 reports, and target resolutions for Polaroid at `0829a4c` and Genesis at `62df63b`, both verified. |
| stop, migrate | SIGTERM; the process exited and `lsof` showed no holder. The script backed up into `~/.polaroid/backups/` (integrity ok), restored with mode `0600`, and reported the same dump digest for source, backup and destination. |
| restart, compare | `polaroidd` without `-db` logged `db_source=default`; the 94 files were identical afterwards, and 5 MCP reads equalled them. |
| update | Nothing else named the old path; the README, AGENTS.md and the #28 prompt above now name the default. |

**Verification at `acb3f2e`**, recorded in the migrated catalog: build `01a1233d-0b43…`, checks `01a1233f-9e39…` (226 and 98 end-to-end checks, every daemon under a temporary `HOME`), parent `01a1233f-9e64-774b-a359-1f34c1e4b308`, verified at `acb3f2e`.

**Rollback**, if ever needed: stop `polaroidd` and start it with `-db` naming `bin/dogfood/polaroid.db`, which is kept unchanged; it lacks anything written after the cutover.

## Reproduce

- **Regression replay:** `make demo` (needs `jq` and `sqlite3`).
- **The live loop, in a store of your own:** run `bin/polaroidd -db /tmp/loop.db` on `127.0.0.1:7417` (stop any other daemon on that port first), load the seed with `scripts/load-fixtures.sh -n 1 examples/development`, and give an agent the #33 prompt above with a commit of your choice. Loading without `-n 1` also appends the correction, as any store loaded from the fixtures has it.
