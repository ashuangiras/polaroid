# MCP server

`polaroidd` serves the [Model Context Protocol](https://modelcontextprotocol.io) at **`/mcp`**, beside the [HTTP API](http-api.md) and on the same listener. The default URL is `http://127.0.0.1:7417/mcp`. It exposes the same operations as tools and resources. Decisions are in [ADR-0014](decisions/0014-mcp-transport.md).

## Transport

- **Protocol:** streamable HTTP, revisions **2026-07-28** and **2025-11-25** ([ADR-0016](decisions/0016-mcp-protocol-2025-11-25.md)). 2026-07-28 is sessionless by design. A 2025-11-25 client begins with the `initialize` handshake, but it gets no session either: each POST is a complete request at both revisions, with identical tools, results and errors.
- **Older clients:** a request naming an older revision is refused with `400 Unsupported protocol version`. An older `initialize` is answered with 2025-11-25, and a client that cannot speak it disconnects.
- **Responses:** JSON (`application/json`), never SSE. There is no `Mcp-Session-Id`, and `GET` and `DELETE` are not used.
- **Limits and security:** bodies are limited to 1 MiB. `/mcp` sits behind the same loopback `Host` check and cross-origin protection as the API, with no authentication ([ADR-0006](decisions/0006-local-unauthenticated-api.md)).
- **Caching:** every `server/discover`, list and `resources/read` result carries `ttlMs: 0`, the protocol's cache hint (SEP-2549) for "immediately stale". The tools change when `polaroidd` is upgraded and records change with every write, so a client must not answer from a cached result. `serverInfo.version` is informational and stays `v1`; it does not change with the tool catalogue.
- **Server capabilities:** `tools` and `resources`, plus short instructions that describe the agent loop. They tell agents to find their repository's bindings and the procedures that may fit their task (`discover_procedures` with the task and `repository`: lexical matches with explanations, latest versions only, relevance judged by the agent; `list_procedures` with `repository` for everything that applies there; a listed scope is the latest version's, and unspecified declares nothing), that evidence under any identifier of a registered repository counts for all of them ([ADR-0022](decisions/0022-repository-identity-in-evidence.md)), that every graph node names its version's own scope, to be read with that version's contract before reuse, that `selection_evidence` explains why a version was selected and does not verify their checkout, that `commit` and `inputs` give `target_verification` for the exact run, and that an unverified target still has to be run and recorded ([ADR-0018](decisions/0018-selection-evidence-and-target-verification.md)). Children are recorded first, each with the inputs its reference maps from the parent's ([ADR-0024](decisions/0024-child-inputs-follow-the-reference-mapping.md)). A reference with a condition applies only when it holds: agents decide each one themselves, record a decision for each, and pass their decisions for target verification ([ADR-0029](decisions/0029-conditional-references-and-applicability-decisions.md)). Lists continue with the same arguments, and the procedure, binding and toil item lists can page a snapshot ([ADR-0023](decisions/0023-pagination-guarantees-and-scope-labels.md)). New procedures declare applicability and origin, and agents check a proposal with `suggest_duplicates` before creating or revising one, preferring to reuse or revise an overlapping procedure ([ADR-0032](decisions/0032-lexical-discovery-and-duplicate-suggestions.md)). The last step asks agents to check `list_feedback` with `status` `open` first, then report problems with Polaroid, and suggestions for it, with `report_feedback`, naming the subject, and to resolve a report they addressed with `resolve_feedback` ([ADR-0015](decisions/0015-feedback-reports.md), [ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md), [ADR-0040](decisions/0040-feedback-resolution.md)). A final step asks agents who see repetitive, mechanical work to look for an existing toil item with `list_toil_items` before creating one, to revise it when they observe the activity again, to cite exact versions and executions, to keep observed facts apart from hypotheses, and to treat its status as their assessment, not verification ([ADR-0034](decisions/0034-toil-items.md)).

## Tools

Arguments are flat JSON objects, named after the record fields in [records.md](records.md).

- **Decoding is strict, as for HTTP request bodies.** Unknown or duplicate members are rejected, and so are values of the wrong JSON type. Free-form objects (`contract`, `instructions`, `inputs`, `evidence`, `environment.attributes`, reference `inputs`, feedback `context`, toil `assessment` and `proposal`) are stored with their member order intact, as Polaroid receives it. MCP clients may reorder members before sending (VS Code Copilot chat does), so keep order-sensitive data in arrays ([records.md](records.md#procedure-version-implemented)).
- **Schemas:** every tool advertises an input schema generated from its argument type.
- **Contract:** this table, and the argument names, JSON types, nesting and requiredness in [`internal/transport/mcp/testdata/full-mode-tools.json`](../../internal/transport/mcp/testdata/full-mode-tools.json), are the v1 contract of `/mcp` ([ADR-0038](decisions/0038-stable-releases-and-compatibility.md)). Tests compare the served schemas with that file, and that file with this table, so any change to a tool's arguments must be made deliberately in both.

| Tool | Read-only | Arguments | Result |
| --- | --- | --- | --- |
| `list_procedures` | yes | `repository`, `scope`, `q`, `lifecycle` (`active` by default, `retired` or `all`), `limit`, `after` and `snapshot`, all optional | `{"procedures":[…]}`, with `next` when more remain |
| `discover_procedures` | yes | `task`; `repository`, `lifecycle` and `limit` optional | [discovery](http-api.md#discover-procedures-for-a-task): ranked candidates with `score`, `matched_terms`, per-term `contributions` and per-field `matches`, with the corpus `considered`, each term's `rarity` and the `detail_cap` |
| `suggest_duplicates` | yes | `method`, `philosophy`; `goal`, `canonical_key`, `repository`, `exclude_procedure_id` and `limit` optional | [duplicates](http-api.md#check-a-proposal-for-duplicates): `key_collision` when the key exists, and advisory `suggestions` with `similarity`, retired procedures included |
| `get_procedure` | yes | `id` or `canonical_key` (exactly one) | procedure history |
| `get_version` | yes | `procedure_id`, `version`; `fields`, optional, a comma-separated subset of its top-level members for [a compact projection](http-api.md#selective-version-projection-fields) instead of the complete version | version, or a version projection |
| `get_graph` | yes | `procedure_id`, `version`; `repository` and `environment` together, optional; `commit` and `inputs` together, optional, with a context; `decisions`, optional, with a target; `format`, optional (`full`, the default, or `plan` for [a compact, dependency-ordered projection](http-api.md#compact-execution-plans-formatplan-and-prepare_execution)) | graph node, [resolved from evidence](records.md#contextual-resolution-implemented) when given a context, with [target verification](records.md#selection-evidence-and-target-verification-implemented) when given a target; or a plan with `format=plan` |
| `create_procedure` | no | `canonical_key`, `origin` (optional), `philosophy`, `method`, `goal` and `applicability` (optional), `contract`, `instructions`, `references` (optional), `revision_reason` | procedure history |
| `revise_procedure` | no | `procedure_id`, `base_version`, then the same version fields | version |
| `record_procedure_origin` | no | `procedure_id`, `repository_id`, `reason` | procedure history |
| `register_repository` | no | `identifier`, `name` | repository |
| `add_repository_alias` | no | `repository_id`, `identifier`, `reason` | repository |
| `get_repository` | yes | `id` or `identifier` (exactly one; canonical or alias) | repository |
| `list_repositories` | yes | `limit` and `after`, optional | `{"repositories":[…]}`, with `next` when more remain |
| `list_bindings` | yes | `repository`; `limit`, `after` and `snapshot` optional | `{"bindings":[…]}`, with `next` when more remain |
| `get_binding` | yes | `id` | binding history |
| `get_binding_revision` | yes | `binding_id`, `revision` | binding revision |
| `resolve_binding` | yes | `binding_id`, `environment`; `commit` and `inputs` together, optional; `decisions`, optional, with a target; `format`, optional (`full` or `plan`, as `get_graph`'s) | binding resolution, with target verification when given a target; or a plan with `format=plan` |
| `prepare_execution` | yes | `binding_id`, `environment`, `commit`, `inputs` (all required); `decisions`, optional | [a compact execution plan](http-api.md#compact-execution-plans-formatplan-and-prepare_execution): `resolve_binding` with `format=plan` and its target required |
| `create_binding` | no | `repository`, `name`, `procedure_id`, `inputs`, `version_policy`, `revision_reason` | binding history |
| `revise_binding` | no | `binding_id`, `base_revision`, `inputs`, `version_policy`, `revision_reason` | binding revision |
| `record_execution` | no | the execution fields: `procedure_id`, `version`, `binding_id` and `binding_revision` (optional), `repository`, `commit`, `environment`, `inputs`, `outcome`, `evidence`, `children` (optional), `decisions` (optional) | execution |
| `get_execution` | yes | `id` | execution |
| `list_executions` | yes | `procedure_id`, `version` (with `procedure_id`), `repository`, `commit`, `evidence_status` (`valid` or `invalidated`), `limit` and `after`, all optional | `{"executions":[…]}`, with `next` when more remain |
| `get_verification` | yes | `execution_id` | verification |
| `list_verifications` | yes | `procedure_id`, `version`; `repository`, `commit` and `environment` optional | `{"verifications":[…]}` |
| `report_feedback` | no | `kind`, `summary`, `details`, `reporter`; `context`, `subject`, `repository` and `execution_id` optional | feedback report |
| `list_feedback` | yes | `kind`, `subject_type`, `subject_id`, `subject_version`, `repository`, `status` (`open` or `resolved`), `limit` and `after`, all optional | `{"feedback":[…]}`, with `next` when more remain |
| `get_feedback` | yes | `id` | feedback report |
| `resolve_feedback` | no | `feedback_id`, `outcome` (`addressed`, `declined`, `not_reproduced` or `duplicate`), `reason`, `actor`; `references` optional | feedback resolution |
| `list_toil_items` | yes | `status`, `repository`, `procedure_id`, `limit`, `after` and `snapshot`, all optional | `{"toil_items":[…]}`, with `next` when more remain |
| `get_toil_item` | yes | `id` or `canonical_key` (exactly one) | toil item history |
| `get_toil_revision` | yes | `toil_item_id`, `revision` | toil revision |
| `create_toil_item` | no | `canonical_key`, then the revision fields: `activity`, `applicability`, `sources`, `observations` (optional), `assessment`, `proposal` (optional), `status`, `status_rationale`, `replacement` and `validation` (optional), `revision_reason`, `author` | toil item history |
| `revise_toil_item` | no | `toil_item_id`, `base_revision`, then the same revision fields | toil revision |
| `invalidate_execution` | no | `execution_id`, `reason`, `actor` | [invalidation state](http-api.md#execution-evidence-invalidation) |
| `get_execution_invalidation` | yes | `execution_id` | invalidation state |
| `get_procedure_lifecycle` | yes | `procedure_id` | [lifecycle](http-api.md#procedure-lifecycle) |
| `retire_procedure` | no | `procedure_id`, `base_event`, `reason`, `actor` | lifecycle |
| `reactivate_procedure` | no | `procedure_id`, `base_event`, `reason`, `actor` | lifecycle |
| `get_catalog_statistics` | yes | none | [statistics](http-api.md#catalog-statistics) |

There are 40 tools: 25 read-only, and 15 that only append. The repository, scope and paging rules are those of the HTTP API ([ADR-0019](decisions/0019-repository-registry.md), [ADR-0020](decisions/0020-procedure-origin-and-applicability.md), [ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md)); discovery and duplicate suggestions are lexical and store nothing ([ADR-0032](decisions/0032-lexical-discovery-and-duplicate-suggestions.md)); toil items follow [the HTTP rules](http-api.md#toil-items) ([ADR-0034](decisions/0034-toil-items.md)); invalidation and retirement follow [ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md): an invalidation is permanent and changes no record, and a retirement is reversed only by a reactivation. `get_catalog_statistics` returns the document of `GET /v1/statistics` and stores nothing ([ADR-0036](decisions/0036-read-only-catalog-statistics.md)); any argument is `invalid_request`. Every write tool applies the HTTP API's [write limits](http-api.md#write-limits) ([ADR-0037](decisions/0037-size-limits-on-new-writes.md)): a value over its limit is a `value_too_large` tool error whose `limits` name the flat argument (`contract`, not `version.contract`), its size and its limit; `get_catalog_statistics` lists the limits under `write_limits`, with the HTTP field paths.


### Results

A successful result carries the record exactly as the HTTP API returns it ([http-api.md](http-api.md)), in two places:

- as structured content;
- as one text item holding the same JSON, with the server's member order.

### Errors

A failed call is a tool error (`isError: true`). It carries the HTTP API's error body, `{"error":{"code","message",…}}`, as text and as structured content, with the same codes: `invalid_request`, `not_found`, `version_conflict` (with `latest_version`), `revision_conflict` (with `latest_revision`), `canonical_key_exists`, `binding_exists`, `repository_identifier_exists`, `origin_exists`, `reference_cycle` (with `cycle`), `graph_too_large`, `execution_invalidated`, `lifecycle_conflict` (with `latest_event`), `lifecycle_unchanged`, `procedure_retired`, `value_too_large` (with `limits`) and `internal`.

Field names in `fields` are the flat argument names. For example, a missing `philosophy` is reported as `philosophy`, not `version.philosophy`, and a reference to a retired procedure as `references[0].procedure_id`. A reference is still reported as `references[i]…`. An unexpected failure is logged, and only `internal` is returned.

## Agent mode (`/mcp/agent`)

**Finalized, under the v1 compatibility guarantee** ([ADR-0038](decisions/0038-stable-releases-and-compatibility.md), amended by [ADR-0043](decisions/0043-knowledge-first-agent-mode.md), built on Git-native living knowledge by [ADR-0048](decisions/0048-git-native-agent-interface.md), finalized by [ADR-0049](decisions/0049-agent-mode-finalized-no-migration.md)): the same stability promise as `/mcp` applies to its three tools, their arguments and their result shapes.

A second endpoint on the same listener, with the same transport rules, serves only the three tools everyday procedural-knowledge use needs ([ADR-0043](decisions/0043-knowledge-first-agent-mode.md)). A client connected to it never loads the full API's schemas, and nothing it does records an execution. It is backed solely by living knowledge in a separate, Git-native store ([ADR-0045](decisions/0045-living-knowledge-kernel.md)), named only by canonical key and an opaque snapshot token: there is no procedure version, binding, or evidence-based selection anywhere in this mode, and it shares no record with `/mcp`'s procedures, versions, bindings, executions or verifications.

| Tool | Read-only | Arguments | Result |
| --- | --- | --- | --- |
| `discover` | yes | `task`; `repository` and `limit` (1 to 50, default 5) optional | `{"snapshot", "candidates":[{key, goal, score}]}`, best first; `{"empty": true, "message"}` when nothing has been published or activated yet (a normal result, not an error) |
| `read` | yes | `key`; `snapshot` (default: the active knowledge), `repository` (checked against applicability), `inputs` (an object) and `full` optional | guidance: `snapshot`, `key`, `scope`, `goal`, `method`, `contract`, `instructions`, `references` `[{name, key, goal, condition, inputs or input_mapping}]`, resolved only one level (read a reference's own `key` for its references); with `full`, also `philosophy` |
| `feedback` | no | `key`, `snapshot` (the one `read` or `discover` gave), `classification`, `summary`, `reporter`; `details`, `repository` and `evidence` (an object) optional | `{id, classification, created_at}`; the stored report is an ordinary [feedback report](http-api.md#feedback), with no subject, whose `context` carries `{classification, evidence}` and, under a reserved `knowledge` member, `{key, snapshot}` |

- **Snapshots pin knowledge.** A snapshot token from `discover` or an earlier `read` always reads exactly that published state, however much is published or activated later; omit it to read the active knowledge.
- `feedback` classifications: `incorrect_applicability`, `missing_precondition`, `incorrect_instruction`, `inadequate_verification` (kind `problem`) and `improved_method` (kind `suggestion`). `product_defect`, `environment_problem` and `transient_failure` are `invalid_request`: they are not procedural knowledge. Before recording, the daemon confirms the pair is real: a snapshot that never served `key`, or a key never published, is `not_found`.
- A read with `repository` set and the procedure's applicability local to another repository is `not_applicable`.
- Errors use the same envelope as the full API's tool errors, with these codes: `invalid_request`, `not_found`, `not_applicable`.
- **Publishing, synchronising and reconciling knowledge is an explicit operator operation**, through `/v1/knowledge/*` ([http-api.md](http-api.md#git-native-knowledge-operator-api)) or `polaroid knowledge-*`, never through agent mode: there is no scheduler, and an agent only discovers, reads and reports feedback.
- **Migrating an agent:** point it at `http://127.0.0.1:7417/mcp/agent` instead of `/mcp`. Keep `/mcp` for administration, execution and verification records, and audit workflows; its 40 tools are unchanged. No legacy SQLite procedure is migrated into Git-native knowledge ([ADR-0049](decisions/0049-agent-mode-finalized-no-migration.md)): the two catalogs are populated independently.

## Agent mode v2 (`/mcp/agent/v2`)

**Proposed with Corpus Logic v1** ([ADR-0050](decisions/0050-corpus-logic.md)). This is a separate, versioned endpoint; `/mcp/agent` above is unchanged.

It serves the same `discover`, `read` and `feedback` tools, with input schemas identical to v1's (a test compares them), plus one read-only tool:

| Tool | Read-only | Arguments | Result |
| --- | --- | --- | --- |
| `resolve` | yes | `goal` (a record key: a procedure, concept or claim); `snapshot` (default: the active knowledge), `context` (an object of string name and value pairs), `then` (a procedure key to compose after the goal) and `at` (RFC 3339; default now) optional | `{version, snapshot, goal, at, complete, incomplete, applicable, specializations, obligations, derived, exceptions, conflicts, unknowns, evidence, superseded, composition}` |

The result members mean:

- **`complete`**: whether every conclusion the missing context could add was examined. When it is false, **`incomplete`** says what could not be enumerated: a rule that reads a context name or value it does not fix, or more than 64 combinations of missing values. Unknowns are then not exhaustive.
- **`obligations`**: each requirement with every independent derivation `{id, source, source_type, via, path, status, exception}`. The status is `required` while any derivation is active, otherwise `waived`.
- **`exceptions`**: each exception that concerns the goal, with its status: `applied`, `rejected`, `not_applicable` or `conflicting`, and a reason.
- **`conflicts`**: requirements that contradict each other, conflicting exceptions, and `undefined_requirement` for a derived requirement that is not a concept.
- **`unknowns`**: each conclusion that holds only for context the caller did not give, `{kind, requirement or predicate and object, source, when, reason}`. `kind` is `obligation` or `derived`; `when` lists the `{name, value}` pairs it needs.
- **`evidence`**: each claim's evidence status (`supported`, `contradicted`, `contested`, `unsupported`). It summarises the evidence and is never a verdict.
- **`composition`**, with `then`: `{verdict, producer, consumer, findings}`. The verdict is `compatible`, `incompatible` or `undetermined`; `producer` and `consumer` are the two formal contracts (inputs, outputs, whether the outputs are complete, pre-, post-conditions, invariants, informal conditions); each finding covers one consumer input, precondition or informal condition, and those not `satisfied` or `established` are the missing prerequisites. Composition reads only the snapshot, never `context`.

`context` is what the caller asserts. It selects which rules apply, but never authorises an exception: project-scoped exceptions and exception conditions need context the server established itself, and this endpoint establishes none. They are reported `not_applicable`, with a reason that says when the caller asserted a matching value. Protected invariants are never waived, at any authority.

Resolution is deterministic. The same snapshot, `context`, `at` and trust material give byte-identical results. A derivation `id` is the first 16 bytes, in hex, of SHA-256 over the reasoning version, the snapshot, the goal and the derivation's parts, joined by NUL bytes ([ADR-0050](decisions/0050-corpus-logic.md)); anyone holding the snapshot can recompute it. It identifies the derivation, not its status.

Requests can fail with these codes:

- `not_found`: the goal is not a record of the snapshot or, with `then`, the goal or `then` is not a procedure of it.
- `invalid_request`.
- `budget_exceeded` (HTTP 422): the evaluation exceeded its time or fact limits. A budget failure never returns a partial result.

Exceptions are verified against the operator-held trust material at `<knowledge-dir>-authorities.json` ([http-api.md](http-api.md#git-native-knowledge-operator-api)).

## Resources

All resources are read-only and returned as `application/json`, with the same body as the matching tool.

| URI template | Same as |
| --- | --- |
| `polaroid://procedures/{id}` | `get_procedure` with `id` |
| `polaroid://procedures/{id}/versions/{version}` | `get_version` |
| `polaroid://bindings/{id}` | `get_binding` |

An unknown record, or a version that is not a number, gets the protocol's resource-not-found error.

## Connecting a client

Start the daemon (`bin/polaroidd`), then point the client at the URL. This repository ships the VS Code configuration in `.vscode/mcp.json`; for another workspace, add the same file:

```json
{"servers": {"polaroid": {"type": "http", "url": "http://127.0.0.1:7417/mcp"}}}
```

**After upgrading `polaroidd`, restart the server in the client.** Some clients cache the tool list despite `ttlMs: 0`. VS Code Copilot chat (VS Code 1.137) keeps it until **MCP: List Servers → polaroid → Restart**, and the new tools appear from the next chat request on.

Verified clients (2026-10-09):

| Client | Result |
| --- | --- |
| VS Code Copilot chat (VS Code 1.137, bundled `@github/copilot` runtime) | Works: the 17 tools of the time discovered, full workflow run from chat. |
| Go SDK v1.8.0 client | Works; used by the automated tests. |
| TypeScript SDK 1.32.1, MCP Inspector 2.10.1 | Work at 2025-11-25 since ADR-0016: 20 tools listed, tools called, no session. Before it they were refused. |

Other clients that support streamable HTTP take the same URL. The client must support protocol revision 2026-07-28 or 2025-11-25.
