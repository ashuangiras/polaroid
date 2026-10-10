# 0031. Scoped verification and local-only hosted CI

**Status:** Accepted. Extends [ADR-0017](0017-development-procedures-as-records.md); changes the triggers of [ADR-0028](0028-prerelease-verification-builds.md)'s release workflow. **Amended:** 2026-10-11 by [#77](https://github.com/ashuangiras/polaroid/issues/77): binding fixtures carry later revisions (`later_revisions`), and the loader appends them, so no binding revision is live-only.
**Date:** 2026-10-10.

## Context

The [#62 baseline](../../development/workflow-baseline-2026-10-10.md) verified a one-paragraph documentation fix with the only verification Polaroid had: the full gate, fresh. It took 440 s of a 679 s task. `go.module.checks` v3 ran the tests before `make check` ran them again (101 s), and forced `GOFLAGS=-count=1`; the Makefile and scripts built the binaries seven times; none of it could detect a wrong sentence or a broken link. Hosted CI was waited for by habit, then, after the owner's decision on #59, silently not, with nothing recorded. The release workflow dry-ran on every packaging pull request, spending hosted minutes on GitHub Free.

## Decision

- **Scoped procedures, chosen per change.** Polaroid gets three local procedures with bindings: `polaroid.change.docs` (`verify-docs`), `polaroid.change.records` (`verify-records`) and `polaroid.change.focused` (`verify-focused`). Each states what applies, what is excluded, the evidence and the escalation; storage, migrations, recovery, ownership, lifecycle, public contracts, dependencies and shared behavior stay with the full `verify-change`. Each calls the shared `dev.change.verify` v4 with an explicit `scope` input, which is part of the verified combination, so a scoped run never stands for full verification. The classification lives in these records and AGENTS.md; no code classifies tasks.
- **Work done once.** `dev.change.verify` v4 builds only when its inputs name a build (conditional `build`), and pins `go.module.checks` v4, which runs each gate command once. `make build` records the source state of `bin/` (commit, uncommitted changes, toolchain and Go environment) and `make binaries` rebuilds only when it differs, so composed targets build once per state and never use a stale build. Go's test cache serves every package except `internal/archtest`, `internal/lifecycle` and `internal/recovery`, whose results depend on inputs the cache does not track. Test and race runs stay separate.
- **Existing evidence.** An identical target that is already verified may be cited instead of rerun. Citing is a lookup and records nothing; a check whose result ages (the vulnerability scan) is rerun. No verification semantics, schema or API change: Polaroid already derives target verification for the exact commit, inputs, versions and decisions, which is what citing needs.
- **Hosted CI by recorded policy.** Waiting for hosted CI is the shared procedure `dev.ci.hosted`, a conditional reference of `dev.change.verify` v4. A repository records its policy once, as the inputs of a `hosted-ci` binding; a repository with paid hosted CI defaults to `required`. Polaroid's policy is `local-only` (GitHub Free): the `ci` workflow runs only on manual dispatch, and the release workflow no longer runs on pull requests; its dry run is dispatched by hand. Tagged releases still package, test and publish in the workflow, and publishing waits for it.
- **Release and upgrade are separate tasks.** Completing a change never publishes a release or upgrades the shared service: `polaroid.record-model.change` v5 upgrades an isolated copy of the live catalog instead, and `polaroid.release.publish` v2 runs only when the owner asks.

## Consequences

- New versions are not selected over evidenced ones until they have evidence, so the first run of `dev.change.verify` v4 through `verify-change` is chosen explicitly. The scoped procedures pin it.
- `scripts/load-fixtures.sh` defers a fixture whose reference is pinned to a version not yet stored; it still checks only a binding's first revision.
- Hosted CI no longer runs on its own: a regression that only Linux would show (the systemd lifecycle job) is caught when someone dispatches `ci` or by the release workflow's archive tests. A change that needs that evidence says so and dispatches it.
- The policy is data: changing it is a new revision of the `hosted-ci` binding, by the owner.
