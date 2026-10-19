# 0064. Session-close knowledge capture for Polaroid

**Status:** Accepted.
**Date:** 2026-10-19

## Context

Polaroid's shared `polaroid.using` Procedure guides an agent through dynamically selected task Procedures with a v7 parent walkthrough. The field-complete publication flow can create or revise all supported knowledge types, but the default workflow did not require the agent to review completed repository work for reusable learning. `feedback` reports a deficiency in an existing Procedure; it is not a general knowledge-capture mechanism.

The owner requires completed work in `github.com/ashuangiras/polaroid` to improve the corpus when the session contains validated, reusable knowledge, while preserving evidence, avoiding duplicate or task-specific content, and not requiring a per-publication confirmation for this one capture workflow. MCP has no universal agent-session-end event, so no server can block arbitrary clients that do not follow the root Procedure.

## Decision

**Add a local session-close capture Procedure.** `polaroid.session-close-capture` applies to `github.com/ashuangiras/polaroid`. The shared `polaroid.using` guidance tells agents in this repository to discover and read it at the parent's pinned snapshot and attach it immediately after starting the v7 dynamic parent. The capture child stays pending while task-specific Procedures are discovered, attached, executed and verified. It is completed last, before the parent is completed or closed. These runtime attachments do not add corpus `composes` edges.

**Capture reusable, verified learning, not session volume.** The agent reviews the session's work, changes and verification evidence, searches the pinned corpus for existing knowledge, and creates or updates only generalized knowledge supported by sources. It must exclude task-specific requests, secrets, personal information, transient environment failures and unsupported speculation. If no candidate meets that bar, it completes the capture child as a no-op and creates no publication. `feedback` remains for its existing purpose; it is not used to smuggle new session knowledge into the corpus.

**Grant one scoped standing publication authorization.** The owner authorizes `polaroid.session-close-capture` to finish field-complete `propose_start`/`propose_advance` reviews and publish resulting knowledge locally when all proposal, citation and catalog validation succeeds. This authorization is limited to this Procedure, this repository, and the local active corpus. Every other knowledge publication remains subject to the owner's approval for that change. The capture Procedure cannot push or sync a remote.

**Use the parent walkthrough as the cross-client gate.** With v7, the capture child is attached at the beginning, so the parent stays `in_progress` until all task children and capture are done or skipped. An agent following the shared root cannot complete its parent before capture. A client can still ignore the root, abandon/close the token, disable its tools, or end its session outside the workflow; MCP has no host-independent session-stop hook. No Copilot-only hook is added.

## Consequences

- Every compliant v7 session in the Polaroid repository performs an explicit capture review, including a deliberate no-op when no reusable knowledge is supported.
- The v1-v6 MCP contracts and generic v7 dynamic-child semantics are unchanged. The capture is a runtime child, not a statically composed catalog relationship.
- Publication continues through the field-complete validation gate and preserves its immutable review receipt. Other proposal workflows keep their existing owner-approval requirement.
- Local knowledge can grow without automatically sharing it. Remote fetch, activation and push remain explicit operator operations under the configured trust policy.
- This is workflow enforcement for agents that follow `polaroid.using`, not a security boundary against non-compliant MCP clients.

**Authorization amendment:** [ADR-0065](0065-capture-publication-authorization.md) replaces automatic final-field publication with a v8 server-checked capture publication or authenticated operator approval. The guidance here remains the knowledge-selection policy; it no longer describes the current write mechanism.
