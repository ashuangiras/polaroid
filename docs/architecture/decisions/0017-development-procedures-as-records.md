# 0017. Polaroid's development procedures are records, loaded from fixtures by canonical key

**Status:** Accepted
**Date:** 2026-10-09

## Context

Polaroid's own development knowledge (how to build it, which checks to run, how to verify a change) exists only as prose in `AGENTS.md` and as Make targets. The product loop has never been used on real work: retrieve a procedure, follow it, record evidence, append a correction, and let a later session reuse it ([#28](https://github.com/ashuangiras/polaroid/issues/28)). Doing so raises these questions:

- where the procedures are maintained, and how they reach a store whose IDs are assigned by the server;
- how shared procedures stay repository-independent;
- how an agent records a run against a working tree that may differ from its commit;
- how a deliberate defect, used to exercise the correction loop, stays distinguishable from a real one;
- how scripted regression checks are kept apart from evidence of an agent's reasoning.

## Decision

- **Records, not code.** The procedures are fixtures in `examples/development/`, in the request shapes of the API: `procedures/<dir>/v1.create.json` and `vN.revise.json`, and `bindings/*.json`. Polaroid's code gains no build or test behavior.
- **Repository-independent identities.** Shared procedures use generic canonical keys: `go.module.build`, `go.module.checks`, and `dev.change.verify`, which references the other two contextually. Repository-local values (commands, artifact paths) are binding inputs, passed to the children through reference input mappings.
- **Canonical keys stand in for IDs.** In a fixture, the `procedure_id` of a reference or a binding may hold a canonical key. `scripts/load-fixtures.sh` replaces it with the ID of the procedure that key names in the target store. No fixture contains an ID from any database.
- **The loader only appends.** It is generic over record shapes and knows no task. For each procedure it reuses the identity with the same canonical key; checks that every stored version equals the fixture's version, or stops; and appends missing versions with `base_version` set to the stored latest. It reuses a binding with the same repository and name if it binds the same procedure, or stops. `-n N` loads versions up to `N`, to rebuild a store as it was before a later version. A second run changes nothing.
- **Agent-written versions are exported verbatim.** A version an agent appends in a live store is copied into the next `vN.revise.json`, so fixture history and store history match.
- **Working-tree state is an input.** `dev.change.verify`'s contract requires `working_tree` among its inputs: `clean`, or `modified:` with a digest of the changes. A run against a modified tree is a different combination from a run at the clean commit, and is never recorded as `clean`. This is a convention of the record, not of Polaroid.
- **Seeded defects are labelled.** A demonstration may seed a deliberately stale instruction only if the version's `revision_reason` says that it is a demonstration seed, and the documentation names it.
- **Two kinds of evidence.** `make demo` replays the loop with scripted outcomes. It proves the lifecycle and is labelled as scripted. Agent-session evidence (what an agent retrieved, ran, concluded and wrote) is recorded in [procedural-loop.md](../../development/procedural-loop.md), with IDs from a live store. Neither stands in for the other.

## Consequences

- The fixture history of `go.module.checks` keeps the labelled stale version 1 next to its correction, as any store loaded from the fixtures does.
- IDs differ in every store. Documentation that cites IDs names the store they come from.
- A fixture whose content differs from a store's stored version cannot be loaded into that store. Corrections are new versions, never edits.
- The live store is a local file under the ignored `bin/`. Only fixtures and evidence summaries are tracked.
