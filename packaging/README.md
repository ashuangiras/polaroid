# Polaroid verification build

This archive is a **prerelease verification build** of [Polaroid](https://github.com/ashuangiras/polaroid), a versioned procedural memory service for agents. It is meant for an independent reviewer to test the exact reviewed source. It is **not a stable production release**: there are no compatibility promises between prereleases, and the binaries are not signed or notarized.

It contains:

| File | What it is |
| --- | --- |
| `polaroidd` | The daemon: JSON HTTP API and MCP server on one loopback listener, SQLite storage |
| `polaroid` | The command-line client, and the local `install`/`status`/… commands for a per-user service |
| `smoke-test.sh` | A repeatable smoke test that installs nothing (below) |
| `README.md` | This guide |

Both binaries are built by GitHub Actions from one clean commit, for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64. They are statically linked and need no Go toolchain or source checkout. `build-manifest.json`, published beside the archives, names the full commit, the version, the Go toolchain and every asset.

## 1. Verify the download

Download the archive for your platform and `SHA256SUMS` from the same release, then:

```sh
sha256sum -c --ignore-missing SHA256SUMS          # Linux
shasum -a 256 -c --ignore-missing SHA256SUMS      # macOS
tar -xzf polaroid_<version>_<os>_<arch>.tar.gz
cd polaroid_<version>_<os>_<arch>
```

On macOS, files downloaded with a web browser are quarantined, and Gatekeeper blocks unsigned binaries. Files downloaded with `curl` are not quarantined. If macOS refuses to run them, run `xattr -d com.apple.quarantine polaroid polaroidd` in this directory.

## 2. Check the build

```sh
./polaroid version
./polaroidd -version
```

Each prints one JSON object: `name`, `version` (the release tag), `revision` (the full source commit), `modified` (`false` for a release), the commit `time` and `go`. Both must report the same `revision`. It must equal `source.commit` in `build-manifest.json` and the commit named in the release notes. Browse that source at `https://github.com/ashuangiras/polaroid/tree/<revision>`.

## 3. Smoke test (installs nothing)

```sh
./smoke-test.sh
```

- **Needs:** a POSIX shell at `/bin/sh`, `curl`, and `mktemp`, `mkdir`, `rm`, `ls`, `cat`, `sed`, `grep`, `head`, `sleep` (fractional seconds) and `dirname`. No Go, no source checkout, no root and no service manager. It runs on Linux without systemd, for example in a minimal container.
- **What it does:** it starts `polaroidd` directly with an explicitly named database in a new temporary directory, on `127.0.0.1` with a free port chosen by the kernel, and with `HOME` set to an empty temporary directory. Then it checks:
  - health;
  - procedure creation, retrieval and revision;
  - that a revision from a stale base is rejected with `version_conflict`;
  - that the procedure reads back unchanged after a restart;
  - one MCP `tools/call` over HTTP (protocol 2026-07-28, with the `Mcp-*` headers and `_meta` the protocol requires);
  - that all data stayed in the temporary directory.
- **Result:** it prints `PASS`/`FAIL` per check, `smoke test: passed=N failed=M`, and exits 0 only when everything passed. It removes its temporary directory afterwards; set `KEEP=1` to keep it.
- **What it leaves alone:** it never registers a service and never reads or writes `~/.polaroid`.
- **What it does not test:** passing it says nothing about the managed service. That is the next section.

To run the daemon by hand instead, always name a database:

```sh
./polaroidd -addr 127.0.0.1:7500 -db /tmp/polaroid-review/polaroid.db
./polaroid -server http://127.0.0.1:7500 health
```

Without `-db` (or `POLAROID_DB`), `polaroidd` serves the per-user catalog `~/.polaroid/data/polaroid.db`.

## 4. Managed-service test (separate, optional)

`polaroid install` runs Polaroid as a per-user service: a launchd LaunchAgent on macOS (needs a GUI login session), or a systemd user service on Linux (needs a reachable user manager: `systemctl --user show-environment` must succeed). Where neither is available, `install` says so and exits non-zero. Managed testing is then unavailable on that host; the smoke test above still applies.

This test registers a real service with your user's service manager. To keep it isolated from any real installation, use a temporary `HOME`, a separate service name and a separate port:

```sh
H=$(mktemp -d)
HOME=$H ./polaroid install -from . -addr 127.0.0.1:27417 -service-name polaroid-review
HOME=$H "$H/.local/bin/polaroid" status            # JSON; exit 0 when running and healthy
curl -s http://127.0.0.1:27417/healthz
HOME=$H "$H/.local/bin/polaroid" restart
HOME=$H "$H/.local/bin/polaroid" stop               # stays stopped
HOME=$H "$H/.local/bin/polaroid" start
HOME=$H "$H/.local/bin/polaroid" uninstall          # unregisters; keeps $H/.polaroid
rm -rf "$H"
```

`install` copies both binaries into `$HOME/.local/bin`, registers the service, starts it and waits until the managed process owns the endpoint and is healthy. `status` reports `running` only in that case. Another process on the port is reported as a conflict and never claimed.

For a real installation, run `./polaroid install -from .` with your normal `HOME`. It serves `~/.polaroid/data/polaroid.db`. Read the [service documentation](https://github.com/ashuangiras/polaroid#run-polaroid-as-a-service) first, and back up any existing catalog before upgrading:

```sh
sqlite3 ~/.polaroid/data/polaroid.db ".backup ~/.polaroid/backups/pre-upgrade.db"
```

- **Going back to a previous build:** `polaroid install -from ~/.local/state/polaroid/previous`. This is binary recovery: it keeps the current catalog, and an older `polaroidd` refuses a catalog whose schema a newer build upgraded.
- **Restoring a backup** is a separate operation that loses the records written after it. Polaroid never does it automatically.

## Limitations

- This is a prerelease for verification: not a stable release, unsigned and not notarized.
- The managed service supports macOS (launchd) and Linux (systemd user services) only. Other platforms run `polaroidd` directly.
- The HTTP API and MCP server have no authentication. They are meant for loopback use (the managed service refuses other addresses).
