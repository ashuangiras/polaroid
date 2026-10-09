# 0015. Feedback reports are immutable, untriaged records about Polaroid itself

**Status:** Accepted
**Date:** 2026-10-09

## Context

Agents that use Polaroid have nowhere to report a problem with Polaroid itself, such as a confusing error, a missing capability or a tool that misbehaved. They also cannot suggest an improvement ([#16](https://github.com/ashuangiras/polaroid/issues/16)). That knowledge ends with the session. These questions shape every report ever stored:

- what a report contains;
- whether Polaroid tracks what happens to a report;
- how an agent learns that it can report.

## Decision

- **An immutable record, like an execution.** A feedback report is written once and never changed or deleted. It has these fields:
  - `id` (a UUIDv7) and `created_at`, set by the server;
  - `kind`, either `problem` or `suggestion`;
  - `summary`, required single-line text. Every Unicode line terminator is rejected, so a list of summaries stays one line per report;
  - `details`, required text that may span lines;
  - `reporter`, in the canonical-key format, naming the agent or person. It is not authenticated ([ADR-0006](0006-local-unauthenticated-api.md));
  - `context`, an optional free-form JSON object that Polaroid stores but never interprets, for example the tool involved or record IDs. An absent `context` is stored and served as `{}`, so a report has one representation. IDs in it are not checked, because a report about a missing or broken record must still be accepted.
- **No triage state.** Polaroid stores reports and lists them. Triage happens outside Polaroid, for example by turning reports into GitHub issues. A state such as "open" or "fixed" would need updates, which this record does not allow.
- **Reports are separate from task knowledge.** A report never changes another record, and it is not linked to procedures by foreign keys.
- **Listing.** All reports, oldest first, optionally filtered by `kind`, with every field, because reports are small and are read for triage. Unknown, repeated or empty query parameters are `400`.
- **Every transport.** HTTP: `POST /v1/feedback`, `GET /v1/feedback[?kind=…]` and `GET /v1/feedback/{id}`. CLI: `feedback [FILE]`, `feedbacks [KIND]` and `get-feedback ID`. MCP: `report_feedback`, plus read-only `list_feedback` and `get_feedback`. `get_feedback` keeps the full HTTP parity of [ADR-0014](0014-mcp-transport.md), which now has 20 tools: 14 read-only and 6 write.
- **Agents are told.** The MCP server's instructions add a step: when Polaroid gets in the way, or could serve better, report it with `report_feedback`.
- **Schema.** Migration 6 adds the `feedback` table. `CHECK` constraints repeat the field rules, and triggers reject `UPDATE` and `DELETE`.

## Consequences

- Reports accumulate forever. A wrong or duplicate report cannot be withdrawn, only followed by another.
- Whoever triages needs an outside record of which reports were handled, for example issue links. Polaroid cannot answer "what is still open?".
- `reporter` is self-asserted, like every identity before access control. Reports are evidence of experience, not of authority.
- The list is unbounded, like the other lists. Pagination, if added, will be opt-in.
