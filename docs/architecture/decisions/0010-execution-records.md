# 0010. Executions are immutable after-the-fact records with a named environment and inline evidence

**Status:** Accepted
**Date:** 2026-10-09

## Context

Increment 3 records what agents actually ran ([#7](https://github.com/ashuangiras/polaroid/issues/7)), so that later work can verify a version in a context and resolve contextual references from evidence. Four choices shape every execution ever stored, and the open questions in [records.md](../records.md) left them unanswered:

- how an environment is identified;
- how evidence is stored;
- when a record is written;
- which commit identifiers are accepted.

## Decision

- **Record once, after the run.** An execution is written once, complete, with an `outcome` of `succeeded` or `failed`, and never changed or deleted. There is no "running" state, so there is nothing to transition and nothing to race on.
- **A named environment with free-form attributes.** `environment` is `{"name": ..., "attributes": {...}}`.
  - `name` uses the canonical-key format, and it is the identity that verification (3.3) will match on.
  - `attributes` is a JSON object that Polaroid stores but never interprets, for example the OS or toolchain versions.
  - Adding an attribute therefore never splits an environment's history.
- **Inline evidence.** `evidence` is a non-empty JSON object, stored verbatim (compacted) like `contract`, and bounded only by the 1 MiB request limit. Large artifacts belong outside Polaroid and are referenced from evidence members, for example by URI and digest. Polaroid does not dereference them.
- **Full commit hashes.** `commit` is 40 (SHA-1) or 64 (SHA-256) lowercase hex characters. Abbreviated hashes are ambiguous over time and are rejected.
- **Bindings are checked for consistency.**
  - An execution may name a `binding_id` and `binding_revision`, together or not at all.
  - The binding must be for the same procedure and repository.
  - If the revision pins a version, `version` must equal the pin. A contextual revision accepts any version, and the execution still records the exact one.
- **Errors.** An unknown procedure or binding is `404`, as for bindings. A missing version or binding revision, and every mismatch, is `400` naming the field. The service checks these by reading immutable records before the write, so the checks cannot race. The schema enforces the same rules with foreign keys and triggers.
- **Listing.** Executions are listed per procedure, with optional `version` and `repository` filters, oldest first. List items omit `inputs` and `evidence`, which can be large.

## Consequences

- Execution history is append-only and auditable. A wrong record cannot be corrected, only followed by a newer one.
- In-flight runs are invisible to Polaroid. An agent that crashes mid-run leaves no record.
- Evidence size is capped by the request limit. Raising it is a separate decision.
- Matching on the environment name puts naming discipline on clients. Two clients that name the same machine differently get separate histories.
