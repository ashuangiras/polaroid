# 0038. Stable releases and the compatibility policy

**Status:** Accepted.
**Date:** 2026-10-11

## Context

ADR-0028 published only prereleases (`vX.Y.Z-label`) "until a compatibility policy exists". Polaroid is now proposed for its first stable release, for the local, single-user scope defined in [docs/development/release-readiness.md](../../development/release-readiness.md). A stable release needs two things: a promise users can rely on, and a release pipeline that can publish it. Until now, every prerelease has said "no compatibility promises", and the pipeline refused tags without a label.

## Decision

**Version numbers.** Releases use semantic versions.

- `vMAJOR.MINOR.PATCH` is a stable release. The first one is `v1.0.0`.
- `vMAJOR.MINOR.PATCH-label` is a prerelease: release candidates `-rc.N`, and verification builds `-verify.N`.
- Prereleases make no compatibility promise of their own.

**What major version 1 keeps compatible.** Within `v1`, a client written against `v1.0.0` keeps working with every later `v1.x.y`:

- **HTTP.** The `/v1` endpoints, their request members and rules, their response members and their status and error codes ([http-api.md](../http-api.md)).
- **MCP.** Tool names, argument names and types, result shapes (the HTTP bodies) and error codes ([mcp.md](../mcp.md)). The protocol revisions the server negotiates are listed there; support for a revision is removed only in a new major.
- **CLI.** Command names, arguments, `NAME=VALUE` parameters, exit statuses (0, 1, 2, and those of the local commands) and the JSON it prints. That JSON is the HTTP body, or the documented result of a local command.
- **Records.** The record contracts in [records.md](../records.md): identities, immutable history, verification and selection rules. Stored records are never rewritten.
- **Catalog files.** A `v1.x.y` build opens every catalog written by an earlier `v1` build, or by a `v0.x` build at schema 10 or lower, and migrates it forward at start-up. `polaroid backup` writes backup format 1; every `v1` build inspects and restores it.

**What a minor or patch release may change.**

- **Minor.** Additions: endpoints, tools, commands, optional request members, response members, error codes for new operations, statistics members (`statistics_version` stays 1), and migrations that add tables or columns. Clients must ignore response members they do not know, as http-api.md already requires.
- **Patch.** Fixes only. A fix may refuse input that the documented contract never allowed. The release notes then name the rule. No patch release adds a migration.
- **Write limits** ([ADR-0037](0037-size-limits-on-new-writes.md)) are never lowered within a major. They may be raised in a minor. Every change increments `write_limits.policy_version` and is named in the release notes. No other tightening of accepted input happens within a major, except the patch fixes above.
- **Removal or a changed meaning** of anything listed above needs a new major. Deprecations are announced in release notes and in the documentation at least one minor release before any removal.

**Migrations and rollback.**

- Migrations are forward-only and run automatically at start-up, as they do now. `polaroid restore` migrates an older backup on its staged copy (ADR-0030).
- Every stable release's notes state the schema version, whether the release migrates, and from which earlier release the binaries can roll back.
  - **No schema change:** the previous release's binaries serve the same catalog, so `polaroid install -from ~/.local/state/polaroid/previous` is a rollback.
  - **Schema change:** older binaries refuse the migrated catalog. Recovery is fix-forward, or a deliberate restore of the pre-upgrade backup with `polaroid restore`, which loses later writes.
- Back up before every upgrade with `polaroid backup`.

**Not covered by the promise:**

- the Go packages under `internal/` (there is no Go API);
- log lines and their format;
- the `-service-name` flag (meant for isolated checks);
- the development scripts and the example records in `examples/`;
- the wording of human-readable `message` texts (branch on `code`);
- the ranking scores of discovery and duplicate suggestions, which may be tuned in a minor (their response members are stable).

**The release pipeline.**

- The release workflow publishes a tag `vX.Y.Z` on `main` as a stable release, marked as the latest release. It publishes a tag `vX.Y.Z-label` as a prerelease, never marked latest.
- Both kinds get the same packaging and native archive tests, the same publish-then-retest, and the same never-overwrite rule.
- `build-manifest.json` keeps format 1. Its `prerelease` member is `false` for a stable release.
- The archive's guide and the release notes describe the release kind and the supported scope.

## Consequences

- Users get one promise: within `v1`, anything that worked keeps working, and upgrades only add. A breaking change needs `v2` and a new API prefix, as http-api.md already says.
- ADR-0028's "only prereleases are published" is superseded. Its packaging, testing and never-overwrite rules still hold.
- Write-limit tightening (ADR-0037 at `v0.7`) is no longer possible within `v1`.
- The `/v1` HTTP prefix and major version 1 coincide by choice. They are separate promises: a `v2` release could keep `/v1`.
- Go embeds a tag as the build's version only when the tag fits the module path. A `v2.0.0` tag needs the module path to end in `/v2`. Without that, the binaries report a pseudo-version and `scripts/package.sh` refuses them. Changing the module path is therefore part of any `v2`.
