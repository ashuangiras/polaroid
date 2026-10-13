# 0043. Knowledge-first agent mode: procedural knowledge without routine execution records

**Status:** Accepted.
**Date:** 2026-10-13

## Context

Polaroid's primary responsibility is procedural knowledge: helping agents discover, understand, apply and improve proven methods of working. Its agent interface had grown around execution recording instead. The full MCP API exposes 39 tools whose schemas alone cost about 14,433 estimated tokens per connection, and the documented `verify-change` loop asks an agent to resolve a binding, read every version in full, record every child and parent execution and confirm verification: about 24,800 estimated tokens and 7 tool calls for one small gate ([context-efficiency-baseline.md](../../development/context-efficiency-baseline.md#knowledge-first-workflow-adr-0043)). A successful execution is not new procedural knowledge, and a failed one is not evidence that the procedure is wrong; recording each run made everyday use too expensive to be worth it.

## Decision

1. **Ownership.** Polaroid owns procedures and their immutable versions, applicability, contracts, instructions, references, discovery and selection, and knowledge feedback and corrections. Execution systems (for example Dispatch) own running commands, logs, retries and execution history. Verification systems own target-specific evidence, attestations, provenance and audit history. Polaroid may reference their records; routine success does not copy them into the catalog.
2. **Agent mode, a separate endpoint: `/mcp/agent`.** Built on the existing MCP server and tool registration, it exposes three tools and nothing else, so a client that connects to it never loads the full API's schemas:
   - `discover`: lexical discovery with compact candidates (identity, latest version, scope, goal) and, with a repository, its bindings of them;
   - `read`: one version or repository binding as executable guidance: method, contract, instructions, effective inputs, and each reference with its child's exact version (the pin, or the latest version for a contextual policy) and mapped inputs (the same mapping verification uses, ADR-0024). Philosophy, revision reason and creation time only with `full`; no history and no graph;
   - `feedback`: a classified finding about a procedure version, stored as an ordinary feedback report (ADR-0015, ADR-0021) whose `context` holds the `classification` and optional compact `evidence`. Accepted classifications: `incorrect_applicability`, `missing_precondition`, `incorrect_instruction`, `inadequate_verification` (problems) and `improved_method` (a suggestion). `product_defect`, `environment_problem` and `transient_failure` are refused as not procedural knowledge.
3. **Exception-driven lifecycle.** Discover, read, do the work with the agent's own tools, check the outcome against the contract, and finish. A successful run writes nothing to Polaroid. Only a finding that changes what is known about a procedure becomes feedback, and a procedure is never revised because some command failed.
4. **Execution recording is optional, not removed.** `/mcp` is unchanged: all 39 tools, including `record_execution`, `get_verification`, resolution from evidence and target verification, remain the compatibility and audit path, and stored executions, verifications and their tables are untouched. Following a procedure verifies nothing by itself; an audit-grade claim needs the verification system's evidence. Agent mode never claims a target is verified.
5. **Shared guidance by reference, not inheritance.** Rules that hold for every procedure may live once in a shared, versioned procedure that a procedure's contract names as `shared_guidance`, read once per session. Polaroid does not interpret the member. The representative example is `agent.guidance.shared` v1 with `dev.change.check` v1 ([examples/knowledge-first](../../../examples/knowledge-first)); no existing procedure is rewritten.

## Consequences

- Measured with [scripts/benchmark-knowledge.py](../../../scripts/benchmark-knowledge.py) (deterministic token estimate, real gate commands, no LLM): session setup 15,596 → 1,301 tokens (39 → 3 tools); one cold successful run 24,798 → 6,292 tokens with the unchanged `dev.change.verify` v4 and 4,963 with the shared-guidance example; 7 → 3 or 4 tool calls; the knowledge-gap run 5,254 tokens. Warm figures are conditional. These are harness estimates, not measured Copilot billing.
- Agent-mode `read` selects the latest version for a contextual policy; evidence-based selection stays in the full API, since it depends on execution records the knowledge-first path no longer requires.
- Clients choose a mode by URL. Existing clients of `/mcp` see no change; an everyday agent is configured with `/mcp/agent`.
- Not decided here: whether Polaroid's own development procedures and bindings move to the knowledge-first path, and whether the full API's execution records are later deprecated. Both are separate decisions.
