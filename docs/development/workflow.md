# Development workflow

How a Copilot session, or any contributor, takes one work item from selection to handoff. The rules themselves are in [AGENTS.md](../../AGENTS.md). This page describes the sequence.

## 1. Select

1. Read [status.md](status.md). It names the next work item and any blockers.
2. Open the work item: a [GitHub issue](https://github.com/ashuangiras/polaroid/issues). [roadmap.md](roadmap.md) gives the order. Items there that are not yet filed must be refined and filed as issues first. Take one work item per branch and pull request.
3. Confirm that the acceptance criteria are testable. If they are not, rewrite them in the issue first. Settle any open decision in the item before you write code, and record it as an ADR if it is consequential.

## 2. Implement

1. Create a branch from an up-to-date `main` before you edit anything. Name it `issue-<number>-<slug>`.
2. Read the code and the tests for every package you will touch. Follow the existing patterns, and keep the change to what the acceptance criteria require.
3. Write a test that captures each criterion: real SQLite and real HTTP, barriers instead of sleeps. Watch the test fail before you make it pass.
4. In the same change, update the contracts that change: [records.md](../architecture/records.md), [http-api.md](../architecture/http-api.md), the README, and a new migration if the schema changes.

## 3. Verify

Choose the verification by what the change can affect, not by the size of its diff, and keep the real output. The bindings of `github.com/ashuangiras/polaroid` name the procedures (AGENTS.md, "Before you call it done"):

| Binding | For | Gate |
| --- | --- | --- |
| `verify-docs` | Prose-only documentation that no program, script or test reads | `make docs-check` |
| `verify-records` | Procedure, repository and binding fixtures in `examples/` | `make records-check` once, from a backup of the live catalog, then `make demo` |
| `verify-focused` | A bounded code change with a regression test, outside storage, migrations, recovery, lifecycle, ownership, public contracts, dependencies and shared behavior. Explanatory wording in a tool or command description qualifies when it changes no name, annotation, schema, accepted value, response, error or behavior; any of those does not | `make focused PKGS='...'` |
| `verify-change` | Everything else, and whenever you are unsure | `make check`, `make vuln`, `make demo`, `make e2e`, `make e2e-mcp E2E_INTEROP=0` |

The commands:

| Gate | Command | Notes |
| --- | --- | --- |
| Offline checks | `make check` | fmt-check, vet, golangci-lint 2.14.0, the build when `bin/` is not current, test, race, deps-check, docs-check, records-check, loader-check. |
| Focused offline checks | `make focused PKGS='...'` | The same, with the tests of `PKGS` only (the changed packages and every package that imports them), and without docs-check, records-check and loader-check. |
| Documentation links | `make docs-check` | Every relative link and anchor in the tracked Markdown files. No Go is built. |
| Records | `make records-check` | Loads the fixtures into an isolated catalog twice and resolves their bindings. `RECORDS_FROM=DIR` starts from a copy of a `polaroid backup`, for example of the live catalog. |
| Fixture loader | `make loader-check` | Creating, comparing, appending and refusing binding revisions (`later_revisions`, [examples/README.md](../../examples/README.md)) in an isolated catalog. |
| Vulnerabilities | `make vuln` | Needs network. Its advisory database changes, so a result from an earlier day is not cited. |
| Live demonstration | `make demo` | Needs `jq` and `sqlite3`. Runs real binaries. Its last steps replay the procedural-memory loop and the multi-repository fixtures with scripted outcomes, and upgrade a schema-6 database. |
| End-to-end scripts | `make e2e`, `make e2e-mcp` | Need bash 4+, `curl`, `jq` and `sqlite3`. Reports go to `bin/e2e/`. `e2e-mcp` also runs independent MCP clients from npm and inspects a local VS Code; `E2E_INTEROP=0` skips them. |
| Managed service | `make lifecycle` | Installs, crashes, stops, upgrades, restores and uninstalls an isolated managed service against the real launchd (needs a GUI login session) or systemd user manager. It exits 77 when no manager is reachable, which means not run. Required when a change can alter how Polaroid is installed or run as a service (the condition of the `polaroid.lifecycle.check` procedure). |
| Hosted CI | the `ci` workflow | Manual dispatch only. Two jobs on `ubuntu-latest`: `make ci`, which is all of the above except `make lifecycle` and `make focused`, with `E2E_INTEROP=0`; and `make lifecycle` against a systemd user manager. Polaroid's policy, recorded as the `hosted-ci` binding, is local-only: development does not wait for it. |

Work is done once. `make demo`, `make e2e`, `make e2e-mcp`, `make records-check` and `make check` reuse `bin/` when `scripts/build.sh` recorded that it was built from the current source state (commit, uncommitted changes and toolchain), and rebuild otherwise. Go's test cache serves every package except `internal/archtest`, `internal/lifecycle` and `internal/recovery`, which always run fresh, because their results depend on inputs the cache does not see (`scripts/go-test.sh`). Test and race runs stay separate: the race detector is different evidence. Report `(cached)` results as cached.

A gate counts only if you ran it. If a tool or the network is unavailable, write "not run", with the reason. A check you have only ever seen pass is unproven. For a new check, show that it fails on a deliberate violation before you rely on it. An identical target already verified (same commit, working tree, environment, inputs, versions and decisions) may be cited instead of rerun; citing is a lookup and records nothing.

With a Polaroid store that holds the [development procedures](../../examples/development) on `/mcp`, resolve the binding at the commit you verify (`commit`, `inputs` and `decisions`) and follow the selected versions, recording each execution ([procedural-loop.md](procedural-loop.md)). Evidence from another commit selects versions; it does not verify yours. A new version is not selected over an evidenced one until it has evidence: choose it explicitly for its first run (get_graph for that version) and say so. If an instruction is wrong, append a corrected version rather than working around it. Report the gates in the pull request either way.

Releases are separate from development: only when the owner asks, with `polaroid.release.publish`, which waits for the release workflow's packaging, archive tests and publication. A dry run of the release workflow is dispatched by hand (`gh workflow run release.yml --ref BRANCH`); pull requests do not start it.

## 4. Hand off

1. Fill in the [pull request template](../../.github/pull_request_template.md). It records the behavior change, compatibility consequences, evidence and what you did not check.
2. Update [status.md](status.md) in the same pull request. It is a snapshot, overwritten each time, not a log. It holds what is implemented now, the verification evidence, open blockers and the single next action. Keep it short; do not open a separate documentation pull request for the handoff.
3. Comment on the issue with the handoff: completed work, evidence, blockers and next action. Close the issue only when its acceptance criteria are met.

## Working efficiently

- Read the selected procedure versions once each, before following them; batch independent reads.
- Start long commands in the background, then wait for them once, bounded, instead of polling or listing directories while nothing depends on the result.
- Use the repository's commands for evidence; do not write a bespoke script where a Make target already prints a deterministic summary line.

## Commits

Keep each commit focused, with an imperative subject line, for example "Add binding revisions". Reference the issue in the body. Never rewrite published history. If a commit message contains backticks, write it with `git commit -F <file>`, so the shell does not expand them.
