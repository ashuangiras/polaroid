# Polaroid: rules for agents and contributors

Polaroid is a versioned procedural memory service for agents, written in Go, with SQLite storage, a JSON HTTP API and an MCP server. This file is the canonical rule set for changing this repository. Tool-specific files, such as `.github/copilot-instructions.md` and `.github/instructions/`, may add platform guidance only. They must not restate or override these rules.

## Before you change anything

1. Read the current state, open blockers and next work item in [docs/development/status.md](docs/development/status.md).
2. Read the contracts your change touches:
   - [docs/architecture/records.md](docs/architecture/records.md): records, identity and versioning.
   - [docs/architecture/http-api.md](docs/architecture/http-api.md): endpoints, errors and limits.
   - [docs/architecture/overview.md](docs/architecture/overview.md): boundaries and dependency direction.
3. Identify the work item and its acceptance criteria. Work items are [GitHub issues](https://github.com/ashuangiras/polaroid/issues); [docs/development/roadmap.md](docs/development/roadmap.md) gives their order. If the criteria are missing or ambiguous, settle them before writing code.
4. Inspect the implementation and its tests before proposing a change. Do not design from the docs alone.

## Rules

- **Task knowledge lives in records, not code.** Adding or changing a task must never require a code change. Do not add task-specific fields, endpoints or branches, and do not interpret the members of `contract` or `instructions`. They are free-form JSON objects.
- **History is immutable and identities are stable.** Never update or delete a stored version, binding revision or execution. Never change a procedure ID or canonical key, or a binding's ID, repository, name or procedure. Never edit an applied migration; add a new one. To change these rules, write an ADR first.
- **Concurrent writes resolve explicitly.** A revision names the version it was based on. A stale base is a conflict. It is never merged or overwritten.
- **Keep the boundaries.** `internal/memory` must not depend on HTTP, SQL or any storage package. `internal/archtest` enforces this.
- **Implement only what is built.** Do not add endpoints, fields or no-op code for planned capabilities. Planned behavior belongs in the roadmap, marked as planned.
- **Never expose internal errors to clients.** Log them and return the generic `internal` error.
- **Add dependencies rarely.** Only add permissively licensed modules, pinned to a version and listed in [docs/development/dependencies.md](docs/development/dependencies.md). `make deps-check` enforces the list.

## While you work

- For every change, explain the observable behavior change and its compatibility consequences: for the API, record contracts, database schema and CLI.
- Add meaningful tests for new behavior and for fixes. Use a real SQLite file and real HTTP. Never use mocks for persistence guarantees, and never use sleeps for synchronization.
- When a contract or behavior changes, update the documentation in the same change: records.md, http-api.md, the README, and an ADR for consequential decisions.

## Before you call it done

- Verify through the Polaroid binding of `github.com/ashuangiras/polaroid` that matches the change (`polaroid bindings github.com/ashuangiras/polaroid`), follow the versions it selects, and record the executions. Each scoped procedure lists what it covers, what it excludes, and when to escalate. A small diff is not proof of low risk: when unsure, use `verify-change`.

  | Binding | Procedure | For | Gate |
  | --- | --- | --- | --- |
  | `verify-docs` | `polaroid.change.docs` | Prose-only documentation that no program, script or test reads | `make docs-check` |
  | `verify-records` | `polaroid.change.records` | Procedure, repository and binding fixtures | `make records-check`, `make demo` |
  | `verify-focused` | `polaroid.change.focused` | A bounded code change with a regression test, outside the areas below | `make focused PKGS='...'` |
  | `verify-change` | `dev.change.verify` | Everything else: storage, migrations, recovery, ownership, lifecycle, public contracts, dependencies, shared behavior, the Makefile, scripts and workflows | `make check`, `make vuln`, `make demo`, `make e2e`, `make e2e-mcp E2E_INTEROP=0` |

  Add `make lifecycle` when the change can alter how Polaroid is installed or runs as a service (`polaroid.lifecycle.check`). A scoped result is reported with its scope, never as verification of the commit.
- Go's test cache applies, except to `internal/archtest`, `internal/lifecycle` and `internal/recovery`, which the Makefile always runs fresh (`scripts/go-test.sh` says why). Report `(cached)` results as cached. Composed targets reuse `bin/` only when it was built from the current source state (`scripts/build.sh`).
- An identical target that is already verified (same commit, working tree, environment, inputs, selected versions and decisions) may be cited instead of rerun. Citing records nothing. Rerun `make vuln` instead of citing a run from an earlier day, because its advisory database changes. Verification never transfers to another commit, even one with an identical tree.
- Hosted CI follows the policy recorded as the `hosted-ci` binding: local-only. Do not wait for or dispatch the `ci` workflow during development; it runs only by manual dispatch. Releases are a separate task, done only when the owner asks (`polaroid.release.publish`), and they wait for the release workflow. Completing a change never publishes a release or upgrades the shared service.
- Report only checks you actually ran, with their real outcome, and say which results were cached. List the checks you did not run and why.
- Leave a handoff in [docs/development/status.md](docs/development/status.md), in the same pull request, and on the issue: completed work, evidence, blockers and the next action.

## Commands

| Command | Does |
| --- | --- |
| `make check` | Runs fmt-check, vet, lint (golangci-lint 2.14.0), the build when `bin/` is not current, test, race, deps-check, docs-check and records-check. |
| `make test` / `make race` | Runs the tests of `PKGS` (default `./...`), or the same tests with the race detector. |
| `make focused PKGS='...'` | The offline checks of `make check`, with the tests of `PKGS` only and without docs-check and records-check. |
| `make docs-check` | Checks every relative link and anchor in the tracked Markdown files. |
| `make records-check` | Loads the fixtures in `examples/` into an isolated catalog twice and resolves their bindings. `RECORDS_FROM=BACKUP` starts from a copy of a `polaroid backup`. |
| `make build` / `make binaries` | Builds `bin/polaroidd` and `bin/polaroid`, or builds them only when `bin/` was not built from the current source state. |
| `make vuln` | Runs govulncheck. Needs network. |
| `make demo` | Runs the live create, revise, conflict, two-repository binding, execution and restart demonstration, then scripted replays of the procedural-memory loop on the development procedures and of the multi-repository fixtures, and an upgrade of a schema-6 database. Needs `jq` and `sqlite3`. |
| `make e2e` / `make e2e-mcp` | Runs the end-to-end scripts: every feature, and `/mcp` with independent clients, against a real daemon. Reports go to `bin/e2e/`. `E2E_INTEROP=0` skips the independent-client checks. |
| `make run ARGS="-db x.db"` | Runs the daemon. Without `-db` or `POLAROID_DB` it serves the per-user catalog, `~/.polaroid/data/polaroid.db`; tests and scripts must always pass a temporary database. |

## Layout

| Path | Responsibility |
| --- | --- |
| `cmd/polaroidd` | Daemon: configuration, wiring, lifecycle |
| `cmd/polaroid` | Generic CLI client, and the local service and catalog commands |
| `internal/memory` | Domain records, validation, version rules, the `Store` contract |
| `internal/storage/sqlite` | `Store` on SQLite: migrations, transactions, immutability triggers |
| `internal/transport/http` | JSON API: parsing, validation, error mapping |
| `internal/transport/mcp` | MCP server at `/mcp`: tools, resources, argument decoding |
| `internal/transport/wire` | Record and error JSON shapes shared by both transports |
| `internal/archtest` | Package-boundary tests |
| `internal/lifecycle` | Per-user installation and the managed service (launchd, systemd user services); no domain, storage or transport code |
| `internal/recovery` | Catalog backup, inspection and restore (ADR-0030); the only way `cmd/polaroid` reaches storage |
| `internal/version` | Build identity from the embedded VCS metadata |
| `examples/` | Example procedure records (task knowledge), and Polaroid's development procedures in `examples/development/`, loaded with `scripts/load-fixtures.sh` |
| `docs/` | Architecture, decisions, workflow, status, roadmap |
