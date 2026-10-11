# 0037. Size limits on new writes

**Status:** Accepted. Extends [ADR-0005](0005-strict-json-with-encoding-json-v2.md) (strict JSON) and [ADR-0036](0036-read-only-catalog-statistics.md) (statistics). Changes no stored record.
**Date:** 2026-10-11

## Context

Records are kept forever, and the only size limit has been the 1 MiB request body. One pasted log in `evidence`, `details` or a reason would stay in the catalog permanently ([#98](https://github.com/ashuangiras/polaroid/issues/98)).

The isolated copy of the live catalog gives these maxima, in bytes ([#96](https://github.com/ashuangiras/polaroid/issues/96) and #98; 43 versions, 201 executions):

| Field | Max | 99th percentile |
| --- | --- | --- |
| execution evidence | 15,422 | 4,312 |
| contract | 8,795 | 6,346 |
| instructions | 6,980 | 6,642 |
| toil proposal | 3,610 | 3,610 |
| reference inputs | 1,881 | 217 |
| execution inputs | 1,861 | 1,846 |
| feedback details | 1,515 | 1,376 |
| method | 1,217 | 1,065 |
| binding and version revision reasons | 1,123 | 840 |
| decision evidence | 960 | 786 |
| philosophy | 705 | 604 |
| execution environment attributes | 575 | 342 |
| decision rationale | 547 | 418 |
| condition | 495 | 470 |
| goal | 367 | 367 |
| feedback summary | 121 | 120 |
| step identifier | 29 | 29 |

All other measured fields are smaller. The largest whole write was a procedure version: 16,899 bytes across all its fields.

## Decision

**Tiers.** Every persisted, user-controlled JSON or text field belongs to one tier. Each limit is a power of two and at least four times the largest value stored in its tier (evidence: 4.2 times):

| Tier | Limit | Fields |
| --- | --- | --- |
| document | 65,536 (64 KiB) | `version.contract`, `version.instructions`, execution `evidence`, toil `revision.assessment` and `revision.proposal` |
| object | 16,384 (16 KiB) | `version.references[].inputs`, binding `revision.inputs`, execution `inputs`, `environment.attributes`, `decisions[].evidence`, feedback `context` |
| text | 8,192 (8 KiB) | `version.philosophy`, `version.method`, `version.revision_reason`, `version.references[].condition`, `origin.reason`, binding `revision.revision_reason`, `decisions[].rationale`, invalidation and lifecycle `reason`, feedback `details`, alias `reason`, toil `revision.status_rationale`, `revision.revision_reason`, `revision.observations[].note`, `revision.replacement.summary`, `revision.replacement.references[].note`, `revision.validation.rationale`, `revision.validation.executions[].note`, `revision.validation.references[].note` |
| line | 2,048 (2 KiB) | `version.goal`, feedback `summary`, repository `name`, toil `revision.activity`, `revision.sources[].steps[]`, `revision.replacement.references[].uri`, `revision.validation.references[].uri` |

**Existing stricter rules stay:**

- canonical keys, binding and reference names, environment names, reporters, actors and authors: 128 bytes;
- repository identifiers: 255 bytes;
- commits: 40 or 64 hex characters;
- outcomes, statuses and kinds: enumerations;
- IDs: must name an existing record.

**Whole write.** The sum of all bounded fields in one request must not exceed 262,144 bytes (256 KiB), so many fields that are each acceptable cannot bypass the limits. The largest write today is 16,899 bytes. A version with a full contract and full instructions (128 KiB) still fits with room to spare.

**Measurement.**

- **JSON:** the length of the value after the compaction used for storage ([records.md](../records.md)): whitespace removed, member order and string escapes kept as received. `"é"` is 2 bytes; its escape `"\u00e9"` is 6.
- **Text:** the UTF-8 length of the string as persisted (nothing is trimmed).
- **Boundary:** exactly at the limit is accepted, one byte over is refused.
- **Absent values:** a value that is not stored, such as an omitted goal, condition, proposal, decision evidence or reference note, is not measured. Feedback without `context` stores `{}` and counts 2 bytes.
- **Nothing is rewritten:** nothing is truncated, summarized or interpreted. `contract`, `instructions`, `evidence` and the other documents are measured, never read.

**Order and error.**

- A write is first checked as before, as far as that needs no stored record. Any problem found there is `400 invalid_request`, and sizes are not reported.
- A structurally valid write with a field over its limit, or over the whole-write limit, is `413 value_too_large`. Its `limits` list every violation: `{"field","bytes","max_bytes"}`.
- Field paths are those of other field errors: `version.references[2].inputs`, `revision.sources[0].steps[1]`. MCP drops the `version.`, `revision.` and `origin.` prefixes, as for every field error.
- The whole write is reported as field `*`.
- The message names fields and numbers, never content.
- Limits are checked before anything is stored, so a refused write creates no identity, version, revision, event or link. Checks against stored records come afterwards, as before: conflicts, existence and retirement in the store's transaction, and the `invalid_request` checks that compare an execution or a toil revision with stored records. A write that fails one of those and is also over a limit gets `value_too_large`.

**Request bodies.** The transports keep their 1 MiB body limit (`413 request_too_large`), checked first on the raw bytes. A whitespace-heavy body can therefore be refused before compaction even though its fields would fit. Field limits apply after compaction. Queries that store nothing are not bounded by this policy: duplicate checks, graph targets, discovery.

**Historical records.** Nothing is migrated or rewritten. Records stored before this decision, whatever their size, stay readable, listed, verifiable and selectable. Backup, inspection and restore copy the database file and never pass through the write path, so they preserve them. Every new write is checked, including a revision that repeats an oversized stored value unchanged: allowing copies would let the limit be bypassed indefinitely. To revise such a procedure, shorten the value, for example by moving detail behind a reference.

**Retries.** Polaroid has no idempotency keys.

- A retry of an accepted write keeps its earlier answer: a conflict (`canonical_key_exists`, `version_conflict`, `revision_conflict`, `origin_exists`, `execution_invalidated`, `lifecycle_conflict`), or another record for executions and feedback. Every write accepted from now on is within limits, so that is unchanged.
- Only a retry of an oversized write that an earlier build accepted now gets `value_too_large` instead of its conflict. Nothing is stored either way.
- The fixture loader compares before it writes, so loading fixtures that match stored records writes nothing and is unaffected.

**Discoverable policy.** The policy is one table in the domain. Validation looks up each limit in it, and the statistics response reports it as an additive member `write_limits`: `policy_version`, `write_max_bytes`, `request_max_bytes` and `fields`, each `{"record","field","kind","max_bytes"}`. There is no separate tool, and the limits are not configurable.

**Within `/v1`.** The API rules ask for a new version prefix when a rule is tightened. This decision stays within `/v1`, as [ADR-0024](0024-child-inputs-follow-the-reference-mapping.md) did: every value stored in the shared catalog is at most a quarter of its new limit, so none of those writes would be refused now, and a second prefix would split clients for no gain. A client that does exceed a limit gets a distinct code, `value_too_large`, never a changed meaning of an accepted write.

**Per-write limits are not growth control.** They bound one write. They do not bound how many writes are made or the catalog's total size. That is retention's question, still unscoped.

## Consequences

- An accidental log dump is refused with a precise, content-free error. Agents store concise evidence and references, not whole logs.
- Clients that wrote large values must shorten them. No stored record is affected.
- Changing a limit needs a new ADR and increments `policy_version`.
