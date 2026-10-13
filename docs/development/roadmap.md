# Roadmap

Increments are ordered. Issue-ready items are filed as [GitHub issues](https://github.com/ashuangiras/polaroid/issues) using the [work item template](../../.github/ISSUE_TEMPLATE/work-item.yml).

> **Work-item authority.** GitHub Issues are the source of truth for scope and acceptance criteria. This roadmap keeps only order and links for filed items, never a second copy. Items not yet issue-ready stay here until they are refined and filed.

Increments 1 to 6, the items from agent feedback, the production-readiness items, #79, #81 and the first increment of capability extraction (#85) and catalog statistics (#96) and write limits (#98) are implemented. Nothing in "Later" is.

## Increment 1 — Procedure identity and immutable versions (done)

Create, list, retrieve and revise procedures with stable IDs, unique canonical keys, append-only versions, stale-base conflicts, SQLite persistence, health endpoint, CLI. Evidence: [status.md](status.md).

## Increment 2 — Bindings and composition (done)

1. [#1 Repository bindings with guarded revisions](https://github.com/ashuangiras/polaroid/issues/1) — **done**
2. [#2 Named subprocedure references in procedure versions](https://github.com/ashuangiras/polaroid/issues/2) (depends on #1) — **done**
3. [#3 Reference-graph validation and bounded traversal](https://github.com/ashuangiras/polaroid/issues/3) (depends on #2) — **done**

## Increment 3 — Execution evidence and resolution (done)

1. [#7 Execution records](https://github.com/ashuangiras/polaroid/issues/7) — **done**
2. [#9 Subprocedure executions](https://github.com/ashuangiras/polaroid/issues/9) (depends on #7) — **done**
3. [#11 Context-specific verification of executions](https://github.com/ashuangiras/polaroid/issues/11) (depends on #7, #9) — **done**
4. [#13 Resolve contextual references from verification evidence](https://github.com/ashuangiras/polaroid/issues/13) (depends on #11) — **done**

## Increment 4 — Agent integration (done)

1. [#15 MCP transport: tools and resources over streamable HTTP](https://github.com/ashuangiras/polaroid/issues/15) — **done**
2. [#16 Feedback box: agents report problems and suggestions about Polaroid](https://github.com/ashuangiras/polaroid/issues/16) (depends on #15) — **done**

## From agent feedback (done)

Filed from feedback reports that agents recorded with `report_feedback` (#16).

1. [#22 Docs: member order of free-form objects is kept as received; MCP clients may reorder](https://github.com/ashuangiras/polaroid/issues/22) — **done**
2. [#20 MCP: signal tool-catalogue changes](https://github.com/ashuangiras/polaroid/issues/20) — **done**, re-scoped to pinning `ttlMs: 0` and documenting client restarts
3. [#21 MCP: also accept protocol 2025-11-25](https://github.com/ashuangiras/polaroid/issues/21) — **done** ([ADR-0016](../architecture/decisions/0016-mcp-protocol-2025-11-25.md))

## Increment 5 — Procedural memory on Polaroid's own development (done)

No new product capability: the loop that increments 1 to 4 built is used on real development work.

1. [#27 Run the end-to-end scripts' deterministic checks in CI](https://github.com/ashuangiras/polaroid/issues/27) — **done**
2. [#28 Polaroid-guided development: reusable development procedures that improve through execution](https://github.com/ashuangiras/polaroid/issues/28) (depends on #27) — **done** ([ADR-0017](../architecture/decisions/0017-development-procedures-as-records.md), [procedural-loop.md](procedural-loop.md))
3. [#31 Separate selection evidence from target verification in resolution](https://github.com/ashuangiras/polaroid/issues/31) (found while using #28) — **done** ([ADR-0018](../architecture/decisions/0018-selection-evidence-and-target-verification.md))
4. [#33 Teach dev.change.verify explicit target verification, and follow it](https://github.com/ashuangiras/polaroid/issues/33) (depends on #31) — **done**, a record change ([procedural-loop.md](procedural-loop.md#target-aware-procedure-33))

## Increment 6 — Several repositories in one catalog (done)

1. [#35 Organize procedural knowledge, execution history and feedback for use by several repositories](https://github.com/ashuangiras/polaroid/issues/35) — **done** ([ADR-0019](../architecture/decisions/0019-repository-registry.md), [ADR-0020](../architecture/decisions/0020-procedure-origin-and-applicability.md), [ADR-0021](../architecture/decisions/0021-targeted-feedback-and-bounded-lists.md), [procedural-loop.md](procedural-loop.md#several-repositories-35))
2. [#37 Consolidate repository identity in verification, pagination guarantees and applicability discovery](https://github.com/ashuangiras/polaroid/issues/37) (depends on #35) — **done** ([ADR-0022](../architecture/decisions/0022-repository-identity-in-evidence.md), [ADR-0023](../architecture/decisions/0023-pagination-guarantees-and-scope-labels.md), [procedural-loop.md](procedural-loop.md#identity-pages-and-first-runs-of-the-shared-versions-37))

## Correctness fixes (done)

1. [#39 A parent is verified even when a linked child ran with inputs its reference mapping does not produce](https://github.com/ashuangiras/polaroid/issues/39) (fixes #9 and #11) — **done** ([ADR-0024](../architecture/decisions/0024-child-inputs-follow-the-reference-mapping.md), [procedural-loop.md](procedural-loop.md#child-inputs-follow-the-reference-mapping-39))

## Production readiness

Second-repository adoption trials are paused while the existing features are prepared for production use.

1. [#41 Persistent per-user home (~/.polaroid) and safe migration of the shared catalog out of bin/](https://github.com/ashuangiras/polaroid/issues/41) — **done** ([ADR-0025](../architecture/decisions/0025-per-user-default-database.md), [procedural-loop.md](procedural-loop.md#a-persistent-per-user-catalog-41))
2. [#43 Per-user installation and managed service (launchd LaunchAgent, systemd user service)](https://github.com/ashuangiras/polaroid/issues/43) — **done** ([ADR-0026](../architecture/decisions/0026-per-user-installation-and-managed-service.md), [status](status.md))
3. [#45 Recovery from previous/ reinstalls the failed build](https://github.com/ashuangiras/polaroid/issues/45) — **done** ([ADR-0027](../architecture/decisions/0027-install-stages-its-source.md))
4. [#46 Downloadable prerelease verification builds with a repository-independent smoke test](https://github.com/ashuangiras/polaroid/issues/46) — **done** ([ADR-0028](../architecture/decisions/0028-prerelease-verification-builds.md))
5. [#50 Conditional subprocedure references with recorded applicability decisions](https://github.com/ashuangiras/polaroid/issues/50) — **done** ([ADR-0029](../architecture/decisions/0029-conditional-references-and-applicability-decisions.md)), published as `v0.2.0-verify.1`
6. [#52 smoke-test.sh fails one check on macOS when TMPDIR ends in /](https://github.com/ashuangiras/polaroid/issues/52) (fixes #46) — **done**, published as `v0.2.0-verify.2`
7. [#56 Catalog backup, inspection and restore](https://github.com/ashuangiras/polaroid/issues/56) — **done** ([ADR-0030](../architecture/decisions/0030-catalog-backup-and-restore.md)), published as `v0.3.0-verify.1`; schedules, retention and remote copies are not scoped
8. [#59 Restore rolls back while a failed daemon may still have the catalog open](https://github.com/ashuangiras/polaroid/issues/59) (fixes #56) — **done**, published as `v0.3.0-verify.2`
9. [#62 Workflow timing baseline: correct the workflow.md gate table](https://github.com/ashuangiras/polaroid/issues/62) — **done**, measured in [workflow-baseline-2026-10-10.md](workflow-baseline-2026-10-10.md)

## Procedure discovery

1. [#79 Procedure discovery by task and duplicate suggestions (lexical, first increment)](https://github.com/ashuangiras/polaroid/issues/79) — **done** ([ADR-0032](../architecture/decisions/0032-lexical-discovery-and-duplicate-suggestions.md)), published as `v0.4.0-verify.1`
2. [#81 Discovery relevance: rarity, purpose priority and capped instruction matches; release and upgrade; agent guidance](https://github.com/ashuangiras/polaroid/issues/81) — **done** ([ADR-0033](../architecture/decisions/0033-discovery-ranking-rarity-purpose-detail.md)), published as `v0.4.0-verify.1` and installed on the shared instance

## Capability extraction and toil identification

1. [#85 Capability extraction and toil assessment, first increment](https://github.com/ashuangiras/polaroid/issues/85): **done**, records only. It covers the method (`procedure.capability.extract`), one demonstrated capability (`dev.change.classify`, extracted from `polaroid.change.records` v3 and referenced by its v4) and one toil assessment, kept in the extraction execution's evidence ([status](status.md)). No code, schema or API changed.
2. [#87 Dedicated toil records](https://github.com/ashuangiras/polaroid/issues/87): **done** ([ADR-0034](../architecture/decisions/0034-toil-items.md)). Toil items with stable identity, append-only revisions and a checked assessment lifecycle (`observed`, `candidate`, `implemented`, `validated`, `retired`), on HTTP, MCP and the CLI, in schema 9. `procedure.capability.extract` v2 promotes assessments into items, and the #85 assessment is a portable example (`examples/toil/`), status `candidate`. Released builds before it do not offer toil items.
3. [#89 Release and adopt toil items](https://github.com/ashuangiras/polaroid/issues/89): **done**.
   - Published as `v0.5.0-verify.1` (tag on `833627e`).
   - The shared managed instance is upgraded to it, at schema 9.
   - The #85 assessment is promoted into the live catalog as `polaroid.change-classification.manual`, status `candidate`.
   - A fresh Copilot session used the toil tools on the live service.

## Catalog lifecycle

1. [#91 Execution-evidence invalidation and procedure retirement](https://github.com/ashuangiras/polaroid/issues/91): **done** ([ADR-0035](../architecture/decisions/0035-evidence-invalidation-and-procedure-retirement.md)). Append-only lifecycle metadata: an execution is withdrawn as evidence, irreversibly, and stops supporting verification and selection; a procedure is retired and reactivated, leaving lists and discovery and refusing new references while everything that exists keeps resolving. Schema 10.
2. [#93 Release and adopt catalog lifecycle](https://github.com/ashuangiras/polaroid/issues/93): **done**.
   - Published as `v0.6.0-verify.1` (tag on `6a6c835`).
   - The shared managed instance is upgraded to it, at schema 10.
   - The misclassified promotion execution `01a12ac5-7fc7` is invalidated on the live catalog, through the new shared procedure `execution.evidence.invalidate`.
   - A fresh Copilot session used the new tools on the live service.
3. [#96 Read-only catalog statistics](https://github.com/ashuangiras/polaroid/issues/96): **done** (PR #97), released in `v0.7.0-verify.1` (#100) ([ADR-0036](../architecture/decisions/0036-read-only-catalog-statistics.md)). One read-only summary on HTTP, MCP and the CLI: record counts, procedures by lifecycle and scope, executions by outcome and evidence status, toil items by status, logical payload bytes and storage sizes. Visibility only; no schema change.
4. [#98 Size limits on new writes](https://github.com/ashuangiras/polaroid/issues/98): **done** (PR #99), released in `v0.7.0-verify.1` (#100) ([ADR-0037](../architecture/decisions/0037-size-limits-on-new-writes.md)). Byte limits on 37 persisted fields and on each whole write, enforced in the domain for every transport (`413 value_too_large`), reported by the statistics; stored records are never re-checked. Per-write protection only: catalog growth is not bounded.
5. [#100 Release and adopt catalog statistics and write limits](https://github.com/ashuangiras/polaroid/issues/100): **done**.
   - Published as `v0.7.0-verify.1` (tag on `7f4ab44`, the merge of PR #99).
   - The shared managed instance is upgraded to it; the schema stays 10.
   - A fresh Copilot session called `get_catalog_statistics` on the live service.
6. **Next, proposed, not filed:** record the shared catalog's statistics over time to measure growth before any retention design ([status](status.md#next-work-item)).
7. **Planned, not scoped:** physical purge, retention, quotas, cleanup, namespaces, promotion between catalogs, separate databases, and a procedure for promoting earlier assessments that `procedure.capability.extract` would reference.

## Stable release

1. [#110 First stable release](https://github.com/ashuangiras/polaroid/issues/110): **done**. `v1.0.0` (tag on `23b2946`) is a stable release for a local, single-user service on loopback, under the compatibility policy of [ADR-0038](../architecture/decisions/0038-stable-releases-and-compatibility.md). The criteria and evidence are in [release readiness](release-readiness.md). The shared managed instance runs it.
   - #102: the CLI keeps a required argument containing `=`.
   - #104: concurrency and storage failures are tested.
   - #106: the contracts state their formats.
   - #108: stable packaging and the readiness document.
   - #112: commit guidance for recorded runs.
2. [#111](https://github.com/ashuangiras/polaroid/issues/111) Multi-statement reads in read-only snapshot transactions: **done** (PR #117, [ADR-0039](../architecture/decisions/0039-read-only-snapshot-transactions.md)). No contract change.
3. [#118](https://github.com/ashuangiras/polaroid/issues/118) External ItsDangerous acceptance reports with their provenance: **done** (PR #119).
4. [#120 Release `v1.0.1` and adopt it](https://github.com/ashuangiras/polaroid/issues/120): the #111 fix as a patch release.
5. Feedback-driven fixes from the 2026-10-12 triage, filed as issues:
   - [#127 Feedback resolution](https://github.com/ashuangiras/polaroid/issues/127) ([ADR-0040](../architecture/decisions/0040-feedback-resolution.md)): an append-only record of how a report was handled, and a `status` filter so Polaroid can answer what is still open. **Next, in progress.** Filed first and done first, because closing the other issues below is expected to resolve the reports they came from.
   - [#122](https://github.com/ashuangiras/polaroid/issues/122): a registered repository identifier given where an ID is required is reported as "not registered".
   - [#123](https://github.com/ashuangiras/polaroid/issues/123): client setup needs a documented user-level MCP entry and the refresh it needs.
   - [#124](https://github.com/ashuangiras/polaroid/issues/124): `go.module.checks` should state a bare `go` line is a minimum, and that unpinned tools must suit the active toolchain.
   - [#125](https://github.com/ashuangiras/polaroid/issues/125): a way to correct an execution whose run was genuine but whose recorded evidence is wrong.
   - [#126](https://github.com/ashuangiras/polaroid/issues/126): `procedure.capability.extract` has no convention for a promotion-only run.

## Agent-facing context efficiency

1. [#131 Reduce Polaroid's agent-facing context consumption (MCP token usage)](https://github.com/ashuangiras/polaroid/issues/131): **first increment done** ([ADR-0042](../architecture/decisions/0042-context-efficient-projections.md)). A benchmark harness and a measured baseline ([context-efficiency-baseline.md](context-efficiency-baseline.md)), and an additive architecture proposal ([context-efficiency-proposal.md](context-efficiency-proposal.md)), of which two pieces are implemented: a `fields` projection on version reads, and a `format=plan` projection of `resolve_binding`/`get_graph` plus the new `prepare_execution` (39 tools, 25 read-only). Measured warm-run reduction: 79.6% fewer response tokens, exceeding the 70% target, with verification correctness checked by the same benchmark. Conditional fetch of immutable records (`ETag`/`If-None-Match`) is proposed but not yet implemented.
2. Knowledge-first agent mode ([ADR-0043](../architecture/decisions/0043-knowledge-first-agent-mode.md)), same issue and PR #132: `/mcp/agent` with `discover`, `read` and `feedback`; execution recording optional; shared-guidance example. Measured cold success 24,798 → 6,410 tokens including session setup (estimate); one real Copilot CLI run per mode: 7,065 fewer prompt tokens per model call, equivalent work.

## Git-core: living knowledge in Git ([ADR-0044](../architecture/decisions/0044-git-core.md))

One integration branch, `git-core`, and at most one production PR. Each milestone ends with executable acceptance tests.

1. **A — Production Git-core: done on the branch.** `internal/gitcore`: durable, atomic publication under one active ref, concurrency across processes, crash, damage and recovery tests, Git CLI interoperability.
2. **B — Knowledge kernel on Git-core: done on the branch ([ADR-0045](../architecture/decisions/0045-living-knowledge-kernel.md)).** `internal/knowledge` and `internal/knowledge/gitstore`: stable keys, canonical documents, snapshot catalogs and compositions, whole-catalog validation, publication with provenance, rollback, deterministic discovery and recovery from the repository alone, independent of Git types. The daemon and its APIs still serve only the legacy SQLite path.
3. **C — Remote synchronisation: done on the branch ([ADR-0046](../architecture/decisions/0046-git-knowledge-synchronisation.md)).** `internal/knowledgesync` and Git-core's remotes: local, HTTPS and SSH remotes, per-call credentials kept out of URLs, history and errors, bounded fetch into quarantine, Git integrity and whole-catalog validation of every incoming publication, an explicit trust policy, fast-forward-only activation through the kernel with a local activation journal, explicit divergence with both histories kept, and fast-forward-only pushes. A [smoke test](git-remote-smoke.md) checks a hosted remote when the owner supplies one.
4. **D — Agent interface: done on the branch ([ADR-0048](../architecture/decisions/0048-git-native-agent-interface.md)).** `internal/knowledgeops` and `internal/transport/httpops`: `/mcp/agent`'s `discover`, `read` and `feedback` rebuilt on the Git-backed kernel, named only by canonical key and an opaque snapshot token (no procedure version, binding or evidence-based selection); an explicit, token-guarded operator surface at `/v1/knowledge/*` and `polaroid knowledge-*` for status, publish, fetch, validate, activate, push, activations and reconcile; independent, idempotent feedback-to-publication reconciliation. `/mcp` is unaffected (still 40 tools, 25 read-only, 15 write) and `/mcp/agent` stays experimental.
5. **E — Migration and recovery (planned):** a controlled, non-destructive path from the SQLite catalog with audit mapping, recovery and rollback.

## Later (unordered, not yet scoped)

- **The #85 assessment's automation (proposed, not filed):** a committed tool that applies `dev.change.classify` rules mechanically. Its inputs, outputs and acceptance criteria are in the live toil item `polaroid.change-classification.manual` (`candidate`; [#89](https://github.com/ashuangiras/polaroid/issues/89)) ([status](status.md)). Filing it as an issue is a decision for the owner.

- Onboard a second real repository: register it, bind the shared development procedures with its own commands, and verify one of its commits through them. It can rely on the identity, pagination and discovery contracts of #37.
- Discovery beyond #81: reporting where a candidate is used as a subprocedure, search terms declared in records, and semantic matching (needs an ADR and likely a dependency).
- Merging two registered repositories (identity consolidation); aliases exist since #35, but a merge does not.
- Access control for reads and writes ([ADR-0006](../architecture/decisions/0006-local-unauthenticated-api.md)).
- **Import from the earlier Python/SQLite PoC — blocked:** no source database or schema is available in this environment. Compatibility is not claimed; scope the importer only once a real database or schema is provided.
