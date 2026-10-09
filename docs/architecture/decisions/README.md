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
| [0007](0007-repository-identity-for-bindings.md) | Repositories are identified by a canonical path, and bindings by repository and local name | Accepted |
| [0008](0008-subprocedure-references.md) | Subprocedure references are stored relationally and written only with their version | Accepted |
| [0009](0009-reference-graph-rules.md) | Reference graphs are acyclic per procedure, bounded, and resolve contextual references to the latest version | Accepted |
| [0010](0010-execution-records.md) | Executions are immutable after-the-fact records with a named environment and inline evidence | Accepted |
| [0011](0011-subprocedure-executions.md) | A parent execution links its already-recorded children, one per reference, from the same repository and commit | Accepted |
| [0012](0012-derived-verification.md) | Verification is derived on read from executions, per combination, and the latest execution decides | Accepted |
| [0013](0013-evidence-based-resolution.md) | Contextual references resolve from evidence in a repository and environment, following verified combinations | Accepted |
| [0014](0014-mcp-transport.md) | MCP is served over stateless streamable HTTP at /mcp, with flat tool arguments and the API's record shapes | Accepted; protocol clause superseded by ADR-0016 |
| [0015](0015-feedback-reports.md) | Feedback reports are immutable, untriaged records about Polaroid itself | Accepted |
| [0016](0016-mcp-protocol-2025-11-25.md) | /mcp also accepts protocol 2025-11-25, through the handshake but without sessions | Accepted |
| [0017](0017-development-procedures-as-records.md) | Polaroid's development procedures are records, loaded from fixtures by canonical key | Accepted |

Template: a title `# NNNN. Decision`, then **Status**, **Date**, and the sections **Context**, **Decision** and **Consequences**. Keep each record under a page.
