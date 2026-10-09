# 0018. Resolution separates selection evidence from target verification

**Status:** Accepted
**Date:** 2026-10-09

## Context

[ADR-0013](0013-evidence-based-resolution.md) resolves contextual references in a repository and an environment. It leaves out the commit and inputs on purpose, because they are usually not known before a run. A node's evidence is the latest verified execution of its version in that context, at any commit and with any inputs. It is reported as `verified_by`.

During [#28](https://github.com/ashuangiras/polaroid/issues/28), an agent resolved a binding, received nodes whose `verified_by` named executions at commit `d306ae9`, and then worked at `ebe54c7`. The selection was correct: earlier success makes a version a good candidate. But nothing in the response said that the evidence came from another commit, and nothing could say whether the selected combination was verified at the commit the agent was about to run. The name `verified_by` invites the wrong reading ([#31](https://github.com/ashuangiras/polaroid/issues/31)).

## Decision

- **Two notions, kept apart.**
  - *Selection evidence* explains why a version or child combination was selected. It may come from any commit and any inputs in the context. It is never verification of anything else.
  - *Target verification* is the status of the selected combination at one exact target: repository, commit, environment name, effective inputs and the selected child-version tree. It is the combination rule of [ADR-0012](0012-derived-verification.md), unchanged: the latest execution of that combination decides.
- **Selection is unchanged.** A target never changes which versions are selected. History from commit A still proposes candidates at commit B.
- **Provenance on every node with evidence.** Next to `verified_by`, which keeps its value for compatibility, a node reports `selection_evidence`: the execution's ID, repository, commit and environment name.
- **An optional, explicit target.** The graph endpoint (with a context) and binding resolution accept `commit` and `inputs`, given together. `inputs` are the root's effective inputs. Polaroid never inspects a checkout and never infers compatibility from ancestry, tree contents, branch names or time. Without a target, no node claims target verification.
- **Each node is judged at the target.**
  - A child's effective inputs come from its reference's input mapping ([ADR-0008](0008-subprocedure-references.md)) applied to its parent's effective inputs: `{"input": p}` takes the parent's member `p`, and `{"value": v}` takes `v`. A child input whose parent member is absent is left out.
  - Each node reports `target_verification` in the shape of a verification-list entry: the target `combination`, `verified`, and `latest_execution_id` and `execution_ids` of the matching executions. With no matching execution, `verified` is `false` and `execution_ids` is empty.
  - Inputs compare in the canonical form of ADR-0012, so member order never matters. Working-tree state is part of the inputs where a procedure's contract puts it there, as `dev.change.verify` does with `working_tree` ([ADR-0017](0017-development-procedures-as-records.md)). Polaroid adds no field for it.
- **Everywhere.** HTTP query parameters `commit` and `inputs` (a JSON object), MCP arguments of the same names, and CLI arguments. Nothing is stored and there is no migration.

## Consequences

- An agent can tell a historically supported candidate from a combination verified where it is about to work. When the target is unverified, it still has to run the procedures and record the execution.
- Target verification follows the selection. Another combination that is verified at the target, but not selected, is not reported here; the verifications endpoint lists it.
- A target whose inputs differ in any value, including `working_tree`, is a different combination, so a modified tree is never verified by a clean run.
- Responses with evidence grow by one object per node. Clients that reject unknown members must accept it, as the compatibility rules for additive fields already require.
