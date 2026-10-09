# Polaroid

**A versioned procedural memory service for agents.**

Agents use Polaroid in a loop:

1. Find the procedure for a task.
2. Interpret its philosophy, method, contract and instructions.
3. Execute it with their own tools.
4. Record the outcome and the evidence.
5. Investigate failures.
6. Append a corrected version.
7. Retrieve the corrected version next time.

Polaroid does not run an LLM, and it does not execute instructions. Its code manages generic records, versions, relationships, resolution and evidence. Task knowledge lives in the records, so adding a task never requires a code change.

> **Status: increment 3 of the [roadmap](docs/development/roadmap.md) is in progress.** Procedure identity, immutable version storage, subprocedure references with cycle checks and a bounded composition graph, repository bindings, and immutable execution records with their child executions, and context-specific verification work end to end. Evidence-based contextual resolution is planned and **not implemented**. The current state and the verification evidence are in [docs/development/status.md](docs/development/status.md).

## What works today

- Create a procedure. It gets a stable ID and a unique canonical key, such as `go.dependency.add`. Its version 1 holds philosophy, method, contract (a JSON object), instructions (a JSON object) and a revision reason.
- List procedures. Retrieve a procedure with its full version history, by ID or by canonical key. Retrieve a single version.
- Append a version against an expected base version. A stale base is rejected with `409`, and history is never overwritten. Of several concurrent revisions from the same base, exactly one succeeds.
- Compose procedures: a version can list named references to other procedures, each with a version policy and a mapping of the child's inputs to parent inputs or literal values. Targets and pinned versions must exist, and references may not form a cycle. `bin/polaroid graph ID N` shows the composition tree with the exact version each reference selects (bounded to 32 levels and 2048 nodes).
- Record what ran: an execution names the exact version (and binding revision, if any), the repository, the full commit hash, a named environment, the effective inputs, `succeeded` or `failed`, and evidence. A parent execution links the child executions that fulfilled its references, which must match each reference's target and pin and the parent's repository and commit. Executions are immutable, and are listed per procedure.
- Check verification: an execution is verified when it succeeded and every reference of its version has a verified child. `bin/polaroid verification ID` says why one is not. `bin/polaroid verifications ID N` lists a version's combinations (repository, commit, environment name, canonical inputs and child-version tree), each judged by its latest execution. Nothing extra is stored; verification is derived from the executions.
- Bind a procedure in a repository under a local name, without copying it. A binding's immutable revisions hold the repository's inputs and a version policy: a pinned version, or a contextual policy that is stored but not resolved yet. Binding revisions use the same expected-base rule.
- Records persist in SQLite. Stored versions and binding revisions are immutable, which the database itself enforces.
- `GET /healthz`, a JSON HTTP API ([contract](docs/architecture/http-api.md)) and a generic CLI.

## Prerequisites

- Go **1.27.1** or newer. `go.mod` selects the **1.27.2** toolchain, which has fixes for vulnerabilities that affect 1.27.1, and Go downloads it automatically unless `GOTOOLCHAIN=local` is set. No C toolchain is needed.
- GNU Make.
- For `make lint`: [golangci-lint](https://golangci-lint.run) **2.14.0**. To use a binary that is not on `PATH`, pass `make lint GOLANGCI_LINT=/path/to/golangci-lint`.
- For `make demo`: `jq`.
- For `make vuln`: network access, to download govulncheck v1.8.0 and its vulnerability database.

## Quick start

```sh
make build                                   # builds bin/polaroidd and bin/polaroid
bin/polaroidd -db polaroid.db &              # listens on 127.0.0.1:7417

bin/polaroid create examples/procedures/go-dependency-add/v1.create.json   # note the "id"
bin/polaroid get-by-key go.dependency.add
bin/polaroid revise <id> examples/procedures/go-dependency-add/v2.revise.json
bin/polaroid get-version <id> 1              # version 1 is unchanged
bin/polaroid revise <id> examples/procedures/go-dependency-add/v2.revise.json   # stale base: exits 1 with version_conflict
kill %1                                      # graceful shutdown
```

Or run the scripted version, which also binds the procedure in two repositories, restarts the daemon and checks that every history persisted: `make demo`.

## Commands

| Command | Does |
| --- | --- |
| `make fmt` / `make fmt-check` | Formats the code, or fails if any file is not formatted. |
| `make vet` / `make lint` | Runs `go vet`, or golangci-lint 2.14.0 with the configuration in [.golangci.yml](.golangci.yml). |
| `make build` | Builds both binaries into `bin/`. |
| `make test` / `make race` | Runs all tests, or all tests under the race detector. |
| `make deps-check` | Verifies the [dependency and license inventory](docs/development/dependencies.md). |
| `make check` | Runs every offline check above. |
| `make vuln` | Runs govulncheck. |
| `make demo` | Runs the live demonstration against a real daemon. |
| `make run ARGS="..."` | Builds and runs `polaroidd`. |
| `make ci` | Runs `check`, `vuln` and `demo`, which is exactly what GitHub Actions runs. |

## Configuration

| `polaroidd` flag | Environment variable | Default |
| --- | --- | --- |
| `-addr` | `POLAROID_ADDR` | `127.0.0.1:7417` (loopback) |
| `-db` | `POLAROID_DB` | `polaroid.db` in the working directory |

Flags override environment variables. On SIGINT or SIGTERM, the daemon stops accepting connections and waits up to 10 seconds for in-flight requests to finish. There is no authentication yet, so keep the daemon on loopback ([ADR-0006](docs/architecture/decisions/0006-local-unauthenticated-api.md)).

The CLI uses `-server URL`, else `$POLAROID_URL`, else `http://127.0.0.1:7417`. It prints each response body (JSON) to stdout, and exits with 0 on success, 1 when the request fails and 2 for a usage error. Run `bin/polaroid help` for the full command list.

## Repository map

| Path | Contents |
| --- | --- |
| [AGENTS.md](AGENTS.md) | The canonical rules for agents and contributors |
| `cmd/polaroidd`, `cmd/polaroid` | The daemon and the CLI |
| `internal/memory` | Domain records and version rules |
| `internal/storage/sqlite` | SQLite persistence |
| `internal/transport/http` | The HTTP API |
| [examples/](examples) | Example procedure records |
| [docs/architecture/](docs/architecture) | [Overview](docs/architecture/overview.md), [records](docs/architecture/records.md), [HTTP API](docs/architecture/http-api.md), [decisions](docs/architecture/decisions/README.md) |
| [docs/development/](docs/development) | [Workflow](docs/development/workflow.md), [status](docs/development/status.md), [roadmap](docs/development/roadmap.md), [dependencies](docs/development/dependencies.md) |

## Module path and license

The module path is `github.com/ashuangiras/polaroid`. The project license has not been chosen yet; see [status](docs/development/status.md).
