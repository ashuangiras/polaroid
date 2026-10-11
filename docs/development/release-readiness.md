# Release readiness

This document defines the scope in which Polaroid is released as stable, and the measurable criteria a stable release must meet. Each criterion links to its evidence or to the blocker that keeps the release a prerelease. The compatibility promise is [ADR-0038](../architecture/decisions/0038-stable-releases-and-compatibility.md).

## Supported operating scope

Polaroid `v1` is a **local, single-user service**. One person's agents and tools on one machine share one catalog through a daemon that listens only on loopback.

| Aspect | Supported | Not supported |
| --- | --- | --- |
| Deployment | `polaroidd` on `127.0.0.1` (default port 7417), run as a per-user service (`polaroid install`) or directly | Remote access, exposure on a network interface, reverse proxies, containers as a hosted service |
| Users | One operating-system user, who owns the catalog file | Several users or tenants sharing a service or a catalog |
| Security | Loopback-only listener, a `Host` check against DNS rebinding, cross-origin protection, strict JSON, size limits ([overview](../architecture/overview.md#security-posture)) | Authentication, authorization, TLS, audit of who wrote what beyond the recorded actor and reporter names |
| Platforms | linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 (static binaries, no runtime dependencies) | Windows, other architectures |
| Service manager | macOS launchd (GUI login session); Linux systemd user manager | Other managers, system-wide services, start before login (Linux lingering is the administrator's choice) |
| Clients | MCP over streamable HTTP, protocol revisions 2026-07-28 and 2025-11-25 (for example VS Code Copilot); the `polaroid` CLI; the JSON HTTP API `/v1` | Other MCP transports (stdio, SSE sessions) |
| Simultaneous agents | Several local clients reading and writing at once; writes are serialized by SQLite, and conflicts are explicit `409`s ([below](#concurrency)) | Several daemons on one catalog; catalogs on network file systems; replication |
| Data | One SQLite file (`~/.polaroid/data/polaroid.db`, WAL, `synchronous=FULL`) with immutable history, enforced by schema triggers | Pruning, retention, quotas, purge |
| Backup and recovery | `polaroid backup` (online, consistent), `inspect-backup`, `restore` with a recovery backup and rollback; binary rollback from `previous/` | Scheduled or remote backups; point-in-time recovery |
| Upgrades | Forward migrations at start-up; a compatible previous build can be reinstalled; the policy in ADR-0038 | Downgrading a migrated catalog |

The service does not run language models or execute instructions. Agents interpret procedures and run them with their own tools.

## Concurrency

Measured in [#104](https://github.com/ashuangiras/polaroid/issues/104) with real SQLite and real HTTP. The behaviour is documented in the [overview](../architecture/overview.md#consistency-and-concurrency).

- **Concurrent revisions and conflicts.** Of concurrent revisions from one base, or creations under one key, exactly one is stored and the others get a `409`. The same holds for binding revisions, invalidations, retirements and toil revisions.
- **Reads during writes.** Statistics, lists and verification read during concurrent execution recording are consistent and never go back. Every accepted write reads back byte for byte.
- **Lock contention.** A writer queues for up to 5 seconds. A write that cannot take the lock in time, for example because another program holds a write transaction, is `500 internal`, stores nothing, and can be retried. Reads continue.
- **Shutdown.** A shutdown under load keeps exactly the writes that were answered `201`, and the database passes SQLite's integrity check.
- **Scale.** Throughput is bounded by one writer at a time. The sustained-use runs ([below](#sustained-use)) give the measured rates.

## Storage growth

**History is kept forever.** Every version, execution, feedback report and lifecycle event is immutable.

- **Retirement and invalidation** change a record's standing: what discovery lists, what verification and selection count. They delete nothing and reclaim no space.
- **Per-write limits** ([ADR-0037](../architecture/decisions/0037-size-limits-on-new-writes.md)) cap each write at 256 KiB and each field at its own limit. They do not cap the catalog.

**Inspect growth** with `polaroid statistics` (MCP `get_catalog_statistics`). It reports:

- record counts;
- the logical bytes of each payload category (count, total, mean, maximum);
- the database pages and file sizes.

Each backup is a directory with a full copy of the catalog: `du -sh ~/.polaroid/backups/*`.

**Keep evidence concise.** Record results, short excerpts, commit hashes, URLs and log locations, never whole logs. Keep large artifacts where they are produced (CI logs, files, issues), and reference them by URL or path with a digest. A value over its limit is refused with `value_too_large`. The error names the field and both sizes.

**Backups are the user's responsibility.**

- Take one before every upgrade, and at a frequency that matches how much work you could afford to lose: daily for active use is a reasonable default.
- Copy important backups off the machine.
- Polaroid never deletes a backup. Remove old ones yourself, after `polaroid inspect-backup` confirms that a newer one is valid.

**When storage fails.** If the disk is full or the database cannot be written:

- the write fails with `500 internal`, the daemon's log names the cause, and no part of the write is stored;
- health and reads keep working;
- free space and retry;
- `polaroid backup` into a full volume fails and leaves nothing behind.

This was measured on a real full volume (#104).

**Decision: no pruning for `v1`.** Pruning is not required for stable within this scope, for three reasons:

- **The measured growth rate is small.** See the measurements below.
- **Writes are capped.** No single write exceeds 256 KiB.
- **Deleting history weakens guarantees.** Verification, selection evidence, execution trees and feedback all depend on history being immutable. Any pruning design needs its own ADR, covering dependency integrity, the consequences for verification, backup and recovery, dry-run planning and an explicit deletion scope.

It is not planned for `v1`.

**Measurements.** Taken from the shared catalog that Polaroid's own development agents used, through `polaroid statistics` and `polaroid executions` on 2026-10-11:

| Measure | Value |
| --- | --- |
| Span | 2026-10-09T16:06Z to 2026-10-11T18:04Z, about 50 hours |
| Executions | 226, about 108 per day of intensive multi-agent development |
| Procedure versions | 43 |
| Database | 1,400,832 bytes, about 6.2 KB per recorded execution, everything else included |
| Execution evidence | mean 1,181 bytes, max 15,422 bytes (limit 65,536) |
| Backups | 23 directories, 14.5 MB in total; the largest 1.3 MB |

**Projection.** At that rate the catalog grows by about 0.7 MB per day, roughly 250 MB per year, well within what one SQLite file on a workstation handles. The worst case is bounded per write (256 KiB), not per day: an agent that records ten maximal writes a minute adds 150 MB an hour. That is the reason to watch `polaroid statistics`. Backups grow with the catalog, one full copy each.

## Sustained use

Two bounded runs on 2026-10-11 against isolated copies of the shared catalog, each restored from a fresh backup and served by its own daemon on another port, with an empty HOME. A script played agents: each loop discovers a task, reads a procedure, resolves a binding, records a leaf and a parent execution with 0.2 to 3 KB of evidence, reads the parent's verification and lists executions. One loop in ten reads the statistics, one in fifty revises a procedure from its latest version, and one in a hundred reports feedback. At the end, every accepted execution was read back and compared byte for byte, and `PRAGMA integrity_check` ran on the stopped catalog.

| | Run 1: saturation | Run 2: agent pace |
| --- | --- | --- |
| Build | `24f2446`: the same Go code as `v1.0.0-rc.1` | `v1.0.0-rc.1` release binaries |
| Load | 30 minutes; 4 workers with no pause between calls | 30 minutes; 8 workers pausing 0 to 2 s (1 s on average) between loops |
| Executions accepted | 58,780 (about 33 a second) | 27,178 (about 15 a second) |
| Readback mismatches | 0 | 0 |
| Errors | 1: a binding resolution got `500 internal` (`database is locked` after the 5 s lock wait) | 0, and no error in the daemon's log |
| Latency p50 / p99 / max | record 2 / 739 / 5,045 ms; resolve 69 / 1,381 / 5,038 ms; discover 4 / 12 / 63 ms | record 2 / 83 / 444 ms; resolve 26 / 112 / 500 ms; discover 4 / 18 / 108 ms |
| Catalog | 226 to 59,006 executions; 1.4 MB to 122.5 MB | 236 to 27,414 executions; 1.4 MB to 57.4 MB |
| Daemon memory (RSS) | about 76 MB at the end | 68 MB at the end, at most 69 MB |
| `integrity_check` | ok | ok |

**Finding: reads that take the write lock.** In run 1, one binding resolution failed. Graph walks, resolutions and verification read inside a write transaction, for one consistent snapshot over several statements. So under continuous writing they wait for the write lock like a writer, and past 5 seconds they fail with `500`. No write was lost or altered, and the retry succeeds. At the paced load of run 2, with twice the clients, no call took more than 0.5 s and none failed. Moving these reads to read-only transactions is [#111](https://github.com/ashuangiras/polaroid/issues/111), after `v1.0.0`; it changes no contract.

Both runs wrote far faster than the shared catalog's measured rate (about 108 executions a day): run 1 recorded about 540 days of that rate in half an hour.

## Release criteria

Each criterion must be met for a stable `vX.Y.Z`. Otherwise the release stays a prerelease and the blocker is listed here. The status is updated with the evidence before tagging.

| # | Criterion | Measure | Evidence | Status |
| --- | --- | --- | --- | --- |
| 1 | Scope and policy are defined | This document and ADR-0038 are merged | [#108](https://github.com/ashuangiras/polaroid/issues/108), PR #109 | Met |
| 2 | Known defects are fixed | The CLI keeps a quoted task with `=` intact, and filter validation is unchanged | [#102](https://github.com/ashuangiras/polaroid/issues/102), PR #103 | Met |
| 3 | Agents learn formats from the contract, not from rejections | Every rule validation enforces on an MCP write is stated in the tool's description or schema, and the documentation matches the implementation | [#106](https://github.com/ashuangiras/polaroid/issues/106), PR #107 | Met |
| 4 | Simultaneous local agents are safe | Conflicts are explicit; accepted writes are never lost; reads stay consistent; shutdown under load keeps exactly the acknowledged writes | [#104](https://github.com/ashuangiras/polaroid/issues/104), PR #105 | Met |
| 5 | Storage failures are safe | A failure mid-write stores nothing; a full disk refuses writes, keeps reads, and recovers after space is freed | #104 tests, and the out-of-space run on a real volume | Met |
| 6 | Growth is manageable without pruning | Measured rate and policy ([above](#storage-growth)) | This document | Met |
| 7 | Sustained use | A bounded run of the supported workload has no errors, no lost or altered writes, and a database that passes `integrity_check` | [Sustained use](#sustained-use): run 2, 30 minutes at agent pace, 27,178 executions | Met. Run 1, at saturation, had one failed read ([#111](https://github.com/ashuangiras/polaroid/issues/111), [limitations](#known-limitations)) |
| 8 | The release target's code is verified | The full gate (`verify-change`) at the release commit's code, and `make lifecycle` against launchd | `verify-change` at `6c98ce3`, verified (`dev.change.verify` `01a12c58-be35`); `make lifecycle` launchd 68/0 (`polaroid.lifecycle.check` `01a12c59-bfe5`). Later commits change documentation only | Met |
| 9 | The pipeline publishes the release kind correctly | A stable tag becomes a stable release marked latest; every archive is tested natively, without systemd, and as a systemd user service | #108 dry run 38162980089; `v1.0.0-rc.1` run 38163822689 published a prerelease that was not marked latest, all nine jobs passing | Pending: the stable kind is shown only by the release run |
| 10 | The candidate works as a user receives it | On native platforms, from the published candidate archives: fresh install, MCP and CLI connection, restart and ownership, upgrade from `v0.7.0-verify.1` with history kept, online backup, inspection and restore, rollback to the previous build, and fresh-client tool discovery | `v1.0.0-rc.1` archives downloaded anonymously and checked; an isolated launchd candidate run on darwin/arm64, 27 checks with none failed; the acceptance chats listed the tools. The other platforms: the release runners' native smoke and harness tests, and the systemd user-service job | Met, natively on darwin/arm64 only |
| 11 | Independent agents adopt it | Fresh agents with public onboarding discover, judge, create or reuse, record and later retrieve procedures in an existing codebase | [Acceptance](#acceptance-by-independent-agents): three fresh chats on google/uuid | Met for discovery, reuse, binding, recording and retrieval. Creation untested; memory confound |
| 12 | The published artifacts are intact | All archives, `SHA256SUMS` and `build-manifest.json`, downloaded anonymously, with checksums, binary hashes and the source commit matching; the unmodified smoke tests pass; no private data | Release execution | Pending |
| 13 | The shared service adopts it | A backup is taken and the inventory is identical; native ownership, version, commit, endpoint, database and schema are confirmed; a fresh Copilot session makes real calls | Upgrade execution | Pending |

## Acceptance by independent agents

Two sources of evidence, kept apart by provenance.

**Reported, not inspected.** The owner reported earlier trials on ItsDangerous in which two agents used Polaroid. Their transcripts and catalogs were not available to this review, so they are cited as reported and are not counted as evidence here.

**Observed trial (2026-10-11).** Three fresh VS Code Copilot chats, one after another, each had a short task in an existing codebase they had not seen: a clone of [google/uuid](https://github.com/google/uuid) at `2d3c2a9`. An isolated managed service on another port served a copy of the shared catalog. The agents had only the public onboarding: the MCP server's instructions and the tool descriptions. Their transcripts and the trial catalog were inspected afterwards. A fourth chat opened in that workspace stopped before any Polaroid call and is not counted.

| Step | Session A (add a test) | Session B (another test, later) | Session C (after #112) |
| --- | --- | --- | --- |
| Discover | `discover_procedures` with its task | `discover_procedures`, then `get_procedure`, `list_procedures` and `list_bindings` | `list_bindings` for the repository first, then `discover_procedures`, `get_binding` and `get_procedure` |
| Judge, reuse or create | Reused the shared `go.module.checks` v4 from its discovery entry. It read the procedure (`get_procedure`) only after recording. It created nothing | Read the same procedure, reused it, and created a binding `check-tests` for the repository | Reused B's binding and its procedure after reading them. It created nothing |
| Register | Registered `github.com/google/uuid` | Its registration conflicted. `get_repository` found A's registration, which it used | Not needed: the repository was registered |
| Record | One execution, with inputs `{}`, at the base commit | One execution at the base commit, with evidence claiming a clean working tree | Committed the new test locally (`56ef415`). It then called `resolve_binding` with that commit and its inputs, and recorded one execution through the binding at that commit. The execution is verified |
| Retrieve | Not applicable | Found A's repository and the same procedure. It did not look up A's execution | Found B's binding and, through it, the procedure. It did not look up earlier executions |

Sessions A and B ran on `v1.0.0-rc.1`. Their prompts said "do not commit", which left them no correct way to record a commit. Session C ran on the build of [#112](https://github.com/ashuangiras/polaroid/issues/112) (`6c98ce3`), and its prompt allowed local commits.

**Findings.**

- **A's and B's recorded executions were wrong evidence.** Each agent ran the checks on uncommitted changes and recorded the base commit as the commit that ran. Both executions were invalidated in the trial catalog. The commit field's contract did not say what to do with uncommitted changes.
- **#112 states it now**, in the tool description, the server's instructions and the record contract: commit locally first and record that commit.
  - Session C, on that build, committed and recorded the commit that ran.
  - It ran the tests just before committing, then committed exactly the tested file with no change in between. So the recorded commit's tree is the tree that ran, but in the reverse order from the guidance.
- Session A recorded against a procedure whose contract it had not read. Its inputs, `{}`, did not state the gate commands it ran. B and C read the contract first.
- No agent created a procedure or reported feedback. The tasks required neither, so creation was not exercised.
- **Confound.** The Copilot user's persistent memory, which mentions Polaroid, was loaded into every chat. The sessions were fresh chats, not agents without any prior knowledge.

**Assessment.** Through the public onboarding, three fresh agents in an unfamiliar codebase did the following:

- discovered procedures and reused one;
- registered the repository and bound the procedure;
- recorded executions;
- built on each other's records: B found A's registration, and C found B's binding.

With the #112 guidance, the last agent recorded a verified execution at the commit that ran. Two things remain unproven: creating a procedure, and a trial without the memory confound.

## Known limitations

- No authentication, authorization or TLS: loopback and one user only.
- No pruning, retention or quotas: growth is managed by concise evidence and by the user's own backup housekeeping.
- A lock held by another program for more than 5 seconds, or a full disk, surfaces to clients as the generic `500 internal`. The daemon's log has the cause.
- Binaries are unsigned and not notarized.
- On macOS, `fsync` does not flush the drive's cache, so a power loss right after a write or backup may lose it.
- Lexical discovery matches shared words, not meaning, so a task described in other words may not find the right procedure.
- Concurrent restores of one catalog are not coordinated.
- **Reads under saturation.** Graph walks, resolutions and verification take the write lock for their snapshot. Under continuous writing from several clients, such a read can wait past the 5-second lock timeout and fail with `500 internal`. It happened once in 29,390 resolutions in sustained run 1, and never at agent pace. Retry. [#111](https://github.com/ashuangiras/polaroid/issues/111) tracks a fix that changes no contract.
- **Evidence is what agents claim.** Polaroid cannot see a working tree. An execution is accurate only if the agent committed what it ran, as the contract says ([#112](https://github.com/ashuangiras/polaroid/issues/112)). Wrong evidence is withdrawn with `invalidate_execution`.
- **Acceptance gaps.** No independent agent has yet created a procedure in a trial, and the observed trials ran with the user's persistent Copilot memory. The ItsDangerous trials are reported, not inspected.
- **Platform coverage.** Install, upgrade, backup, restore and rollback ran natively only on macOS arm64, on the maintainer's machine. On Linux, the release workflow tests each archive natively, and as a systemd user service on amd64 (`test-managed-service`). darwin/amd64 is tested by the release runners' smoke tests and harness only.
