# 0025. The default database is per-user, in ~/.polaroid

**Status:** Accepted. Supersedes the default database path of the original daemon configuration.
**Date:** 2026-10-10

## Context

Without `-db` or `POLAROID_DB`, `polaroidd` opened `polaroid.db` in its working directory. Which catalog it served therefore depended on where it was started, and the shared development catalog ended up in `bin/dogfood/`, inside the build output that `make clean` removes ([#41](https://github.com/ashuangiras/polaroid/issues/41)). A catalog is long-lived, append-only history; it must not live with disposable binaries or depend on the shell's current directory.

The SQLite driver creates new database files with mode `0644` less the umask, and gives the `-wal` and `-shm` files the database file's mode (observed with modernc.org/sqlite under umask `022`).

## Decision

- **Default location.** Without `-db` or `POLAROID_DB`, the database is `<home>/.polaroid/data/polaroid.db`, where `<home>` is Go's `os.UserHomeDir` (`$HOME` on Linux and macOS). Precedence stays `-db` > `POLAROID_DB` > default. An explicit path, relative or absolute, means what it meant before: a relative one is relative to the working directory.
- **No silent fallback.** `-db ""` and a set but empty `POLAROID_DB` are usage errors. A home directory that cannot be determined, or is not absolute, is a startup error that names `-db` and `POLAROID_DB` as the remedy. The daemon never falls back to the working directory.
- **Layout.** `~/.polaroid/data/polaroid.db` and `~/.polaroid/backups/`. Nothing else is created: no configuration file, log directory or placeholder. `backups/` is created only when a backup is written. Binaries are never placed in `~/.polaroid`.
- **Private on POSIX.** For the default location only, the daemon creates missing directories with mode `0700` and, if the database file is missing, creates it empty with mode `0600` before SQLite opens it, so SQLite's `-wal` and `-shm` files are `0600` too. It never changes the mode of an existing directory or file, and never of a user-supplied path. Existing default-home entries that grant group or other access are reported with a warning at startup. Supported platforms are Linux and macOS; on other platforms the location is used, but no mode is promised.
- **Diagnostics.** The startup line logs the absolute database path and its source: `flag`, `environment` or `default`.
- **No automatic migration.** The daemon does not look for, merge or move older databases. An existing catalog is moved once, deliberately, with `scripts/migrate-catalog.sh`: a SQLite online backup (which includes committed WAL content) into `backups/`, an integrity check, a restore into the private destination, and a refusal to replace an existing destination. Source and backup are kept.
- **Data outlives the installation.** Build cleanup, reinstalling and upgrading binaries never touch `~/.polaroid`. A future uninstall operation must preserve it unless deletion is explicitly requested.
- **No schema change.** The database format and contents are unchanged.

## Consequences

- Starting `polaroidd` without `-db` or `POLAROID_DB` now serves the per-user catalog instead of `./polaroid.db`. To keep using an older database, pass `-db PATH` or set `POLAROID_DB=PATH`, or migrate it once with the script.
- One user's daemons share one catalog by default, wherever they are started. Tests, the demo and the end-to-end scripts always pass a temporary database and run the daemon with a temporary `HOME`, so they cannot reach the real catalog.
- An environment without a usable home directory (some service managers) must pass `-db` or `POLAROID_DB`.
