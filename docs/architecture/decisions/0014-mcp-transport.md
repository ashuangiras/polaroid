# 0014. MCP is served over stateless streamable HTTP at /mcp, with flat tool arguments and the API's record shapes

**Status:** Accepted. The protocol clause (2026-07-28 only) is superseded by [ADR-0016](0016-mcp-protocol-2025-11-25.md).
**Date:** 2026-10-09

## Context

Agents use tools through the Model Context Protocol. Polaroid has only a JSON HTTP API and a CLI ([#15](https://github.com/ashuangiras/polaroid/issues/15)). The owner chose the official Go SDK, `github.com/modelcontextprotocol/go-sdk`. These choices were open:

- the transport and protocol revision;
- the tool surface;
- the argument and result shapes;
- how the SDK's dependencies and license fit the dependency rules;
- where the code lives.

## Decision

- **Transport.** `polaroidd` serves MCP at `/mcp` on its existing listener, behind the same loopback-host check and cross-origin protection as the API.
  - It uses the SDK's streamable HTTP handler in stateless mode with JSON responses. Only protocol revision **2026-07-28** is supported. That revision is sessionless (SEP-2567): every POST is one complete request, which suits a store with no server-to-client requests.
  - Bodies are limited to 1 MiB.
  - There is no stdio transport: agents connect to the running daemon by URL, and the database keeps a single owner.
- **Package.** `internal/transport/mcp` calls `memory.Service` directly, like `internal/transport/http`. Neither transport imports the other. The record response shapes and the mapping of errors to codes move to a shared package, `internal/transport/wire`, so the two transports cannot drift.
- **Tools.** There are 17 tools, with full parity with the HTTP API: 12 read tools annotated read-only, and 5 write tools.
  - Arguments are flat objects named with the record field names from [records.md](../records.md). One `get_procedure` tool takes `id` or `canonical_key`.
  - Polaroid decodes arguments itself, with the same strict rules as the API: unknown or duplicate members are rejected, and free-form objects keep their member order. The SDK's typed decoding gives no such guarantee. Input schemas are generated from the argument types and advertised.
  - Results are the API's record shapes, as structured content and as JSON text.
  - Domain errors are tool errors (`isError`) carrying the API's error body and codes. Internal errors are logged, and only `internal` is returned.
- **Resources.** `polaroid://procedures/{id}`, `polaroid://procedures/{id}/versions/{version}` and `polaroid://bindings/{id}` are read-only JSON resources that return the same bodies as the corresponding tools.
- **Dependencies.** The SDK compiles in eight more modules, all MIT or BSD-3-Clause. The SDK's own license file holds the Apache-2.0 and MIT texts while the project relicenses. `make deps-check` classifies such a file as `Apache-2.0 AND MIT`, and accepts it because both parts are permissive.

## Consequences

- An MCP client connects with only a URL, `http://127.0.0.1:7417/mcp`, while `polaroidd` runs.
- Clients that cannot speak 2026-07-28 cannot connect until they upgrade.
- Polaroid now has 18 third-party modules. A change of the SDK's license, or new SDK dependencies, shows up in `make deps-check`.
- New domain operations must be exposed in both transports. Their shared shapes keep that cheap.
