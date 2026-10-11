# 0034. Toil items: versioned assessments of repetitive work, with an assessment lifecycle

**Status:** Accepted. Builds on [ADR-0019](0019-repository-registry.md), [ADR-0020](0020-procedure-origin-and-applicability.md), [ADR-0021](0021-targeted-feedback-and-bounded-lists.md), [ADR-0022](0022-repository-identity-in-evidence.md) and [ADR-0023](0023-pagination-guarantees-and-scope-labels.md).
**Date:** 2026-10-11

## Context

[#85](https://github.com/ashuangiras/polaroid/issues/85) recorded a toil assessment (repetitive, mechanical work that a tool might replace) inside the evidence of an extraction execution. Evidence is immutable and unindexed, so nobody can find the assessment, revise it when the activity is observed again, or say what became of its proposal ([#87](https://github.com/ashuangiras/polaroid/issues/87)). An assessment is task knowledge about work, not a procedure: it must not be one (it has no method to follow), nor an execution (it is not one run).

## Decision

- **Identity.** A toil item has a stable UUIDv7 `id`, a unique `canonical_key` (the procedure key format, in its own namespace: a toil item and a procedure may share a key) and `created_at`. A second item with the same key gets `409 canonical_key_exists`. Different keys may describe overlapping work: there is no semantic deduplication, and agents look for existing items with the list before creating one.
- **Revisions.** Content lives in revisions numbered 1, 2, … without gaps, appended against `base_revision`, which must be the latest (`409 revision_conflict` with `latest_revision`, as for bindings). Each revision is a complete statement, not a patch. Stored items and revisions are never changed or deleted. A revision holds:
  - `activity`: one line;
  - `applicability`: `{"shared": {}}` or `{"repository": "<repository id>"}`, the procedure-version convention. It is required: an assessment is always made somewhere. Responses add `scope`;
  - `sources`: one or more exact procedure versions (`procedure_id`, `version`), unique per revision, each with optional `steps`;
  - `observations`: supporting executions, `{execution_id, note}`, unique per revision;
  - `assessment`: a free-form object, whose `repetition` and `burden` must each be an object with `status` `observed`, `hypothesis` or `unknown`; every other member is free;
  - `proposal` (optional): a free-form object with `inputs`, `outputs`, `remaining_judgment`, `failure_and_recovery` (any non-null JSON) and `acceptance_criteria` (a non-empty array of non-blank strings);
  - `status` and `status_rationale` (required text);
  - `replacement` (optional): `summary` and one or more declared `references`;
  - `validation` (optional): `rationale`, validating `executions` (`{execution_id, note}`) and declared `references`, at least one of the two;
  - `revision_reason`, `author` (an actor in the feedback `reporter` format, for example `copilot.vscode`) and the server's `created_at`.
- **Links.**
  - Sources and executions are internal and must exist (`400` naming the link). A failed execution is valid support: an observation records what an execution shows, not that it succeeded.
  - Observations support the assessment. Validation executions are evidence offered for a replacement. They are separate lists, and Polaroid derives nothing from either: no verification, outcome or success is read or inferred.
  - `references` are declared `{uri, note?}`, where `uri` is any non-blank single line (a URL, a path, a commit). They are stored and returned, never fetched, opened or executed.
  - `steps` are declared identifiers, not checked against the version's `instructions`. Polaroid never reads instructions, and they need not contain `steps` or step IDs. Each is non-blank single-line text, unique per source; an empty list is the same as none.
  - A local item applies in one registered repository, which must exist. Its executions must have run there (by identity, aliases included), and its source versions must be applicable there. A shared item may cite procedures and executions from several repositories, so it can describe an activity repeated across them.
- **Lifecycle.** These are assessment states written by agents, not Polaroid's execution verification. `validated` is a judgment recorded with its evidence, not `verified`, and no status retires, revises or changes any procedure.

  | Status | Means | Requires | Allows |
  | --- | --- | --- | --- |
  | `observed` | the activity was seen | rationale | proposal |
  | `candidate` | a proposal awaits a decision; no claim that it is worthwhile | proposal | |
  | `implemented` | a replacement exists, not yet validated | proposal, replacement | |
  | `validated` | an agent judged, citing evidence, that the replacement meets the criteria | proposal, replacement, validation | |
  | `retired` | no longer tracked; the rationale is the reason | rationale | proposal, replacement, validation |

  Revision 1 is `observed` or `candidate`. Allowed transitions, each status also to itself (a revision without a status change):
  - `observed` → `candidate`, `retired`;
  - `candidate` → `observed`, `implemented`, `retired`;
  - `implemented` → `candidate`, `validated`, `retired`;
  - `validated` → `candidate`, `retired`: reassessment after the replacement fails;
  - `retired` → `observed`: the activity recurs.

  A failed replacement is reassessed by appending a `candidate` revision that explains the failure; the earlier `implemented` and `validated` revisions stay in the history. Fields a status does not allow are refused, so a `candidate` cannot carry a stale validation.
- **Operations.** Additive within `/v1`, with the same names and errors on every transport:

  | Operation | HTTP | MCP | CLI |
  | --- | --- | --- | --- |
  | create | `POST /v1/toil-items` | `create_toil_item` | `create-toil-item` |
  | item and history | `GET /v1/toil-items/{id}`, `GET /v1/toil-items/by-key/{key}` | `get_toil_item` | `get-toil-item`, `get-toil-item-by-key` |
  | one revision | `GET /v1/toil-items/{id}/revisions/{revision}` | `get_toil_revision` | `get-toil-revision` |
  | revise | `POST /v1/toil-items/{id}/revisions` | `revise_toil_item` | `revise-toil-item` |
  | list | `GET /v1/toil-items` | `list_toil_items` | `toil-items` |

- **Lists.** Compact summaries (`id`, `canonical_key`, `created_at`, `latest_revision`, and the latest revision's `activity`, `status`, `scope`, `applicability` and `updated_at`), ordered by canonical key, with the existing `limit`, `after` and `snapshot` ([ADR-0023](0023-pagination-guarantees-and-scope-labels.md)). The filters apply to the latest revision:
  - `status`;
  - `repository`: shared items, plus items local to the repository that identifier belongs to, matched by identity with aliases honoured; an unregistered identifier sees shared items only;
  - `procedure_id`: items whose sources cite that procedure, at any version.

  A live traversal never repeats or skips an item that existed throughout, but reads each page as it is then: a concurrent revision may change an item's summary, or move it in or out of a status filter. A snapshot fixes the items and their latest revisions at the first page.
- **Storage.** Migration 9 adds `toil_items`, `toil_revisions`, `toil_revision_sources` and `toil_revision_executions`. Each revision is written in one transaction, with its links before the revision row, as with references. Triggers reject UPDATE and DELETE, revision gaps, links added to a stored revision, revision 1 outside `observed` and `candidate`, transitions outside the table, and a revision without a source or with validation but no evidence. CHECK constraints repeat the field rules, and foreign keys bind sources to procedure versions and links to executions. No existing row changes. Backups, inspection and restore include the tables, because they count every table.

## Consequences

- Assessments become records that agents can find, filter, revise and follow over time, with their provenance. The first is the #85 assessment, promoted by `procedure.capability.extract` v2.
- Polaroid checks structure and links only. Whether an activity is toil, whether burden matters, and whether a replacement is good are agents' judgments, recorded with their rationale.
- Schema 9 cannot be opened by earlier builds: going back means a fix-forward or a deliberate restore. Every existing record, response and request is unchanged.
- Lists filter by status, applicability and procedure only. Searching activity text, prioritizing, detecting toil and fetching references are not part of this decision.
