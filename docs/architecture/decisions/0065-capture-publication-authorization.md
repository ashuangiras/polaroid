# 0065. Authorize reviewed agent publications at activation

**Status:** Accepted.
**Date:** 2026-10-19

## Context

[ADR-0062](0062-field-complete-knowledge-publication.md) made every agent publication field-complete but activated the candidate on the last field answer. [ADR-0064](0064-session-close-knowledge-capture.md) granted a narrow standing authorization to local capture, but that authorization existed only in procedure guidance: the server could not distinguish a capture from an ordinary proposal. Agent-supplied attribution and rationale are not credentials.

## Decision

`/mcp/agent/v8` separates review from activation. The final `propose_advance` validates the candidate and returns `ready` with a SHA-256 hash of the complete proposal, including its base snapshot, provenance, changes and review receipt; it does not publish. An unchanged review still closes without a commit. Drafts remain ephemeral; the creation event optionally stores the `walkthrough_token` supplied at v8 start. A capture-bound draft is accepted only when the open dynamic `polaroid.using` parent belongs to `github.com/ashuangiras/polaroid` and its pinned catalog includes the attached local `polaroid.session-close-capture` Procedure. The parent snapshot pins the authorization policy, not every subsequent candidate base: multiple sequential captures can each review the then-active snapshot while the same parent remains open.

`propose_publish_capture` reconstructs and revalidates the exact draft and compares its hash. The server checks that its persisted parent is still open, its capture child is the active final step, at least one task child exists, and **all** other plan leaves are done or skipped. The child must not already have an outcome. The publication uses the kernel's atomic active-snapshot compare-and-publish gate; success consumes the draft token. A wrong hash, stale base, changed draft, ineligible or closed parent never publishes. Within one daemon, walkthrough mutation and capture publication share a lock; the kernel remains the authoritative guard against concurrent active-ref changes.

An ordinary draft is reviewable but cannot use capture publication. An operator can inspect its complete reviewed field inventory and candidate hash through authenticated `GET /v2/knowledge/drafts/{token}`, then explicitly approve the **current exact candidate** with authenticated `POST /v2/knowledge/drafts/{token}/publication` and `{"candidate_hash":"..."}`. The existing operator token is required; neither the draft token nor its proposer/rationale is operator authorization. The same endpoint may explicitly approve a capture-bound draft. This does not push to a remote. Operator direct publications remain separately authenticated and receipt-validated.

The old v6/v7 proposal tool schemas stay fixed, but their `propose_start` and `propose_advance` calls refuse all writes with migration guidance to v8. Their discovery, reads, resolution and walkthroughs continue to work. Migration 17 adds the nullable walkthrough binding to the existing draft event log; no applied migration or Git history is changed. An older binary refuses a catalog already upgraded to schema 17. The legacy HTTP API, record documents and CLI request contracts do not change.

## Consequences

- Validation is not permission: a draft can be ready without changing the active snapshot. A per-draft hash is a concurrency/identity check, not a secret authorization token.
- Capture authorization is a server-side *publication boundary* for clients using this API, not proof that a client really completed repository work or collected trustworthy evidence. Agents report walkthrough outcomes; the server checks the persisted reports and validates knowledge, not the external work itself.
- Procedure keys and repository identifiers in the capture exception are explicit, narrow authorization policy, not task-specific record interpretation. Other task knowledge remains in corpus records.
- Earlier v6/v7 proposal writes are intentionally incompatible. An installed v7 service continues its old behavior until a separately approved upgrade; changing source does not upgrade the shared daemon.
