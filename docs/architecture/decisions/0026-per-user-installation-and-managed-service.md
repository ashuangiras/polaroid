# 0026. Per-user installation and a managed service: launchd on macOS, systemd user services on Linux

**Status:** Accepted. Extends [ADR-0025](0025-per-user-default-database.md). Installation staging and recovery amended by [ADR-0027](0027-install-stages-its-source.md).
**Date:** 2026-10-10

## Context

Since ADR-0025 the catalog lives in `~/.polaroid`, but the shared instance still ran from a binary in the source checkout, started by hand from a shell. Nothing restarted it after a crash or at login, and an upgrade or `make clean` could remove the binary it ran from ([#43](https://github.com/ashuangiras/polaroid/issues/43)). The service managers that run per-user background services are launchd agents on macOS and systemd user services on Linux. Both are documented for unprivileged use.

## Decision

- **Local lifecycle commands.** `polaroid install`, `start`, `stop`, `restart`, `status`, `uninstall` and `version` are local operations. They do not call the API, and they work while the daemon is stopped. Their code is in `internal/lifecycle`, which depends on neither the domain, storage nor transports.
- **Installation.**
  - `install -from DIR` copies the `polaroid` and `polaroidd` built in `DIR` to `~/.local/bin`. There is no guessed or downloaded source.
  - Both binaries must report the same embedded VCS revision. The build's `polaroidd -version` and `polaroid version` print it.
  - The installation record is `~/.local/state/polaroid/install.json`, with the build and the SHA-256 of every installed file. The previous installation is kept in `~/.local/state/polaroid/previous/`.
  - A binary or service definition at an install path that the record does not account for is never overwritten.
  - Files are staged next to their targets and renamed into place. If replacement or registration fails, the previous files are restored.
  - Repeating an install of the same build changes nothing. It only ensures that the service is registered and running.
  - `install` registers login startup and starts the service, then reports whether it became healthy.
- **The service.** The definition runs `~/.local/bin/polaroidd` by absolute path with explicit `-addr` and `-db`. The defaults are `127.0.0.1:7417` and `~/.polaroid/data/polaroid.db`, and the definition sets `HOME` itself. It does not rely on an inherited `PATH`, `POLAROID_DB` or working directory. Non-loopback addresses are refused (ADR-0006). Overrides are `install -addr` and `install -db`, recorded in the definition.
  - **macOS:** `~/Library/LaunchAgents/io.github.ashuangiras.polaroid.plist`, in the user's GUI login domain.
    - `RunAtLoad` starts it at login. `KeepAlive` with `SuccessfulExit` false respawns it only after an unsuccessful exit or a signal. `ThrottleInterval` 10 allows at most one spawn every 10 seconds. `ExitTimeOut` 20 gives graceful shutdown time, and `Umask` is 077.
    - `start` is `launchctl bootstrap`. `stop` is `launchctl bootout`, which sends SIGTERM; the job is unloaded until the next login or `start`, so it cannot respawn.
    - launchd bounds the retry rate, not the count.
  - **Linux:** `~/.config/systemd/user/polaroid.service`, enabled for `default.target`.
    - `Restart=on-failure` with `RestartSec=2`, bounded by `StartLimitBurst=5` within `StartLimitIntervalSec=60`. `TimeoutStopSec=20` and `UMask=0077`.
    - `stop` is `systemctl --user stop`. systemd never restarts a unit after a stop it performed, nor after an exit with status 0 or SIGTERM.
  - **Startup scope:** login. A LaunchAgent runs only in a GUI login session. A systemd user manager runs from the first login to the last logout. Neither runs before login. Lingering is an administrator's choice that `install` never makes.
- **Diagnostics.**
  - **Linux:** the journal (`journalctl --user -u polaroid.service`). Retention is the journal's own bounded configuration.
  - **macOS:** `polaroidd -log-file` writes to `~/Library/Logs/Polaroid/polaroidd.log` (directory `0700`, file `0600`). Its standard output and error, so panics too, are redirected there. It is rotated to `polaroidd.log.1` at 4 MiB, so at most two files are kept. launchd's own output goes to the same file.
- **Status and ownership.** A health probe alone proves nothing about ownership.
  - The managed instance is the process the manager reports (`launchctl list`, whose three-column form is documented, or `systemctl --user show`) when it is also the process listening on the endpoint (`lsof` on macOS, `/proc` on Linux).
  - A different listener is reported as a conflict, with its PID and command. It is never stopped or claimed.
  - States and exit codes for `status`:

    | State | Exit |
    | --- | --- |
    | `running`: owned and healthy | 0 |
    | `failed`: unreachable, conflicting, crashed or last start failed | 1 |
    | `stopped`: installed but stopped | 3 |
    | `not-installed` | 4 |
    | `starting` | 5 |

    Exit 2 stays a usage error. Every lifecycle command prints the resulting status as JSON on stdout.
  - `start`, `restart` and `install` wait, bounded by `-wait` (default 30s), for the owned and healthy state. If it does not arrive, they stop the service, so neither manager keeps retrying a broken start, and they report `failed` with the diagnostics location. `start` refuses while another process holds the endpoint.
- **Upgrades.** Installing a different build over a running service does the following, in order:
  1. Stop the service gracefully.
  2. Keep the current installation in `previous/`.
  3. Replace both binaries.
  4. Start the service and verify that it is healthy.

  CLI and daemon therefore never run as a mismatched pair. A failed start does not roll back automatically: the new `polaroidd` may already have migrated the database, and an older `polaroidd` refuses a newer schema. Recovery is to fix forward, or to restore a backup taken before the upgrade and then reinstall from `previous/`. Polaroid never claims that an older binary can open a database migrated by a newer one.
- **Uninstall.** Uninstall does the following:
  1. Stop and unregister the service.
  2. Remove the definition, both binaries (when they still match the record), the macOS log files and `~/.local/state/polaroid`.
  3. Leave everything else in place, including `~/.polaroid` (data and backups).

  There is no purge.
- **The default database is the default wherever it is named.** `polaroidd` applies ADR-0025's private creation (`0700` directories, `0600` files) to `~/.polaroid/data/polaroid.db` also when it is passed explicitly, as the service definition does.
- **Isolation for validation.** `install -service-name` gives a separately named service. It is used, with a temporary `HOME`, port and database, by the real-manager check (`make lifecycle`). Tests never touch the user's installation or catalog.

## Consequences

- Polaroid runs with no source checkout, Go or Make. Binaries come only from a directory the operator names.
- macOS restarts are rate-bounded but not count-bounded. A daemon that keeps crashing after a successful start is respawned every 10 seconds until stopped, and its log stays bounded.
- An upgrade that fails to start leaves the service stopped, not running the old build. That is deliberate, because of the schema boundary.
- Other platforms, and sessions without a reachable manager (no GUI login on macOS, no user manager on Linux), get an error that says so and suggests running `polaroidd` directly.
