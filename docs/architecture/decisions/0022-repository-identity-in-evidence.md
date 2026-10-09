# 0022. Evidence matches by registered repository identity

**Status:** Accepted. Supersedes the exact-identifier rule for evidence in [ADR-0019](0019-repository-registry.md).
**Date:** 2026-10-09

## Context

ADR-0019 registered repositories with a canonical identifier and explicit aliases, but kept verification combinations ([ADR-0012](0012-derived-verification.md)) and resolution evidence ([ADR-0013](0013-evidence-based-resolution.md), [ADR-0018](0018-selection-evidence-and-target-verification.md)) on exact identifier strings ([#37](https://github.com/ashuangiras/polaroid/issues/37)). An execution recorded under an alias neither verified nor failed the same commit under the canonical identifier, even though registering the alias is an explicit statement that both name one repository. Before a second repository relies on the catalog, an alias must mean the same thing everywhere.

## Decision

- **Identity.** An identifier's repository identity is the registered repository it belongs to, as canonical identifier or alias; an unregistered identifier is its own identity and matches only itself, exactly. Only registration establishes identity: similar names, forks, matching commits or identical trees never do.
- **Combinations.** A combination is the identity, the commit, the environment name, the canonical inputs and the child-version tree. Every other component still has to match exactly, and a combination's status is still decided by its latest execution: ordered by `created_at`, then `id`, across all identifiers of the identity. So a later failure recorded under an alias withdraws the verification a run under the canonical identifier gave, and the reverse.
- **Where it applies.** The verification list's `repository` filter, target verification, and contextual resolution (selection evidence) all match by identity. Selection evidence still names the execution and the identifier it was recorded with.
- **Records keep their provenance.** An execution's `repository` is always the identifier submitted with it. Identity is derived on every read from the registry, which is append-only, and is never stored on the execution.
- **Responses** add `repository_id`, the registered repository, wherever an execution, a selection evidence or a combination is reported, and omit it for unregistered identifiers. A combination's `repository` is the identity's canonical identifier when it is registered, and the identifier otherwise.
- **Registering an alias after executions exist** is retroactive for derived verification, by design: from that moment, executions recorded earlier under the alias belong to the same combinations as those under the canonical identifier. That can change a combination's current status in either direction, because the latest execution across both identifiers decides. Historical outcomes, evidence and identifiers never change. Identifiers are never moved or removed, so registration only ever merges combinations; it never splits one.
- **Writes are unchanged.** A child execution names the same identifier as its parent; a binding's executions name the binding's identifier; alias registration still rejects a taken identifier and a binding-name collision across the repository's identifiers (`409`).

## Consequences

- Agents can record runs under any registered identifier of a repository and see one verification state. They should still prefer the canonical identifier, which lookups by alias return.
- `combination.repository` changes from the alias to the canonical identifier for combinations whose executions were recorded under an alias; the executions themselves are unchanged. Nothing changes for unregistered identifiers, or for repositories without aliases.
- Verification never moves between commits: a rebased commit with an identical tree is a different target ([ADR-0018](0018-selection-evidence-and-target-verification.md)).
- Registration is the only way to merge identities, and it is irreversible. Registering a wrong alias would combine evidence that should not be combined, which is why an alias requires a reason.
