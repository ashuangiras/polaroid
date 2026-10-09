# 0012. Verification is derived on read from executions, per combination, and the latest execution decides

**Status:** Accepted; a child whose inputs do not match its reference's mapping does not verify its parent ([ADR-0024](0024-child-inputs-follow-the-reference-mapping.md))
**Date:** 2026-10-09

## Context

Executions ([ADR-0010](0010-execution-records.md)) and their child links ([ADR-0011](0011-subprocedure-executions.md)) record what ran. Roadmap 3.3 ([#11](https://github.com/ashuangiras/polaroid/issues/11)) must say whether a version is verified in a context. Success in one repository, commit, environment, input set or child-version combination must not stand for another. Five choices were open:

- whether a verification is stored or computed;
- what a succeeded parent needs from its children;
- what identifies a context;
- which of several runs in one context decides;
- how verification is read.

## Decision

- **Derived on read.** A verification is a pure function of immutable executions and links. Nothing new is stored and there is no migration, so it can never drift from its evidence. Each read runs in one transaction, so it sees one snapshot.
- **Coverage rule.** An execution is verified when both of these hold:
  - its outcome is `succeeded`;
  - every reference of its version is fulfilled by a linked child that is itself verified, checked recursively.

  A leaf is verified when it succeeded. An unverified execution reports only its direct problems: `outcome_failed`, `missing_child`, or `child_not_verified` (naming the child).
- **Combination.** The context of an execution is made of these parts:
  - its `repository`, `commit` and `environment.name`. Attributes stay uninterpreted, as ADR-0010 decided;
  - its effective `inputs` in canonical form. Object members are sorted, insignificant whitespace is removed, and strings and floats are canonicalized as in RFC 8785. Integers are kept exact, unlike RFC 8785, so distinct integers above 2^53 are never merged;
  - its **child-version tree**: each linked child, in the parent version's reference order, with its reference name, its version and its own tree. The target procedure is fixed by the reference, so it is not repeated.

  Changing any child's version anywhere in the tree makes a new combination. That combination needs a fresh parent execution, and older executions stay with the combination they were recorded in.
- **The latest execution decides.** A combination's status is the verification of its latest execution, ordered by `created_at` and then `id`, as executions are listed. A regression after a success is therefore visible, and every execution stays in the combination's history.
- **Reads.**
  - `GET /v1/executions/{id}/verification` judges one execution.
  - `GET /v1/procedures/{id}/versions/{n}/verifications` lists a version's combinations, ordered by their first execution, with `verified`, `latest_execution_id` and `execution_ids`. It can be filtered by `repository`, `commit` and `environment`.
  - The domain logic lives in `internal/memory` behind a small reader interface, as the composition graph does ([ADR-0009](0009-reference-graph-rules.md)).
- **Bounds.** Each execution has at most one parent, and a child is recorded before its parent. Execution trees are therefore disjoint and acyclic, and one list visits each execution at most once. No extra limit is needed.

## Consequences

- Verification semantics can be refined later without migrating stored data. But the answer for old evidence would change with them, so such a refinement needs an ADR.
- Reads cost work proportional to the executions they cover. Nothing is cached.
- A parent recorded without links to all its references can never be verified. An agent must record and link a child for every reference.
- Matching on the environment name inherits ADR-0010's naming discipline.
