# 0004. Append-only versions guarded by an expected base version

**Status:** Accepted
**Date:** 2026-10-09

## Context

Agents revise procedures after investigating failures, and several agents may revise the same procedure concurrently. Later work records exactly which version an execution used, so a stored version must never change after creation. A revision derived from an outdated version must not silently discard someone else's change.

## Decision

- A procedure's identity (`id` and `canonical_key`) is immutable. Its versions are append-only and numbered 1, 2, 3 and so on, without gaps.
- A revision carries `base_version`. In one write transaction, the store checks that `base_version` equals the latest version and then inserts version `base_version + 1`. Any other base returns `409 version_conflict` together with the latest version, and nothing is stored. Conflicts are never merged automatically.
- The database enforces the same invariants itself, independently of the application code:
  - triggers reject `UPDATE` and `DELETE` on procedures and versions;
  - a trigger rejects any version number other than latest plus 1;
  - unique constraints cover canonical keys and `(procedure_id, version)`.
- Version 1 is created in the same transaction as the procedure.

## Consequences

- History can be audited, and references to "version N" stay meaningful forever.
- Clients need a read, modify, write-with-base loop, and must re-read on a conflict.
- Nothing can be deleted or corrected in place. Retiring procedures, aliases and identity consolidation need new, explicit records in later increments. Relaxing any trigger needs a new ADR.
