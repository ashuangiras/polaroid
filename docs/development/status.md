# Status

This page is a snapshot of the repository's current state, replaced at every handoff. It is not a work log or an issue tracker. Work items live in GitHub issues; until those exist, they live in the [roadmap](roadmap.md).

**As of 2026-10-09:** the bootstrap is complete, and increment 1 (procedure identity and immutable versions) is implemented.

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

All checks ran on darwin/arm64 on 2026-10-09. CI has never run, because there is no remote.

| Check | Toolchain | Result |
| --- | --- | --- |
| `make ci` (check, vuln and demo) with `GOLANGCI_LINT=<golangci-lint 2.14.0 release binary>` | Go 1.27.2, which CI uses | **Pass.** golangci-lint `0 issues`; 6/6 packages `ok` in `go test` and `go test -race`; `deps-check: PASS (10 modules …)`; govulncheck `No vulnerabilities found.`; `demo: PASS`. |
| `make check` with the same golangci-lint binary | Go 1.27.1, the local default | **Pass.** |
| `make vuln` | Go 1.27.1 | **Fail, as expected.** 9 reachable standard-library vulnerabilities, all fixed in 1.27.2 (see blocker 2). |
| `make lint` with the `golangci-lint` on `PATH` (2.12.2) | Go 1.27.1 | **Fail, as designed.** The output reads `golangci-lint 2.14.0 is required, found 2.12.2` (see blocker 3). |
| Test inventory | Go 1.27.2 | The source has 37 `Test` functions, and 37 top-level tests passed (plus 38 subtests). No test failed. |

Negative checks showed that the gates detect what they claim to detect. Each mutation below was made temporarily, the expected tests failed, and the mutation was reverted:

- Removing the stale-base check fails the storage and HTTP conflict tests. The schema's contiguity trigger still blocked the corrupt write.
- Disabling the version-update trigger fails `TestSchemaRejectsChangesToStoredRecords`.
- Leaking internal error text fails `TestInternalErrorsAreNotExposed`.
- Making the CLI exit 0 on API errors fails `TestFailedRequestsExitOne`.
- Adding `net/http` to `internal/memory` fails `TestPackageBoundaries`.
- Five corrupted copies of the inventory each fail `deps-check`: a missing row, a wrong license, a wrong version, a stale row, and a non-permissive license.

## Blockers and open decisions

1. **No Git remote or GitHub repository.** `ashuangiras/polaroid` and `angirasa_risk/polaroid` do not exist. Consequences:
   - GitHub Issues cannot be used yet; the roadmap holds issue-ready drafts.
   - The module path is the placeholder `example.com/polaroid`.
   - The workflow has never run on GitHub Actions.

   **Owner action:** create the repository, then follow "Publishing the repository" in [workflow.md](workflow.md).
2. **The local Go toolchain is 1.27.1, with `GOTOOLCHAIN=local`.** Binaries built locally contain 9 reachable standard-library vulnerabilities (GO-2026-6603 to GO-2026-6617). `go.mod` selects 1.27.2, but this shell setting ignores that. **Fix:** run `brew upgrade go`, or unset `GOTOOLCHAIN`. Until then, use `GOTOOLCHAIN=go1.27.2 make ci`.
3. **The local golangci-lint is 2.12.2; 2.14.0 is pinned.** 2.12.2 cannot lint with the Go 1.27.2 toolchain. **Fix:** install golangci-lint 2.14.0. It was verified here from its release binary in `/tmp`, without being installed.
4. **No project license has been chosen.** Without a `LICENSE` file, the code is all rights reserved. All dependencies are permissive. **Owner decision.**
5. **The PoC import is pending.** No earlier Python/SQLite database or schema is available, so compatibility is not claimed.

## Next work item

[Roadmap 2.1: repository bindings with guarded revisions](roadmap.md#21-repository-bindings-with-guarded-revisions--next). Its acceptance criteria are summarized below; the full text is in the roadmap.

1. Two repositories bind the same procedure without copying its instructions, and the procedure's history is unchanged.
2. An unknown procedure gets `404`. An unknown pinned version gets `400`. A duplicate repository-local name gets `409`.
3. A stale `base_revision` gets `409`. Of several concurrent revisions from one base, exactly one succeeds.
4. Binding revisions are immutable, enforced by triggers, and survive a close and reopen.
5. A contextual policy is stored but not yet resolved.

Before starting, decide on the repository identifier format, and record the decision in an ADR.
