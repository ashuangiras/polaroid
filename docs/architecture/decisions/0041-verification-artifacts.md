# 0041. Verification artifacts

**Status:** Accepted.
**Date:** 2026-10-12

## Context

`dev.change.verify` answers one question: did this exact commit pass its gate? Nothing it produces is runnable by someone who was not the agent that ran the gate. Three ways an independent party could get something to run are each heavier than every change needs: an official release ([ADR-0028](0028-prerelease-verification-builds.md), tag required); hosted CI ([ADR-0031](0031-scoped-verification-and-local-only-ci.md), a pushed commit and a repository that opts in); or asking them to clone and build it themselves, which assumes a Go toolchain and loses the exact provenance (commit, toolchain, modified-tree) `scripts/package.sh` already checks for a release.

A fourth, lighter option already exists in substance: `scripts/build.sh` builds and reuses Polaroid's own binaries by source state, and `scripts/package.sh` cross-compiles, checksums and writes a manifest, both for a release tag. Reusing that mechanism without the tag requirement, and writing the result outside the repository (`/dist/`, already ignored), gives an independent reviewer exactly what they need: the binaries built from the verified commit, and a manifest to confirm the provenance, in local path or at an agreed-upon transfer location, without implying a release.

## Decision

Completing a Go code change that affects Polaroid's executable behaviour produces a **verification artifact**: cross-compiled binaries from the exact verified commit, with a manifest, under `dist/artifacts/<commit>/` (already covered by `.gitignore`'s `/dist/`).

- **Not a release.** No tag, no `GITHUB_SERVER_URL`/`GITHUB_RUN_ID` provenance, no GitHub Release, no upload, no signing. `scripts/package.sh` and `polaroid.release.publish` are unchanged and remain the only path to an official, fully cross-platform, versioned release.
- **Targets.** Linux AMD64 by default, for portable third-party execution without the reviewer's own toolchain assumptions matching the builder's. The local OS/ARCH (for example Darwin ARM64) is added when local testing is useful; a caller names its own target list.
- **Manifest (`manifest.json`, beside the binaries).** Repository, the full commit, the Go toolchain, and, per target, OS, architecture, each executable's name and SHA-256, and the build command. A `verification` member separates what this step itself claims from what it never claims:
  - `local_gate`: a reference to the verification evidence (the `dev.change.verify`, or caller, execution that verified this exact commit) this artifact was built for. Never generated if the commit was not already verified.
  - `independent_review`: always `{"status": "not_reviewed"}` when this artifact is built. Building it never sets this to anything else; only a separately recorded review would, and nothing in this decision defines that record.
- **Idempotent.** An existing artifact under `<output_dir>/<commit>/` whose manifest names the same targets and build command, and whose listed checksums still match the files on disk, is reused, not rebuilt. A mismatch (a different target list, a different build command, or a checksum that no longer matches) is a different build and is not reused.
- **Procedure, not code.** The method is a new shared Polaroid procedure, `go.module.artifact`, parameterized like `go.module.build` (module packages, targets, output directory, the working tree, all inputs). `dev.change.verify` references it conditionally, applicable only when the effective inputs name `module_packages`: a caller that never sets that input (every binding except `verify-change`, as of this decision) never attempts it, and no existing binding's behaviour changes. The reference runs last, after `build`, `checks` and `hosted-ci`, consistent with those already running only for an applicable, not-yet-failed run.
- **The `verify-change` binding opts in**, naming `module_packages` (`./cmd/polaroid`, `./cmd/polaroidd`), `targets` (`linux/amd64`) and `output_dir` (`dist/artifacts`). A failed build, or a checksum that does not match, is a failed `go.module.artifact` execution, which (per `dev.change.verify`'s existing rule) makes the parent's outcome `failed`: an artifact never backs a change reported as fully complete when it, or anything before it, failed.
- **Evidence only, never the binaries.** The catalog records the `go.module.artifact` execution's evidence (manifest path, each target's checksum, built-or-reused), never the binaries or a build log. An independent reviewer is pointed at the manifest's path (or wherever it was transferred), not at a record inside Polaroid.

## Consequences

- Reviewing a change no longer requires trusting the agent's report of what the gate did, or waiting for a release: the same commit that was verified can be run, by binary, by anyone with the artifact.
- Still distinct, by design: a built artifact is not an independent review (`independent_review.status` stays `not_reviewed` until someone records one), and an independent review is not an official release (that stays `polaroid.release.publish`, requiring a tag).
- `dev.change.verify`'s contract is otherwise unchanged: every binding that does not name `module_packages` runs exactly as it did under version 4.

## Addendum: PR #130 review (#129)

- **Handoff, outside the local machine.** `.github/workflows/artifact.yml`, dispatched by hand (never on push or a pull request, consistent with the local-only hosted-CI policy this never gates): one job runs `make artifact` and hands the result off as a workflow-run artifact, a real downloadable URL requiring no release, tag or deployment; a second job retrieves it as an independent consumer would (a separate runner, nothing carried over from the build), validates every checksum against the manifest, inspects each binary's embedded `vcs.revision`/`vcs.modified`, and smoke-tests it (`polaroidd -version`, `polaroid version`).
- **No accidental skip.** `dev.change.verify` v7 makes the `artifact` reference's decision accountable: deciding it `not_applicable` for a change that affects executable behaviour now needs a stated, affirmative reason the change is non-executable; `module_packages`'s bare absence from a binding is never that reason by itself. `polaroid.change.focused` v3 (and the `verify-focused` binding) now set `module_packages` too: a focused code change is still an executable change, and previously never built an artifact at all. A non-executable change (docs, fixtures, records) is unaffected.
- **Toolchain-aware reuse.** `scripts/build-artifact.sh`'s reuse check also compares the manifest's recorded Go toolchain (`go env GOVERSION`) against the current one; a mismatch, like a different target list, a different build command or a checksum that no longer matches, forces a rebuild. `scripts/check-artifact.sh` (`make artifact-check`) is a targeted regression test for all four.

