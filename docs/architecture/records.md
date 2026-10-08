# Record contracts

This page defines Polaroid's records, how they are identified and how they are versioned. Each section is marked as **implemented** or **planned**. The [HTTP API](http-api.md) serves the implemented records with exactly these field names.

## Implemented records

### Procedure (implemented)

A procedure is a stable identity. It does not belong to any repository, so many repositories can reuse it without copying its instructions.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `id` | string | server | A UUIDv7 in lowercase hex with hyphens. Clients must treat it as opaque. Never changes. |
| `canonical_key` | string | client, at creation | 1–128 bytes, matching `^[a-z0-9]+([._-][a-z0-9]+)*$`, for example `go.dependency.add`. Unique across all procedures. Never changes. |
| `created_at` | RFC 3339 timestamp, UTC | server | |
| `latest_version` | integer ≥ 1 | derived | The highest version number. |

The canonical key has exactly one accepted spelling: lowercase, with single separators. So exact-match uniqueness also rules out duplicates that differ only by case or separator. The storage layer enforces uniqueness, and a duplicate gets `409 canonical_key_exists`.

Semantic duplicate detection, aliases and identity merging are **not implemented**. They are later work.

### Procedure version (implemented)

A version is one immutable definition of a procedure.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `procedure_id` | string | server | The owning procedure. |
| `version` | integer ≥ 1 | server | 1 at creation. Each revision gets the latest version plus 1. |
| `philosophy` | string | client | Required and not blank. Why the procedure works the way it does. |
| `method` | string | client | Required and not blank. The approach in brief. |
| `contract` | JSON object | client | Required. For example inputs, outputs, preconditions and postconditions. The members are free-form. |
| `instructions` | JSON object | client | Required. The members are free-form, for example `steps`. |
| `revision_reason` | string | client | Required and not blank. For version 1, why the procedure was created. Later, what changed and why. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

Polaroid checks the shape of `contract` and `instructions`, never their meaning. Each must be a JSON object with valid UTF-8 and unique member names at every level. Polaroid removes insignificant whitespace before storing it. Member order, values, number formatting and string escapes are kept exactly as submitted. String fields are stored exactly as submitted, without trimming.

The established design also gives a version **references** to other procedures. They are **not implemented**. A request that contains `references` is rejected as an unknown field, not silently dropped. See [planned records](#planned-records-not-implemented).

### Versioning rules (implemented)

1. A procedure and its version 1 are created in one atomic write.
2. A revision names a `base_version`. It is stored only if `base_version` is the latest version at the moment of writing. The new version is then `base_version + 1`.
3. Any other base, older or newer, is a conflict: `409 version_conflict`, with `latest_version` in the response. Nothing is stored. Concurrent revisions from the same base therefore produce exactly one success.
4. Versions are never modified or deleted, and their numbers have no gaps. The database rejects violations with triggers, even for clients that bypass `polaroidd`.
5. The intended agent loop is: read the latest version, edit it, and submit it with `base_version` set to that version. On a conflict, re-read, re-apply the change and submit again.

## Planned records (not implemented)

These follow the established design. None of them exist in code, storage or the API yet. Their fields and rules are settled in the [roadmap](../development/roadmap.md) work items, and the open questions below must be answered before implementation.

### Repository binding and binding revision (increment 2)

- A **repository binding** is a repository-local reference to a shared procedure. It holds the repository identity, the procedure, local input values and a version-selection policy.
- A **binding revision** is an immutable snapshot of the binding's configuration. Revisions are guarded by a base revision, as procedure versions are, so concurrent edits conflict instead of overwriting each other.

### Subprocedure reference (increment 2)

A procedure version may refer to other procedures through **named references**. Each reference has a name, a target procedure, a version policy and explicit child inputs mapped from the parent's inputs.

- The version policy either **pins** an exact version or asks for **contextual** resolution.
- Validation: the target exists, a pinned version exists, and references form no cycle. Graph traversal is bounded.

### Execution and subprocedure execution (increment 3)

- An **execution** records exactly what ran: the exact procedure version (and binding revision, if any), the effective inputs, the repository, the commit, the environment, the outcome and observable evidence.
- A **subprocedure execution** is a child execution linked to its parent through the named reference it fulfilled.
- When a reference asked for contextual resolution, the execution still records the exact versions actually used.

### Verification semantics (increment 3)

- Verification is specific to one combination of repository, commit, environment, effective inputs and child versions. Success in one repository does not establish success in another.
- When the child-version combination changes, the parent needs fresh verification. Historical evidence stays associated with the combination it was recorded for.
- A successful parent execution is validated against its children's evidence.

### Open questions

- What does contextual resolution select before any evidence exists: the latest version, or the latest version verified in a matching context?
- How are a repository and an environment identified? For example, by remote URL or by a canonical name, and with which environment attributes?
- How is evidence stored, inline or by reference, and with what size limits?

## Compatibility

- Field names and rules on this page are part of the `/v1` API contract. Adding an optional field is backward-compatible. Renaming or removing a field, or tightening a rule, needs a new API version and an ADR.
- Schema changes are new migrations, and they never rewrite stored records. A column added later must give existing rows a value that means "absent", not an invented one.
