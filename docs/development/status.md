# Status

This page is a snapshot of the repository's current state, replaced at every handoff. It is not a work log or an issue tracker. Work items live in [GitHub issues](https://github.com/ashuangiras/polaroid/issues).

**As of 2026-10-09:** increments 1 to 4 are implemented. Increment 4 covered [#15](https://github.com/ashuangiras/polaroid/issues/15) (the MCP transport) and [#16](https://github.com/ashuangiras/polaroid/issues/16) (the feedback box). Increment 3 covered [#7](https://github.com/ashuangiras/polaroid/issues/7) (execution records), [#9](https://github.com/ashuangiras/polaroid/issues/9) (subprocedure executions), [#11](https://github.com/ashuangiras/polaroid/issues/11) (context-specific verification) and [#13](https://github.com/ashuangiras/polaroid/issues/13) (contextual resolution from evidence). Increment 2 covered [#1](https://github.com/ashuangiras/polaroid/issues/1) repository bindings, [#2](https://github.com/ashuangiras/polaroid/issues/2) named subprocedure references, and [#3](https://github.com/ashuangiras/polaroid/issues/3) reference-graph validation with bounded traversal. The repository is the private repository [ashuangiras/polaroid](https://github.com/ashuangiras/polaroid), and the local toolchain is Go 1.27.2.

## Implemented

- **Procedures:** a stable UUIDv7 ID and a unique canonical key, enforced by a `UNIQUE` constraint, with a `409 canonical_key_exists` response for duplicates.
- **Versions:** version 1 is stored atomically with the procedure. Philosophy, method and revision reason are typed text. Contract and instructions are free-form JSON objects, checked for shape only.
- **Revisions:** a revision is appended only against the latest `base_version`. Any other base gets `409 version_conflict`, which includes `latest_version`. Of several concurrent revisions from the same base, exactly one succeeds.
- **Immutability in the database:** triggers reject `UPDATE` and `DELETE` and non-contiguous version numbers. `CHECK` constraints enforce JSON objects and the canonical-key format.
- **Bindings** ([#1](https://github.com/ashuangiras/polaroid/issues/1), [ADR-0007](../architecture/decisions/0007-repository-identity-for-bindings.md)):
  - a binding has a stable UUIDv7 ID, a repository identifier in canonical-path form, a local name, and the procedure ID. `(repository, name)` is unique (`409 binding_exists`);
  - binding revisions hold `inputs` (a free-form JSON object), a `version_policy` of `{"pin": N}` or `{"contextual": {}}`, and a reason. They are appended against `base_revision` (`409 revision_conflict` with `latest_revision`);
  - an unknown procedure gets `404`, and a pin to a missing version gets `400` naming `revision.version_policy.pin`;
  - contextual policies are stored and returned verbatim. `GET /v1/bindings/{id}/resolution?environment=…` resolves the latest revision from evidence (#13);
  - migration `0002` adds the tables with immutability, contiguity and pin-existence triggers. A schema-version-1 database is upgraded in place.
- **Subprocedure references** ([#2](https://github.com/ashuangiras/polaroid/issues/2), [ADR-0008](../architecture/decisions/0008-subprocedure-references.md)):
  - a version may carry `references`, each with a unique `name`, a target `procedure_id`, a `version_policy` (the binding type), and `inputs` that map each child input to `{"input": "<parent input>"}` or `{"value": <JSON>}`;
  - an unknown target, a missing pinned version, a duplicate name or a malformed mapping gets `400`, with every failing reference named in `fields`;
  - references are returned in order, byte-for-byte. A version without references has no `references` field, so older versions are served unchanged;
  - migration `0003` stores references in `procedure_version_references`. A trigger and a deferred foreign key allow them only in the transaction that creates their version, and triggers make them immutable and check pinned versions.
- **Composition graph** ([#3](https://github.com/ashuangiras/polaroid/issues/3), [ADR-0009](../architecture/decisions/0009-reference-graph-rules.md)):
  - a pinned reference selects its pinned version. Without a resolution context, a contextual one selects the target's latest version; the write-time check always does;
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
- **Verification** ([#11](https://github.com/ashuangiras/polaroid/issues/11), [ADR-0012](../architecture/decisions/0012-derived-verification.md)):
  - derived on read from executions and links. Nothing is stored and there is no migration; each read is one snapshot (one transaction);
  - an execution is verified when it succeeded and every reference of its version has a linked, verified child, recursively. Otherwise it reports its direct problems: `outcome_failed`, `missing_child`, `child_not_verified`;
  - a combination is repository, commit, `environment.name`, canonical inputs (sorted members, RFC 8785 floats, exact integers) and the child-version tree. Its status is that of its latest execution;
  - `GET /v1/executions/{id}/verification` and `GET /v1/procedures/{id}/versions/{n}/verifications[?repository&commit&environment]`. CLI commands: `verification`, `verifications`.
- **Contextual resolution** ([#13](https://github.com/ashuangiras/polaroid/issues/13), [ADR-0013](../architecture/decisions/0013-evidence-based-resolution.md)):
  - a context is a repository and an environment name. A version is verified there when its latest execution there is verified; that execution is its evidence;
  - under a node with evidence, references follow the child executions it linked, so a verified combination is used as a whole. Otherwise a contextual reference selects the highest verified version, else the latest. A pinned reference selects its pin, with the pin's evidence if any;
  - edges report `selected_by` (`pin`, `evidence`, `latest`), and nodes with evidence report `verified_by`. Cycles and limits are still checked on read;
  - `GET /v1/procedures/{id}/versions/{n}/graph?repository=…&environment=…` and `GET /v1/bindings/{id}/resolution?environment=…`. CLI: `graph ID N REPO ENV`, `resolve BINDING_ID ENV`. Nothing is stored and there is no migration.
- **MCP server** ([#15](https://github.com/ashuangiras/polaroid/issues/15), [ADR-0014](../architecture/decisions/0014-mcp-transport.md), [contract](../architecture/mcp.md)):
  - `polaroidd` serves `/mcp` on its listener, behind the same loopback-host check and cross-origin protection: stateless streamable HTTP, JSON responses, protocol 2026-07-28 only, 1 MiB bodies. Built on the official Go SDK v1.8.0;
  - 20 tools with full HTTP API parity (14 annotated read-only). Arguments are flat, named after record fields, decoded strictly (unknown and duplicate members rejected, member order kept); results and error bodies are the HTTP API's, as structured content and text; field errors use the flat names;
  - resource templates `polaroid://procedures/{id}`, `polaroid://procedures/{id}/versions/{version}` and `polaroid://bindings/{id}`;
  - the record shapes, strict decoding and error classification moved into `internal/transport/wire`, shared by both transports; the HTTP tests pass unchanged against it.
- **Feedback box** ([#16](https://github.com/ashuangiras/polaroid/issues/16), [ADR-0015](../architecture/decisions/0015-feedback-reports.md)):
  - an immutable report about Polaroid itself: `kind` (`problem` or `suggestion`), a single-line `summary` (every Unicode line terminator rejected), `details`, a canonical-key `reporter`, and an optional free-form `context`, stored as `{}` when absent. No triage state; reading or writing one changes no other record;
  - `POST /v1/feedback`, `GET /v1/feedback/{id}` and `GET /v1/feedback[?kind=…]` (oldest first, full reports, strict query). CLI: `feedback`, `feedbacks`, `get-feedback`. MCP: `report_feedback`, `list_feedback`, `get_feedback`; the server instructions now ask agents to report problems and suggestions;
  - migration `0006` adds the `feedback` table with `CHECK` constraints mirroring the field rules and immutability triggers.
- **Persistence:** SQLite in WAL mode with `synchronous=FULL`. Migrations are counted by `user_version`, and a database with a newer schema is refused.
- **HTTP API v1** ([contract](../architecture/http-api.md)):
  - procedures: create, list, get by ID, get by key, get one version, get a version's composition graph, revise, and `/healthz`;
  - bindings: create, list by repository, get with history, get one revision, revise;
  - JSON errors throughout, for invalid input, missing records, conflicts and internal failures;
  - strict decoding, a 1 MiB limit, a loopback-host check and cross-origin protection.
- **`polaroidd`:** `-addr` and `-db` flags or the `POLAROID_*` environment variables, loopback by default, server timeouts, and graceful shutdown on SIGINT or SIGTERM.
- **`polaroid` CLI:** a generic client. It reads JSON from a file or stdin, prints response bodies to stdout, and exits with 0, 1 or 2. Binding commands: `bindings`, `bind`, `get-binding`, `get-binding-revision`, `revise-binding`.
- **Supporting material:** example records, the live demo (`make demo`, now including a two-repository binding), package-boundary tests, a dependency and license gate, CI workflow, Copilot instructions, and the docs and ADRs.

**Not implemented:** discovery, aliases, access control, pagination, and the PoC import. See the [roadmap](roadmap.md).

## Verification evidence

| Check | Where | Result |
| --- | --- | --- |
| `make ci` with `GOLANGCI_LINT=<golangci-lint 2.14.0 release binary>`, branch `issue-16-feedback-box` | darwin/arm64, local Go 1.27.2 | **Pass.** `0 issues`; all 7 packages with tests `ok` in `go test` and in `go test -race`; `deps-check: PASS (18 modules …)`; `No vulnerabilities found.`; `demo: PASS`. |
| Live upgrade and agent loop for #16 | darwin/arm64, `polaroidd` on 127.0.0.1:7417 | **Pass.** A schema-5 database with 2 procedures and 3 executions, written earlier from Copilot chat, upgraded to schema 6 with them intact. Over raw MCP JSON-RPC: `server/discover` instructions include `report_feedback`; `tools/list` has 20 tools (14 read-only); a `report_feedback` call stored a report that `get_feedback`, `polaroid get-feedback` and `polaroid feedbacks` return byte-identically. Invalid arguments named `kind`, `summary`, `details`, `reporter` and `context`. |
| Local manual-test scripts (gitignored), with the new feedback sections | darwin/arm64 | **Pass.** HTTP and CLI: 197/197 checks. MCP: 81/81 checks. |
| GitHub Actions `ci`, runs 37932118685 (PR for #13) and 37932138196 (`main` at `774c442`) | ubuntu-latest | **Pass.** |
| GitHub Actions `ci`, runs 37929496783 (PR for #11) and 37929513551 (`main` at `76f24e5`) | ubuntu-latest | **Pass.** |
| GitHub Actions `ci`, runs 37927228332 (PR for #9) and 37927243040 (`main` at `2d2f897`) | ubuntu-latest | **Pass.** |
| GitHub Actions `ci`, runs 37925117183 (PR for #7) and 37925136844 (`main` at `0dcff5a`) | ubuntu-latest | **Pass.** |
| GitHub Actions `ci`, runs 37923015793 (PR for #3) and 37923034159 (`main` at `c85211c`) | ubuntu-latest | **Pass.** |
| GitHub Actions `ci`, runs 37918349018 (PR for #1) and 37918365752 (`main` at `ce8e024`) | ubuntu-latest | **Pass.** |
| GitHub Actions `ci`, run [37912026720](https://github.com/ashuangiras/polaroid/actions/runs/37912026720) on commit `7fb84cd` (increment 1) | ubuntu-latest, Go 1.27.2, golangci-lint 2.14.0 | **Pass.** |
| `make lint` with the `golangci-lint` on `PATH` (2.12.2) | darwin/arm64 | **Fails, as designed.** The output reads `golangci-lint 2.14.0 is required, found 2.12.2`. |
| Test inventory | darwin/arm64, Go 1.27.2 | The source has 148 `Test` functions, and 148 top-level tests passed (plus 229 subtests). |

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
- Ignoring a reference without a linked child fails `TestVerificationCoverageRule`, `TestVerificationReadsStoredExecutions` and `TestExecutionVerification`.
- Letting any verified execution decide a combination fails `TestListCombinations` and `TestVerificationCombinations`.
- Comparing inputs compacted instead of canonical fails `TestCombinationKey` and `TestVerificationCombinations`.
- Canonicalizing integers as doubles (plain RFC 8785) fails `TestCombinationKey`: two distinct integers above 2^53 merged.
- Leaving the child-version tree out of the combination key fails `TestListCombinations`, `TestVerificationReadsStoredExecutions` and `TestVerificationCombinations`.
- Selecting the lowest instead of the highest verified version fails two domain resolution tests, `TestResolveUsesStoredEvidence`, `TestGraphResolvedInContext` and `TestBindingResolution`.
- Letting the oldest execution of a version decide (SQL order reversed) fails `TestResolveUsesStoredEvidence` and `TestGraphResolvedInContext`. The HTTP case was added after this check first passed it.
- Ignoring a parent's verified child tree fails `TestVerifiedParentFixesItsChildVersions`, `TestResolveUsesStoredEvidence` and `TestGraphResolvedInContext`. The HTTP case was made discriminating after this check first passed it.
- Ignoring the environment when looking up evidence fails `TestGraphResolvedInContext` and `TestBindingResolution`.
- Decoding MCP tool arguments leniently fails `TestToolArgumentsAreStrict` (an unknown member was accepted).
- Returning internal error text from an MCP tool fails `TestInternalErrorsAreNotExposed` in `transport/mcp`.
- Accepting every protocol revision fails `TestOlderProtocolsAreRefused` (a 2025-06-18 client could call tools).
- Returning the domain's nested field paths from MCP tools fails `TestToolArgumentsAreStrict`.
- Accepting a multi-line feedback summary in validation fails `TestFeedbackFieldRules` and `TestInvalidFeedbackIsRejected`; the schema `CHECK` still rejected it (as a `500`). Dropping the newline `CHECK` fails `TestSchemaRejectsInvalidAndChangedFeedback`, as does dropping the context-object `CHECK`.
- Disabling either feedback immutability trigger fails `TestSchemaRejectsInvalidAndChangedFeedback`.
- Storing an absent context as nothing instead of `{}` fails `TestFeedbackContextIsOptional`. Skipping the reporter check fails `TestFeedbackRequiresEveryField` and `TestInvalidFeedbackIsRejected`.
- Removing the feedback list's `ORDER BY` or its `kind` filter fails `TestFeedbackRoundTripAndList`. Skipping the service's kind check fails `TestListFeedbackQuery` and `TestFeedbackThroughTools`; accepting an empty `?kind=` fails `TestListFeedbackQuery`.
- Dropping `report_feedback` from the MCP instructions fails `TestToolsAndResourcesAreAdvertised`.
- Listing the MCP SDK as `MIT`, as `Apache-2.0`, or as `Apache-2.0 AND GPL-3.0` in a copy of the inventory each fails `deps-check`.
- Leaking internal error text fails `TestInternalErrorsAreNotExposed`.
- Making the CLI exit 0 on API errors fails `TestFailedRequestsExitOne`.
- Adding `net/http` to `internal/memory` fails `TestPackageBoundaries`.
- Five corrupted copies of the inventory each fail `deps-check`: a missing row, a wrong license, a wrong version, a stale row, and a non-permissive license.

## Blockers and open decisions

1. **The local golangci-lint is 2.12.2; 2.14.0 is pinned.** 2.12.2 cannot lint with Go 1.27.2. CI installs 2.14.0 itself. Locally, pass `GOLANGCI_LINT=<path to 2.14.0>` until it is installed. Deferred by the owner.
2. **No project license has been chosen.** Without a `LICENSE` file, the code is all rights reserved. All dependencies are permissive. Deferred by the owner.
3. **The PoC import is pending.** No earlier Python/SQLite database or schema is available, so compatibility is not claimed.

## Next work item

No increment is scoped beyond increment 4. Candidates, for the owner to choose and file:

- Triage the first real feedback reports from agents using `/mcp`, and turn them into issues.
- Decide whether `/mcp` should also accept protocol 2025-11-25, which the TypeScript SDK and MCP Inspector need. That would amend ADR-0014.
- Scope an item from the roadmap's "Later" list.
