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

Run the gates and keep their real output:

| Gate | Command | Notes |
| --- | --- | --- |
| Offline checks | `make check` | fmt-check, vet, golangci-lint 2.14.0, build, test, race, deps-check. |
| Vulnerabilities | `make vuln` | Needs network. |
| Live demonstration | `make demo` | Needs `jq`. Runs real binaries. |
| End-to-end scripts | `make e2e`, `make e2e-mcp` | Need bash 4+, `curl`, `jq` and `sqlite3`. Reports go to `bin/e2e/`. `e2e-mcp` also runs independent MCP clients from npm and inspects a local VS Code; `E2E_INTEROP=0` skips them. |
| CI | the `ci` workflow | Runs `make ci`, which is all of the above with `E2E_INTEROP=0`, on `ubuntu-latest`. |

A gate counts only if you ran it. If a tool or the network is unavailable, write "not run", with the reason. A check you have only ever seen pass is unproven. For a new check, show that it fails on a deliberate violation before you rely on it.

## 4. Hand off

1. Fill in the [pull request template](../../.github/pull_request_template.md). It records the behavior change, compatibility consequences, evidence and what you did not check.
2. Update [status.md](status.md). It is a snapshot, overwritten each time, not a log. It holds what is implemented now, the verification evidence, open blockers and the single next action.
3. Comment on the issue with the handoff: completed work, evidence, blockers and next action. Close the issue only when its acceptance criteria are met.

## Commits

Keep each commit focused, with an imperative subject line, for example "Add binding revisions". Reference the issue in the body. Never rewrite published history. If a commit message contains backticks, write it with `git commit -F <file>`, so the shell does not expand them.
