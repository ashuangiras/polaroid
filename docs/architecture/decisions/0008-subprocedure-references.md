# 0008. Subprocedure references are stored relationally and written only with their version

**Status:** Accepted
**Date:** 2026-10-09

## Context

A procedure version may compose other procedures through named references ([#2](https://github.com/ashuangiras/polaroid/issues/2)). Each reference names a target procedure, a version policy and the child's inputs. Reference-graph validation and traversal follow in [#3](https://github.com/ashuangiras/polaroid/issues/3), so references must be queryable as a graph. A version is immutable once stored ([ADR-0004](0004-append-only-versions-with-expected-base.md)), and that must also hold for its references. Polaroid never interprets a procedure's `contract`, so it cannot check child inputs against the target's inputs.

## Decision

- A reference is `{name, procedure_id, version_policy, inputs}`.
  - `name` uses the canonical-key format and is unique within the version.
  - `procedure_id` names the target by its stable ID, as bindings do.
  - `version_policy` is the binding type: `{"pin": N}` or `{"contextual": {}}`.
- `inputs` maps each child input name to exactly one of:
  - `{"input": "<parent input name>"}`, which passes a parent input through;
  - `{"value": <any JSON>}`, which passes a literal.

  Polaroid checks only this shape, never the names against either contract.
- An unknown target or a missing pinned version is a field error (`400`), not `404`. The reference is part of the submitted definition.
- References are rows of `procedure_version_references`, keyed by `(procedure_id, version, position)`. Request order is kept. A target is a foreign key to `procedures`, and a trigger checks that a pinned version exists.
- References are written only together with their version. A trigger rejects a reference row for a version that already exists, so references are inserted before the version row in the same transaction. A deferred foreign key to `procedure_versions` makes the commit fail if the version row never arrives. Update and delete triggers make reference rows immutable.
- A version without references reads back without a `references` field. Versions stored before migration `0003` are therefore returned byte-for-byte as before, and `"references": []` is the same as no references.

## Consequences

- #3 can walk the graph with SQL over one table.
- No later write, not even raw SQL, can add references to an existing version or change them.
- Child inputs are not checked against the target's contract. An agent sees a mismatched name only when it runs the procedure. That check belongs in records, not code.
