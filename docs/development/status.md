# Status

This page is a snapshot of the repository's current state, replaced at every handoff. It is not a work log or an issue tracker. Work items live in [GitHub issues](https://github.com/ashuangiras/polaroid/issues).

**As of 2026-10-09:** increments 1 and 2 are implemented, and so are the first two items of increment 3: [#7](https://github.com/ashuangiras/polaroid/issues/7) (execution records) and [#9](https://github.com/ashuangiras/polaroid/issues/9) (subprocedure executions). Increment 2 covered [#1](https://github.com/ashuangiras/polaroid/issues/1) repository bindings, [#2](https://github.com/ashuangiras/polaroid/issues/2) named subprocedure references, and [#3](https://github.com/ashuangiras/polaroid/issues/3) reference-graph validation with bounded traversal. The repository is the private repository [ashuangiras/polaroid](https://github.com/ashuangiras/polaroid), and the local toolchain is Go 1.27.2.

## Implemented

- **Procedures:** a stable UUIDv7 ID and a unique canonical key, enforced by a `UNIQUE` constraint, with a `409 canonical_key_exists` response for duplicates.
- **Versions:** version 1 is stored atomically with the procedure. Philosophy, method and revision reason are typed text. Contract and instructions are free-form JSON objects, checked for shape only.
- **Revisions:** a revision is appended only against the latest `base_version`. Any other base gets `409 version_conflict`, which includes `latest_version`. Of several concurrent revisions from the same base, exactly one succeeds.
- **Immutability in the database:** triggers reject `UPDATE` and `DELETE` and non-contiguous version numbers. `CHECK` constraints enforce JSON objects and the canonical-key format.
- **Bindings** ([#1](https://github.com/ashuangiras/polaroid/issues/1), [ADR-0007](../architecture/decisions/0007-repository-identity-for-bindings.md)):
  - a binding has a stable UUIDv7 ID, a repository identifier in canonical-path form, a local name, and the procedure ID. `(repository, name)` is unique (`409 binding_exists`);
  - binding revisions hold `inputs` (a free-form JSON object), a `version_policy` of `{"pin": N}` or `{"contextual": {}}`, and a reason. They are appended against `base_revision` (`409 revision_conflict` with `latest_revision`);
  - an unknown procedure gets `404`, and a pin to a missing version gets `400` naming `revision.version_policy.pin`;
  - contextual policies are stored and returned verbatim, and nothing resolves them;
  - migration `0002` adds the tables with immutability, contiguity and pin-existence triggers. A schema-version-1 database is upgraded in place.
- **Subprocedure references** ([#2](https://github.com/ashuangiras/polaroid/issues/2), [ADR-0008](../architecture/decisions/0008-subprocedure-references.md)):
  - a version may carry `references`, each with a unique `name`, a target `procedure_id`, a `version_policy` (the binding type), and `inputs` that map each child input to `{"input": "<parent input>"}` or `{"value": <JSON>}`;
  - an unknown target, a missing pinned version, a duplicate name or a malformed mapping gets `400`, with every failing reference named in `fields`;
  - references are returned in order, byte-for-byte. A version without references has no `references` field, so older versions are served unchanged;
  - migration `0003` stores references in `procedure_version_references`. A trigger and a deferred foreign key allow them only in the transaction that creates their version, and triggers make them immutable and check pinned versions.
- **Composition graph** ([#3](https://github.com/ashuangiras/polaroid/issues/3), [ADR-0009](../architecture/decisions/0009-reference-graph-rules.md)):
  - a pinned reference selects its pinned version. A contextual one selects the target's latest version, until execution evidence exists;
  - every write of a version with references expands its graph in the write transaction. A path that reaches a procedure already on it, at any version, gets `409 reference_cycle` with the `cycle` path, and nothing is stored. This covers `A → A`, `A → B → A`, a cycle closed by a later revision of a target, and two concurrent half-cycles (exactly one is stored);
  - limits are a depth of 32 and 2048 nodes. Beyond them the response is `422 graph_too_large`, never partial data;
  - `GET /v1/procedures/{id}/versions/{n}/graph` (CLI `graph ID N`) returns the nested tree with the exact version of every node, read from one snapshot.
- **Execution records** ([#7](https://github.com/ashuangiras/polaroid/issues/7), [ADR-0010](../architecture/decisions/0010-execution-records.md)):
  - an execution is written once after a run and never changed. It records:
    - the exact procedure `version`, and an optional binding revision;
    - the `repository` and a full commit hash (40 or 64 lowercase hex characters);
    - a named `environment` with free-form attributes;
    - the effective `inputs`;
    - an `outcome` of `succeeded` or `failed`;
    - non-empty inline `evidence`;
  - an unknown procedure or binding gets `404`. These get `400` naming the field: a missing version or binding revision, a binding of another procedure or repository, and a version other than the binding revision's pin;
  - `POST /v1/executions`, `GET /v1/executions/{id}`, and `GET /v1/executions?procedure_id=…[&version=…][&repository=…]`, which returns summaries oldest first. CLI commands: `record`, `get-execution`, `executions`;
  - migration `0004` adds the table, with foreign keys to the version and binding revision, a binding-consistency trigger, `CHECK`s on the commit, environment, outcome and JSON fields, and immutability triggers.
- **Subprocedure executions** ([#9](https://github.com/ashuangiras/polaroid/issues/9), [ADR-0011](../architecture/decisions/0011-subprocedure-executions.md)):
  - a parent execution lists `children: [{reference, execution_id}]`, naming executions recorded earlier. A child must:
    - fulfil a reference of the parent's version;
    - have run that reference's target, at exactly the pinned version for a pinned reference (any version for a contextual one);
    - share the parent's repository and commit;
  - each reference has at most one child, and each execution has at most one parent. Every violation is `400` naming `children[i].reference` or `children[i].execution_id`;
  - concurrent parents cannot claim the same child: exactly one succeeds. Trees nest, and `children` is omitted when empty, so earlier executions are served unchanged;
  - migration `0005` adds `execution_children`. A trigger and a deferred foreign key allow links only in the parent's transaction, a trigger checks each link against its reference, and links are immutable.
- **Persistence:** SQLite in WAL mode with `synchronous=FULL`. Migrations are counted by `user_version`, and a database with a newer schema is refused.
- **HTTP API v1** ([contract](../architecture/http-api.md)):
  - procedures: create, list, get by ID, get by key, get one version, get a version's composition graph, revise, and `/healthz`;
  - bindings: create, list by repository, get with history, get one revision, revise;
  - JSON errors throughout, for invalid input, missing records, conflicts and internal failures;
  - strict decoding, a 1 MiB limit, a loopback-host check and cross-origin protection.
- **`polaroidd`:** `-addr` and `-db` flags or the `POLAROID_*` environment variables, loopback by default, server timeouts, and graceful shutdown on SIGINT or SIGTERM.
- **`polaroid` CLI:** a generic client. It reads JSON from a file or stdin, prints response bodies to stdout, and exits with 0, 1 or 2. Binding commands: `bindings`, `bind`, `get-binding`, `get-binding-revision`, `revise-binding`.
- **Supporting material:** example records, the live demo (`make demo`, now including a two-repository binding), package-boundary tests, a dependency and license gate, CI workflow, Copilot instructions, and the docs and ADRs.

**Not implemented:** verification, evidence-based contextual resolution (and any resolution of contextual binding policies), discovery, aliases, MCP, access control, and the PoC import. See the [roadmap](roadmap.md).

## Verification evidence

| Check | Where | Result |
| --- | --- | --- |
| `make ci` with `GOLANGCI_LINT=<golangci-lint 2.14.0 release binary>`, branch `issue-9-subprocedure-executions` | darwin/arm64, local Go 1.27.2 | **Pass.** `0 issues`; 6/6 packages `ok` in `go test` and in `go test -race`; `deps-check: PASS (10 modules …)`; `No vulnerabilities found.`; `demo: PASS`. |
| GitHub Actions `ci`, runs 37925117183 (PR for #7) and 37925136844 (`main` at `0dcff5a`) | ubuntu-latest | **Pass.** |
| GitHub Actions `ci`, runs 37923015793 (PR for #3) and 37923034159 (`main` at `c85211c`) | ubuntu-latest | **Pass.** |
| GitHub Actions `ci`, runs 37918349018 (PR for #1) and 37918365752 (`main` at `ce8e024`) | ubuntu-latest | **Pass.** |
| GitHub Actions `ci`, run [37912026720](https://github.com/ashuangiras/polaroid/actions/runs/37912026720) on commit `7fb84cd` (increment 1) | ubuntu-latest, Go 1.27.2, golangci-lint 2.14.0 | **Pass.** |
| `make lint` with the `golangci-lint` on `PATH` (2.12.2) | darwin/arm64 | **Fails, as designed.** The output reads `golangci-lint 2.14.0 is required, found 2.12.2`. |
| Test inventory | darwin/arm64, Go 1.27.2 | The source has 110 `Test` functions, and 110 top-level tests passed (plus 184 subtests). |

Negative checks showed that the gates detect what they claim to detect. Each mutation below was made temporarily, the expected tests failed, and the mutation was reverted:

- Removing the stale-base check fails the storage and HTTP conflict tests. The schema's contiguity trigger still blocked the corrupt write.
- Disabling the version-update trigger fails `TestSchemaRejectsChangesToStoredRecords`.
- Removing the stale-`base_revision` check fails `TestAppendBindingRevisionRequiresLatestBaseAndKeepsHistory`, `TestStaleBindingRevisionIsRejected` and both `TestConcurrentBindingRevisionsFromSameBase` tests.
- Removing the pinned-version check on binding creation fails `TestCreateBindingRejections` and `TestBindingCreationRejections`. The pin-existence trigger still blocked the write.
- Disabling the binding-revision update trigger fails `TestSchemaRejectsChangesToBindings`.
- Removing the reference-target check fails `TestReferencesToMissingTargetsAreRejected` and three `TestInvalidReferencesAreRejected` cases. The schema still blocked the write.
- Disabling the trigger that keeps references with their version fails `TestSchemaRejectsChangesToReferences/add_to_an_existing_version`.
- Always serializing `references` fails `TestVersionsWithoutReferencesOmitTheField` and `TestVersionsFromBeforeReferencesReadBackUnchanged`.
- Skipping the write-time graph check fails the storage cycle, concurrency and limit tests and both HTTP graph tests.
- Disabling cycle detection in the walker fails `TestExpandGraphRejectsCycles`, the four storage cycle tests and `TestReferenceCyclesAreRejected`. The limits still stop the walk.
- Disabling the depth limit fails `TestExpandGraphLimits`, `TestCompositionGraphLimits` and `TestGraphsOverTheLimitAreRefused`.
- Skipping the service's execution-target checks fails seven `TestExecutionTargetsAreChecked` cases and `TestExecutionCommands`. The schema still blocked every bad write.
- Disabling the execution-binding trigger fails two `TestSchemaRejectsInvalidAndChangedExecutions` cases.
- Accepting abbreviated commits in validation fails `TestExecutionFieldRules` and `TestInvalidExecutionsAreRejected`. The schema `CHECK` still rejected them.
- Discarding the service's child-link problems fails six `TestChildLinksAreChecked` cases. The schema still blocked every bad link.
- Disabling the store's "already linked" detection fails `TestAChildHasAtMostOneParent`, `TestConcurrentParentsClaimAChildOnce` and one HTTP case. The `UNIQUE` constraint still blocked the second claim.
- Disabling the link-consistency trigger fails three `TestSchemaRejectsInvalidAndChangedChildLinks` cases. This check first showed that those cases were being rejected for the wrong reason: a committed control case had already linked the shared child and parent. The test now gives each case its own parent and child.
- Leaking internal error text fails `TestInternalErrorsAreNotExposed`.
- Making the CLI exit 0 on API errors fails `TestFailedRequestsExitOne`.
- Adding `net/http` to `internal/memory` fails `TestPackageBoundaries`.
- Five corrupted copies of the inventory each fail `deps-check`: a missing row, a wrong license, a wrong version, a stale row, and a non-permissive license.

## Blockers and open decisions

1. **The local golangci-lint is 2.12.2; 2.14.0 is pinned.** 2.12.2 cannot lint with Go 1.27.2. CI installs 2.14.0 itself. Locally, pass `GOLANGCI_LINT=<path to 2.14.0>` until it is installed. Deferred by the owner.
2. **No project license has been chosen.** Without a `LICENSE` file, the code is all rights reserved. All dependencies are permissive. Deferred by the owner.
3. **The PoC import is pending.** No earlier Python/SQLite database or schema is available, so compatibility is not claimed.

## Next work item

Refine roadmap item **3.3 Context-specific verification** and file it as an issue. Settle these first:

- what a verification is: a stored record, or a value computed from executions;
- which context keys it matches on, for example repository, commit, `environment.name`, effective inputs and child versions;
- whether a succeeded parent must have a child for every reference, and how the children's outcomes count;
- when a changed child-version combination needs fresh verification.
