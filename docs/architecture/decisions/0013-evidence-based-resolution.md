# 0013. Contextual references resolve from evidence in a repository and environment, following verified combinations

**Status:** Accepted
**Date:** 2026-10-09

## Context

[ADR-0009](0009-reference-graph-rules.md) made contextual references select the target's latest version "until execution evidence exists". Contextual binding policies ([ADR-0007](0007-repository-identity-for-bindings.md)) are stored but never resolved. Verification ([ADR-0012](0012-derived-verification.md), [#11](https://github.com/ashuangiras/polaroid/issues/11)) now judges executions per combination. Roadmap 3.4 ([#13](https://github.com/ashuangiras/polaroid/issues/13)) resolves contextual references from that evidence. Six choices were open:

- what context a resolving agent supplies;
- when a version counts as verified in it;
- which verified version wins;
- whether verified combinations constrain their children;
- what happens without evidence;
- which endpoints resolve.

## Decision

- **Context = repository + environment name.** The commit and inputs are usually not known before a run, so they are not part of the context.
- **Verified in the context.** A version is verified in a context when its latest execution in that repository and environment, of any commit and inputs, is verified. That execution is the version's *evidence*. A newer regression therefore withdraws the version, consistent with "the latest execution decides".
- **Walk with evidence.** Every node may carry evidence, and the walk works as follows:
  - **Under a node with evidence:** each reference selects the version of the child execution that the evidence linked for it, and that child execution becomes the child's evidence. Verified executions link every reference, so the walk follows the whole verified combination rather than mixing versions that were never verified together.
  - **Under a node without evidence:**
    - a pinned reference selects its pin, with the pin's own evidence if there is any;
    - a contextual reference selects the **highest version number** verified in the context;
    - otherwise it selects the target's **latest** version, unverified.
  - The root carries its own evidence when it is verified.
- **Reporting.**
  - Every edge reports `selected_by`: `pin`, `evidence` or `latest`.
  - Every node with evidence reports `verified_by`, the execution ID.
  - Without a context, contextual references still select the latest version, so the existing graph is unchanged apart from `selected_by`.
- **Same safety checks.** Evidence can select older versions whose references differ, so the resolving walk keeps ADR-0009's cycle detection and limits, and reports `409 reference_cycle` or `422 graph_too_large` on read. The write-time check keeps selecting the latest version.
- **Endpoints.**
  - `GET /v1/procedures/{id}/versions/{n}/graph` takes optional `repository` and `environment`, given together. Its query string is now strict like the list endpoints'. It used to be ignored, and was never documented.
  - `GET /v1/bindings/{id}/resolution?environment=…` resolves the binding's latest revision in the binding's repository: a pin selects its version, and a contextual policy selects as a contextual reference does. It returns the revision used, the root's `selected_by`, and the resolved graph.
  - Each resolution reads one snapshot. Nothing is stored.

## Consequences

- Agents get reproducible selections: a verified parent brings its verified children.
- A first run in a new context has no evidence and runs the latest versions. Its execution then becomes evidence.
- A context name that differs between agents splits the evidence (ADR-0010's naming discipline).
- The resolution of a binding can change whenever executions are recorded. The response says which revision and evidence it used.
