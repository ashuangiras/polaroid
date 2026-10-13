# Record contracts

This page defines Polaroid's records, how they are identified and how they are versioned. Each section is marked as **implemented** or **planned**. The [HTTP API](http-api.md) serves the implemented records with exactly these field names.

Summary: a **procedure** is a shared identity with immutable **versions**. A version may **reference** other procedures it composes, and declares where it **applies**. A **repository** is a registered identity with a canonical identifier and aliases. A **binding** lets one repository use a procedure under a local name, with immutable **binding revisions** that hold the repository's inputs and version policy. An **execution** records one finished run of an exact version, and may link the **child executions** that fulfilled its references. **Verification** is derived from executions per combination of context and child versions. A **feedback report** may name its subject. A **toil item** is a versioned assessment of repetitive work, with append-only **toil revisions** and an assessment lifecycle. An execution can be **invalidated** as evidence, and a procedure **retired** and reactivated, through append-only lifecycle metadata that never changes the records it describes. All of them live in one shared catalog; repository scope organizes records and is not access control.

## Implemented records

### Procedure (implemented)

A procedure is a stable identity. It does not belong to any repository, so many repositories can reuse it without copying its instructions.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `id` | string | server | A UUIDv7 in lowercase hex with hyphens. Clients must treat it as opaque. Never changes. |
| `canonical_key` | string | client, at creation | 1–128 bytes, matching `^[a-z0-9]+([._-][a-z0-9]+)*$`, for example `go.dependency.add`. Unique across all procedures. Never changes. |
| `created_at` | RFC 3339 timestamp, UTC | server | |
| `latest_version` | integer ≥ 1 | derived | The highest version number. |
| `scope` | string | derived | Version `latest_version`'s [applicability](#applicability-and-origin-implemented): `shared`, `local` or `unspecified`. It describes that version only; resolution may select another, whose own `scope` its graph node reports ([ADR-0023](decisions/0023-pagination-guarantees-and-scope-labels.md)). |
| `goal`, `applicability` | string, JSON object | derived | Version `latest_version`'s, omitted when it has none. |
| `origin` | `{repository_id, reason, created_at}` | client, once | Where and why the procedure was first created. Omitted until recorded. |
| `lifecycle_status` | string | derived | `active` or `retired`: the [lifecycle](#procedure-lifecycle-implemented) state. Always present. |
| `lifecycle_event` | integer ≥ 1 | derived | The latest lifecycle event; in a procedure's history only, and omitted when it has none. |

The canonical key has exactly one accepted spelling: lowercase, with single separators. So exact-match uniqueness also rules out duplicates that differ only by case or separator. The storage layer enforces uniqueness, and a duplicate gets `409 canonical_key_exists`. Canonical keys stay unique across the whole catalog, not per repository.

**Lexical discovery and duplicate suggestions** are reads, not records ([ADR-0032](decisions/0032-lexical-discovery-and-duplicate-suggestions.md), [HTTP API](http-api.md#discover-procedures-for-a-task)). Discovery matches the words of a task against each procedure's latest version (canonical key, `goal`, `method`, `philosophy`, and the string values of `contract` and `instructions`), honouring applicability for a repository, and ranks candidates by how rare each matched word is among them and by where it occurs, the stated purpose first ([ADR-0033](decisions/0033-discovery-ranking-rarity-purpose-detail.md)). A duplicate check reports a proposal's existing canonical key as a collision and overlapping summaries as advisory suggestions. Neither reads executions or evidence, and neither stores, merges or refuses anything. Semantic duplicate detection and procedure identity merging are **not implemented**.

### Procedure version (implemented)

A version is one immutable definition of a procedure.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `procedure_id` | string | server | The owning procedure. |
| `version` | integer ≥ 1 | server | 1 at creation. Each revision gets the latest version plus 1. |
| `philosophy` | string | client | Required and not blank. Why the procedure works the way it does. |
| `method` | string | client | Required and not blank. The approach in brief. |
| `goal` | string | client | Optional, single-line and not blank when given. What the procedure achieves; searched by discovery. Omitted from responses when absent. |
| `scope` | string | derived | This version's own declaration: `shared`, `local` or `unspecified`. Always present in responses. |
| `applicability` | JSON object | client | Optional: `{"shared": {}}`, or `{"repository": "<repository id>"}` for a version local to one registered repository. Absent means *unspecified*, and is omitted from responses. See [applicability and origin](#applicability-and-origin-implemented). |
| `contract` | JSON object | client | Required. For example inputs, outputs, preconditions and postconditions. The members are free-form. |
| `instructions` | JSON object | client | Required. The members are free-form, for example `steps`. |
| `references` | list of [references](#subprocedure-reference-implemented) | client | Optional. The procedures this version composes, in the order given. Omitted from responses when there are none; `[]` and `null` mean none. |
| `revision_reason` | string | client | Required and not blank. For version 1, why the procedure was created. Later, what changed and why. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

Polaroid checks the shape of `contract` and `instructions`, never their meaning. Each must be a JSON object with valid UTF-8 and unique member names at every level. Polaroid removes insignificant whitespace before storing it. Member order, values, number formatting and string escapes are kept exactly as submitted; a response writes an escaped character that needs no escape as the character itself (`"\u00e9"` is returned as `"é"`). String fields are stored exactly as submitted, without trimming. Each has a [size limit](#write-limits-implemented).

Member order is kept as Polaroid receives it, but JSON gives it no meaning (RFC 8259), and some clients reorder members before sending; VS Code Copilot chat does so over MCP. Keep order-sensitive data, such as steps, in arrays, never as ordered object members. This applies to every free-form object on this page.

**Selective projection (`fields=`, #131, [ADR-0042](decisions/0042-context-efficient-projections.md)):** a read of one version may name a subset of `philosophy`, `method`, `goal`, `applicability`, `contract`, `instructions`, `references`, `revision_reason`, getting only those plus the identity above (`procedure_id`, `version`, `scope`, `created_at`, always present). `contract` and `instructions` are each returned whole or not at all: a projection never selects a member inside either, because Polaroid never interprets their content. Without `fields`, the response is the complete version, unchanged.

### Subprocedure reference (implemented)

A reference is a named use of another procedure by a version. It is part of the version, so it is immutable with it ([ADR-0008](decisions/0008-subprocedure-references.md)).

| Field | Type | Rules |
| --- | --- | --- |
| `name` | string | Canonical-key format, unique within the version. |
| `procedure_id` | string | The target procedure, which must exist. |
| `version_policy` | JSON object | The [binding policy type](#binding-revision-implemented): `{"pin": N}`, where the target must have version `N`, or `{"contextual": {}}`, resolved from evidence ([contextual resolution](#contextual-resolution-implemented)). |
| `inputs` | JSON object | Required, and may be `{}`. Maps each child input name to exactly one source: `{"input": "<parent input name>"}` passes a parent input through, and `{"value": <any JSON>}` passes a literal. Stored compacted, otherwise as submitted. |
| `condition` | string | Optional, not blank. Present: the reference is **conditional**, and the text says, for agents, when the referenced work applies. Absent: the reference is **required** ([ADR-0029](decisions/0029-conditional-references-and-applicability-decisions.md)). Never interpreted. |

Conditional means conditional on applicability, not optional: when the condition holds, the work is required. Every reference stored before conditions existed is required.

Polaroid validates the shape only. It does not check input names against either procedure's `contract`, which it never interprets. Field errors name the reference by position, for example `version.references[1].inputs.module`. An unknown target is `400` on `version.references[i].procedure_id`, and a missing pinned version is `400` on `version.references[i].version_policy.pin`. Every failing reference is listed.

References are written in the same transaction as their version, and the database rejects adding, changing or removing them afterwards.

### Composition graph (implemented)

A version's references, their targets' references, and so on, form its composition graph ([ADR-0009](decisions/0009-reference-graph-rules.md)).

- **Selected versions:** a pinned reference selects its pinned version. Without a resolution context, a contextual reference selects the target's **latest version** at the time of the check or read; the write-time check always does. With a context, [contextual resolution](#contextual-resolution-implemented) selects from evidence.
- **No cycles:** a path may not reach a procedure that is already on it, at any version. `A → A` and `A v2 → B → A v1` are both cycles. Every write of a version with references expands its graph in the same transaction. A cycle gets `409 reference_cycle`, and nothing is stored. This includes a cycle closed by a later revision of a contextually referenced target.
- **Limits:** a graph may be at most **32** references deep and **2048** nodes in the expanded tree. A target shared by two references counts under each. A larger graph gets `422 graph_too_large`, never partial data. Writes check the new version's graph. A later revision elsewhere can still push an existing version's graph over the limits, and reading that graph then returns `422`.
- Versions stored before cycle checking existed are never rewritten. If one holds a cycle, reading its graph returns `409 reference_cycle`.

**Compact projection (`format=plan`, `prepare_execution`, #131, [ADR-0042](decisions/0042-context-efficient-projections.md)):** the same resolved graph, flattened to one step per node in dependency order (a node's own references before the node itself) with only `procedure_id`, `version`, `selected_by`, a conditional reference's `decision`, its effective `inputs` at the target, and a `status` derived from the same target verification this section describes — never a second derivation. `next_action` prefers a `decision_required` step over a `pending` one, whatever their relative order, since deciding a conditional reference's applicability always precedes running anything. It is a read-time projection of this graph, not a different record: see [http-api.md](http-api.md#compact-execution-plans-formatplan-and-prepare_execution) for its exact shape.

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
- **Where identity counts:** repository filters on lists (bindings, executions, feedback, and procedure discovery) cover every identifier of the registered repository; applicability compares repository IDs; a binding's local name is unique across the repository's identifiers, so an alias that would bring a second binding of a name is refused with `409 binding_exists`; and verification combinations and resolution evidence match by identity ([below](#verification-implemented), [ADR-0022](decisions/0022-repository-identity-in-evidence.md)). Writes still compare identifiers exactly: a child execution names its parent's identifier, and an execution under a binding names the binding's. Prefer the canonical identifier, which lookups by alias return.

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
- **Declaration, admissibility and verification are separate** ([ADR-0023](decisions/0023-pagination-guarantees-and-scope-labels.md)). `shared` declares a contract intended for reuse and `local` one for a single repository; `unspecified` declares nothing, stays admissible everywhere for compatibility, and is no claim of general reuse. None of them says a version was verified anywhere. Every version and graph node reports its own `scope`; a procedure's `scope` is that of `latest_version` only. Before reusing a procedure, resolve it in your repository, read the selected node's `version` and `scope`, and read that version's contract.

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
| `repository` | string | client | The canonical path ([ADR-0007](decisions/0007-repository-identity-for-bindings.md)), exactly as submitted, forever. |
| `repository_id` | string | derived | The registered repository `repository` belongs to, read from the registry on every response; omitted while it is not registered ([ADR-0022](decisions/0022-repository-identity-in-evidence.md)). |
| `commit` | string | client | A full commit hash: 40 or 64 lowercase hex characters. It claims that exactly this commit ran. A run on uncommitted changes is evidence for no commit, so commit them locally first. Polaroid cannot check this. |
| `environment` | object | client | `{"name": ..., "attributes": {...}}`. `name` uses the canonical-key format and is the environment's identity. `attributes` is a free-form JSON object, which may be `{}` and is never interpreted. |
| `inputs` | JSON object | client | The effective inputs. Free-form, and may be `{}`. |
| `outcome` | string | client | `succeeded` or `failed`. |
| `evidence` | JSON object | client | Free-form and non-empty, for example commands with their exit codes, or links to external artifacts with digests. At most 65,536 bytes, measured as stored ([write limits](http-api.md#write-limits), ADR-0037). |
| `children` | list of `{reference, execution_id}` | client | Optional. The [child executions](#subprocedure-execution-implemented) that fulfilled the version's references, in the order given. Omitted from responses when there are none. |
| `decisions` | list of `{reference, applicable, rationale, evidence}` | client | Optional. One [applicability decision](#applicability-decision-implemented) per conditional reference. Omitted from responses when there are none. |
| `created_at` | RFC 3339 timestamp, UTC | server | |
| `evidence_status` | string | derived | `valid`, or `invalidated` after an [invalidation](#execution-evidence-invalidation-implemented). Always present, also in list items. |
| `invalidation` | `{reason, actor, created_at}` | lifecycle | Present only when the execution is invalidated. |

`environment.attributes`, `inputs` and `evidence` are stored like `contract`: compacted, but otherwise exactly as submitted.

An unknown procedure or binding is `404`. A missing version or binding revision, a version that is not [applicable](#applicability-and-origin-implemented) in `repository`, and every mismatch, is `400` naming the field. The database enforces the same rules with foreign keys and triggers, and it rejects `UPDATE` and `DELETE`. Recording an execution changes no other record. `created_at` is assigned in the write transaction, strictly after every stored execution's, so it is commit order ([ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md)).

### Subprocedure execution (implemented)

A child execution is an ordinary execution that fulfilled one of a parent version's references. It is recorded first, and the parent links it in `children` when the parent is recorded ([ADR-0011](decisions/0011-subprocedure-executions.md)). A link is accepted only if all of these hold:

- `reference` names a reference of the parent's version;
- the child execution exists and ran the reference's target procedure;
- for a pinned reference, the child ran exactly the pinned version. A contextual reference accepts any version, and the child records the exact one;
- the child has the parent's `repository` and `commit`. Its environment may differ;
- the child ran with exactly the inputs the reference maps from the parent's `inputs` ([ADR-0024](decisions/0024-child-inputs-follow-the-reference-mapping.md)). The mapping is applied as for [target verification](#selection-evidence-and-target-verification-implemented): `{"input": p}` takes the parent's member `p`, `{"value": v}` takes `v`, a child input whose parent member is absent is left out, and an empty mapping gives `{}`. The result and the child's `inputs` are compared in the canonical form [combinations](#verification-implemented) use. An extra, missing or different member is a mismatch: there is no subset match and no override.

Each reference is fulfilled by at most one child, and none is required. Each execution is the child of at most one parent. Every violation is `400` naming `children[i].reference` or `children[i].execution_id`; an input mismatch's message gives the child's inputs and the mapped ones. Children may have children of their own, so a tree is recorded bottom-up. Links are written only in the parent's transaction and never change. The database enforces all of these rules too, except the input rule: a trigger cannot reproduce the canonical comparison, so a link written with direct SQL can break it. Such a link, like any stored before the rule, is still served unchanged, but it never verifies its parent ([below](#verification-implemented)).

### Applicability decision (implemented)

An execution records, for each conditional reference of its version, whether the referenced work applied to that run ([ADR-0029](decisions/0029-conditional-references-and-applicability-decisions.md)). Decisions are part of the execution, written with it and never changed.

| Field | Type | Rules |
| --- | --- | --- |
| `reference` | string | A conditional reference of the version. At most one decision per reference. |
| `applicable` | boolean | Required. `true`: the work applied, and a verified child is required. `false`: it did not, and no child may be linked for the reference. |
| `rationale` | string | Required and not blank, for both answers: the basis of the decision, for example the files a change touched. |
| `evidence` | JSON object | Optional supporting context, stored compacted and never interpreted. Omitted when absent. |

- **Refused (`400` on `decisions[i]`):** a reference the version does not have; a required reference, which cannot be skipped and takes no decision; a duplicate; `applicable: false` with a child linked for the same reference.
- **Accepted but never verified:** a missing decision, and an applicable decision without its child, so that failed and partial runs stay recordable. A skip is the decision itself: no child execution is recorded or fabricated for it.
- **Trust boundary:** Polaroid checks the structure. Whether the condition held and whether the rationale is true are the recording agent's claims, kept for review. Polaroid never evaluates conditions or classifies tasks.
- **Database:** constraints and triggers repeat the structural rules (one decision per reference, only for a conditional reference that exists, a non-blank rationale, an object `evidence`, no child for a reference decided not applicable in either insertion order, written only with the parent, never updated or deleted). UTF-8 and whitespace-only checks are the service's.

### Verification (implemented)

Verification is derived from executions and their links on every read. Nothing is stored for it ([ADR-0012](decisions/0012-derived-verification.md)).

- **Verified execution.** An execution is verified when it is valid evidence (not [invalidated](#execution-evidence-invalidation-implemented)), it succeeded **and** every reference of its version is fulfilled by a linked child that ran with the inputs the reference maps and is itself verified, recursively, except a conditional reference the execution [decided](#applicability-decision-implemented) not applicable. Every conditional reference needs a decision. An execution of a version without references is verified when it succeeded. A parent without a child for some required or applicable reference is never verified. Each direct reason is reported as `invalidated`, `outcome_failed`, `missing_decision`, `missing_child`, `child_inputs_mismatch` (a stored link whose child's inputs differ from the mapped ones, [ADR-0024](decisions/0024-child-inputs-follow-the-reference-mapping.md)), `child_invalidated` (the linked child is invalidated) or `child_not_verified`. A mismatched child that is itself unverified gets both of the relevant child codes, and every ancestor of an unverified execution is unverified. The same verification decides combination statuses, contextual selection and target verification.
- **Combination.** An execution verifies one combination, made of these parts:
  - its repository **identity**, `commit` and `environment.name`. `environment.attributes` is not part of it. The identity of a registered identifier, canonical or alias, is its repository; an unregistered identifier is its own identity and matches only itself. Similar names, forks, equal commits or identical trees never make two identifiers one. A combination reports `repository` (the canonical identifier, or the unregistered identifier) and `repository_id` when registered ([ADR-0022](decisions/0022-repository-identity-in-evidence.md));
  - its `inputs` in canonical form: object members sorted, insignificant whitespace removed, and strings and non-integer numbers canonicalized as in RFC 8785. Integers are kept exact, so `1.0`, `1e0` and `1` are equal, but distinct large integers never are. Member order never matters, so equivalent inputs that arrive with their members in a different order, as some MCP clients send them, are the same combination. Array elements keep their order, so `[1, 2]` and `[2, 1]` are different inputs;
  - its **child-version tree**: each linked child's reference name and version, with that child's own tree, and each conditional reference decided not applicable as `{"reference", "skipped": true}`, in the version's reference order. An execution that ran a conditional child and one that skipped it are different combinations; two skips with differently worded rationales are the same one.

  Success in one combination says nothing about another. Changing any child's version anywhere in the tree makes a new combination, which needs a fresh parent execution. Earlier executions stay with the combination they were recorded in.
- **Status of a combination.** It is the verification of its **latest valid** execution, by `created_at` and then `id`, across every identifier of the identity; invalidated executions are listed in `invalidated_execution_ids` and never decide. A failure after a success therefore makes the combination unverified until a newer execution succeeds, under whichever identifier either was recorded. Every execution remains listed in `execution_ids`.
- **Registering an alias later** is retroactive for derived verification: from then on, executions recorded earlier under the alias belong to the same combinations as those under the canonical identifier, and the latest valid execution across both decides each status, so a status can change in either direction. No execution changes, and identifiers are never removed, so registration only ever merges combinations.

### Contextual resolution (implemented)

Contextual references and contextual binding policies resolve from verification evidence in a requesting context ([ADR-0013](decisions/0013-evidence-based-resolution.md)). Nothing is stored for a resolution.

- **Context:** a `repository` and an `environment` name. The commit and inputs are not part of it. The repository matches by identity.
- **Verified in the context:** a version is verified in a context when its latest valid (not invalidated) execution with that repository and environment is verified, whatever its commit and inputs. That execution is the version's *evidence*: selection evidence, not verification at any particular commit ([below](#selection-evidence-and-target-verification-implemented)).
- **Selection:** the walk starts at the root, which carries its own evidence if it has any.
  - **Under a node with evidence:** every reference selects the version of the child execution that the evidence linked for it, and that child execution becomes the child node's evidence. A verified combination is thus followed as a whole. A conditional reference the evidence decided not applicable has no child to follow: it selects as under a node without evidence, and the edge reports `skipped_by`, the evidence's ID ([ADR-0029](decisions/0029-conditional-references-and-applicability-decisions.md)).
  - **Under a node without evidence:**
    - a pinned reference selects its pin, with the pin's own evidence if any;
    - a contextual reference selects the **highest version number** verified in the context;
    - if none is verified, it selects the **latest** version, unverified.
- **Reporting:** every graph edge has `selected_by` (`pin`, `evidence` or `latest`). Every node with evidence has `verified_by`, the evidence's execution ID, and `selection_evidence`, which says where that execution ran.
- **Safety:** evidence can select older versions, so the walk keeps the cycle and size checks of the [composition graph](#composition-graph-implemented) and reports them on read.
- **Without a context:** the graph endpoint selects as before, with contextual references taking the latest version.

### Selection evidence and target verification (implemented)

Resolution answers two different questions, and reports them separately ([ADR-0018](decisions/0018-selection-evidence-and-target-verification.md)).

- **Selection evidence** says why a version, or a whole child combination, was selected: an earlier verified execution in the context, at **any commit and with any inputs**. Each such node reports `selection_evidence`: `execution_id`, `repository` (the identifier that execution was recorded with), `repository_id` when registered, `commit` and `environment`. It is a reason to try the version, not verification of anything else. `verified_by` repeats the execution ID, for compatibility, and means the same.
- **Target verification** says whether the selected combination is verified at one exact target. It is reported only when the request names a target: a `commit` and the root's effective `inputs`, given together, in the context's repository and environment. Each node then reports `target_verification` for its own selected combination at that target:
  - the repository, the target commit and the environment name;
  - its effective inputs: the root's are the target's `inputs`; a child's come from its reference's input mapping applied to its parent's effective inputs (`{"input": p}` takes the parent's member `p`, `{"value": v}` takes `v`, and a child input whose parent member is absent is left out);
  - its selected child-version tree, pins included.

  The status is that of the combination's latest valid (not invalidated) execution, as for any [combination](#verification-implemented). With no execution there, the node is unverified. A child's success never verifies its parent, and a parent recorded with another child combination does not verify the selected one.
- **Conditional references at a target.** The target names its own decisions, keyed by reference path (names from the root joined by `/`). A reference decided not applicable is expected skipped, and its subtree gets no target verification; an applicable one is expected executed. A node with an undecided conditional reference below it is unverified, lists the paths in `undecided` and looks up no execution: a target's applicability is never inferred from earlier runs, so a run that skipped work never verifies a target that decides the work applies ([ADR-0029](decisions/0029-conditional-references-and-applicability-decisions.md)). Graphs always show every reference and its condition.
- **No inference.** Polaroid compares commits, environment names and canonical inputs exactly. It never inspects a checkout and never infers compatibility from ancestry, tree contents, branch names or time. Without a target, no node claims target verification. Working-tree state counts only as part of the inputs, where a procedure's contract puts it there (for example `working_tree` in [ADR-0017](decisions/0017-development-procedures-as-records.md)'s procedures).
- **Selection is unchanged by a target**, and target verification never changes a recorded execution or its own verification.

### Feedback report (implemented)

A feedback report tells Polaroid's maintainers about a problem with Polaroid or with a record it holds, or suggests an improvement. It is written once and never changed or deleted ([ADR-0015](decisions/0015-feedback-reports.md), [ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md)). Its disposition is a separate record, [feedback resolution](#feedback-resolution-implemented), that never changes it.

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
| `status` | string | derived | `open` or `resolved`: whether the report has a resolution ([ADR-0040](decisions/0040-feedback-resolution.md)). Always present. |
| `resolution` | object | derived | The report's [resolution](#feedback-resolution-implemented). Present only once resolved. |

`subject` says what the report is about; `repository` and `execution_id` say where it was made. They must agree: the execution ran in that repository (by identity); for a procedure subject, it ran that procedure and version; for a binding subject, it used that binding and revision; for an execution subject, it is that execution; and the repository of a repository, binding or execution subject is the report's repository. A disagreement is `400` naming the field.

A report without `subject`, including every report stored before subjects existed, keeps the meaning ADR-0015 gave it: it is about the Polaroid service, and the `subject_type=service` filter includes it. Its stored fields are never changed. A later alias makes an older report's identifier match its repository in filters, and a later applicability change does not affect reports at all.

Reporting feedback changes no other record. The database enforces the field rules with `CHECK` constraints, checks that a subject exists with a trigger, and rejects `UPDATE` and `DELETE`.

### Feedback resolution (implemented)

A resolution records how a feedback report was handled, without changing the report: it is lifecycle metadata, stored apart from it, exactly as an [execution-evidence invalidation](#execution-evidence-invalidation-implemented) is stored apart from its execution ([ADR-0040](decisions/0040-feedback-resolution.md)).

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `outcome` | string | client | `addressed`, `declined`, `not_reproduced` or `duplicate`. |
| `reason` | string | client | Required, not blank: how the report was handled. |
| `actor` | string | client | Who resolves it, in the canonical-key format (as a feedback `reporter`). Not authenticated. |
| `references` | array | client | Optional. Declared `{uri, note?}` values, as a toil item's: issue URLs, PRs, commits, or the procedure version or execution that addressed the report. Stored and returned, never fetched. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

- **Irreversible and single.** A report has at most one resolution; a second one is `409 feedback_resolved`. There is no reopening: a report that recurs is answered by a new report, which may cite the old one in `context`.
- **Separate from the report.** The report keeps every field it was written with; only its derived `status` and `resolution` change.
- **No lighter state.** `outcome` is always one of the four final values; there is no "tracked" status linking a report to work in progress. That link, like all triage, stays outside Polaroid, in `references` once resolved, exactly as ADR-0015 already left it for every report.
- **Listing.** `GET /v1/feedback`'s `status` filter (`open` or `resolved`) narrows the list; without it, every report is listed, open and resolved alike, so a client that never learns of `status` sees no change.

### Toil item (implemented)

A toil item records an agent's assessment of repetitive, mechanical work that a tool might replace: where it happens, what shows it repeats, what is only a hypothesis, a proposal, and what became of it ([ADR-0034](decisions/0034-toil-items.md)). It is task knowledge, like a procedure, but it is neither a procedure (it has no method to follow) nor an execution (it is not one run). Its status is an assessment state written by agents, never execution verification: Polaroid checks structure and links, and derives nothing from an item, nor does an item change any other record.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `id` | string | server | A UUIDv7. Never changes. |
| `canonical_key` | string | client, at creation | The procedure key format, unique among toil items (a separate namespace from procedures). A duplicate is `409 canonical_key_exists`. Never changes. |
| `created_at` | RFC 3339 timestamp, UTC | server | |
| `latest_revision` | integer ≥ 1 | derived | The highest revision number. |
| `activity`, `status`, `scope`, `applicability`, `updated_at` | | derived | The latest revision's, and its `created_at`. |

There is no semantic deduplication: different keys may describe overlapping work, so agents list existing items (by repository and procedure) before creating one.

### Toil revision (implemented)

A toil revision is one immutable, complete statement of the assessment, numbered from 1 without gaps. A revision is stored only if `base_revision` is the latest; any other base is `409 revision_conflict` with `latest_revision`. An item and its revision 1 are created in one atomic write.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `toil_item_id`, `revision`, `created_at` | | server | |
| `activity` | string | client | Required, one line. |
| `applicability` | JSON object | client | Required: `{"shared": {}}` or `{"repository": "<registered repository id>"}`. Responses add `scope` (`shared` or `local`). |
| `sources` | array | client | At least one exact procedure version `{procedure_id, version, steps?}`, unique per revision. `steps` are declared, non-blank one-line identifiers, unique per source, never checked against the version's `instructions`; an empty list is the same as none. |
| `observations` | array | client | Optional executions supporting the assessment, `{execution_id, note}`, each cited once. Always returned, `[]` when none. |
| `assessment` | JSON object | client | Free-form, except that `repetition` and `burden` are required objects whose `status` is `observed`, `hypothesis` or `unknown`. Other members (for example `error_rate` or `worth`) are not interpreted. |
| `proposal` | JSON object | client | Optional. Free-form, but `inputs`, `outputs`, `remaining_judgment` and `failure_and_recovery` must be present and not `null`, and `acceptance_criteria` must be a non-empty array of non-blank strings. |
| `status` | string | client | `observed`, `candidate`, `implemented`, `validated` or `retired`; see the lifecycle below. |
| `status_rationale` | string | client | Required, not blank: why the status holds, or for `retired`, why the item is no longer tracked. |
| `replacement` | JSON object | client | Optional. `summary` (not blank) and `references`, at least one `{uri, note?}`. |
| `validation` | JSON object | client | Optional. `rationale` (not blank), `executions` (`{execution_id, note}`) and `references` (`{uri, note?}`), at least one entry between them. A stored validation always returns both arrays. |
| `revision_reason` | string | client | Required, not blank. |
| `author` | string | client | The agent or person writing the revision, in the canonical-key format (as a feedback `reporter`). Not authenticated. |

**Links.** Source versions and executions must exist (`400` naming the link). A failed execution is valid support: an observation records what an execution shows, not that it succeeded. `uri` is any non-blank single line (a URL, a path, a commit); references are stored and returned, never fetched, opened or executed. A local item's executions must have run in its repository, by identity with aliases included, and its source versions must be applicable there. A shared item may cite procedures and executions from several repositories.

**Lifecycle.**

| Status | Means | Requires | Also allows |
| --- | --- | --- | --- |
| `observed` | the activity was seen | | `proposal` |
| `candidate` | a proposal awaits a decision; no claim that it is worthwhile | `proposal` | |
| `implemented` | a replacement exists, not yet validated | `proposal`, `replacement` | |
| `validated` | an agent judged, citing evidence, that the replacement meets the acceptance criteria | `proposal`, `replacement`, `validation` | |
| `retired` | no longer tracked | | `proposal`, `replacement`, `validation` |

Revision 1 is `observed` or `candidate`. Each later revision's status must follow its base revision's: `observed` → `candidate`, `retired`; `candidate` → `observed`, `implemented`, `retired`; `implemented` → `candidate`, `validated`, `retired`; `validated` → `candidate`, `retired`; `retired` → `observed`; and every status to itself. A replacement that fails is reassessed by appending a `candidate` revision that explains the failure; the earlier revisions stay. `validated` is a recorded judgment with its evidence, not `verified`.

**Storage.** The database rejects `UPDATE` and `DELETE` of items, revisions and their links, revision gaps, links written after their revision, a revision 1 outside `observed` and `candidate`, transitions outside the table, a revision without a source, and validation evidence without a validation or a validation without evidence. `CHECK` constraints repeat the field and status rules.

### Execution-evidence invalidation (implemented)

An invalidation says that an execution must no longer be used as evidence: it did not do what its version claims, for example because it was recorded against the wrong version ([ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md), [HTTP API](http-api.md#execution-evidence-invalidation)). It is lifecycle metadata, stored apart from the execution, which never changes.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `reason` | string | client | Required, not blank: what the execution claims and why that is wrong. |
| `actor` | string | client | Who invalidates it, in the canonical-key format (as a feedback `reporter`). Not authenticated. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

- **Irreversible and single.** An execution has at most one invalidation; a second one is `409 execution_invalidated`. There is no reinstatement: a mistaken or replaced claim is answered by recording a new, genuine execution.
- **Separate from outcome and structure.** The execution keeps its `outcome`, children, decisions and evidence. Its `evidence_status` becomes `invalidated`, and its [verification](#verification-implemented) is `false` with the problem `invalidated`, next to any structural problem it has anyway.
- **Propagation without substitution.** A parent linking an invalidated child reports `child_invalidated` and is not verified, and every ancestor of it is unverified. The parent keeps its recorded child: no other run of the child is ever put in its place.
- **Combinations, selection and targets** use only valid executions as evidence: a combination's status, `latest_execution_id` and target verification come from its latest valid execution, and [contextual resolution](#contextual-resolution-implemented) falls back to the previous valid run of the version, another version, or no evidence. A parent run that links an invalidated child is itself valid evidence that is unverified, so it withdraws its version in the context instead of letting an older parent run decide; invalidate it too if an older run should count again.
- **Nothing else changes:** not the version, not unrelated executions, toil items or feedback that cite it.

### Procedure lifecycle (implemented)

A procedure identity is `active` or `retired` ([ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md), [HTTP API](http-api.md#procedure-lifecycle)). Its state is the latest of its append-only lifecycle events, or `active` when it has none.

| Field | Type | Set by | Rules |
| --- | --- | --- | --- |
| `event` | integer ≥ 1 | server | `base_event + 1`. Events are numbered without gaps. |
| `status` | string | operation | `retired` (retirement) or `active` (reactivation). Events alternate, and the first is a retirement. |
| `reason` | string | client | Required, not blank. |
| `actor` | string | client | The canonical-key format. |
| `created_at` | RFC 3339 timestamp, UTC | server | |

- **Concurrency.** A change names `base_event`, the latest event the caller read (`0` without one). A stale base is `409 lifecycle_conflict` with `latest_event`; a change to the current state is `409 lifecycle_unchanged`.
- **Reads.** Retrieval by ID, key and exact version, graphs, binding resolution, executions and verification work as before and report `lifecycle_status`. The procedure list and discovery leave retired procedures out unless `lifecycle` is `retired` or `all`. Duplicate checks always include them, with their `lifecycle_status`, and a key collision with a retired procedure is still a collision: retirement never invites a second identity for the same knowledge.
- **Existing references.** Resolution selects exactly what it selected before, pins included, and never replaces a retired node. The node carries `retirement` (its latest event) next to its selection evidence and target verification: historical evidence that an execution satisfied its contract stays visible, and so does the fact that the procedure should not be used for new work.
- **New writes refused** (`409 procedure_retired`): a version of a retired procedure; a version with a pinned or contextual reference to one; a binding, or a binding revision, of one. Reactivate it first. Executions, origins, feedback and toil items are accepted, because they record what happened.
- **No cascade.** Retiring a procedure changes no parent, reference, binding or execution, and does not retire the procedures that reference it.

**Storage.** Migration 10 adds `execution_invalidations` (one row per execution at most) and `procedure_lifecycle_events`. The database rejects `UPDATE` and `DELETE` of both, a gap in event numbers, an event that does not change the status (so the first must be a retirement), and inserting a version, a reference, a binding or a binding revision that a retired procedure forbids. `CHECK` constraints repeat the reason and actor rules.

### Write limits (implemented)

Every client-written text and JSON field of every record above has a byte limit, and each write as a whole is bounded too ([ADR-0037](decisions/0037-size-limits-on-new-writes.md); the table is in the [HTTP API](http-api.md#write-limits)). Documents (contract, instructions, evidence, toil assessment and proposal) may be 64 KiB, smaller objects (inputs, attributes, contexts, decision evidence) 16 KiB, prose (philosophy, method, reasons, rationales, notes, details, conditions) 8 KiB, single lines (goal, summary, name, activity, step identifiers, URIs) 2 KiB, a whole write 256 KiB. Identifiers keep their own, stricter formats.

- JSON is measured as stored: after whitespace is removed, with member order and escapes as submitted. Text is measured in UTF-8 bytes, as stored. A response writes an escaped character as itself, so it can be shorter than what was measured.
- The limits apply to new writes only, in the domain, before anything is stored. Records stored earlier are never re-checked, rewritten or refused on read, backup or restore. A new version or revision that repeats an oversized stored value is a new write and is refused.
- Nothing is truncated or summarized, and no document is interpreted to measure it.

## Instruction steps (recommended convention)

`instructions` is a free-form JSON object, and Polaroid never reads its members. This convention helps agents skip what does not apply and stop at the right point; procedures may adopt it, and nothing enforces it. A version lists `steps`, each an object:

| Member | Meaning |
| --- | --- |
| `id`, `action` | A stable name and what to do. |
| `when` | When the step is needed. Omit it for a step that is always needed. |
| `required_by` | The repository policy or rule that makes the step mandatory whatever `when` says, for example `"AGENTS.md: make check before every change"`. |
| `satisfied_when` | What already satisfies the step without doing it again, for example "the commit's build is already recorded and verified". |
| `done_when` | The observable result that completes the step. |
| `escalate_when` | When to stop and ask a person instead of continuing. |

The agent judges each step against its task and states its judgment in the execution's `evidence`. Steps are not references, so Polaroid derives nothing from them. Work whose skipping must be recorded and must count for verification belongs in a [conditional reference](#subprocedure-reference-implemented), with a [decision](#applicability-decision-implemented) per run. Polaroid does not reuse earlier evidence for `satisfied_when`: an applicable reference still needs its own execution. The example procedures in `examples/task-aware/` follow this convention.

## Planned records (not implemented)

No records are planned in the current increments. Later work (semantic discovery, access control, the PoC import) is listed in the [roadmap](../development/roadmap.md), and its records are designed when it is refined into issues.

## Compatibility

- Field names and rules on this page are part of the `/v1` API contract, and of the compatibility promise of major version 1 ([ADR-0038](decisions/0038-stable-releases-and-compatibility.md)). Adding an optional field is backward-compatible. Renaming or removing a field, or changing its meaning, needs a new major version and an ADR. Before `v1.0.0`, two rules were tightened within `/v1` by ADR: child inputs must follow the reference mapping ([#39](https://github.com/ashuangiras/polaroid/issues/39), ADR-0024), and writes have size limits ([#98](https://github.com/ashuangiras/polaroid/issues/98), ADR-0037). Within `v1`, write limits are never lowered, and only a patch release may refuse input that the documented contract never allowed, naming the rule in its release notes.
- Schema changes are new migrations, and they never rewrite stored records. A column added later must give existing rows a value that means "absent", not an invented one.
- Migration 7 ([#35](https://github.com/ashuangiras/polaroid/issues/35)) adds the repository registry, origins, `goal` and `applicability`, and the feedback subject columns. Existing versions read back unspecified, existing reports without a subject, and nothing is registered. Responses gain `scope` on procedures, and omit every absent new field, so existing versions, bindings, executions and reports are served as before. Requests that do not use the new fields behave as before, except that recording an execution, creating or revising a binding, or writing a version is refused where a declared applicability forbids it, which no stored version could declare before.
- [#37](https://github.com/ashuangiras/polaroid/issues/37) has no migration. Responses add `scope` on every version and graph node, and `repository_id` on executions, selection evidence and combinations of registered identifiers. Derived verification and resolution now match evidence by repository identity: nothing changes for unregistered identifiers or repositories without aliases, while executions recorded under an alias join their repository's combinations, which may change those combinations' status, and whose `combination.repository` becomes the canonical identifier. No stored record changes.
- [#39](https://github.com/ashuangiras/polaroid/issues/39) ([ADR-0024](decisions/0024-child-inputs-follow-the-reference-mapping.md)) has no migration, and it tightens one rule as a correctness fix, without a new API version: a child link the old rule accepted although its child ran with other inputs than the reference maps made a parent verified for work it never composed. Recording such a link is now `400` naming `children[i].execution_id`. Stored links, including any written that way earlier, read back unchanged, but their parents are no longer verified; they report `child_inputs_mismatch`, and their combinations, contextual selection and target verification change with them. Verification responses can carry the new problem code. The database does not enforce the rule.
- Migration 8 ([#50](https://github.com/ashuangiras/polaroid/issues/50), [ADR-0029](decisions/0029-conditional-references-and-applicability-decisions.md)) adds `condition` to references (NULL: required, which every existing reference is) and the `execution_decisions` table. Every stored version, execution, verification, combination and selection reads back unchanged; the new fields are additive and omitted when absent.
- Migration 9 ([#87](https://github.com/ashuangiras/polaroid/issues/87), [ADR-0034](decisions/0034-toil-items.md)) adds `toil_items`, `toil_revisions`, `toil_revision_sources` and `toil_revision_executions`. No existing row or response changes, and toil items affect no other record's verification, selection or discovery. Builds before schema 9 cannot open a migrated catalog or restore a schema-9 backup.
- Migration 10 ([#91](https://github.com/ashuangiras/polaroid/issues/91), [ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md)) adds `execution_invalidations` and `procedure_lifecycle_events`, and triggers that refuse new versions, references, bindings and binding revisions of retired procedures. No existing row changes: every stored execution is valid and every procedure active, so verification, combinations, selection, target verification, lists and discovery read back as before. Responses add `evidence_status` and `lifecycle_status` (always) and `invalidation`, `lifecycle_event`, `retirement` and `invalidated_execution_ids` (when they apply). Builds before schema 10 cannot open a migrated catalog or restore a schema-10 backup.
- Migration 11 ([#127](https://github.com/ashuangiras/polaroid/issues/127), [ADR-0040](decisions/0040-feedback-resolution.md)) adds `feedback_resolutions`. No existing row changes: every stored report is open, so every existing response is the same apart from the new `status` (always) and `resolution` (once resolved) fields, and `GET /v1/feedback` without `status` still lists every report. Builds before schema 11 cannot open a migrated catalog or restore a schema-11 backup.
