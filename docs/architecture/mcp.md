# MCP server

`polaroidd` serves the [Model Context Protocol](https://modelcontextprotocol.io) at **`/mcp`**, beside the [HTTP API](http-api.md) and on the same listener. The default URL is `http://127.0.0.1:7417/mcp`. It exposes the same operations as tools and resources. Decisions are in [ADR-0014](decisions/0014-mcp-transport.md).

## Transport

- **Protocol:** streamable HTTP, revisions **2026-07-28** and **2025-11-25** ([ADR-0016](decisions/0016-mcp-protocol-2025-11-25.md)). 2026-07-28 is sessionless by design. A 2025-11-25 client begins with the `initialize` handshake, but it gets no session either: each POST is a complete request at both revisions, with identical tools, results and errors.
- **Older clients:** a request naming an older revision is refused with `400 Unsupported protocol version`. An older `initialize` is answered with 2025-11-25, and a client that cannot speak it disconnects.
- **Responses:** JSON (`application/json`), never SSE. There is no `Mcp-Session-Id`, and `GET` and `DELETE` are not used.
- **Limits and security:** bodies are limited to 1 MiB. `/mcp` sits behind the same loopback `Host` check and cross-origin protection as the API, with no authentication ([ADR-0006](decisions/0006-local-unauthenticated-api.md)).
- **Caching:** every `server/discover`, list and `resources/read` result carries `ttlMs: 0`, the protocol's cache hint (SEP-2549) for "immediately stale". The tools change when `polaroidd` is upgraded and records change with every write, so a client must not answer from a cached result. `serverInfo.version` is informational and stays `v1`; it does not change with the tool catalogue.
- **Server capabilities:** `tools` and `resources`, plus short instructions that describe the agent loop. They tell agents to find their repository's bindings and the procedures that apply there (`list_procedures` with `repository`; a listed scope is the latest version's, and unspecified declares nothing), that evidence under any identifier of a registered repository counts for all of them ([ADR-0022](decisions/0022-repository-identity-in-evidence.md)), that every graph node names its version's own scope, to be read with that version's contract before reuse, that `selection_evidence` explains why a version was selected and does not verify their checkout, that `commit` and `inputs` give `target_verification` for the exact run, and that an unverified target still has to be run and recorded ([ADR-0018](decisions/0018-selection-evidence-and-target-verification.md)). Children are recorded first, each with the inputs its reference maps from the parent's ([ADR-0024](decisions/0024-child-inputs-follow-the-reference-mapping.md)). Lists continue with the same arguments, and the procedure and binding lists can page a snapshot ([ADR-0023](decisions/0023-pagination-guarantees-and-scope-labels.md)). New procedures declare applicability and origin. The last step asks agents to report problems with Polaroid, and suggestions for it, with `report_feedback`, naming the subject ([ADR-0015](decisions/0015-feedback-reports.md), [ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md)).

## Tools

Arguments are flat JSON objects, named after the record fields in [records.md](records.md).

- **Decoding is strict, as for HTTP request bodies.** Unknown or duplicate members are rejected, and so are values of the wrong JSON type. Free-form objects (`contract`, `instructions`, `inputs`, `evidence`, `environment.attributes`, reference `inputs`, feedback `context`) are stored with their member order intact, as Polaroid receives it. MCP clients may reorder members before sending (VS Code Copilot chat does), so keep order-sensitive data in arrays ([records.md](records.md#procedure-version-implemented)).
- **Schemas:** every tool advertises an input schema generated from its argument type.

| Tool | Read-only | Arguments | Result |
| --- | --- | --- | --- |
| `list_procedures` | yes | `repository`, `scope`, `q`, `limit`, `after` and `snapshot`, all optional | `{"procedures":[…]}`, with `next` when more remain |
| `get_procedure` | yes | `id` or `canonical_key` (exactly one) | procedure history |
| `get_version` | yes | `procedure_id`, `version` | version |
| `get_graph` | yes | `procedure_id`, `version`; `repository` and `environment` together, optional; `commit` and `inputs` together, optional, with a context | graph node, [resolved from evidence](records.md#contextual-resolution-implemented) when given a context, with [target verification](records.md#selection-evidence-and-target-verification-implemented) when given a target |
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
| `resolve_binding` | yes | `binding_id`, `environment`; `commit` and `inputs` together, optional | binding resolution, with target verification when given a target |
| `create_binding` | no | `repository`, `name`, `procedure_id`, `inputs`, `version_policy`, `revision_reason` | binding history |
| `revise_binding` | no | `binding_id`, `base_revision`, `inputs`, `version_policy`, `revision_reason` | binding revision |
| `record_execution` | no | the execution fields: `procedure_id`, `version`, `binding_id` and `binding_revision` (optional), `repository`, `commit`, `environment`, `inputs`, `outcome`, `evidence`, `children` (optional) | execution |
| `get_execution` | yes | `id` | execution |
| `list_executions` | yes | `procedure_id`, `version` (with `procedure_id`), `repository`, `commit`, `limit` and `after`, all optional | `{"executions":[…]}`, with `next` when more remain |
| `get_verification` | yes | `execution_id` | verification |
| `list_verifications` | yes | `procedure_id`, `version`; `repository`, `commit` and `environment` optional | `{"verifications":[…]}` |
| `report_feedback` | no | `kind`, `summary`, `details`, `reporter`; `context`, `subject`, `repository` and `execution_id` optional | feedback report |
| `list_feedback` | yes | `kind`, `subject_type`, `subject_id`, `subject_version`, `repository`, `limit` and `after`, all optional | `{"feedback":[…]}`, with `next` when more remain |
| `get_feedback` | yes | `id` | feedback report |

There are 25 tools: 16 read-only, and 9 that only append. The repository, scope and paging rules are those of the HTTP API ([ADR-0019](decisions/0019-repository-registry.md), [ADR-0020](decisions/0020-procedure-origin-and-applicability.md), [ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md)).

### Results

A successful result carries the record exactly as the HTTP API returns it ([http-api.md](http-api.md)), in two places:

- as structured content;
- as one text item holding the same JSON, with the server's member order.

### Errors

A failed call is a tool error (`isError: true`). It carries the HTTP API's error body, `{"error":{"code","message",…}}`, as text and as structured content, with the same codes: `invalid_request`, `not_found`, `version_conflict` (with `latest_version`), `revision_conflict`, `canonical_key_exists`, `binding_exists`, `repository_identifier_exists`, `origin_exists`, `reference_cycle`, `graph_too_large` and `internal`.

Field names in `fields` are the flat argument names. For example, a missing `philosophy` is reported as `philosophy`, not `version.philosophy`. A reference is still reported as `references[i]…`. An unexpected failure is logged, and only `internal` is returned.

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
