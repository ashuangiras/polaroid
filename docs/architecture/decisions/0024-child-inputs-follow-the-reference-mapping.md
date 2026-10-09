# 0024. A child execution must have run with the inputs its reference maps

**Status:** Accepted. Extends [ADR-0011](0011-subprocedure-executions.md) and [ADR-0012](0012-derived-verification.md).
**Date:** 2026-10-09

## Context

A reference maps the parent's effective inputs to the child's: `{"input": p}` takes the parent's member `p`, `{"value": v}` takes `v`, and a child input whose parent member is absent is left out ([ADR-0008](0008-subprocedure-references.md)). Target verification already derives each child's inputs this way ([ADR-0018](0018-selection-evidence-and-target-verification.md)).

Linking a child to a parent never compared the child's inputs with that mapping ([#39](https://github.com/ashuangiras/polaroid/issues/39)):
- recording the parent checks reference, target, pin, repository, commit and applicability;
- the `execution_children_match_reference` trigger checks target, pin, repository and commit;
- verification checks outcome and recursive coverage.

So a parent that should have built service A could link a successful build of service B and be verified. Resolution could then report a verified parent above a child that is unverified for the inputs the parent implies.

## Decision

- **The rule.** A linked child is valid for its reference only if two inputs are equal: the reference's mapping applied to the parent's effective inputs, and the child's recorded inputs. Equality is the existing canonical input equality ([records.md](../records.md#verification-implemented)): member order is irrelevant, strings and non-integer numbers are canonicalized as in RFC 8785, and integers are compared exactly. There is no subset match, and no implicit override: an extra, missing or different member is a mismatch, and an empty mapping requires `{}`. The child's environment may still differ.
- **One implementation.** The domain applies the mapping (the one function target verification uses) and compares canonical forms (the one canonicalization combinations use). Writes and reads both call it.
- **Writes.** Recording a parent whose child does not match is `400`, naming `children[i].execution_id`, with the expected and recorded inputs in the message. Nothing is stored.
- **Reads.** Stored links are never changed or removed. A child that does not match is reported as a new direct problem, `child_inputs_mismatch`, with `reference` and `execution_id`, and the parent is not verified. If that child is itself unverified, `child_not_verified` is reported too. Because verification is recursive, every ancestor of such a parent is unverified (`child_not_verified`). The same verification decides combination statuses, contextual selection (only verified executions are evidence, and links are followed only from verified ones) and target verification.
- **No schema change.** The database keeps enforcing what it enforced. It does not enforce this rule. A trigger cannot reproduce RFC 8785 canonicalization, exact-integer comparison and the mapping rules. A trigger approximating them would disagree with the domain, and a link it wrongly refused or accepted would be worse than none. A link inserted with direct SQL can still be stored; it can never verify a parent, because verification is derived on every read.

## Consequences

- **Writes.** A client that linked children recorded with different inputs from the mapped ones, previously accepted, now gets `400`. It must record the child with the inputs its parent's reference implies, or change the reference's mapping in a new procedure version.
- **Historical records.** The verification of historical parents with mismatched links changes from verified to unverified, and with it their combinations' statuses, contextual selection and target verification. Their outcome, evidence and links do not change. The development store was checked before the change: all 18 of its links match, so none of its verification changes.
- **Agents.** The way to record a composed run correctly is the one the procedures already describe: compute each child's effective inputs from the reference's mapping, as target verification reports in `target_verification.combination.inputs`, and run the child with exactly those.
