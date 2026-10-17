# 0062. Field-complete knowledge publication

**Status:** Accepted. Extends [ADR-0059](0059-grounded-publication.md)'s publication gate and replaces the four-field-only scope of [ADR-0060](0060-interactive-proposal-drafts.md) for new guided proposals; preserves v1-v5 MCP contracts. Introduces versioned `/v2/knowledge` authoring routes because existing `/v1` publication and rollback requests cannot carry a complete review receipt.
**Date:** 2026-10-17

## Context

[ADR-0060](0060-interactive-proposal-drafts.md) and [ADR-0061](0061-draft-every-field-every-time.md) make a useful start: every interactive draft visits a Procedure's four condition lists, including lists that already have values. But those are only four of the fields in one part of one record type. A Procedure also has a separate guidance document (`goal`, `philosophy`, `method`, `applicability`, `contract`, `instructions`, `references`) and typed semantics such as `definition`, `inputs`, `outputs`, `requires`, `composes`, and `precedes`. Domain, Concept, Claim, Rule, Evidence and Exception records each admit their own typed fields. A single-shot `propose` can still bypass the guided review, and the operator publication, rollback and Git synchronization paths do not require that every applicable field was considered.

The owner decided that publication must be a full review, not an append-only patch: expose every field applicable to the entity and its current value, then explicitly keep, add, remove, replace or clear it. The owner also decided that this applies to every publication route, including rollback and remote activation; the agent must see the complete field inventory and current state in one `propose_start` response; new or replacement content remains citation-grounded; and review provenance belongs in an immutable commit trailer, not in every knowledge document.

## Decision

### Field inventory and operation semantics

The complete inventory is generated from the schemas Polaroid already validates, not maintained as a second hand-written list:

- **Procedure guidance document:** every authorable top-level field other than its identity key: `goal`, `philosophy`, `method`, `applicability`, `contract`, `instructions`, and `references`. `contract` and `instructions` remain opaque JSON values: Polaroid does not inspect their members ([AGENTS.md](../../../AGENTS.md)).
- **Typed JSON-LD records:** every field admitted for the selected type by `internal/knowledge/recordcheck.go`, including `label`, content, relationships and type-specific metadata, across Domain, Concept, Claim, Rule, Procedure, Evidence and Exception. The record `id`, `type` and `@context` are system-managed. Nested objects and lists are exposed as whole top-level values; for example, a reference list, port list, authority, approval, assertion or condition is not silently reduced to a subset of its members.
- **Procedure proposals cover both layers.** When proposing a Procedure, the guidance document and its typed Procedure record are each reviewed if present; creating a Procedure creates both. Other record types have only their typed record.

Every draft presents the complete ordered field inventory and the snapshot's existing values in one `propose_start` response, then collects one decision per field in a deterministic order. Each field has exactly one operation:

| Operation | Meaning |
| --- | --- |
| `keep` | Preserve the complete existing value. Not valid for a new entity. |
| `add` | Append the supplied entries to a list field. |
| `remove` | Remove the specifically named entries from a list field; every named entry must exist. |
| `replace` | Set the complete scalar, object or list value to the supplied value. |
| `clear` | Set an optional field to its empty/zero state. A required value still fails normal catalog validation. |

`add` and targeted `remove` apply only to lists. `keep`, `replace` and `clear` apply to scalar or object fields, with list replacement and clearing also permitted. For a new entity, every applicable field must be explicitly set (`replace`, or `add` for list contents) or explicitly cleared if optional; `keep` is not an answer. A no-op review does not create a knowledge publication.

For content-bearing values, each `add` or non-empty `replace` is citation-checked: each supplied quote must occur verbatim in the corresponding new field content. `keep`, `remove` and `clear` need no citation. Structured identity/reference values still undergo the existing key, type, graph, governance and semantic checks; citations do not replace those checks.

### Review receipt and enforcement

Each published change carries one `Polaroid-Field-Review` trailer containing a canonical, machine-readable receipt for every changed Procedure document and typed record: entity kind/type, stable key, one operation for every applicable field, operation values needed to reproduce additions/removals, and citations for new/replacement content. The receipt is checked against the parent and candidate states; it is not a caller assertion that the kernel accepts without checking. Unchanged entities need no receipt, but if a caller includes one it must be a complete, valid no-change review too. Explicit `replace` with the existing value and `clear` of an already-empty optional field remain valid no-op decisions; `add` and `remove` must describe an actual list delta. It is immutable Git publication provenance, not a field in Procedure or JSON-LD record documents.

The same receipt rule applies at every publication boundary:

- `Kernel.Publish` refuses any create or update without a complete, matching receipt; unchanged entities and whole-entity removals do not need a field receipt.
- `POST /v2/knowledge/publications` and `polaroid knowledge-publish` accept and pass the same receipts. `POST /v1/knowledge/publications` remains a documented route but refuses writes with `invalid_request`, directing clients to `/v2`; other v1 knowledge reads and remote-management routes remain unchanged.
- `POST /v2/knowledge/rollbacks` and `polaroid knowledge-rollback` require a receipt for every restored or changed entity; they cannot silently reintroduce an old state. The v1 rollback route refuses with the same migration error.
- Remote candidate validation checks each incoming commit against its parent and rejects a changed entity without a valid receipt. Activation rechecks the validated transition before changing the active ref. A push can transfer only histories accepted under this rule.
- Existing snapshots remain readable without rewriting or backfilling them. A legacy entity is grandfathered while unchanged; its next update must review every applicable field. A new incoming transition that changes an entity without a receipt is rejected.

The receipt makes the requirement auditable across transport boundaries, including operator clients and remote Git histories. Existing records do not gain `reviewed_fields` or other new JSON properties.

### MCP and compatibility

**A new `/mcp/agent/v6` endpoint** exposes the full-field `propose_start`/`propose_advance` flow. `propose_start` returns every applicable field's name, value/presence, requiredness and allowed operations together, pinned to its snapshot; `propose_advance` accepts exactly the next field's operation and citation material, and advances until all fields are answered. Its append is conditional on the draft's current event count, so concurrent advances cannot silently overwrite a decision; a stale advance returns `draft_conflict`. The final call validates the complete candidate and receipt through the shared publication gate. Failures leave the draft open for correction; stale snapshots conflict rather than merging.

The v1-v5 endpoint schemas and their existing tool descriptions remain unchanged. Because their write tools cannot submit a full-field review receipt, v4 single-shot `propose` and v5 draft write calls are refused with an actionable migration error directing callers to v6; no proposal is published. Every route that can construct a publication uses the kernel's shared receipt gate. Operator publication/rollback writes move to `/v2/knowledge/publications` and `/v2/knowledge/rollbacks`; their v1 paths refuse writes with a migration error, while v1 reads and remote-management routes remain available. These are intentional, versioned compatibility consequences of enforcing the requirement across all write paths.

The final candidate is checked by the existing catalog, JSON-LD, SHACL, completeness, rule-governance, composition and reference validation. A real field change is publishable even if it does not alter a Corpus Logic resolution (for example, an instruction or method correction); an exactly unchanged candidate is a no-op, not a redundant publication.

## Consequences

- Agents and operators see and decide every applicable authorable field before a new or changed entity is accepted. They can preserve, extend, correct or remove existing content without replacing a whole entity blindly.
- New and changed content stays grounded by the existing citation self-consistency rule; keeping or removing content does not require citations.
- Each commit records what was reviewed, while knowledge document formats remain unchanged. A later reader can audit the exact field operations and citations from Git history.
- The stricter gate blocks old incomplete write calls and incoming legacy commits that *change* entities without a receipt. It does not revalidate or rewrite unchanged historical content.
- Draft events remain ephemeral SQLite state with a new migration. Existing migrations and the current v5 draft table are not edited.
- This does not authorize an agent to run the operator CLI or publish on its own initiative; the existing operator-authorisation rules remain in force.
