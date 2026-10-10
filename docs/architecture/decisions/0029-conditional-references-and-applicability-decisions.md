# 0029. Conditional references and recorded applicability decisions

**Status:** Accepted. Extends [ADR-0008](0008-subprocedure-references.md), [ADR-0011](0011-subprocedure-executions.md), [ADR-0012](0012-derived-verification.md), [ADR-0013](0013-evidence-based-resolution.md) and [ADR-0018](0018-selection-evidence-and-target-verification.md).
**Date:** 2026-10-10

## Context

Every reference of a version is required: a parent is verified only when every reference has a verified child. Agents therefore run every subprocedure, even when the work does not apply to the task, for example an integration suite for a change confined to prose. Nothing in a record can say that a subprocedure applies only to some work, and nothing can record that an agent decided it did not, and why ([#50](https://github.com/ashuangiras/polaroid/issues/50)).

Polaroid must not judge applicability itself. Task knowledge lives in records, and conditions are prose for agents. Polaroid stores the declaration and the decision, checks their structure, and derives verification from them.

## Decision

### Declaration: `condition` on a reference

- A reference may carry `condition`: non-blank text saying when the referenced work applies. A reference **with** a condition is *conditional*. A reference **without** one is *required*, as every existing reference is.
- Conditional means conditional on applicability, never optional at the agent's convenience. When the condition holds, the work is required.
- Polaroid never reads the text. It is returned on versions and graph edges, so discovery shows it.

### Decision: `decisions` on an execution

An execution may list one decision per conditional reference of its version:

```json
{"reference": "integration", "applicable": false, "rationale": "Only docs/*.md changed (git diff --name-only); the integration suite reads no Markdown.", "evidence": {"changed_files": ["docs/guide.md"]}}
```

- `reference` names a conditional reference of the version.
- `applicable` is a boolean and is required.
- `rationale` is required, non-blank text, for both answers. It states the basis of the decision.
- `evidence` is an optional free-form JSON object, never interpreted.

Write rules (`400`, naming `decisions[i]`):

- a decision for a reference the version does not have;
- a decision for a required reference, which cannot be skipped and takes no decision;
- two decisions for one reference;
- a not-applicable decision while `children` links a child for the same reference.

**Incomplete records stay writable.** A missing decision, or an applicable decision without a linked child, is accepted, so failed and partial runs can still be recorded. Such an execution is never verified. A skip is the decision itself: no child execution is fabricated for it.

### Verification

An execution is verified when it succeeded and every reference of its version holds:

- **required:** a linked child that ran with the mapped inputs and is verified, as before (ADR-0012, ADR-0024);
- **conditional, applicable:** the same as required;
- **conditional, not applicable:** no child, and the decision's rationale. The write rules guarantee both;
- **conditional, no decision:** never; the problem is `missing_decision`.

An applicable reference without a child is `missing_child`, and with an unverified child `child_not_verified`, as for required references. Pins, repository and commit, scope admission and mapped inputs apply to every linked child, conditional or not.

**Trust boundary.** Polaroid checks that each decision is structurally sound. Whether the rationale is true, and whether the condition really did not hold, is the recording agent's claim, kept in the record for review. Polaroid does not evaluate conditions, classify tasks or reuse earlier evidence for "already satisfied" work.

### Combination identity

A combination's child-version tree records, in reference order:

- for each executed child, its reference and version with its own tree, as before;
- for each not-applicable reference, `{"reference": "<name>", "skipped": true}`.

An execution that ran a conditional child and one that skipped it are therefore different combinations. Two skips with differently worded rationales are the same combination. An undecided reference adds nothing and the execution is unverified.

### Target verification

A target names, beside `commit` and `inputs`, the target's own decisions:

- `decisions` is an object from **reference paths** to booleans, for example `{"integration": false, "verify/lifecycle": true}`. A path is the names of the references from the root, joined by `/`.
- Each path must reach a conditional reference of the selected graph that is not inside a not-applicable reference's subtree. Any other path is `400` on `decisions`.

With a target:

- **Expected tree.** A node's expected child tree takes each decision into account. A not-applicable reference is expected skipped, and its subtree gets no target verification. An applicable or required reference is expected executed.
- **Undecided references.** A conditional reference in the node's subtree without a target decision is *undecided*. The node then reports `verified: false`, the undecided paths in `undecided` and `execution_ids: []`. Polaroid looks up no execution for it and never infers a decision from historical runs.
- **Edge `decision`.** Each conditional edge reports `decision`: `applicable`, `not_applicable` or `undecided`.

A run that skipped a reference never verifies a target that decides the reference applies. That target expects a different combination.

### Discovery and selection

- **Full graph.** Graphs always expand every reference, conditional ones included, with their `condition`, whatever any evidence decided.
- **Skipped evidence.** When a node's evidence (ADR-0013) skipped a conditional reference, there is no child execution to follow.
  - The reference selects as under a node without evidence: its pin, else the highest version verified in the context, else the latest.
  - The edge reports `skipped_by`, the evidence execution's ID.
  - `selected_by` and the child node's `selection_evidence` describe how the child was actually selected.

### Storage

Migration 8:

- **References:** adds a nullable `condition` column to `procedure_version_references`. NULL means required, so every existing reference stays required. The existing triggers keep references immutable.
- **Decisions:** adds `execution_decisions`, written only together with the parent execution, never updated or deleted.
- **Database enforcement.** Constraints and triggers repeat the structural rules:
  - one decision per reference;
  - a decision only for an existing reference that has a condition;
  - non-blank rationale;
  - an object `evidence`;
  - no child link for a not-applicable reference, in either insertion order.
- **Service only:** the UTF-8 and trimmed-blank checks. The undecided and applicable-without-child cases are left to verification.

Historical executions have no decisions and only required references, so their verification and combinations are unchanged.

## Consequences

- **Additive API.** A reference may add `condition`, and responses add it when present. Executions may add `decisions`, omitted when there are none. Combinations may add skipped entries. Graph edges may add `condition`, `decision` and `skipped_by`. Target verification may add `undecided`. The graph and resolution endpoints, `get_graph`, `resolve_binding` and the CLI take a `decisions` target argument. The new problem code is `missing_decision`.
- **What does not change.** Old requests behave as before, and versions without conditional references verify, resolve and select exactly as before.
- **Agents own applicability.** An agent that marks applicable work not applicable can verify a parent without doing it. The rationale is recorded for review, and a later target that needs the work cannot reuse that verification.
- **Instruction steps.** A recommended convention, not enforced and never read by the service, is documented in records.md: `when`, `required_by`, `satisfied_when`, `done_when` and `escalate_when` on steps.
