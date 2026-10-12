# 0040. Feedback resolution

**Status:** Accepted. Amends [ADR-0015](0015-feedback-reports.md); follows the pattern of [ADR-0035](0035-evidence-invalidation-and-procedure-retirement.md), where lifecycle state is a separate append-only record next to an immutable one.
**Date:** 2026-10-12

## Context

ADR-0015 left out any triage state on purpose: "Triage happens outside Polaroid … Polaroid cannot answer 'what is still open?'". With reports accumulating in the shared catalog, every triage pass re-reads all of them and works out again which are already handled ([#127](https://github.com/ashuangiras/polaroid/issues/127)). Agents calling `list_feedback` cannot tell a report fixed months ago from a live one, so they may act on stale reports or report them again.

## Decision

A report stays immutable, exactly as ADR-0015 left it. Its disposition is a separate, append-only record, next to it, following the lifecycle-event pattern of ADR-0035 rather than changing the report.

- **Resolving.** `POST /v1/feedback/{id}/resolution` `{outcome, reason, actor, references?}`, MCP `resolve_feedback`, CLI `resolve-feedback`.
  - `outcome` is one of `addressed`, `declined`, `not_reproduced` or `duplicate`. These are the only outcomes: a lighter "tracked" state is not part of this record. Linking a report to work in progress, for example an open issue, stays outside Polaroid, exactly as ADR-0015 already does for every report; `references` can still cite that work once the report is resolved.
  - `reason` is required text: how it was handled.
  - `actor` is in canonical-key form, like `invalidate_execution`'s.
  - `references` is optional: declared [`ExternalReference`](0034-toil-items.md) values (`uri`, optional `note`) — issue URLs, PRs, commits, or the procedure version or execution that addressed the report. They are stored and returned, never fetched.
- **Irreversible.** A report has at most one resolution. A second resolution, also a concurrent one, is `409 feedback_resolved`. If the problem comes back, it gets a new report, which may cite the old one in `context`; there is no reopening.
- **Reads.** A report carries `status`, `open` or `resolved`, and `resolution` when it is resolved. `list_feedback` gets an optional `status` filter; with no filter it still lists every report, open and resolved alike, so existing clients see no change ([ADR-0038](0038-stable-releases-and-compatibility.md): additions only). `get_catalog_statistics` adds feedback counts by status.
- **Initial use on the shared catalog**, once this ships and the service is upgraded: reports already fixed by closed issues are resolved `addressed`; the client tool-list caching reports (`01a121e7-a0f3`, `01a1224e-fe89`, `01a1289c-d7c9`) are resolved `addressed` too, because the operator step they describe is documented (`mcp.md`, `polaroid.service.manage`); reports behind an issue that is still open stay open until it closes.
- **Schema.** Migration 11 adds `feedback_resolutions`, one row per report at most, with the same `reason`/`actor` shape and write limits as `execution_invalidations`, plus `outcome` and a JSON `references` array. Triggers reject `UPDATE` and `DELETE`, matching ADR-0035's invalidation and lifecycle tables.

## Consequences

- Polaroid can now answer "what is still open?": `list_feedback?status=open`. Closing a feedback-driven issue is expected to resolve the report(s) it came from, citing the issue or PR, as part of the same change that closes the issue — the same discipline ADR-0035 expects of invalidating wrong evidence.
- A resolution cannot be undone or corrected in place, like an invalidation: a wrong resolution is answered by a new report, not by reopening the old one.
- Every existing response is unchanged apart from the added `status` field (always present) and `resolution` (present only once resolved); the unfiltered list and every other read keep their shape.
- Schema 11 cannot be opened by earlier builds.
