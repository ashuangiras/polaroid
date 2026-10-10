# 0028. Prerelease verification builds published from tags on main

**Status:** Accepted. **Amended:** 2026-10-10 by [ADR-0031](0031-scoped-verification-and-local-only-ci.md): the release workflow's dry run is dispatched by hand, not started by pull requests.
**Date:** 2026-10-10

## Context

Until now Polaroid was built only from a checkout (`make build`). An independent reviewer needs to download the exact reviewed build and test it, without Go or the repository ([#46](https://github.com/ashuangiras/polaroid/issues/46)). The binaries reported a commit (`version.Build.Revision`) but no version.

## Decision

- **Versions come from tags.** A build's version is the main module version that Go embeds from the VCS: the tag at the built commit when the tree is clean (for example `v0.1.0-verify.1`), otherwise a pseudo-version, or `(devel)` without VCS information. `polaroid version`, `polaroidd -version` and the installation record report it as `version`, beside the full `revision`. `polaroidd`'s start-up line also names the `version`. There are no linker flags, so a local build at the same tag reports the same version.
- **Only prereleases are published.** The release workflow (`.github/workflows/release.yml`) packages only tags of the form `vX.Y.Z-label` that are on `main`. It runs on a tag push, or by hand for an existing tag. Stable releases are out of scope until a compatibility policy exists.
- **Assets.**
  - `polaroid_<tag>_<os>_<arch>.tar.gz` for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64. Each holds `polaroid`, `polaroidd`, `README.md` (an installation and testing guide) and `smoke-test.sh`.
  - `SHA256SUMS` over every archive.
  - `build-manifest.json`: the full commit, version, Go toolchain, build flags, CI run, and per-target archive and binary hashes.

  `scripts/package.sh` builds both binaries for every target from one clean checkout of the tag, with `CGO_ENABLED=0` and `-trimpath`. It reads each binary's embedded revision, `modified` flag, version and target with `go version -m`, without running it, so cross-compiled binaries are checked too. It refuses binaries that contain the build's paths, and archives that contain anything else. The packaged pair is a matching pair under `install`'s existing check (ADR-0026, ADR-0027).
- **Test the archives, then publish, then test what was published.** Each archive is downloaded from the packaging job, checked against `SHA256SUMS`, extracted and smoke-tested on a native runner of its own OS and architecture: `ubuntu-latest`, `ubuntu-24.04-arm`, `macos-15-intel` and `macos-latest`. Each test runs with no checkout, no Go on `PATH` and an empty `HOME`. The linux/amd64 archive is also tested in an Alpine container without systemd, where `polaroid install` must report the service manager as unavailable. Only if all of these pass is the release created, as a prerelease that is never marked latest. A final job downloads the published assets, compares them byte for byte with the tested ones, and repeats the linux/amd64 test.
- **Never overwrite.** The workflow fails if a release for the tag already exists, both before packaging and before publishing. A changed build needs a new tag.
- **Minimal permissions.** The workflow has no default permissions. Packaging has `contents: read`, publishing alone `contents: write`, and the test jobs none. The checkout keeps no credentials.
- **The smoke test is independent of the repository.** `smoke-test.sh` needs a POSIX shell and `curl`. It runs `polaroidd` directly with an explicit temporary database, a kernel-chosen loopback port and a temporary empty `HOME`, and never registers a service or touches `~/.polaroid`. Passing it says nothing about the managed service, which the archive's guide tests separately.

## Consequences

- A reviewer can map an archive to a commit three ways: through `SHA256SUMS` and the manifest, through `polaroid version`, and through the release notes and CI run.
- Binaries are unsigned and not notarized. On macOS, a browser download is quarantined; the guide says how to clear that.
- `version` is a new member of the version JSON and of the installation record. Old records without it read as an empty version.
