# Polaroid

This archive holds a release of [Polaroid](https://github.com/ashuangiras/polaroid), a versioned procedural memory service for agents. `./polaroid version` prints which release it is.

- **Stable releases (`vX.Y.Z`)** are supported for the scope below and follow the [compatibility policy](https://github.com/ashuangiras/polaroid/blob/main/docs/architecture/decisions/0038-stable-releases-and-compatibility.md): within major version 1, later releases only add.
- **Prereleases (`vX.Y.Z-label`)** let a reviewer test the exact build of one commit, and make no compatibility promise.

Binaries are not signed or notarized.

**Supported scope.**

- One user on one machine runs the service on loopback (`127.0.0.1`). Agents and people on that machine use it through MCP, the `polaroid` command-line client and the JSON HTTP API.
- Several local agents may use it at the same time.
- There is no authentication, authorization or TLS. Do not expose it on a network or share it between users.
- Details and known limitations: [release readiness](https://github.com/ashuangiras/polaroid/blob/main/docs/development/release-readiness.md).

It contains:

| File | What it is |
| --- | --- |
| `polaroidd` | The daemon: JSON HTTP API and MCP server on one loopback listener, SQLite storage |
| `polaroid` | The command-line client, and the local `install`/`status`/`backup`/… commands |
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

- **Needs:** a POSIX shell at `/bin/sh`, `curl`, and `mktemp`, `mkdir`, `rm`, `ls`, `cat`, `sed`, `grep`, `head`, `sleep` (fractional seconds) and `dirname`. No Go, no `sqlite3`, no source checkout, no root and no service manager. It runs on Linux without systemd, for example in a minimal container.
- **What it does:** it starts `polaroidd` directly with an explicitly named database in a new temporary directory, on `127.0.0.1` with a free port chosen by the kernel, and with `HOME` set to an empty temporary directory. Then it checks:
  - health;
  - procedure creation, retrieval and revision;
  - that a revision from a stale base is rejected with `version_conflict`;
  - that the procedure reads back unchanged after a restart;
  - one MCP `tools/call` over HTTP (protocol 2026-07-28, with the `Mcp-*` headers and `_meta` the protocol requires);
  - a conditional reference: a run that skips it with a recorded rationale is verified, a run without a decision is not, skipping a required reference is refused, and a target that decides the work applies is not verified by the skip;
  - backup and restore: `polaroid backup` of the running catalog into the temporary directory, `inspect-backup`, a restore refused while `polaroidd` has the catalog open, `restore -plan`, a restore refused without `-replace`, `restore -replace` after stopping the daemon, the snapshot read back through the API (a later record is gone, earlier history and verifications are intact), a valid recovery backup, and a truncated backup rejected by `inspect-backup` and `restore`;
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

## 4. Install as a per-user service

`polaroid install` runs Polaroid as a per-user service that starts at login and restarts after a crash, without root:

- on macOS, a launchd LaunchAgent, which needs a GUI login session;
- on Linux, a systemd user service, which needs a reachable user manager (`systemctl --user show-environment` must succeed).

Where neither is available, `install` says so and exits non-zero; run `polaroidd` directly there.

```sh
./polaroid install -from .                  # copies both binaries to ~/.local/bin, registers and starts the service
export PATH="$HOME/.local/bin:$PATH"        # add this line to your shell's startup file yourself
polaroid status                             # JSON; exit 0 when running and healthy
```

- The service runs `~/.local/bin/polaroidd -addr 127.0.0.1:7417 -db ~/.polaroid/data/polaroid.db`.
- `install` reports `running` only when the managed process owns the endpoint and is healthy. Another process on the port is reported as a conflict and never claimed.
- `polaroid stop`, `start`, `restart` and `status` operate it. `polaroid uninstall` unregisters it and removes the binaries, and keeps `~/.polaroid` (the catalog and its backups).
- Diagnostics:
  - macOS: `~/Library/Logs/Polaroid/polaroidd.log`;
  - Linux: `journalctl --user -u polaroid.service`.

The [service documentation](https://github.com/ashuangiras/polaroid#run-polaroid-as-a-service) has the details.

**An isolated trial** registers a real service, but with a temporary `HOME`, a separate service name and a separate port, so it never touches a real installation:

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

## 5. Use it

**From an agent (MCP).** Point any MCP client that supports streamable HTTP, with protocol 2026-07-28 or 2025-11-25, at `http://127.0.0.1:7417/mcp`. In VS Code, add this to a workspace's `.vscode/mcp.json`:

```json
{"servers": {"polaroid": {"type": "http", "url": "http://127.0.0.1:7417/mcp"}}}
```

The server's instructions describe the agent loop: discover a procedure, resolve it, follow it, record the run, improve the procedure. The client lists 38 tools; each tool's description and argument schema state the formats it accepts. A client that was connected before an upgrade keeps its old tool list: restart its connection to the server (in VS Code, **MCP: List Servers**, then the server, then **Restart Server**), and start a new agent session.

**From the command line.**

```sh
polaroid health
polaroid statistics                                          # record counts, payload sizes, storage, write limits
polaroid discover 'publish a prerelease' repository=github.com/owner/repo
polaroid help                                                # every command
```

The CLI prints each response as JSON, and exits 0 on success, 1 when a request fails and 2 for a usage error. The API is documented in [http-api.md](https://github.com/ashuangiras/polaroid/blob/main/docs/architecture/http-api.md), and the MCP tools in [mcp.md](https://github.com/ashuangiras/polaroid/blob/main/docs/architecture/mcp.md).

## 6. Back up, upgrade and roll back

```sh
polaroid backup                                              # consistent while the service runs; into ~/.polaroid/backups/
polaroid inspect-backup ~/.polaroid/backups/polaroid-<UTC time>
```

- **Upgrade.** Back up first. Then verify and extract the new archive and run `./polaroid install -from .` in its directory. The catalog stays where it is. A build that adds a schema migration applies it at start-up; the release notes say whether a release migrates.
- **Going back to the previous build:** `polaroid install -from ~/.local/state/polaroid/previous`. This is binary recovery: it keeps the current catalog. It works when the schema did not change. An older `polaroidd` refuses a catalog that a newer build migrated.
- **Restoring a backup** is a separate operation that loses the records written after it. Polaroid never does it automatically.
  1. Plan it with `polaroid restore -plan BACKUP`.
  2. Run `polaroid restore -replace BACKUP`. It keeps a recovery backup of the current catalog and stops and restarts the managed service. It puts the original back if the restored catalog does not start and the failed daemon is confirmed stopped. Otherwise it changes nothing more and reports `rollback-blocked`, with the recovery steps ([details](https://github.com/ashuangiras/polaroid#back-up-and-restore-the-catalog)).

  Rehearse it first on a copy with `-db /tmp/copy.db`.
- **Growth.** History is kept forever, and nothing prunes it:
  - Keep evidence short and link to logs.
  - Watch the catalog with `polaroid statistics`.
  - Backups are full copies that you keep or delete yourself.

  See the [storage policy](https://github.com/ashuangiras/polaroid/blob/main/docs/development/release-readiness.md#storage-growth).

## Limitations

- Unsigned and not notarized.
- The managed service supports macOS (launchd) and Linux (systemd user services) only. Other platforms run `polaroidd` directly.
- The HTTP API and MCP server have no authentication. They are meant for one user on loopback (the managed service refuses other addresses).
- Nothing prunes history; retiring a procedure or invalidating an execution changes its standing, not the database's size.
