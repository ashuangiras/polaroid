# 0011. A parent execution links its already-recorded children, one per reference, from the same repository and commit

**Status:** Accepted; a child must also have run with its reference's mapped inputs ([ADR-0024](0024-child-inputs-follow-the-reference-mapping.md))
**Date:** 2026-10-09

## Context

A composed version ([ADR-0008](0008-subprocedure-references.md), [ADR-0009](0009-reference-graph-rules.md)) runs its references as subprocedures. Roadmap 3.2 ([#9](https://github.com/ashuangiras/polaroid/issues/9)) links a parent execution to the child executions that fulfilled its references, so that verification (3.3) can judge a parent by its children. Executions are written once, after the run ([ADR-0010](0010-execution-records.md)), and children finish before their parent.

## Decision

- **The parent lists its children.** Children are recorded first, as ordinary executions. The parent's record carries `children: [{reference, execution_id}]`, and the links are validated and stored in the same transaction as the parent. No record is ever updated to add a link.
- **A link must agree with the parent's version and run:**
  - `reference` names a reference of the parent's version.
  - The child ran that reference's target procedure.
  - For a pinned reference, the child ran exactly the pinned version. A contextual reference accepts any version, and the child records the exact one.
  - The child has the parent's `repository` and `commit`. Its environment may differ, for example a separate CI job.
- **Cardinality:**
  - Each reference is fulfilled by at most one child, and none is required. Coverage rules belong to verification.
  - Each execution is the child of at most one parent.
  - Children may have children, so trees of any depth are recorded bottom-up.
- **Errors:** every violation is `400` naming `children[i].reference` or `children[i].execution_id`. This includes a child already linked to another parent. The database enforces uniqueness, so concurrent parents cannot claim the same child.
- **Storage:** links are rows of `execution_children`. Each row repeats the parent's procedure, version, repository and commit, so that triggers can check the link before the parent row exists. Links are inserted before the parent row, which works as follows:
  - a trigger rejects a link to a parent that already exists;
  - a deferred foreign key to `executions (id, procedure_id, version, repository, commit_hash)` fails the commit if the parent row never arrives or differs;
  - further triggers enforce the rules above and reject `UPDATE` and `DELETE`.
- **Representation:** `children` is returned in submission order, and is omitted when there are none. A child's own record does not name its parent.

## Consequences

- A tree of executions is immutable from the moment its root is written.
- An agent must record every child before the parent. A child recorded after its parent cannot be linked to it later. It stays a standalone execution.
- Finding a child's parent requires searching the links. No endpoint serves that yet.
