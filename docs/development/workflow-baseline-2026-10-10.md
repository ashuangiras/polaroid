# Agent workflow timing baseline (2026-10-10)

This report measures Polaroid's current agent development workflow on one small, real task, before any optimization ([#62](https://github.com/ashuangiras/polaroid/issues/62)). Nothing was changed to speed it up: no procedures, checks, caching or CI policy. The sanitized event log is attached to #62.

## Task and versions

| | |
| --- | --- |
| Task | A correction to `docs/development/workflow.md`: the gate table now lists `make lifecycle`, and its CI row names both jobs of the `ci` workflow. Docs only. |
| Work item / PR | [#62](https://github.com/ashuangiras/polaroid/issues/62) / [#63](https://github.com/ashuangiras/polaroid/pull/63) |
| Commits | Base `0b4797e`; verified `25f7f04`; merged as `34f3546` (same tree) |
| Procedures | Binding `verify-change` (`01a12169-52ff-7804-a50d-6719c951301c`), revision 1. Root `dev.change.verify` v3, with `go.module.build` v2 and `go.module.checks` v3, all selected by evidence. No narrower procedure exists for documentation changes, and none was added. |
| Executions | Build `01a1279e-02c6-7228-8557-c495506e258c`, checks `01a1279e-053e-7937-8ac4-8648879e7b33`, parent `01a1279e-079f-72bc-bf10-5cb95234010c`. The parent is verified, and a second resolve shows this run as the target's latest. |
| Environment | darwin/arm64; Go 1.27.2 (matches `go.mod`); golangci-lint 2.14.0 passed through `GOLANGCI_LINT` (2.12.2 is on `PATH`); bash 5.2.37; jq 1.8.1; sqlite3 3.51.0; gh 2.102.0. The Polaroid CLI was the installed `v0.3.0-verify.2`, against the live instance. |
| Caches | Warm and not cleared: Go build cache 6.5 GB, module cache 9.6 GB, golangci-lint cache 127 MB. `GOFLAGS` was empty in the shell. The procedure sets `GOFLAGS=-count=1` for the tests, so none were cached: 0 `(cached)` lines. |

## Instrumentation and its limits

- **Commands:** each local command ran through a small wrapper outside the repository (`/tmp/p62/ev.py`). It records category, description, command, start and end (UTC), elapsed time from `time.monotonic_ns`, exit status, and `HEAD` and dirty-file count before and after. Point events (task start, task end, edit start and end) are marks.
- **Agent tool calls:** taken from the Copilot chat transcript's `tool.execution_start` and `tool.execution_complete` timestamps, with tool names but no arguments.
- **Unattributed time:** wall time covered by neither a tool call nor a timed command. It may contain model reasoning and response generation, orchestration, tool-dispatch overhead or user pauses; there were no user pauses in this run. Model reasoning and tokens are **not exposed** by this environment and are not reported.
- **Overhead:** time inside a tool call but outside a timed command is listed as overhead, for example terminal startup and the `gh`/`git` command wrappers' own shell.
- **Granularity:** sub-steps inside `make check` (vet, lint, build, test, race, deps-check) are not timed separately. Only `go test`'s own per-package times are available for them.

## Headline

| | Seconds | Share |
| --- | ---: | ---: |
| **End-to-end** (task start mark to task complete mark, monotonic) | **678.9** | 100% |
| Timed commands (sequential, no overlap among them) | 448.4 | 66% |
| Tool-call time outside timed commands (overhead) | 30.5 | 4% |
| Unattributed | 200.0 | 29% |
| Hosted CI wait | not performed | owner decision; not estimated |
| *Outside the task:* instrumentation setup, including task selection | 171.2 | — |
| *Outside the task:* report writing | from 21:01:40Z; total in the #62 comment | — |

The 56 agent tool calls made while the gates ran in the background (27 `list_dir`, 27 `get_terminal_output`, 1 `read_file` and 1 `grep_search` of the Makefile) overlap the gate window. They are not added to it. Summed operation durations are not end-to-end time.

## Phases

| Phase (UTC) | Wall s | Attributed s | Unattributed s |
| --- | ---: | ---: | ---: |
| Discovery, work item, binding and procedure reads (20:49:45–20:51:05) | 80.4 | 13.1 | 67.3 |
| Edit, fact check, commit (20:51:05–20:51:43) | 38.1 | 5.8 | 32.4 |
| Context and resolve (20:51:44–20:51:45) | 0.8 | 0.8 | 0.0 |
| Preparing the gate run (20:51:45–20:52:11) | 26.4 | 2.5 | 23.9 |
| **Build and checks, in the background** (20:52:11–20:59:32) | **440.3** | 436.2 | 4.1 |
| Evidence extraction and record script (20:59:32–21:00:28) | 56.5 | 2.3 | 54.2 |
| Recheck, record, confirm (21:00:28–21:00:31) | 2.9 | 2.9 | 0.0 |
| Push, PR, merge, pull (21:00:31–21:01:04) | 32.9 | 14.8 | 18.1 |

## Commands, in order

| Time (UTC) | s | Category | Command | Notes |
| --- | ---: | --- | --- | --- |
| 20:49:45 | 0.022 | Polaroid read | `polaroid list repository=…` | 12 procedures |
| 20:49:46 | 0.027 | Polaroid read | `polaroid bindings repository=…` | exit 2, agent usage error |
| 20:49:59 | 0.020 | inspection | `polaroid help` | |
| 20:50:00 | 0.019 | Polaroid read | `polaroid bindings REPO` | |
| 20:50:23 | 1.344 | Git/PR | `gh issue create` | #62 |
| 20:50:25 | 0.021 | Polaroid read | `polaroid get-binding` | revision 1 |
| 20:50:25 | 0.074 | Polaroid read | 3 × `polaroid get-version` | exit 2: zsh did not split the agent's loop variable |
| 20:50:40 | 0.085 | Polaroid read | 3 × `polaroid get-version` | v3, v2, v3 |
| 20:51:05 | 0.053 | Git/PR | `git switch -c` | |
| 20:51:20 | 0.188 | inspection | grep of `lifecycle-check.sh` and `ci.yml` | fact check of the edit |
| 20:51:43 | 0.104 | Git/PR | `git commit` | `25f7f04` |
| 20:51:44 | 0.221 | verify | context: remote, toolchain, clean tree | |
| 20:51:45 | 0.097 | Polaroid read | `polaroid resolve` (before) | all three nodes unverified |
| 20:52:11 | 0.072 | build | toolchain check | |
| 20:52:12 | 6.718 | build | `make build` | warm build cache |
| 20:52:19 | 0.133 | build | artifact mtimes, tree unchanged | |
| 20:52:20 | 0.419 | checks | tool versions | |
| 20:52:20 | 100.953 | checks | `GOFLAGS=-count=1 go test ./...` | 9 `ok`, none cached; `internal/lifecycle` 97.1 s |
| 20:54:02 | 243.636 | checks | `GOFLAGS=-count=1 make check` | `0 issues.`, `deps-check: PASS`; test: lifecycle 106.6 s; race: lifecycle 104.6 s |
| 20:58:06 | 6.781 | checks | `make vuln` | `No vulnerabilities found.` |
| 20:58:13 | 37.660 | checks | `make demo` | `demo: PASS` |
| 20:58:51 | 28.695 | checks | `make e2e` | `passed=245 failed=0` |
| 20:59:20 | 10.488 | checks | `make e2e-mcp E2E_INTEROP=0` | `passed=98 failed=0` |
| 20:59:31 | 0.123 | checks | tree after the gates | clean |
| 21:00:28 | 0.119 | verify | recheck of HEAD, tree, inputs | |
| 21:00:29 | 0.141 | recording | 3 × `polaroid record` | build, checks, parent |
| 21:00:30 | 0.022 | recording | `polaroid verification` | verified |
| 21:00:31 | 0.031 | Polaroid read | `polaroid resolve` (confirm) | root and children verified by this run |
| 21:00:52 | 2.183 | Git/PR | `git push` | |
| 21:00:55 | 3.098 | Git/PR | `gh pr create` | #63 |
| 21:00:58 | 3.591 | Git/PR | `gh pr merge --merge` | no CI wait |
| 21:01:02 | 1.136 | Git/PR | switch to main and pull | merge tree equals `25f7f04` |

## Summary by category

Seconds of timed commands, plus agent tool time where no command ran.

| Category | Seconds | Notes |
| --- | ---: | --- |
| Checks (build child, checks child, context, recheck) | 436.0 | 64% of end-to-end |
| Git/PR (issue, branch, commit, push, PR, merge, pull) | 11.5 | |
| Polaroid reads (discovery, binding, versions, resolve ×2) | 0.4 | |
| Recording (3 records, 1 verification) | 0.2 | |
| Inspection (help, fact check; `read_file`, `grep_search`) | 0.4 | |
| Editing (`replace_string_in_file`, script `create_file`) | < 0.2 | the edit phase is 38.1 s of wall time, mostly unattributed |
| Hosted CI waiting | 0 | not performed |
| Tool overhead outside commands | 30.5 | |
| Unattributed | 200.0 | |

**Polaroid calls:** 16, totalling 0.54 s, with the slowest a 0.097 s resolve.

| Operation | Calls | Failed |
| --- | ---: | --- |
| `list` | 3 | 1 (usage) |
| `get-binding` | 1 | |
| `get-version` | 6 | 3 (agent shell error) |
| `resolve` | 2 | |
| `record` | 3 | |
| `verification` | 1 | |

## Repeated work

- **Building the binaries.** `go build -trimpath ./cmd/polaroidd ./cmd/polaroid` runs **seven** times per verification:
  - the build child's `make build` (6.7 s, the only one timed separately);
  - `make check`'s `build`;
  - the `build` prerequisite of `make demo`, `make e2e` and `make e2e-mcp`;
  - `make build` again inside `scripts/e2e.sh` and `scripts/e2e-mcp.sh`.

  The cause is target prerequisites and scripts that rebuild to be self-contained.
- **Running the tests.** `go test ./...` runs fresh **twice**: the checks child's `unit` step (100.95 s, timed directly) and `make check`'s `test` (not timed separately). `go.module.checks` v3 runs `unit` "for fast feedback" before `make check`, and `GOFLAGS=-count=1` forbids reusing results. `go test -race ./...` is a third, instrumented run of the same tests.
- **`internal/lifecycle`.** This package took 97.1 s, 106.6 s and 104.6 s in the three test runs (as `go test` reports). Packages run in parallel, so each run's length is at least this package's.

The duplicate plain test run cost about 101 s, measured directly from the `unit` step. The extra builds after the first are not separately measurable here.

## Observed bottlenecks

- The background build and checks took 440.3 s, 65% of the end-to-end time. Within that, `make check` took 243.6 s, the unit run 101.0 s, `make demo` 37.7 s, `make e2e` 28.7 s, `make e2e-mcp` 10.5 s, `make vuln` 6.8 s and `make build` 6.7 s.
- `internal/lifecycle` was the longest package in every test run, at 97 to 107 seconds each.
- Unattributed time was 200 s, 29% of the run, spread over all interactive phases. The 66 gaps between tool calls had a median of 7.9 s and a maximum of 34.2 s.
- Polaroid itself took 0.54 s across all 16 calls. It is not a bottleneck in this run.
- Git and GitHub operations took 11.5 s. No hosted CI was waited for.

## Proposed explanations (not verified here)

- `internal/lifecycle`'s duration may come from its tests' real process start, readiness and stop waits. That has not been profiled.
- The duplicate unit run and the seven builds follow directly from the procedure and the Makefile as written, as described above.
- The unattributed time is consistent with model turns between tool calls: reading output and composing the next command or file. Two agent mistakes (a CLI usage error, and zsh not word-splitting a loop variable) each added a round trip.

## Comparing a later run

To compare, use the same kind of task (docs only, warm caches) and measure the same way: the event wrapper plus the transcript tool-call timestamps. Compare end-to-end time, the background gate window, the per-command times, and the unattributed share. This baseline ran every gate with `GOFLAGS=-count=1`, as `go.module.checks` v3 then required. Since [#65](https://github.com/ashuangiras/polaroid/issues/65), a docs-only change is verified through the `verify-docs` binding, and `go.module.checks` v4 lets Go's test cache serve what the gate allows, so a later run differs in check selection and cache policy as well as in its task.
