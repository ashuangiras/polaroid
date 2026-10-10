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

> **Status: increments 1–5 of the [roadmap](docs/development/roadmap.md) are implemented, and so is the multi-repository data foundation ([#35](https://github.com/ashuangiras/polaroid/issues/35)), with identity, pagination and discovery contracts made precise in [#37](https://github.com/ashuangiras/polaroid/issues/37).** Procedure identity, immutable version storage, subprocedure references with cycle checks and a bounded composition graph, a repository registry with aliases, declared procedure applicability and origin, repository bindings, immutable execution records with their child executions, context-specific verification, evidence-based contextual resolution, an MCP server, and targeted feedback reports work end to end. The current state and the verification evidence are in [docs/development/status.md](docs/development/status.md).

## What works today

- Create a procedure. It gets a stable ID and a unique canonical key, such as `go.dependency.add`. Its version 1 holds philosophy, method, contract (a JSON object), instructions (a JSON object) and a revision reason.
- List procedures. Retrieve a procedure with its full version history, by ID or by canonical key. Retrieve a single version.
- Append a version against an expected base version. A stale base is rejected with `409`, and history is never overwritten. Of several concurrent revisions from the same base, exactly one succeeds.
- Compose procedures: a version can list named references to other procedures, each with a version policy and a mapping of the child's inputs to parent inputs or literal values. Targets and pinned versions must exist, and references may not form a cycle. `bin/polaroid graph ID N` shows the composition tree with the exact version each reference selects (bounded to 32 levels and 2048 nodes).
- Record what ran: an execution names the exact version (and binding revision, if any), the repository, the full commit hash, a named environment, the effective inputs, `succeeded` or `failed`, and evidence. A parent execution links the child executions that fulfilled its references, which must match each reference's target and pin, the parent's repository and commit, and the inputs the reference maps from the parent's. Executions are immutable, and are listed per procedure.
- Check verification: an execution is verified when it succeeded and every reference of its version has a verified child that ran with the mapped inputs. `bin/polaroid verification ID` says why one is not. `bin/polaroid verifications ID N` lists a version's combinations (repository, commit, environment name, canonical inputs and child-version tree), each judged by its latest execution. Nothing extra is stored; verification is derived from the executions.
- Resolve from evidence: `bin/polaroid graph ID N REPO ENV` resolves contextual references in a repository and environment. A verified parent's execution fixes its children's versions; otherwise a contextual reference takes the highest version verified there, else the latest. Every edge says how it was selected (`pin`, `evidence` or `latest`), and every node with evidence names the execution that verifies it.
- Bind a procedure in a repository under a local name, without copying it. A binding's immutable revisions hold the repository's inputs and a version policy: a pinned version, or a contextual policy, which `bin/polaroid resolve BINDING_ID ENV` resolves from evidence. Binding revisions use the same expected-base rule.
- Organize one catalog for several repositories: register a repository with a canonical identifier and explicit aliases (`bin/polaroid register`, `alias`, `repository-by-identifier`); declare per version whether a procedure is shared or local to one repository, and record where it came from (`origin`). Bindings, executions and composition respect applicability, and verification stays per repository and commit: evidence recorded under any registered identifier of a repository counts for all of them, and executions keep the identifier they were recorded with ([ADR-0022](docs/architecture/decisions/0022-repository-identity-in-evidence.md)). Every version and graph node names its own scope; `unspecified` declares nothing. `bin/polaroid list repository=… scope=… q=…` finds a repository's procedures.
- Page through any list with `limit` and `after` (`bin/polaroid feedbacks limit=20`), repeating the other parameters; lists without `limit` are complete, as before. Pages are live reads; `snapshot=true` pages the procedure or binding list as it was at the first page ([ADR-0023](docs/architecture/decisions/0023-pagination-guarantees-and-scope-labels.md)).
- Records persist in SQLite. Stored versions and binding revisions are immutable, which the database itself enforces.
- Skip work that does not apply, on the record: a reference with a `condition` applies only when the condition holds. Each execution records a decision per conditional reference (applicable, with its child, or not applicable, with a rationale), verification and combinations account for it, and a target passes its own `decisions`, so a skipped check never verifies a task that needs it. Polaroid checks the decision's structure and never evaluates conditions ([ADR-0029](docs/architecture/decisions/0029-conditional-references-and-applicability-decisions.md), [examples](examples/task-aware)).
- Report on Polaroid: an agent or person records a `problem` or a `suggestion` with a one-line summary, details, a reporter name, an optional subject (the service, a repository, a procedure version, a binding revision or an execution) and the repository and execution it was made in (`bin/polaroid feedback`, the MCP tool `report_feedback`). Reports are immutable and untriaged; `bin/polaroid feedbacks` lists and filters them for triage elsewhere, for example as GitHub issues.
- `GET /healthz`, a JSON HTTP API ([contract](docs/architecture/http-api.md)) and a generic CLI.
- An MCP server at `/mcp` ([contract](docs/architecture/mcp.md)): 25 tools with the same operations, records and error codes as the HTTP API, plus read-only resources for procedures, versions and bindings. It speaks stateless streamable HTTP, protocol revisions 2026-07-28 and 2025-11-25.

## Prerequisites

- Go **1.27.1** or newer. `go.mod` selects the **1.27.2** toolchain, which has fixes for vulnerabilities that affect 1.27.1, and Go downloads it automatically unless `GOTOOLCHAIN=local` is set. No C toolchain is needed.
- GNU Make.
- For `make lint`: [golangci-lint](https://golangci-lint.run) **2.14.0**. To use a binary that is not on `PATH`, pass `make lint GOLANGCI_LINT=/path/to/golangci-lint`.
- For `make demo`: `jq` and the `sqlite3` CLI.
- For `make e2e` and `make e2e-mcp`: bash 4 or newer, `curl` and `jq`; `make e2e` also needs the `sqlite3` CLI. `make e2e-mcp` uses npm, when available, for its interoperability checks.
- For `make vuln`: network access, to download govulncheck v1.8.0 and its vulnerability database.

## Quick start

```sh
make build                                   # builds bin/polaroidd and bin/polaroid
bin/polaroidd -db /tmp/polaroid-try.db &     # a scratch catalog; without -db: ~/.polaroid/data/polaroid.db

bin/polaroid create examples/procedures/go-dependency-add/v1.create.json   # note the "id"
bin/polaroid get-by-key go.dependency.add
bin/polaroid revise <id> examples/procedures/go-dependency-add/v2.revise.json
bin/polaroid get-version <id> 1              # version 1 is unchanged
bin/polaroid revise <id> examples/procedures/go-dependency-add/v2.revise.json   # stale base: exits 1 with version_conflict
kill %1                                      # graceful shutdown
```

Or run the scripted version, which also binds the procedure in two repositories, restarts the daemon and checks that every history persisted: `make demo`.

## Download a verification build

[Releases](https://github.com/ashuangiras/polaroid/releases) publish **prerelease verification builds**, not stable releases: `polaroid` and `polaroidd` for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64, built by GitHub Actions from one tagged commit on `main` ([ADR-0028](docs/architecture/decisions/0028-prerelease-verification-builds.md)). Each archive, `polaroid_<tag>_<os>_<arch>.tar.gz`, holds both binaries, an installation and testing guide and `smoke-test.sh`. That script needs only a POSIX shell and `curl`, and it installs nothing and never touches `~/.polaroid`. Each release also has `SHA256SUMS` and `build-manifest.json` (commit, version, Go toolchain, assets).

```sh
tag=v0.1.0-verify.1    # see the release page for the current one
base=https://github.com/ashuangiras/polaroid/releases/download/$tag
curl -fsSLO "$base/polaroid_${tag}_linux_amd64.tar.gz" -O "$base/SHA256SUMS"
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf "polaroid_${tag}_linux_amd64.tar.gz" && cd "polaroid_${tag}_linux_amd64"
./polaroid version && ./polaroidd -version && ./smoke-test.sh
```

- **Who publishes.** Maintainers publish by pushing an annotated prerelease tag (`vX.Y.Z-label`) on `main`. `.github/workflows/release.yml` packages it with `scripts/package.sh`, tests every archive on a native runner (and linux/amd64 in a container without systemd), publishes, then downloads and tests the published assets.
- **No overwrites.** An existing release is never replaced; a changed build needs a new tag.

## Use it from an agent (MCP)

While `polaroidd` runs, point any MCP client that supports streamable HTTP and protocol 2026-07-28 or 2025-11-25 at `http://127.0.0.1:7417/mcp`. In VS Code, this repository's [.vscode/mcp.json](.vscode/mcp.json) already does that: start `bin/polaroidd`, and Copilot chat lists the `polaroid` tools. For another workspace, add the same file:

```json
{"servers": {"polaroid": {"type": "http", "url": "http://127.0.0.1:7417/mcp"}}}
```

The server's instructions describe the agent loop. The tools, their arguments and their errors are in [docs/architecture/mcp.md](docs/architecture/mcp.md).

Polaroid's own development procedures (build, checks, verify a change) are records too, in [examples/development](examples/development). Load them with `scripts/load-fixtures.sh examples/development`, and an agent can verify a change by resolving the `verify-change` binding of `github.com/ashuangiras/polaroid`. [docs/development/procedural-loop.md](docs/development/procedural-loop.md) shows an agent following them, correcting a stale instruction and recording the evidence.

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
| `make demo` | Runs the live demonstration against a real daemon, including a scripted replay of the procedural-memory loop on the development procedures. |
| `make run ARGS="..."` | Builds and runs `polaroidd`. |
| `make ci` | Runs `check`, `vuln`, `demo`, `e2e` and `e2e-mcp` with `E2E_INTEROP=0`, which is exactly what GitHub Actions runs. |
| `make e2e` / `make e2e-mcp` | Runs the end-to-end scripts against a real daemon and writes a report of every command, its output and each check to `bin/e2e/`. `e2e-mcp` also tries the TypeScript SDK and MCP Inspector when npm is available, and inspects a local VS Code install; `E2E_INTEROP=0` skips those checks and reports them as skipped. |
| `make lifecycle` | Installs, crashes, stops, upgrades and uninstalls an isolated managed service with the real launchd or systemd user manager (exit 77 when none is reachable). CI runs it on Linux. |

## Configuration

| `polaroidd` flag | Environment variable | Default |
| --- | --- | --- |
| `-addr` | `POLAROID_ADDR` | `127.0.0.1:7417` (loopback) |
| `-db` | `POLAROID_DB` | `~/.polaroid/data/polaroid.db` in the user's home directory |

Flags override environment variables. On SIGINT or SIGTERM, the daemon stops accepting connections and waits up to 10 seconds for in-flight requests to finish. There is no authentication yet, so keep the daemon on loopback ([ADR-0006](docs/architecture/decisions/0006-local-unauthenticated-api.md)).

The CLI uses `-server URL`, else `$POLAROID_URL`, else `http://127.0.0.1:7417`. It prints each response body (JSON) to stdout, and exits with 0 on success, 1 when the request fails and 2 for a usage error. Run `bin/polaroid help` for the full command list. The service commands (`install`, `start`, `stop`, `restart`, `status`, `uninstall`, `version`) are local and work while the daemon is stopped; see [Run Polaroid as a service](#run-polaroid-as-a-service).

### Where the catalog lives

- **Default.** Without `-db` and `POLAROID_DB`, `polaroidd` uses `~/.polaroid/data/polaroid.db`, where `~` is the home directory Go reports (`$HOME` on Linux and macOS), wherever the daemon is started ([ADR-0025](docs/architecture/decisions/0025-per-user-default-database.md)). The start-up line names the file in use and where the choice came from: `db=/home/u/.polaroid/data/polaroid.db db_source=default` (or `flag`, `environment`).
- **Overrides.** `-db PATH` wins over `POLAROID_DB=PATH`, which wins over the default. A relative path is relative to the working directory, as before. `-db ""` and a set but empty `POLAROID_DB` are refused. If the home directory cannot be determined, is not absolute or does not exist, the daemon exits with an error asking for `-db` or `POLAROID_DB`; it never falls back to the working directory.
- **Layout.** `~/.polaroid/data/polaroid.db` (with SQLite's `-wal` and `-shm` files) and `~/.polaroid/backups/`, which is created only when a backup is written. There are no configuration or log files there, and binaries are never placed there.
- **Permissions (Linux and macOS).** The daemon creates missing `~/.polaroid` and `~/.polaroid/data` directories with mode `0700`, and a missing database with mode `0600`, so that SQLite's `-wal` and `-shm` files are `0600` too. It never changes the mode of an existing directory or file, nor anything at a path given with `-db` or `POLAROID_DB`; at start-up it warns about each existing default-location entry that other users can access. Fix those with `chmod` yourself. Other platforms use the same location without these guarantees.
- **Older databases.** Before #41 the default was `polaroid.db` in the working directory. Nothing is discovered, merged or moved automatically. Keep using an old file with `-db /path/to/polaroid.db` or `POLAROID_DB=/path/to/polaroid.db`, or move it once: stop `polaroidd`, run `scripts/migrate-catalog.sh /path/to/polaroid.db`, then start `polaroidd` without `-db`. The script takes a SQLite online backup (which includes committed transactions still in the `-wal` file) into `~/.polaroid/backups/`, checks it, restores it to `~/.polaroid/data/polaroid.db` with mode `0600`, and refuses if that destination already exists. It keeps the source and the backup. To roll back, stop the daemon and start it with `-db` naming the source; anything written to the new catalog meanwhile is not in the source.
- **Data outlives the build.** `make clean`, rebuilding and replacing binaries never touch `~/.polaroid`, and neither does `polaroid uninstall`.

## Run Polaroid as a service

On macOS and Linux, Polaroid can run as a per-user service that starts at login and restarts after a crash, with no root access and without the source checkout ([ADR-0026](docs/architecture/decisions/0026-per-user-installation-and-managed-service.md)). Other platforms get an error; run `polaroidd` directly there.

```sh
make build                              # in a git checkout; any directory with both binaries works
bin/polaroid install -from bin          # copies polaroid and polaroidd to ~/.local/bin, registers and starts the service
export PATH="$HOME/.local/bin:$PATH"    # add this line to your shell's startup file yourself; install never edits it
polaroid status                         # JSON; exit 0 when running and healthy
```

- **Install versus start.** `install` copies the binaries, writes the service definition, registers it to start at login, starts it, and waits (`-wait`, 30s by default) until the managed process serves the endpoint and `/healthz` answers. It reports `running`, or `failed` with the reason. `start` only starts an installed service, with the same wait. Repeating `install` with the same build changes nothing and only makes sure the service runs.
- **What it installs.**
  - `~/.local/bin/polaroid` and `~/.local/bin/polaroidd`, copied from the directory named by `-from`. Polaroid never guesses a source or downloads one.
  - The service definition. macOS: `~/Library/LaunchAgents/io.github.ashuangiras.polaroid.plist`. Linux: `~/.config/systemd/user/polaroid.service`.
  - The installation record, `~/.local/state/polaroid/install.json`: the build and the SHA-256 of every installed file. `polaroid version` and `polaroidd -version` print a binary's build.
  - `install` refuses to overwrite a binary or definition that the record does not account for.
- **The service.** It runs `~/.local/bin/polaroidd -addr 127.0.0.1:7417 -db ~/.polaroid/data/polaroid.db`, with the absolute paths written into the definition and `HOME` set there too. It needs nothing from your shell (`PATH`, `POLAROID_DB`, the working directory).
  - **Other address or catalog:** run `polaroid install -from DIR -addr 127.0.0.1:7500` or `-db /abs/path.db`. The values are recorded in the definition and kept by later installs; the address must be loopback.
  - **Restarts:** after an unexpected exit the service restarts. macOS: launchd respawns it at most once every 10 seconds. Linux: systemd waits 2 seconds, at most 5 starts per minute.
  - **Stopping:** `polaroid stop` stops it gracefully (SIGTERM) until the next `polaroid start` or login, and it does not respawn.
- **When it runs.** At login, until logout. A LaunchAgent runs in a macOS GUI login session. A systemd user service runs while your user manager does, which is from your first login to your last logout. Neither starts before you log in. Keeping it running without a login on Linux (`loginctl enable-linger`) is your administrator's decision; `install` never enables it.
- **Status.** `polaroid status` prints JSON with the state, the managed PID, the build, the binaries, the endpoint, the database and the diagnostics location. The service is `running` only when the process the service manager reports is the one listening on the endpoint, and it is healthy. Any other process holding the endpoint, such as a `polaroidd` you started by hand, is reported under `conflict` and never stopped. `start` and `install` refuse to start the service while the endpoint is taken. Exit statuses:

  | Exit | State |
  | --- | --- |
  | 0 | `running` |
  | 1 | `failed` (crashed, unreachable, conflicting, or its last start failed) |
  | 3 | `stopped` |
  | 4 | `not-installed` |
  | 5 | `starting` |

- **Diagnostics.**
  - macOS: `~/Library/Logs/Polaroid/polaroidd.log` (directory `0700`, file `0600`). It holds the daemon's log, standard output and error. At 4 MiB it rotates to `polaroidd.log.1`, so at most two files exist.
  - Linux: the journal, `journalctl --user -u polaroid.service`, kept within journald's configured limits.
  - A start that does not become healthy is stopped again, so that the manager does not keep retrying it. `status` then reports `failed` with the reason until the next successful start or `stop`.
- **Upgrades.** `make build` the new version, then `polaroid install -from bin`. It does the following, in order:
  1. Copy the two binaries from `-from` into a private temporary directory, and check that they are one build. Only these staged bytes are hashed and installed, so `-from` may be any directory, including `previous/` ([ADR-0027](docs/architecture/decisions/0027-install-stages-its-source.md)).
  2. Stop the service gracefully.
  3. Keep the current binaries, definition and record in `~/.local/state/polaroid/previous/`. The new copy is complete before it replaces the old one.
  4. Replace both binaries by staged atomic renames, so the CLI and daemon are never a mismatched pair.
  5. Start the service and verify that it is healthy.

  If a replacement or registration step fails, the previous installation is restored and restarted. If the new build fails to start, the service is left stopped. Nothing is rolled back, and no data is restored or downgraded automatically. Recover in one of two ways:
  - **Fix forward** with a corrected build: `polaroid install -from DIR`.
  - **Go back to the previous build:** `polaroid install -from ~/.local/state/polaroid/previous`. This is binary recovery. It installs exactly the build kept there and serves the **current** catalog, with every record written since. If the failed build already upgraded the catalog's schema, the older `polaroidd` refuses to start (`database schema version N is newer than this build supports`) and `status` reports `failed`. Then fix forward.

  **Restoring a catalog backup** is a separate data operation, never part of binary recovery. It loses every record written after the backup. Do it only deliberately:
  1. `polaroid stop`.
  2. Move the current `polaroid.db` and its `-wal` and `-shm` files aside, and keep them.
  3. Copy the backup to the catalog path with mode `0600`.
  4. `polaroid start`.

  Never point the service at a different, older catalog, such as a pre-#41 `bin/dogfood/polaroid.db`, as a substitute for going back to a previous build.

  Back up before an upgrade with `sqlite3 ~/.polaroid/data/polaroid.db ".backup ~/.polaroid/backups/pre-upgrade.db"`.
- **Uninstall.** `polaroid uninstall` does the following:
  1. Stop and unregister the service.
  2. Remove the definition, both binaries (if they are still the installed ones), the macOS log files and `~/.local/state/polaroid`.
  3. Keep `~/.polaroid`: the catalog and its backups.

  There is no purge command.
- **Validation.** `make lifecycle` runs an isolated installation against the real launchd or systemd user manager: a temporary `HOME` whose path contains spaces, its own service name (`-service-name`, meant for such checks only), port and catalog. It builds three revisions of the working tree (an upgrade, and one that fails to start) and checks recovery from `previous/`. It never touches your installation.

## Repository map

| Path | Contents |
| --- | --- |
| [AGENTS.md](AGENTS.md) | The canonical rules for agents and contributors |
| `cmd/polaroidd`, `cmd/polaroid` | The daemon and the CLI |
| `internal/memory` | Domain records and version rules |
| `internal/storage/sqlite` | SQLite persistence |
| `internal/transport/http` | The HTTP API |
| `internal/transport/mcp` | The MCP server at `/mcp` |
| `internal/transport/wire` | Record and error JSON shapes shared by both transports |
| `internal/lifecycle`, `internal/version` | Per-user installation, the managed service, and build identity |
| [examples/](examples) | Example procedure records, and Polaroid's own development procedures |
| [docs/architecture/](docs/architecture) | [Overview](docs/architecture/overview.md), [records](docs/architecture/records.md), [HTTP API](docs/architecture/http-api.md), [MCP](docs/architecture/mcp.md), [decisions](docs/architecture/decisions/README.md) |
| [docs/development/](docs/development) | [Workflow](docs/development/workflow.md), [status](docs/development/status.md), [roadmap](docs/development/roadmap.md), [dependencies](docs/development/dependencies.md), [procedural loop](docs/development/procedural-loop.md) |

## Module path and license

The module path is `github.com/ashuangiras/polaroid`. The project license has not been chosen yet; see [status](docs/development/status.md).
