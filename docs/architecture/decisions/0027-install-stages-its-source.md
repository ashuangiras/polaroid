# 0027. Install stages its source first; binary recovery is separate from data restore

**Status:** Accepted. Amends the installation and upgrade parts of [ADR-0026](0026-per-user-installation-and-managed-service.md).
**Date:** 2026-10-10

## Context

ADR-0026 keeps the replaced installation in `~/.local/state/polaroid/previous/`, and the documented recovery is `polaroid install -from ~/.local/state/polaroid/previous`. `install` read and hashed the source, then stopped the service and rotated `previous/`, and only then copied the source. With `previous/` as the source, the rotation replaced the recovery binaries with the installed (failed) build before they were copied. The "recovery" reinstalled the failed build, and the record named the build that had been validated, not the bytes installed ([#45](https://github.com/ashuangiras/polaroid/issues/45), reproduced by tests and on real launchd). The rotation also deleted `previous/` before writing its replacement, so a failure there lost the recovery copy.

ADR-0026 also described recovery as "restore a backup, then reinstall from `previous/`", which mixes two different operations.

## Decision

- **Stage first.** Before it stops the service, rotates `previous/` or writes any installed file, `install` copies `polaroid` and `polaroidd` from `-from` into a new private temporary directory (`0700`, under `TMPDIR`). Validation (`-version`, the matching pair), the SHA-256s in the record and the installed files all come from those staged bytes. The staging directory is removed when `install` returns, on success or failure. A source inside `previous/`, or a symlink resolving there, is therefore safe.
- **Rotate `previous/` without a gap.** The new recovery copy is written completely beside `previous/` and then renamed into place. If writing it fails, the old copy is left as it was, and the running installation is restarted unchanged.
- **Binary recovery keeps the catalog.** `install -from previous/` reinstalls the previous build on the current catalog, with every record written since. An older `polaroidd` refuses a catalog whose schema a newer build has upgraded (`database schema version N is newer than this build supports`). Then the service reports `failed`, and the way out is to fix forward.
- **Data restore is a separate, deliberate operation.** Restoring a catalog backup loses everything written after the backup. `install` never restores, downgrades or switches catalogs, also not after a failed upgrade. A different, older catalog (such as the pre-ADR-0025 `bin/dogfood/polaroid.db`) is never a substitute for binary recovery.
- The daemon's start-up line also names the build's revision (`revision=`), so that logs show which build a manager started.

## Consequences

- Ownership checks, staged renames and the rollback of a failed replacement are unchanged.
- `install` needs `TMPDIR` to allow execution, since validation runs the staged copies. A `noexec` temporary directory makes `install` fail before anything changes; point `TMPDIR` elsewhere.
- `make lifecycle` now builds three revisions and exercises recovery against the real manager. That includes recovery after an upgrade that fails to start, and recovery through a symlink whose path has spaces.
