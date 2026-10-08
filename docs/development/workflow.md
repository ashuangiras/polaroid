# Development workflow

How a Copilot session, or any contributor, takes one work item from selection to handoff. The rules themselves are in [AGENTS.md](../../AGENTS.md). This page describes the sequence.

## 1. Select

1. Read [status.md](status.md). It names the next work item and any blockers.
2. Open the work item. Use the GitHub issue once the repository is published. Until then, use the issue-ready draft in [roadmap.md](roadmap.md). Take one work item per branch and pull request.
3. Confirm that the acceptance criteria are testable. If they are not, rewrite them in the issue or draft first. Settle any open decision in the item before you write code, and record it as an ADR if it is consequential.

## 2. Implement

1. Create a branch before you edit anything. Name it `issue-<number>-<slug>`, or `roadmap-<item>-<slug>` while issues do not exist.
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
| CI | the `ci` workflow | Runs `make ci`, which is all of the above, on `ubuntu-latest`. |

A gate counts only if you ran it. If a tool or the network is unavailable, write "not run", with the reason. A check you have only ever seen pass is unproven. For a new check, show that it fails on a deliberate violation before you rely on it.

## 4. Hand off

1. Fill in the [pull request template](../../.github/pull_request_template.md). It records the behavior change, compatibility consequences, evidence and what you did not check.
2. Update [status.md](status.md). It is a snapshot, overwritten each time, not a log. It holds what is implemented now, the verification evidence, open blockers and the single next action.
3. Comment on the issue with the handoff: completed work, evidence, blockers and next action. Close the issue only when its acceptance criteria are met.

## Commits

Keep each commit focused, with an imperative subject line, for example "Add binding revisions". Reference the issue in the body. Never rewrite published history. If a commit message contains backticks, write it with `git commit -F <file>`, so the shell does not expand them.

## Publishing the repository (owner action, pending)

There is no remote yet. When the owner creates one:

1. Replace the provisional module path, as [ADR-0002](../architecture/decisions/0002-go-toolchain-and-provisional-module-path.md) describes.
2. Push, and confirm that the `ci` workflow passes.
3. File the roadmap items as issues with the work item template, and replace the drafts in the roadmap with links.
