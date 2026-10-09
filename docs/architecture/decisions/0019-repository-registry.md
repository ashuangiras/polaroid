# 0019. Repositories are registered records with a canonical identifier and explicit aliases

**Status:** Accepted; the exact-identifier rule for verification and resolution evidence is superseded by [ADR-0022](0022-repository-identity-in-evidence.md)
**Date:** 2026-10-09

## Context

[ADR-0007](0007-repository-identity-for-bindings.md) identifies a repository by a canonical path string, and keeps no repository records: a repository is the string its bindings and executions share. With several repositories using one catalog ([#35](https://github.com/ashuangiras/polaroid/issues/35)), that is not enough:

- a procedure cannot be declared local to a repository without a stable thing to point at;
- a moved or renamed repository, or one known by two spellings (for example a mirror host), cannot be recognized as the same;
- nothing has a display name.

Stored bindings and executions carry their identifier strings, and those records are immutable. Their identifiers must keep working, and their provenance must not be rewritten.

## Decision

- **Registry.** A repository has a server-assigned UUIDv7 `id`, a single-line display `name`, one **canonical identifier**, and zero or more **aliases**. Identifiers use the ADR-0007 format, which Polaroid still validates and never rewrites: normalization stays the client's job, and the format has one accepted spelling.
- **Explicit only.** Repositories are created by `POST /v1/repositories` (`identifier`, `name`). Aliases are added by `POST /v1/repositories/{id}/aliases` (`identifier`, `reason`). Nothing is registered by a read, by a binding, by an execution or by the migration. Polaroid never infers that two identifiers are the same repository from their spelling, so a fork is a different repository unless someone registers it as an alias.
- **Identifiers are unique and permanent.** An identifier belongs to at most one repository, as canonical identifier or alias, and is never moved, changed or removed. A taken identifier gets `409 repository_identifier_exists`, also when two registrations race: exactly one succeeds. Repositories and identifiers are immutable; the display name is fixed at registration.
- **Lookup.** `GET /v1/repositories/{id}`, `GET /v1/repositories/by-identifier/{identifier}` (canonical or alias; an alias returns the same repository), and `GET /v1/repositories`.
- **Association is derived, not stored.** Bindings, executions and feedback keep the identifier they were written with. Which repository a record belongs to is read through the registry, so registering an identifier later, or an alias, associates the earlier records without changing them.
- **Where identity is used.**
  - List filters by repository (bindings, executions, feedback) match every identifier of the repository the given identifier is registered to; an unregistered identifier matches only itself, as before.
  - Applicability ([ADR-0020](0020-procedure-origin-and-applicability.md)) compares repository IDs.
  - A binding's local name is unique within the repository across its identifiers: a new binding whose name another identifier of the same repository already uses gets `409 binding_exists`, and an alias whose bindings would duplicate a name gets `409 binding_exists` and is not added.
  - Verification combinations and resolution evidence still compare identifier strings exactly ([ADR-0012](0012-derived-verification.md), [ADR-0013](0013-evidence-based-resolution.md)). Registering an alias never merges or changes any combination.
- **Compatibility.** Every existing request is unchanged. Unregistered identifiers stay valid wherever they were valid, for procedures that are not local to a repository.

## Consequences

- Agents should record bindings and executions with the canonical identifier, which lookup by alias returns; evidence recorded under an alias selects and verifies only under that alias.
- Reassigning an identifier, renaming a repository or merging two repositories needs a later, explicit decision; none is possible now.
- This organizes records. It is not access control or tenant isolation ([ADR-0006](0006-local-unauthenticated-api.md)).
