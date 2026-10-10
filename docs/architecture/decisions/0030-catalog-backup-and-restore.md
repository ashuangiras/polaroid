# 0030. Catalog backup, inspection and restore

**Status:** Accepted. Extends [ADR-0025](0025-per-user-default-database.md) and [ADR-0026](0026-per-user-installation-and-managed-service.md); changes the `cmd/polaroid` boundary of [overview.md](../overview.md).
**Date:** 2026-10-10

## Context

The catalog is one SQLite file in WAL mode. Today a backup needs the `sqlite3` CLI (`polaroid.service.manage`, `scripts/migrate-catalog.sh`), and there is no restore path: [ADR-0027](0027-install-stages-its-source.md) only says that restoring data is a separate, deliberate operation. Copying the file of a running catalog is unsafe, because committed transactions may still live only in `-wal`. Replacing a catalog under a running writer, or beside a stale `-wal`, can corrupt it.

## Decision

Three local commands of `polaroid`. Like `install` and `status`, they never call the API. The `polaroid` process opens the database files itself with the SQLite driver that `polaroidd` uses. `cmd/polaroid` may therefore depend on storage, but only through `internal/recovery`, which in turn uses `internal/storage/sqlite` and `internal/lifecycle`; `internal/archtest` enforces this. API commands still never touch storage.

**Catalog selection** for `backup` and `restore`: `-db`, else `POLAROID_DB`, else the installed service's catalog, else `~/.polaroid/data/polaroid.db`. A catalog is *managed* when its path equals the installation record's `db`.

**`polaroid backup [-db FILE] [-dir DIR]`**
- **Opening the source:** it opens the catalog read-write but never writes to it. It runs no migration, and it refuses a missing file or a schema newer than the binary supports.
- **Snapshot:** it runs `VACUUM INTO` into an empty `0600` file in a private staging directory under `DIR` (default `~/.polaroid/backups`, `0700`). `VACUUM INTO` copies one read transaction, so committed `-wal` content is included and concurrent writers keep writing. The result is a single rollback-journal file with the same `user_version`.
- **Validation:** integrity, foreign keys, a schema version within the range the binary supports, and a Polaroid catalog (it has the `procedures` table).
- **`backup.json`:**
  - format `polaroid-backup`, `format_version` 1;
  - `created_at`;
  - `reason` (`backup` or `pre-restore`);
  - the source: its absolute path, whether it is managed, and the host;
  - `schema_version`;
  - the Polaroid build;
  - the database file's name, size and SHA-256;
  - row counts per table.
- **Publication:** the staging directory is renamed to `polaroid-<UTC time>[-<reason>][-N]`. The name is new: rename refuses an existing non-empty directory on Linux and macOS, so a published backup is never overwritten. Staging is removed on every ordinary failure. A backup is reported successful only after the published copy reads back valid.

**`polaroid inspect-backup BACKUP`** opens the database read-only and immutable, so it creates no sidecar and changes nothing. It reports problems and never repairs. It checks:
- the metadata, its format and its version;
- the database file, its size and its SHA-256;
- integrity and foreign keys;
- that the actual `user_version` equals the recorded one;
- schema compatibility.

Exit status: 0 valid, 1 invalid.

**Schema compatibility.** A backup with schema *N* restores with a binary that supports *M* (the number of embedded migrations) only if 1 ≤ *N* ≤ *M*. If *N* < *M*, the restore migrates the staged copy explicitly, and the plan says so. A destination whose schema is newer than *M* is refused, because replacing it would be an implicit downgrade. Inspection never migrates.

**`polaroid restore [-db FILE] [-dir DIR] [-plan] [-replace] [-wait D] BACKUP`**
- **The plan:** `-plan` prints the plan and changes nothing:
  - the backup report and the destination (whether it exists, its schema, its sidecars, whether it is managed);
  - the service state and any migration;
  - the recovery-backup directory, the actions, and the blockers.

  Exit status: 0 when the restore could run, 3 when it is blocked.
- **Refusals (exit 3, nothing changed):**
  - an invalid backup, an incompatible schema, or an existing catalog without `-replace`;
  - for the managed catalog, a service that is neither `running` nor `stopped` (for example `failed`, `starting`, or an endpoint held by another process);
  - for any other path, a `-wal` or `-shm` file beside the destination.
- **Offline boundary:**
  - The managed catalog is taken offline through the lifecycle commands: the service is stopped with its ownership checks, and restarted afterwards if it was running.
  - Any other catalog must already be offline. A `-wal` or `-shm` file means that a connection is open or that one ended uncleanly. That check is advisory: it cannot stop a process from opening the file during the restore, and stopping such processes is the operator's responsibility.
  - Sidecars are never deleted. If they are still present after the managed service stops, the restore aborts.
- **Order of operations:**
  1. Copy the backup into a private `0700` staging directory beside the destination, check its SHA-256, migrate it if needed, and validate it.
  2. Stop the managed service if it is running.
  3. Take a `pre-restore` backup of the existing catalog into `DIR` and validate it. If that fails, nothing is replaced.
  4. Hard-link the current catalog to `<db>.pre-restore-<time>`.
  5. `rename` the staged `0600` file over the destination, which is atomic on one filesystem, and sync the directory. A new destination is published with `link`, which never overwrites a file that appears meanwhile.
  6. Start the service again if it was running, and wait until it is healthy.
  7. On success, remove the hard link. The recovery backup stays.
- **Failures:**
  - Any failure before step 5 leaves the catalog unchanged. The service is restarted if this run stopped it.
  - If the restart fails, the restored file is kept as `<db>.failed-restore-<time>`. The original is renamed back, and the service is started again. The command reports the original, the failed copy and the recovery backup.
- **History:** restoring replaces everything written after the snapshot. Those records survive only in the recovery backup. Records are never merged, and stored rows are never edited.

## Consequences

- Backups and restores need only the packaged binaries. `make lifecycle`, the end-to-end scripts and the packaged smoke test exercise them.
- `polaroid` grows by the SQLite driver.
- **Crash recovery:** an interruption leaves recoverable states, never a mixed catalog.
  - Before step 5: the catalog is unchanged, and a staging directory may be left behind.
  - After step 5: the restored catalog is in place, the original is kept as `<db>.pre-restore-<time>`, and the recovery backup exists. The service may be stopped; run `polaroid start`.
  - Leftover `.staging-*` and `.polaroid-restore-*` directories are safe to delete.
- **Durability:** files and directories are synced. On Linux that survives power loss on common filesystems. On macOS, `fsync` does not force the drive's cache (`F_FULLFSYNC` is not used), so a power loss shortly after a backup or restore may lose it.
- **Not provided:** concurrent restores of one catalog are not coordinated, and the advisory offline check is the only guard for unmanaged paths.
- **Not part of this decision:** schedules, retention, remote copies, and listing backups. They need decisions of their own.
