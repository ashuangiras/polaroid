# Status

This page is a snapshot of the repository's current state, replaced at every handoff. It is not a work log or an issue tracker. Work items live in [GitHub issues](https://github.com/ashuangiras/polaroid/issues).

**As of 2026-10-09:** increment 1 (procedure identity and immutable versions) is implemented. The repository is published as the private repository [ashuangiras/polaroid](https://github.com/ashuangiras/polaroid), CI passes, and the local toolchain is Go 1.27.2.

## Implemented

- **Procedures:** a stable UUIDv7 ID and a unique canonical key, enforced by a `UNIQUE` constraint, with a `409 canonical_key_exists` response for duplicates.
- **Versions:** version 1 is stored atomically with the procedure. Philosophy, method and revision reason are typed text. Contract and instructions are free-form JSON objects, checked for shape only.
- **Revisions:** a revision is appended only against the latest `base_version`. Any other base gets `409 version_conflict`, which includes `latest_version`. Of several concurrent revisions from the same base, exactly one succeeds.
- **Immutability in the database:** triggers reject `UPDATE` and `DELETE` and non-contiguous version numbers. `CHECK` constraints enforce JSON objects and the canonical-key format.
- **Persistence:** SQLite in WAL mode with `synchronous=FULL`. Migrations are counted by `user_version`, and a database with a newer schema is refused.
- **HTTP API v1** ([contract](../architecture/http-api.md)):
  - create, list, get by ID, get by key, get one version, revise, and `/healthz`;
  - JSON errors throughout, for invalid input, missing records, conflicts and internal failures;
  - strict decoding, a 1 MiB limit, a loopback-host check and cross-origin protection.
- **`polaroidd`:** `-addr` and `-db` flags or the `POLAROID_*` environment variables, loopback by default, server timeouts, and graceful shutdown on SIGINT or SIGTERM.
- **`polaroid` CLI:** a generic client. It reads JSON from a file or stdin, prints response bodies to stdout, and exits with 0, 1 or 2.
- **Supporting material:** example records, the live demo (`make demo`), package-boundary tests, a dependency and license gate, CI workflow, Copilot instructions, and the docs and ADRs.

**Not implemented:** bindings, references and composition, executions and evidence, contextual resolution, discovery, aliases, MCP, access control, and the PoC import. See the [roadmap](roadmap.md).

## Verification evidence

| Check | Where | Result |
| --- | --- | --- |
| GitHub Actions `ci`, run [37912026720](https://github.com/ashuangiras/polaroid/actions/runs/37912026720) on commit `7fb84cd` | ubuntu-latest, Go 1.27.2, golangci-lint 2.14.0 | **Pass.** `0 issues`; 6/6 packages `ok` in `go test` and in `go test -race`; `deps-check: PASS (10 modules …)`; `No vulnerabilities found.`; `demo: PASS`. |
| `make ci` with `GOLANGCI_LINT=<golangci-lint 2.14.0 release binary>` | darwin/arm64, local Go 1.27.2 | **Pass**, with the same results. |
| `make lint` with the `golangci-lint` on `PATH` (2.12.2) | darwin/arm64 | **Fails, as designed.** The output reads `golangci-lint 2.14.0 is required, found 2.12.2`. |
| Test inventory | darwin/arm64, Go 1.27.2 | The source has 37 `Test` functions, and 37 top-level tests passed (plus 38 subtests). |

Negative checks showed that the gates detect what they claim to detect. Each mutation below was made temporarily, the expected tests failed, and the mutation was reverted:

- Removing the stale-base check fails the storage and HTTP conflict tests. The schema's contiguity trigger still blocked the corrupt write.
- Disabling the version-update trigger fails `TestSchemaRejectsChangesToStoredRecords`.
- Leaking internal error text fails `TestInternalErrorsAreNotExposed`.
- Making the CLI exit 0 on API errors fails `TestFailedRequestsExitOne`.
- Adding `net/http` to `internal/memory` fails `TestPackageBoundaries`.
- Five corrupted copies of the inventory each fail `deps-check`: a missing row, a wrong license, a wrong version, a stale row, and a non-permissive license.

## Blockers and open decisions

1. **The local golangci-lint is 2.12.2; 2.14.0 is pinned.** 2.12.2 cannot lint with Go 1.27.2. CI installs 2.14.0 itself. Locally, pass `GOLANGCI_LINT=<path to 2.14.0>` until it is installed. Deferred by the owner.
2. **No project license has been chosen.** Without a `LICENSE` file, the code is all rights reserved. All dependencies are permissive. Deferred by the owner.
3. **The PoC import is pending.** No earlier Python/SQLite database or schema is available, so compatibility is not claimed.

## Next work item

[#1 Repository bindings with guarded revisions](https://github.com/ashuangiras/polaroid/issues/1). Before starting, decide how a repository is identified, and record the decision in an ADR.
