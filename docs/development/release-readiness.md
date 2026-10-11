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
- **Scale.** Throughput is bounded by one writer at a time. The sustained-use run ([below](#sustained-use)) gives the measured rates.

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

Pending: a bounded run of the supported workload against an isolated copy of the shared catalog. Its duration, workload and results are filled in here before release.

## Release criteria

Each criterion must be met for a stable `vX.Y.Z`. Otherwise the release stays a prerelease and the blocker is listed here. The status is updated with the evidence before tagging.

| # | Criterion | Measure | Evidence | Status |
| --- | --- | --- | --- | --- |
| 1 | Scope and policy are defined | This document and ADR-0038 are merged | [#108](https://github.com/ashuangiras/polaroid/issues/108) | Pending |
| 2 | Known defects are fixed | The CLI keeps a quoted task with `=` intact, and filter validation is unchanged | [#102](https://github.com/ashuangiras/polaroid/issues/102), PR #103 | Met |
| 3 | Agents learn formats from the contract, not from rejections | Every rule validation enforces on an MCP write is stated in the tool's description or schema, and the documentation matches the implementation | [#106](https://github.com/ashuangiras/polaroid/issues/106), PR #107 | Met |
| 4 | Simultaneous local agents are safe | Conflicts are explicit; accepted writes are never lost; reads stay consistent; shutdown under load keeps exactly the acknowledged writes | [#104](https://github.com/ashuangiras/polaroid/issues/104), PR #105 | Met |
| 5 | Storage failures are safe | A failure mid-write stores nothing; a full disk refuses writes, keeps reads, and recovers after space is freed | #104 tests, and the out-of-space run on a real volume | Met |
| 6 | Growth is manageable without pruning | Measured rate and policy ([above](#storage-growth)) | This document | Met |
| 7 | Sustained use | A bounded run of the supported workload has no errors, no lost or altered writes, and a database that passes `integrity_check` | [Sustained use](#sustained-use) | Pending |
| 8 | The release target's code is verified | The full gate (`verify-change`) at the release commit's code, and `make lifecycle` against launchd | Release execution | Pending |
| 9 | The pipeline publishes the release kind correctly | A stable tag becomes a stable release marked latest; every archive is tested natively, without systemd, and as a systemd user service | #108 dry run; release run | Pending |
| 10 | The candidate works as a user receives it | On native platforms, from the published candidate archives: fresh install, MCP and CLI connection, restart and ownership, upgrade from `v0.7.0-verify.1` with history kept, online backup, inspection and restore, rollback to the previous build, and fresh-client tool discovery | Candidate checks | Pending |
| 11 | Independent agents adopt it | Fresh agents with public onboarding discover, judge, create or reuse, record and later retrieve procedures in an existing codebase | [Acceptance](#acceptance-by-independent-agents) | Pending |
| 12 | The published artifacts are intact | All archives, `SHA256SUMS` and `build-manifest.json`, downloaded anonymously, with checksums, binary hashes and the source commit matching; the unmodified smoke tests pass; no private data | Release execution | Pending |
| 13 | The shared service adopts it | A backup is taken and the inventory is identical; native ownership, version, commit, endpoint, database and schema are confirmed; a fresh Copilot session makes real calls | Upgrade execution | Pending |

## Acceptance by independent agents

Pending. Reported external evidence and a new trial are recorded here before release, with their provenance.

## Known limitations

- No authentication, authorization or TLS: loopback and one user only.
- No pruning, retention or quotas: growth is managed by concise evidence and by the user's own backup housekeeping.
- A lock held by another program for more than 5 seconds, or a full disk, surfaces to clients as the generic `500 internal`. The daemon's log has the cause.
- Binaries are unsigned and not notarized.
- On macOS, `fsync` does not flush the drive's cache, so a power loss right after a write or backup may lose it.
- Lexical discovery matches shared words, not meaning, so a task described in other words may not find the right procedure.
- Concurrent restores of one catalog are not coordinated.
- Managed-service checks ran natively on macOS (arm64) on the maintainer's machine. Linux managed-service checks run in the repository's `make lifecycle` on a systemd host when dispatched; the release runners test the archives without the managed service.
