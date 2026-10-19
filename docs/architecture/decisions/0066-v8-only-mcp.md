# 0066. Serve only the v8 agent MCP endpoint

**Status:** Accepted.
**Date:** 2026-10-19

## Context

The legacy SQLite operator MCP endpoint exposed 42 tools and three resources, duplicating the JSON API. Seven older Git-native agent endpoints had different tool catalogs and proposal semantics. Keeping these endpoints risks connecting an agent to obsolete publication behavior. The stored SQLite catalog and Git-native knowledge remain separate authorities.

## Decision

`/mcp/agent/v8` is the sole MCP endpoint. Remove the `/mcp` operator endpoint and `/mcp/agent` through `/mcp/agent/v7`, including their endpoint-specific adapters. The v8 catalog remains `discover`, `read`, `feedback`, `resolve`, `start`, `attach_child`, `advance`, `propose_start`, `propose_advance`, and `propose_publish_capture`. Its review-before-activation and capture/operator-approval boundaries remain unchanged. The two MCP **protocol** revisions, 2026-07-28 and 2025-11-25, remain supported.

The following is the complete mapping of the retired 42-tool catalog to the existing JSON API. A CLI client may use `polaroid`'s corresponding generic commands instead. This maps **operations**, not the distinct MCP envelope; clients must explicitly migrate and handle HTTP status codes.

| Retired tools | Equivalent HTTP operations |
| --- | --- |
| `list_procedures`, `discover_procedures`, `suggest_duplicates` | `GET /v1/procedures`, `GET /v1/procedures/discovery`, `POST /v1/procedures/duplicates` |
| `get_procedure`, `get_version`, `get_graph` | `GET /v1/procedures/{id}` or `/by-key/{key}`, `GET /v1/procedures/{id}/versions/{n}`, `GET /v1/procedures/{id}/versions/{n}/graph` (including contextual resolution and plan format) |
| `create_procedure`, `revise_procedure`, `record_procedure_origin` | `POST /v1/procedures`, `POST /v1/procedures/{id}/versions`, `POST /v1/procedures/{id}/origin` |
| `register_repository`, `add_repository_alias`, `get_repository`, `list_repositories` | `POST /v1/repositories`, `POST /v1/repositories/{id}/aliases`, `GET /v1/repositories/{id}` or `/by-identifier/{identifier}`, `GET /v1/repositories` |
| `list_bindings`, `get_binding`, `get_binding_revision`, `resolve_binding`, `prepare_execution`, `create_binding`, `revise_binding` | `GET /v1/bindings?repository=...`, `GET /v1/bindings/{id}`, `GET /v1/bindings/{id}/revisions/{n}`, `GET /v1/bindings/{id}/resolution`, `GET /v1/bindings/{id}/preparation`, `POST /v1/bindings`, `POST /v1/bindings/{id}/revisions` |
| `record_execution`, `get_execution`, `list_executions`, `get_verification`, `list_verifications` | `POST /v1/executions`, `GET /v1/executions/{id}`, `GET /v1/executions`, `GET /v1/executions/{id}/verification`, `GET /v1/procedures/{id}/versions/{n}/verifications` |
| `report_feedback`, `list_feedback`, `get_feedback`, `resolve_feedback` | `POST /v1/feedback`, `GET /v1/feedback`, `GET /v1/feedback/{id}`, `POST /v1/feedback/{id}/resolution` |
| `list_toil_items`, `get_toil_item`, `get_toil_revision`, `create_toil_item`, `revise_toil_item` | `GET /v1/toil-items`, `GET /v1/toil-items/{id}` or `/by-key/{key}`, `GET /v1/toil-items/{id}/revisions/{n}`, `POST /v1/toil-items`, `POST /v1/toil-items/{id}/revisions` |
| `invalidate_execution`, `get_execution_invalidation` | `POST /v1/executions/{id}/invalidation`, `GET /v1/executions/{id}/invalidation` |
| `get_procedure_lifecycle`, `retire_procedure`, `reactivate_procedure` | `GET /v1/procedures/{id}/lifecycle/events`, `POST /v1/procedures/{id}/retirement`, `POST /v1/procedures/{id}/reactivation` |
| `get_catalog_statistics`, `list_walkthroughs`, `get_walkthrough` | `GET /v1/statistics`, `GET /v1/walkthroughs`, `GET /v1/walkthroughs/{token}` |

The retired resources `polaroid://procedures/{id}`, `polaroid://procedures/{id}/versions/{version}` and `polaroid://bindings/{id}` map to `GET /v1/procedures/{id}`, `GET /v1/procedures/{id}/versions/{version}` and `GET /v1/bindings/{id}` respectively. The authenticated knowledge-operator HTTP/CLI operations remain separate from both this SQLite API and the unauthenticated agent MCP tools. No operator powers move into v8.

## Consequences

This explicitly supersedes the route and full-mode compatibility promises of [ADR-0038](0038-stable-releases-and-compatibility.md) and [ADR-0049](0049-agent-mode-finalized-no-migration.md), not their historical description of past releases. An upgraded daemon returns 404 for retired MCP paths; existing clients must update their server URL to `/mcp/agent/v8` or migrate operator/evidence calls to HTTP/CLI, then restart their MCP client to refresh tools. No SQLite migration, record rewrite, knowledge publication, installed-service upgrade or release is implied. Existing bindings, executions, feedback, toil, walkthroughs and Git-native snapshots remain intact.
