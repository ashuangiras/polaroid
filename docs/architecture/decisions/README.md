# Architecture decision records

Short records of consequential choices. An accepted ADR is never edited in substance. To change a decision, add a new ADR that supersedes it, and mark the old one `Superseded by ADR-NNNN`.

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](0001-single-module-layered-packages.md) | One Go module with layered internal packages | Accepted |
| [0002](0002-go-toolchain-and-provisional-module-path.md) | Go 1.27.1 minimum, the Go 1.27.2 toolchain, and the provisional module path `example.com/polaroid` (since replaced by `github.com/ashuangiras/polaroid`) | Accepted |
| [0003](0003-sqlite-with-modernc-driver.md) | SQLite through the pure-Go `modernc.org/sqlite` driver | Accepted |
| [0004](0004-append-only-versions-with-expected-base.md) | Append-only versions guarded by an expected base version | Accepted |
| [0005](0005-strict-json-with-encoding-json-v2.md) | Strict JSON decoding with `encoding/json/v2` | Accepted |
| [0006](0006-local-unauthenticated-api.md) | A loopback-only, unauthenticated API until access control exists | Accepted |
| [0007](0007-repository-identity-for-bindings.md) | Repositories are identified by a canonical path, and bindings by repository and local name | Accepted; repository records added by ADR-0019 |
| [0008](0008-subprocedure-references.md) | Subprocedure references are stored relationally and written only with their version | Accepted |
| [0009](0009-reference-graph-rules.md) | Reference graphs are acyclic per procedure, bounded, and resolve contextual references to the latest version | Accepted |
| [0010](0010-execution-records.md) | Executions are immutable after-the-fact records with a named environment and inline evidence | Accepted |
| [0011](0011-subprocedure-executions.md) | A parent execution links its already-recorded children, one per reference, from the same repository and commit | Accepted |
| [0012](0012-derived-verification.md) | Verification is derived on read from executions, per combination, and the latest execution decides | Accepted |
| [0013](0013-evidence-based-resolution.md) | Contextual references resolve from evidence in a repository and environment, following verified combinations | Accepted; reporting extended by ADR-0018 |
| [0014](0014-mcp-transport.md) | MCP is served over stateless streamable HTTP at /mcp, with flat tool arguments and the API's record shapes | Accepted; protocol clause superseded by ADR-0016 |
| [0015](0015-feedback-reports.md) | Feedback reports are immutable, untriaged records about Polaroid itself | Accepted; subjects added by ADR-0021, resolution by ADR-0040 |
| [0016](0016-mcp-protocol-2025-11-25.md) | /mcp also accepts protocol 2025-11-25, through the handshake but without sessions | Accepted |
| [0017](0017-development-procedures-as-records.md) | Polaroid's development procedures are records, loaded from fixtures by canonical key | Accepted |
| [0018](0018-selection-evidence-and-target-verification.md) | Resolution separates selection evidence from target verification | Accepted |
| [0019](0019-repository-registry.md) | Repositories are registered records with a canonical identifier and explicit aliases | Accepted |
| [0020](0020-procedure-origin-and-applicability.md) | Procedure origin is set once on the identity; applicability is declared per version | Accepted |
| [0021](0021-targeted-feedback-and-bounded-lists.md) | Feedback has a typed subject, and lists are filterable with opt-in keyset pagination | Accepted |
| [0022](0022-repository-identity-in-evidence.md) | Evidence matches by registered repository identity | Accepted |
| [0023](0023-pagination-guarantees-and-scope-labels.md) | Lists are live traversals with bound cursors; discovery can page a snapshot; every version names its scope | Accepted |
| [0024](0024-child-inputs-follow-the-reference-mapping.md) | A child execution must have run with the inputs its reference maps | Accepted |
| [0025](0025-per-user-default-database.md) | The default database is per-user, in ~/.polaroid | Accepted |
| [0026](0026-per-user-installation-and-managed-service.md) | Per-user installation and a managed service: launchd on macOS, systemd user services on Linux | Accepted |
| [0027](0027-install-stages-its-source.md) | Install stages its source first; binary recovery is separate from data restore | Accepted |
| [0028](0028-prerelease-verification-builds.md) | Prerelease verification builds published from tags on main | Accepted |
| [0029](0029-conditional-references-and-applicability-decisions.md) | Conditional references and recorded applicability decisions | Accepted |
| [0030](0030-catalog-backup-and-restore.md) | Catalog backup, inspection and restore with the packaged binaries | Accepted |
| [0031](0031-scoped-verification-and-local-only-ci.md) | Scoped verification procedures, work done once, and local-only hosted CI | Accepted |
| [0032](0032-lexical-discovery-and-duplicate-suggestions.md) | Lexical procedure discovery by task, and advisory duplicate suggestions | Accepted; ranking replaced by 0033 |
| [0033](0033-discovery-ranking-rarity-purpose-detail.md) | Discovery ranks by term rarity, stated purpose and bounded detail | Accepted |
| [0034](0034-toil-items.md) | Toil items: versioned assessments of repetitive work, with an assessment lifecycle | Accepted |
| [0035](0035-evidence-invalidation-and-procedure-retirement.md) | Execution-evidence invalidation and procedure retirement | Accepted |
| [0036](0036-read-only-catalog-statistics.md) | Read-only catalog statistics | Accepted |
| [0037](0037-size-limits-on-new-writes.md) | Size limits on new writes | Accepted |
| [0038](0038-stable-releases-and-compatibility.md) | Stable releases and the compatibility policy | Accepted |
| [0039](0039-read-only-snapshot-transactions.md) | Multi-statement reads use read-only snapshot transactions | Accepted |
| [0040](0040-feedback-resolution.md) | Feedback resolution: an append-only record of how a report was handled | Accepted |
| [0041](0041-verification-artifacts.md) | Verification artifacts: a lightweight, local, gitignored build for independent review, never implying a release | Accepted |
| [0042](0042-context-efficient-projections.md) | Context-efficient projections: `fields` on a version read, `format=plan` and `prepare_execution` | Accepted |
| [0043](0043-knowledge-first-agent-mode.md) | Knowledge-first agent mode at `/mcp/agent`: discover, read, feedback; execution recording optional | Accepted (finalized by ADR-0049) |
| [0044](0044-git-core.md) | Git-core: a Git repository as the durable store of living knowledge | Accepted (milestones A–C) |
| [0045](0045-living-knowledge-kernel.md) | The living knowledge kernel on Git-core | Accepted (milestone B) |
| [0046](0046-git-knowledge-synchronisation.md) | Git-native knowledge synchronisation | Accepted (milestone C) |
| [0047](0047-knowledge-identities-and-origin.md) | Knowledge identities and origin, the foundations of the Git-backed agent interface | Accepted (milestone D foundations) |
| [0048](0048-git-native-agent-interface.md) | Git-native agent interface, finalized: `/mcp/agent` on living knowledge, and an explicit operator surface at `/v1/knowledge` | Accepted (milestone D; finalized by ADR-0049) |
| [0049](0049-agent-mode-finalized-no-migration.md) | `/mcp/agent` finalized under ADR-0038's v1 guarantee; milestone E does not migrate the legacy SQLite catalog | Accepted (milestone E; amended by ADR-0051) |
| [0050](0050-corpus-logic.md) | Corpus Logic v1: typed knowledge records and bounded reasoning on Git-native knowledge | Accepted |
| [0051](0051-agent-mode-discover-kinds.md) | `discover` gains a `kind` selector: procedure matching (default, unchanged), corpus statistics and a tool list | Accepted |

Template: a title `# NNNN. Decision`, then **Status**, **Date**, and the sections **Context**, **Decision** and **Consequences**. Keep each record under a page.
