# Record contracts

This page defines Polaroid's records, how they are identified and how they are versioned. Each section is marked as **implemented** or **planned**. The [HTTP API](http-api.md) serves the implemented records with exactly these field names.

Summary: a **procedure** is a shared identity with immutable **versions**. A version may **reference** other procedures it composes. A **binding** lets one repository use a procedure under a local name, with immutable **binding revisions** that hold the repository's inputs and version policy.

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
| `references` | list of [references](#subprocedure-reference-implemented) | client | Optional. The procedures this version composes, in the order given. Omitted from responses when there are none; `[]` and `null` mean none. |
| `revision_reason` | string | client | Required and not blank. For version 1, why the procedure was created. Later, what changed and why. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

Polaroid checks the shape of `contract` and `instructions`, never their meaning. Each must be a JSON object with valid UTF-8 and unique member names at every level. Polaroid removes insignificant whitespace before storing it. Member order, values, number formatting and string escapes are kept exactly as submitted. String fields are stored exactly as submitted, without trimming.

### Subprocedure reference (implemented)

A reference is a named use of another procedure by a version. It is part of the version, so it is immutable with it ([ADR-0008](decisions/0008-subprocedure-references.md)).

| Field | Type | Rules |
| --- | --- | --- |
| `name` | string | Canonical-key format, unique within the version. |
| `procedure_id` | string | The target procedure, which must exist. |
| `version_policy` | JSON object | The [binding policy type](#binding-revision-implemented): `{"pin": N}`, where the target must have version `N`, or `{"contextual": {}}`, stored but not resolved yet. |
| `inputs` | JSON object | Required, and may be `{}`. Maps each child input name to exactly one source: `{"input": "<parent input name>"}` passes a parent input through, and `{"value": <any JSON>}` passes a literal. Stored compacted, otherwise as submitted. |

Polaroid validates the shape only. It does not check input names against either procedure's `contract`, which it never interprets. Field errors name the reference by position, for example `version.references[1].inputs.module`. An unknown target is `400` on `version.references[i].procedure_id`, and a missing pinned version is `400` on `version.references[i].version_policy.pin`. Every failing reference is listed.

References are written in the same transaction as their version, and the database rejects adding, changing or removing them afterwards. Cycles, including self-references, are not detected yet, and no endpoint traverses the graph ([#3](https://github.com/ashuangiras/polaroid/issues/3)).

### Versioning rules (implemented)

1. A procedure and its version 1 are created in one atomic write.
2. A revision names a `base_version`. It is stored only if `base_version` is the latest version at the moment of writing. The new version is then `base_version + 1`.
3. Any other base, older or newer, is a conflict: `409 version_conflict`, with `latest_version` in the response. Nothing is stored. Concurrent revisions from the same base therefore produce exactly one success.
4. Versions are never modified or deleted, and their numbers have no gaps. The database rejects violations with triggers, even for clients that bypass `polaroidd`.
5. The intended agent loop is: read the latest version, edit it, and submit it with `base_version` set to that version. On a conflict, re-read, re-apply the change and submit again.

### Repository binding (implemented)

A binding records that a repository uses a shared procedure under a repository-local name. It refers to the procedure by ID and never copies its content, so the procedure's history is the same for every repository that binds it. Identity rules are in [ADR-0007](decisions/0007-repository-identity-for-bindings.md).

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `id` | string | server | A UUIDv7, opaque to clients. Never changes. |
| `repository` | string | client, at creation | A canonical path: 1–255 bytes of `/`-separated segments, each of lowercase ASCII letters, digits, `.`, `_` and `-`. No empty, `.` or `..` segment, and no `.git` suffix. For example `github.com/ashuangiras/polaroid`, or a chosen name such as `scratch`. Never changes. |
| `name` | string | client, at creation | The repository-local name, in the canonical-key format. `(repository, name)` is unique. Never changes. |
| `procedure_id` | string | client, at creation | An existing procedure. Never changes. |
| `created_at` | RFC 3339 timestamp, UTC | server | |
| `latest_revision` | integer ≥ 1 | derived | The highest revision number. |

For a repository with a remote, clients derive `repository` from the remote: host and path, lowercase, without scheme, user, port or `.git`. Polaroid validates the format and never rewrites it. A duplicate `(repository, name)` gets `409 binding_exists`. The same procedure may be bound under several names in one repository. Polaroid has no repository records: a repository is the identifier its bindings share.

### Binding revision (implemented)

A binding revision is one immutable configuration of a binding.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `binding_id` | string | server | The owning binding. |
| `revision` | integer ≥ 1 | server | 1 at creation. Each revision gets the latest revision plus 1. |
| `inputs` | JSON object | client | Required, and may be `{}`. The repository's local input values. The members are free-form, and they are not checked against the procedure's `contract`. |
| `version_policy` | JSON object | client | Exactly one of `{"pin": N}` or `{"contextual": {}}`. |
| `revision_reason` | string | client | Required and not blank. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

- **`{"pin": N}`** selects version `N` of the bound procedure. `N` must be an integer of at least 1, and the procedure must have that version when the revision is stored; otherwise the request gets `400` naming `revision.version_policy.pin`.
- **`{"contextual": {}}`** asks for contextual resolution. It is stored and returned exactly as given. Contextual resolution is **not implemented** (increment 3): no endpoint resolves it, and no response names a selected version. The `contextual` object accepts no members yet. Any member is rejected, so parameters can be added later without changing the meaning of stored policies.

`inputs` is stored like `contract`: insignificant whitespace removed, everything else exactly as submitted.

Binding revisions follow the [versioning rules](#versioning-rules-implemented) with `base_revision` in place of `base_version`. A binding and its revision 1 are created in one atomic write. A revision is stored only if `base_revision` is the latest revision; any other base gets `409 revision_conflict` with `latest_revision`. Database triggers reject `UPDATE` and `DELETE` of bindings and revisions, gaps in revision numbers, and a pin to a version the procedure does not have.

## Planned records (not implemented)

These follow the established design. None of them exist in code, storage or the API yet. Their fields and rules are settled in the [roadmap](../development/roadmap.md) work items, and the open questions below must be answered before implementation.

### Reference-graph validation (increment 2)

Reject versions whose references would form a cycle, including a self-reference, and serve a bounded traversal of a version's composition graph ([#3](https://github.com/ashuangiras/polaroid/issues/3)).

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
- How is an environment identified, and with which attributes? (Repositories are identified by canonical path; see [ADR-0007](decisions/0007-repository-identity-for-bindings.md).)
- How is evidence stored, inline or by reference, and with what size limits?

## Compatibility

- Field names and rules on this page are part of the `/v1` API contract. Adding an optional field is backward-compatible. Renaming or removing a field, or tightening a rule, needs a new API version and an ADR.
- Schema changes are new migrations, and they never rewrite stored records. A column added later must give existing rows a value that means "absent", not an invented one.
