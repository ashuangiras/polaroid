# Record contracts

This page defines Polaroid's records, how they are identified and how they are versioned. Each section is marked as **implemented** or **planned**. The [HTTP API](http-api.md) serves the implemented records with exactly these field names.

Summary: a **procedure** is a shared identity with immutable **versions**. A version may **reference** other procedures it composes, and declares where it **applies**. A **repository** is a registered identity with a canonical identifier and aliases. A **binding** lets one repository use a procedure under a local name, with immutable **binding revisions** that hold the repository's inputs and version policy. An **execution** records one finished run of an exact version, and may link the **child executions** that fulfilled its references. **Verification** is derived from executions per combination of context and child versions. A **feedback report** may name its subject. All of them live in one shared catalog; repository scope organizes records and is not access control.

## Implemented records

### Procedure (implemented)

A procedure is a stable identity. It does not belong to any repository, so many repositories can reuse it without copying its instructions.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `id` | string | server | A UUIDv7 in lowercase hex with hyphens. Clients must treat it as opaque. Never changes. |
| `canonical_key` | string | client, at creation | 1–128 bytes, matching `^[a-z0-9]+([._-][a-z0-9]+)*$`, for example `go.dependency.add`. Unique across all procedures. Never changes. |
| `created_at` | RFC 3339 timestamp, UTC | server | |
| `latest_version` | integer ≥ 1 | derived | The highest version number. |
| `scope` | string | derived | The latest version's [applicability](#applicability-and-origin-implemented): `shared`, `local` or `unspecified`. |
| `goal`, `applicability` | string, JSON object | derived | The latest version's, omitted when it has none. |
| `origin` | `{repository_id, reason, created_at}` | client, once | Where and why the procedure was first created. Omitted until recorded. |

The canonical key has exactly one accepted spelling: lowercase, with single separators. So exact-match uniqueness also rules out duplicates that differ only by case or separator. The storage layer enforces uniqueness, and a duplicate gets `409 canonical_key_exists`. Canonical keys stay unique across the whole catalog, not per repository.

Semantic duplicate detection and procedure identity merging are **not implemented**. They are later work.

### Procedure version (implemented)

A version is one immutable definition of a procedure.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `procedure_id` | string | server | The owning procedure. |
| `version` | integer ≥ 1 | server | 1 at creation. Each revision gets the latest version plus 1. |
| `philosophy` | string | client | Required and not blank. Why the procedure works the way it does. |
| `method` | string | client | Required and not blank. The approach in brief. |
| `goal` | string | client | Optional, single-line and not blank when given. What the procedure achieves; searched by discovery. Omitted from responses when absent. |
| `applicability` | JSON object | client | Optional: `{"shared": {}}`, or `{"repository": "<repository id>"}` for a version local to one registered repository. Absent means *unspecified*, and is omitted from responses. See [applicability and origin](#applicability-and-origin-implemented). |
| `contract` | JSON object | client | Required. For example inputs, outputs, preconditions and postconditions. The members are free-form. |
| `instructions` | JSON object | client | Required. The members are free-form, for example `steps`. |
| `references` | list of [references](#subprocedure-reference-implemented) | client | Optional. The procedures this version composes, in the order given. Omitted from responses when there are none; `[]` and `null` mean none. |
| `revision_reason` | string | client | Required and not blank. For version 1, why the procedure was created. Later, what changed and why. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

Polaroid checks the shape of `contract` and `instructions`, never their meaning. Each must be a JSON object with valid UTF-8 and unique member names at every level. Polaroid removes insignificant whitespace before storing it. Member order, values, number formatting and string escapes are kept exactly as submitted. String fields are stored exactly as submitted, without trimming.

Member order is kept as Polaroid receives it, but JSON gives it no meaning (RFC 8259), and some clients reorder members before sending; VS Code Copilot chat does so over MCP. Keep order-sensitive data, such as steps, in arrays, never as ordered object members. This applies to every free-form object on this page.

### Subprocedure reference (implemented)

A reference is a named use of another procedure by a version. It is part of the version, so it is immutable with it ([ADR-0008](decisions/0008-subprocedure-references.md)).

| Field | Type | Rules |
| --- | --- | --- |
| `name` | string | Canonical-key format, unique within the version. |
| `procedure_id` | string | The target procedure, which must exist. |
| `version_policy` | JSON object | The [binding policy type](#binding-revision-implemented): `{"pin": N}`, where the target must have version `N`, or `{"contextual": {}}`, resolved from evidence ([contextual resolution](#contextual-resolution-implemented)). |
| `inputs` | JSON object | Required, and may be `{}`. Maps each child input name to exactly one source: `{"input": "<parent input name>"}` passes a parent input through, and `{"value": <any JSON>}` passes a literal. Stored compacted, otherwise as submitted. |

Polaroid validates the shape only. It does not check input names against either procedure's `contract`, which it never interprets. Field errors name the reference by position, for example `version.references[1].inputs.module`. An unknown target is `400` on `version.references[i].procedure_id`, and a missing pinned version is `400` on `version.references[i].version_policy.pin`. Every failing reference is listed.

References are written in the same transaction as their version, and the database rejects adding, changing or removing them afterwards.

### Composition graph (implemented)

A version's references, their targets' references, and so on, form its composition graph ([ADR-0009](decisions/0009-reference-graph-rules.md)).

- **Selected versions:** a pinned reference selects its pinned version. Without a resolution context, a contextual reference selects the target's **latest version** at the time of the check or read; the write-time check always does. With a context, [contextual resolution](#contextual-resolution-implemented) selects from evidence.
- **No cycles:** a path may not reach a procedure that is already on it, at any version. `A → A` and `A v2 → B → A v1` are both cycles. Every write of a version with references expands its graph in the same transaction. A cycle gets `409 reference_cycle`, and nothing is stored. This includes a cycle closed by a later revision of a contextually referenced target.
- **Limits:** a graph may be at most **32** references deep and **2048** nodes in the expanded tree. A target shared by two references counts under each. A larger graph gets `422 graph_too_large`, never partial data. Writes check the new version's graph. A later revision elsewhere can still push an existing version's graph over the limits, and reading that graph then returns `422`.
- Versions stored before cycle checking existed are never rewritten. If one holds a cycle, reading its graph returns `409 reference_cycle`.

### Versioning rules (implemented)

1. A procedure and its version 1 are created in one atomic write.
2. A revision names a `base_version`. It is stored only if `base_version` is the latest version at the moment of writing. The new version is then `base_version + 1`.
3. Any other base, older or newer, is a conflict: `409 version_conflict`, with `latest_version` in the response. Nothing is stored. Concurrent revisions from the same base therefore produce exactly one success.
4. Versions are never modified or deleted, and their numbers have no gaps. The database rejects violations with triggers, even for clients that bypass `polaroidd`.
5. The intended agent loop is: read the latest version, edit it, and submit it with `base_version` set to that version. On a conflict, re-read, re-apply the change and submit again.

### Repository (implemented)

A repository is a registered identity ([ADR-0019](decisions/0019-repository-registry.md)). Nothing registers one implicitly: not a read, a binding, an execution or a migration.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `id` | string | server | A UUIDv7. Never changes. |
| `name` | string | client, at registration | The display name: single-line and not blank. Never changes. |
| `identifier` | string | client, at registration | The canonical identifier, in the [repository identifier format](#repository-binding-implemented). Never changes. |
| `aliases` | list of `{identifier, reason, created_at}` | client, appended | Other identifiers of the same repository, oldest first, each with the reason it is the same repository. Always present, `[]` when there are none. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

- **Identifiers are unique and permanent.** An identifier belongs to at most one repository, as its canonical identifier or as an alias, and is never moved or removed. Registering a taken identifier, either way, is `409 repository_identifier_exists`; of two racing registrations, exactly one succeeds.
- **Normalization is not identity.** Polaroid validates the one accepted spelling and never rewrites it. It never decides that two identifiers are the same repository: a fork, a rename or a mirror is a different repository until someone adds its identifier as an alias.
- **Association is derived.** Bindings, executions and feedback keep the identifier they were written with. Which repository they belong to is read through the registry, so a later registration or alias associates earlier records without changing them. Unregistered identifiers stay valid wherever they were valid.
- **Where identity counts:** repository filters on lists (bindings, executions, feedback, and procedure discovery) cover every identifier of the registered repository; applicability compares repository IDs; and a binding's local name is unique across the repository's identifiers, so an alias that would bring a second binding of a name is refused with `409 binding_exists`. Verification combinations and resolution evidence still compare identifier strings exactly, so an alias never merges or changes evidence. Record runs with the canonical identifier.

### Applicability and origin (implemented)

[ADR-0020](decisions/0020-procedure-origin-and-applicability.md) separates where a procedure came from and where its contract is meant to hold.

- **Origin** is provenance on the procedure identity: `{repository_id, reason}`, given at creation or recorded later with `POST /v1/procedures/{id}/origin`, at most once (`409 origin_exists`). The repository must be registered. Origin never limits where a procedure applies.
- **Applicability** is declared per version, so changing it is a new version with a `revision_reason`, and earlier versions keep their declaration. Promotion from local to shared, and narrowing, are such versions. Applicability is a declaration, not evidence: verification stays per recorded target, and promotion transfers none.
- **Applicable in a repository:** an unspecified or shared version is applicable everywhere; a local version only under an identifier registered to its repository.
- **Composition:** a local version admits children that are unspecified, shared, or local to its own repository; a shared or unspecified version admits no local child. On write, a pinned reference's version must be admitted, and a contextual target must have at least one admitted version; otherwise `400` on `version.references[i].procedure_id`. On read, contextual selection only considers admitted versions, and ignores evidence links to versions it does not admit.
- **Bindings:** a new binding, or a new binding revision, is refused (`400`) if a pinned version is not applicable in the binding's repository, or, for a contextual policy, if the procedure's latest version is not. Existing bindings are never invalidated: after a narrowing version, a contextual binding elsewhere keeps resolving to the newest version still applicable to it.
- **Resolution:** a binding's contextual policy selects among applicable versions. The graph endpoint with a repository refuses a version that is not applicable there (`400` on `repository`).
- **Executions:** the version must be applicable in the execution's repository, and each linked child must be admitted by the parent's version (`400`). Executions recorded earlier are unchanged.
- **Existing versions** are unspecified. Nothing is classified by migration; a version that declares applicability is appended by someone who has read the contract and its bindings.

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

For a repository with a remote, clients derive `repository` from the remote: host and path, lowercase, without scheme, user, port or `.git`. Polaroid validates the format and never rewrites it. A duplicate `(repository, name)` gets `409 binding_exists`, and so does a name that another identifier of the same [registered repository](#repository-implemented) already uses. The same procedure may be bound under several names in one repository. A binding needs no registered repository unless its procedure is local to one.

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
- **`{"contextual": {}}`** asks for contextual resolution. It is stored and returned exactly as given, and binding reads never name a selected version. `GET /v1/bindings/{id}/resolution` resolves it in an environment ([contextual resolution](#contextual-resolution-implemented)). The `contextual` object accepts no members yet. Any member is rejected, so parameters can be added later without changing the meaning of stored policies.

`inputs` is stored like `contract`: insignificant whitespace removed, everything else exactly as submitted.

Binding revisions follow the [versioning rules](#versioning-rules-implemented) with `base_revision` in place of `base_version`. A binding and its revision 1 are created in one atomic write. A revision is stored only if `base_revision` is the latest revision; any other base gets `409 revision_conflict` with `latest_revision`. Database triggers reject `UPDATE` and `DELETE` of bindings and revisions, gaps in revision numbers, and a pin to a version the procedure does not have.

### Execution (implemented)

An execution is an immutable record of one finished run, written once after the run and never changed or deleted ([ADR-0010](decisions/0010-execution-records.md)).

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `id` | string | server | A UUIDv7. |
| `procedure_id`, `version` | string, integer ≥ 1 | client | The exact version that ran. It must exist. |
| `binding_id`, `binding_revision` | string, integer ≥ 1 | client | Optional. Give both or neither. The binding must be for this procedure and repository, and a pinned revision must pin `version`. Both fields are omitted from responses when no binding was used. |
| `repository` | string | client | The canonical path ([ADR-0007](decisions/0007-repository-identity-for-bindings.md)). |
| `commit` | string | client | A full commit hash: 40 or 64 lowercase hex characters. |
| `environment` | object | client | `{"name": ..., "attributes": {...}}`. `name` uses the canonical-key format and is the environment's identity. `attributes` is a free-form JSON object, which may be `{}` and is never interpreted. |
| `inputs` | JSON object | client | The effective inputs. Free-form, and may be `{}`. |
| `outcome` | string | client | `succeeded` or `failed`. |
| `evidence` | JSON object | client | Free-form and non-empty, for example commands with their exit codes, or links to external artifacts with digests. Limited only by the 1 MiB request size. |
| `children` | list of `{reference, execution_id}` | client | Optional. The [child executions](#subprocedure-execution-implemented) that fulfilled the version's references, in the order given. Omitted from responses when there are none. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

`environment.attributes`, `inputs` and `evidence` are stored like `contract`: compacted, but otherwise exactly as submitted.

An unknown procedure or binding is `404`. A missing version or binding revision, a version that is not [applicable](#applicability-and-origin-implemented) in `repository`, and every mismatch, is `400` naming the field. The database enforces the same rules with foreign keys and triggers, and it rejects `UPDATE` and `DELETE`. Recording an execution changes no other record. `created_at` is assigned in the write transaction, strictly after every stored execution's, so it is commit order ([ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md)).

### Subprocedure execution (implemented)

A child execution is an ordinary execution that fulfilled one of a parent version's references. It is recorded first, and the parent links it in `children` when the parent is recorded ([ADR-0011](decisions/0011-subprocedure-executions.md)). A link is accepted only if all of these hold:

- `reference` names a reference of the parent's version;
- the child execution exists and ran the reference's target procedure;
- for a pinned reference, the child ran exactly the pinned version. A contextual reference accepts any version, and the child records the exact one;
- the child has the parent's `repository` and `commit`. Its environment may differ.

Each reference is fulfilled by at most one child, and none is required. Each execution is the child of at most one parent. Every violation is `400` naming `children[i].reference` or `children[i].execution_id`. Children may have children of their own, so a tree is recorded bottom-up. Links are written only in the parent's transaction and never change. The database enforces all of these rules too.

### Verification (implemented)

Verification is derived from executions and their links on every read. Nothing is stored for it ([ADR-0012](decisions/0012-derived-verification.md)).

- **Verified execution.** An execution is verified when it succeeded **and** every reference of its version is fulfilled by a linked child that is itself verified, recursively. An execution of a version without references is verified when it succeeded. A parent without a child for some reference is never verified. Each direct reason is reported as `outcome_failed`, `missing_child` or `child_not_verified`.
- **Combination.** An execution verifies one combination, made of these parts:
  - its `repository`, `commit` and `environment.name`. `environment.attributes` is not part of it;
  - its `inputs` in canonical form: object members sorted, insignificant whitespace removed, and strings and non-integer numbers canonicalized as in RFC 8785. Integers are kept exact, so `1.0`, `1e0` and `1` are equal, but distinct large integers never are. Member order never matters, so equivalent inputs that arrive with their members in a different order, as some MCP clients send them, are the same combination. Array elements keep their order, so `[1, 2]` and `[2, 1]` are different inputs;
  - its **child-version tree**: each linked child's reference name and version, with that child's own tree, in the version's reference order.

  Success in one combination says nothing about another. Changing any child's version anywhere in the tree makes a new combination, which needs a fresh parent execution. Earlier executions stay with the combination they were recorded in.
- **Status of a combination.** It is the verification of its **latest** execution, by `created_at` and then `id`. A failure after a success therefore makes the combination unverified until a newer execution succeeds. Every execution remains listed in `execution_ids`.

### Contextual resolution (implemented)

Contextual references and contextual binding policies resolve from verification evidence in a requesting context ([ADR-0013](decisions/0013-evidence-based-resolution.md)). Nothing is stored for a resolution.

- **Context:** a `repository` and an `environment` name. The commit and inputs are not part of it.
- **Verified in the context:** a version is verified in a context when its latest execution with that repository and environment is verified, whatever its commit and inputs. That execution is the version's *evidence*: selection evidence, not verification at any particular commit ([below](#selection-evidence-and-target-verification-implemented)).
- **Selection:** the walk starts at the root, which carries its own evidence if it has any.
  - **Under a node with evidence:** every reference selects the version of the child execution that the evidence linked for it, and that child execution becomes the child node's evidence. A verified combination is thus followed as a whole.
  - **Under a node without evidence:**
    - a pinned reference selects its pin, with the pin's own evidence if any;
    - a contextual reference selects the **highest version number** verified in the context;
    - if none is verified, it selects the **latest** version, unverified.
- **Reporting:** every graph edge has `selected_by` (`pin`, `evidence` or `latest`). Every node with evidence has `verified_by`, the evidence's execution ID, and `selection_evidence`, which says where that execution ran.
- **Safety:** evidence can select older versions, so the walk keeps the cycle and size checks of the [composition graph](#composition-graph-implemented) and reports them on read.
- **Without a context:** the graph endpoint selects as before, with contextual references taking the latest version.

### Selection evidence and target verification (implemented)

Resolution answers two different questions, and reports them separately ([ADR-0018](decisions/0018-selection-evidence-and-target-verification.md)).

- **Selection evidence** says why a version, or a whole child combination, was selected: an earlier verified execution in the context, at **any commit and with any inputs**. Each such node reports `selection_evidence`: `execution_id`, `repository`, `commit` and `environment`. It is a reason to try the version, not verification of anything else. `verified_by` repeats the execution ID, for compatibility, and means the same.
- **Target verification** says whether the selected combination is verified at one exact target. It is reported only when the request names a target: a `commit` and the root's effective `inputs`, given together, in the context's repository and environment. Each node then reports `target_verification` for its own selected combination at that target:
  - the repository, the target commit and the environment name;
  - its effective inputs: the root's are the target's `inputs`; a child's come from its reference's input mapping applied to its parent's effective inputs (`{"input": p}` takes the parent's member `p`, `{"value": v}` takes `v`, and a child input whose parent member is absent is left out);
  - its selected child-version tree, pins included.

  The status is that of the combination's latest execution, as for any [combination](#verification-implemented). With no execution there, the node is unverified. A child's success never verifies its parent, and a parent recorded with another child combination does not verify the selected one.
- **No inference.** Polaroid compares commits, environment names and canonical inputs exactly. It never inspects a checkout and never infers compatibility from ancestry, tree contents, branch names or time. Without a target, no node claims target verification. Working-tree state counts only as part of the inputs, where a procedure's contract puts it there (for example `working_tree` in [ADR-0017](decisions/0017-development-procedures-as-records.md)'s procedures).
- **Selection is unchanged by a target**, and target verification never changes a recorded execution or its own verification.

### Feedback report (implemented)

A feedback report tells Polaroid's maintainers about a problem with Polaroid or with a record it holds, or suggests an improvement. It is written once and never changed or deleted. It has no triage state ([ADR-0015](decisions/0015-feedback-reports.md), [ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md)).

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `id` | string | server | A UUIDv7. |
| `kind` | string | client | `problem` or `suggestion`. |
| `summary` | string | client | Required, non-blank, valid UTF-8, and a single line: no line terminator of any kind. |
| `details` | string | client | Required, non-blank, valid UTF-8. May span lines. |
| `reporter` | string | client | The agent or person reporting, in the canonical-key format, for example `copilot.vscode`. Not authenticated. |
| `context` | JSON object | client | Optional and free-form, for example the tool or endpoint involved, or record IDs. Stored like `contract` and never interpreted. IDs in it are not checked. Absent is stored and returned as `{}`. |
| `subject` | JSON object | client | Optional. What the report is about: `{"type": "service"}`, `{"type": "repository", "repository_id"}`, `{"type": "procedure", "procedure_id"[, "version"]}`, `{"type": "binding", "binding_id"[, "revision"]}` or `{"type": "execution", "execution_id"}`. The record must exist; a member of another type is `400`. Omitted when absent. |
| `repository` | string | client | Optional. The repository identifier the report was made in, registered or not. Omitted when absent. |
| `execution_id` | string | client | Optional. A related execution, which must exist. Omitted when absent. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

`subject` says what the report is about; `repository` and `execution_id` say where it was made. They must agree: the execution ran in that repository (by identity); for a procedure subject, it ran that procedure and version; for a binding subject, it used that binding and revision; for an execution subject, it is that execution; and the repository of a repository, binding or execution subject is the report's repository. A disagreement is `400` naming the field.

A report without `subject`, including every report stored before subjects existed, keeps the meaning ADR-0015 gave it: it is about the Polaroid service, and the `subject_type=service` filter includes it. Its stored fields are never changed. A later alias makes an older report's identifier match its repository in filters, and a later applicability change does not affect reports at all.

Reporting feedback changes no other record. The database enforces the field rules with `CHECK` constraints, checks that a subject exists with a trigger, and rejects `UPDATE` and `DELETE`.

## Planned records (not implemented)

No records are planned in the current increments. Later work (semantic discovery, access control, the PoC import) is listed in the [roadmap](../development/roadmap.md), and its records are designed when it is refined into issues.

## Compatibility

- Field names and rules on this page are part of the `/v1` API contract. Adding an optional field is backward-compatible. Renaming or removing a field, or tightening a rule, needs a new API version and an ADR.
- Schema changes are new migrations, and they never rewrite stored records. A column added later must give existing rows a value that means "absent", not an invented one.
- Migration 7 ([#35](https://github.com/ashuangiras/polaroid/issues/35)) adds the repository registry, origins, `goal` and `applicability`, and the feedback subject columns. Existing versions read back unspecified, existing reports without a subject, and nothing is registered. Responses gain `scope` on procedures, and omit every absent new field, so existing versions, bindings, executions and reports are served as before. Requests that do not use the new fields behave as before, except that recording an execution, creating or revising a binding, or writing a version is refused where a declared applicability forbids it, which no stored version could declare before.
