# 0020. Procedure origin is set once on the identity; applicability is declared per version

**Status:** Accepted
**Date:** 2026-10-09

## Context

Every procedure can be bound in any repository, and nothing records where it came from or where its contract is meant to hold ([#35](https://github.com/ashuangiras/polaroid/issues/35)). Two different things are missing:

- **Origin:** where and why a procedure was first created. It is provenance, and a procedure that originated in one repository can still be shared.
- **Applicability:** where the procedure's contract is intended to be used. It is a declaration, not evidence: verification stays per recorded target ([ADR-0012](0012-derived-verification.md), [ADR-0018](0018-selection-evidence-and-target-verification.md)).

Versions are immutable and conflict-checked ([ADR-0004](0004-append-only-versions-with-expected-base.md)). A changeable field on the procedure would change what every existing version means without a trace.

## Decision

- **Applicability belongs to the version.** A version may declare `applicability`: `{"shared": {}}`, or `{"repository": "<repository id>"}` for a version local to one registered repository ([ADR-0019](0019-repository-registry.md)). Absent means *unspecified*: every version stored before this decision, and any new version that omits it, so existing requests keep working. Changing applicability, for example promoting a local procedure to shared or narrowing a shared one, is a new version with a `revision_reason`; the earlier versions keep their declaration.
- **Goal.** A version may also carry `goal`, a single line saying what the procedure achieves, for discovery. Absent means none.
- **Origin is set once on the identity.** `origin` is `{"repository_id", "reason"}`, given when the procedure is created, or recorded later by `POST /v1/procedures/{id}/origin`, at most once (`409 origin_exists`). It never changes applicability.
- **Applicable in a repository:** an unspecified or shared version is applicable everywhere; a local version only where the identifier is registered to its repository.
- **Composition.** Each version admits children by its own applicability: a local version admits unspecified, shared, and versions local to its own repository; a shared or unspecified version admits only versions that are not local. So a shared parent never presents a local dependency as available everywhere.
  - On write, a pinned reference's version must be admitted, and a contextual reference's target must have at least one admitted version (`400` on `version.references[i].procedure_id`).
  - On read, contextual selection considers only admitted versions: the highest verified one, else the latest admitted one. Evidence links to versions that are not admitted are ignored.
- **Where versions apply.**
  - Creating or revising a binding: a pinned version must be applicable in the binding's repository; for a contextual policy, the procedure's latest version must be, because a contextual binding follows the procedure forward (`400`).
  - Resolving a binding: contextual selection considers only applicable versions.
  - The graph endpoint with a repository: the requested version must be applicable there (`400` on `repository`).
  - Recording an execution: the version must be applicable in its repository, and each child must be admitted by the parent's version (`400`).
  Existing bindings, pins and executions are never invalidated; narrowing only stops newer versions from being selected where they do not apply.
- **Reported** on procedure histories and list items, from the latest version: `scope` (`shared`, `local` or `unspecified`), `applicability` and `goal`, plus `origin`; and on graph nodes, `applicability` when declared.
- **No classification by migration.** Existing procedures stay unspecified until someone who has read their contracts and bindings appends a version that declares applicability.

## Consequences

- Promotion copies the definition into a new version. That new version has no evidence anywhere, so promotion transfers no verification, not even within the repository it came from.
- A contextual binding whose procedure was narrowed keeps resolving to the newest version still applicable to it, and a new contextual binding there is refused.
- Agents can tell shared, local and undeclared procedures apart; an undeclared procedure is not a claim that it works everywhere.
