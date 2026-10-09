# MCP server

`polaroidd` serves the [Model Context Protocol](https://modelcontextprotocol.io) at **`/mcp`**, beside the [HTTP API](http-api.md) and on the same listener. The default URL is `http://127.0.0.1:7417/mcp`. It exposes the same operations as tools and resources. Decisions are in [ADR-0014](decisions/0014-mcp-transport.md).

## Transport

- **Protocol:** streamable HTTP, revision **2026-07-28** only. That revision is sessionless: each POST is a complete request.
- **Older clients:** a client that cannot negotiate 2026-07-28 is refused during the handshake.
- **Responses:** JSON (`application/json`), never SSE. There is no `Mcp-Session-Id`, and `GET` and `DELETE` are not used.
- **Limits and security:** bodies are limited to 1 MiB. `/mcp` sits behind the same loopback `Host` check and cross-origin protection as the API, with no authentication ([ADR-0006](decisions/0006-local-unauthenticated-api.md)).
- **Server capabilities:** `tools` and `resources`, plus short instructions that describe the agent loop.

## Tools

Arguments are flat JSON objects, named after the record fields in [records.md](records.md).

- **Decoding is strict, as for HTTP request bodies.** Unknown or duplicate members are rejected, and so are values of the wrong JSON type. Free-form objects (`contract`, `instructions`, `inputs`, `evidence`, `environment.attributes`, reference `inputs`) are stored with their member order intact.
- **Schemas:** every tool advertises an input schema generated from its argument type.

| Tool | Read-only | Arguments | Result |
| --- | --- | --- | --- |
| `list_procedures` | yes | none | `{"procedures":[…]}` |
| `get_procedure` | yes | `id` or `canonical_key` (exactly one) | procedure history |
| `get_version` | yes | `procedure_id`, `version` | version |
| `get_graph` | yes | `procedure_id`, `version`; `repository` and `environment` together, optional | graph node, [resolved from evidence](records.md#contextual-resolution-implemented) when given a context |
| `create_procedure` | no | `canonical_key`, `philosophy`, `method`, `contract`, `instructions`, `references` (optional), `revision_reason` | procedure history |
| `revise_procedure` | no | `procedure_id`, `base_version`, then the same version fields | version |
| `list_bindings` | yes | `repository` | `{"bindings":[…]}` |
| `get_binding` | yes | `id` | binding history |
| `get_binding_revision` | yes | `binding_id`, `revision` | binding revision |
| `resolve_binding` | yes | `binding_id`, `environment` | binding resolution |
| `create_binding` | no | `repository`, `name`, `procedure_id`, `inputs`, `version_policy`, `revision_reason` | binding history |
| `revise_binding` | no | `binding_id`, `base_revision`, `inputs`, `version_policy`, `revision_reason` | binding revision |
| `record_execution` | no | the execution fields: `procedure_id`, `version`, `binding_id` and `binding_revision` (optional), `repository`, `commit`, `environment`, `inputs`, `outcome`, `evidence`, `children` (optional) | execution |
| `get_execution` | yes | `id` | execution |
| `list_executions` | yes | `procedure_id`; `version` and `repository` optional | `{"executions":[…]}` |
| `get_verification` | yes | `execution_id` | verification |
| `list_verifications` | yes | `procedure_id`, `version`; `repository`, `commit` and `environment` optional | `{"verifications":[…]}` |

### Results

A successful result carries the record exactly as the HTTP API returns it ([http-api.md](http-api.md)), in two places:

- as structured content;
- as one text item holding the same JSON, with the server's member order.

### Errors

A failed call is a tool error (`isError: true`). It carries the HTTP API's error body, `{"error":{"code","message",…}}`, as text and as structured content, with the same codes: `invalid_request`, `not_found`, `version_conflict` (with `latest_version`), `revision_conflict`, `canonical_key_exists`, `binding_exists`, `reference_cycle`, `graph_too_large` and `internal`.

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

Verified clients (2026-10-09):

| Client | Result |
| --- | --- |
| VS Code Copilot chat (VS Code 1.137, bundled `@github/copilot` runtime) | Works: 17 tools discovered, full workflow run from chat. |
| Go SDK v1.8.0 client | Works; used by the automated tests. |
| TypeScript SDK 1.32.1, MCP Inspector 2.10.1 | Refused: their newest protocol is 2025-11-25. |

Other clients that support streamable HTTP take the same URL. The client must support protocol revision 2026-07-28.
