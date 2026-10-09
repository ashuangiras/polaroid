# 0021. Feedback has a typed subject, and lists are filterable with opt-in keyset pagination

**Status:** Accepted; the pagination contract is refined by [ADR-0023](0023-pagination-guarantees-and-scope-labels.md)
**Date:** 2026-10-09

## Context

Feedback reports ([ADR-0015](0015-feedback-reports.md)) are about "Polaroid itself", with record IDs, if any, in a free-form `context` that is never checked. With several repositories, reports about a procedure version, a binding or an execution need to be found by subject and repository ([#35](https://github.com/ashuangiras/polaroid/issues/35)). Every list is unbounded, and http-api.md promised that pagination, if added, would be opt-in.

## Decision

- **Subject.** A report may name its `subject`, a typed envelope whose references are checked:
  - `{"type": "service"}`: the Polaroid service;
  - `{"type": "repository", "repository_id": …}`;
  - `{"type": "procedure", "procedure_id": …[, "version": N]}`;
  - `{"type": "binding", "binding_id": …[, "revision": N]}`;
  - `{"type": "execution", "execution_id": …}`.
  A member that does not belong to the type, or a record that does not exist, is `400`.
- **Context, apart from the subject.** A report may also give `repository` (an identifier, registered or not) and `execution_id` (an existing execution). They must agree with each other and with the subject: the execution ran in that repository (by identity, [ADR-0019](0019-repository-registry.md)); for a procedure subject, it ran that procedure and version; for a binding subject, it used that binding and revision; for an execution subject, it is that execution; for a repository or binding subject, the repository is that repository. Disagreement is `400`. `details` and the free-form `context` stay as they are.
- **Existing reports** have no subject, and keep the meaning ADR-0015 gave them: about the Polaroid service. A new report without `subject` means the same, so old requests are unchanged. Responses omit absent fields.
- **Filters.** `GET /v1/feedback` adds `subject_type`, `subject_id`, `subject_version` and `repository`. `subject_type=service` includes reports without a subject. `repository` matches the report's repository context, or a repository subject, by identity. `GET /v1/executions` no longer requires `procedure_id`, and adds `commit`; `repository` matches by identity. `GET /v1/procedures` adds `repository` (latest version applicable there), `scope` and `q`, a case-insensitive substring of the canonical key or goal ([ADR-0020](0020-procedure-origin-and-applicability.md)).
- **Pagination is opt-in** on the procedure, repository, binding, execution and feedback lists. `limit` (1 to 500) returns at most that many items and, if more exist, `next`: an opaque cursor to pass back as `after`, which needs `limit`. Without `limit`, lists are complete, as before, and have no `next`.
- **Order and continuation.** Keyset pagination on a stable, unique order: procedures by canonical key, bindings by name, and repositories, executions and feedback by `created_at` then `id`. For the last three, `created_at` is assigned inside the write transaction and is strictly later than every stored one, so a record committed while a client pages always sorts after its cursor: continuing never skips or repeats a record. In the key-ordered lists, a record created during paging is seen only if it sorts after the cursor; no record is repeated.

## Consequences

- Subject and context make "reports about version 2 of this procedure in this repository" one query. Triage still happens outside Polaroid, by linking reports to GitHub issues; there is no triage state.
- An alias registered later makes older reports with that identifier match the repository filter; the reports themselves never change.
- A stored `created_at` can be later than the clock by a few nanoseconds when writes race; it is still the record's commit order.
