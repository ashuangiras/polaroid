# 0051. `discover` gains a `kind` selector: procedure matching, corpus statistics, and a tool list

**Status:** Accepted. Amends [ADR-0049](0049-agent-mode-finalized-no-migration.md)'s freeze of `/mcp/agent`'s three (now four, with [ADR-0050](0050-corpus-logic.md)'s `resolve`) tools, under the additive-change allowance [ADR-0038](0038-stable-releases-and-compatibility.md) already gives a minor release; reads [ADR-0047](0047-knowledge-identities-and-origin.md) and [ADR-0050](0050-corpus-logic.md) for the identities and typed records it counts.
**Date:** 2026-10-15

## Context

`/mcp/agent`'s `discover` does one job today: lexical matching of a task against published procedures ([ADR-0043](0043-knowledge-first-agent-mode.md), [ADR-0048](0048-git-native-agent-interface.md)). There is no way, from agent mode, to see what the Git-native corpus currently holds — how many records of each type, how they are distributed across domains and repositories — or to get a self-description of agent mode's own tools without a client reading the MCP protocol's `tools/list`. The legacy `/mcp`'s `get_catalog_statistics` ([ADR-0036](0036-read-only-catalog-statistics.md)) answers the first kind of question, but only for the SQLite catalog; it does not and cannot describe Git-native knowledge, which [ADR-0049](0049-agent-mode-finalized-no-migration.md) deliberately keeps a separate store with no migration between the two. Nothing in `internal/knowledge` or `internal/gitcore` computes counts over the corpus today.

`/mcp/agent`'s tools are finalized under ADR-0038's v1 compatibility guarantee (ADR-0049): tool names, arguments, required fields and result shapes do not change without that same commitment. [ADR-0038](0038-stable-releases-and-compatibility.md) itself allows a minor release to add optional request members and response members, as long as a client that sends none of them keeps seeing exactly today's behavior and shape. This ADR uses that allowance for everything except one point it makes deliberately, not just additively: `task` stops being unconditionally required.

## Decision

**`discover` gains one new optional argument, `kind`**, with values `"procedures"` (the default — omitting `kind` reproduces today's exact behavior and schema), `"stats"` and `"tools"`. `read`, `feedback` and `resolve` are unchanged. `discover` is one Go handler (`tools.agentDiscover`) registered identically at both `/mcp/agent` (v1, three tools) and `/mcp/agent/v2` (ADR-0050, the same three plus `resolve`): this change necessarily reaches both endpoints, not only v1, and keeps intact the invariant `cmd/polaroidd/e2e_corpus_test.go` already checks — that `discover`, `read` and `feedback`'s schemas are byte-identical between v1 and v2. `kind="tools"`, below, is the one place behavior differs by endpoint, the same way the registered tool set already does: it lists `resolve` only when called at `/mcp/agent/v2`.

**`task` becomes conditionally required.** It is required when `kind` is absent or `"procedures"`, and ignored (clients should omit it) for `"stats"` and `"tools"`. This is the one non-additive change to the existing contract: the schema's `required` list for `task` is removed, and the rule is enforced at call time instead — a `"procedures"` call without `task` is `invalid_request`, exactly as a missing required field is today. `repository` keeps its existing meaning for every kind: for `"stats"`, it scopes `procedures.total` and `procedures.by_repository` to what applies in that repository (shared plus that repository's own), the same test `Applicability.ApplicableIn` already performs for `"procedures"`; `by_type` and `by_domain`, which carry no per-repository applicability, are never scoped by it. `limit` is not used by `"stats"` or `"tools"` and is rejected if given with them, to avoid silently ignored arguments.

**`kind="stats"`** returns counts over the Git-native corpus at a snapshot (the active one, or a given `snapshot`, exactly as `read` already resolves one):

```json
{"snapshot": "…", "kind": "stats", "generated_at": "…",
 "by_type": {"Domain": 4, "Concept": 12, "Claim": 9, "Rule": 6, "Procedure": 8, "Evidence": 3, "Exception": 1},
 "by_domain": {"go.testing": 7, "git.workflow": 5, "unclassified": 3},
 "procedures": {"total": 23, "by_repository": {"shared": 18, "unspecified": 2, "github.com/ashuangiras/polaroid": 3}}}
```

- **`by_type`** is a count of every *typed* record in the snapshot (`Catalog.Records()`) by its `RecordType` ([record.go](../../../internal/knowledge/record.go)) — `Domain, Concept, Claim, Rule, Procedure, Evidence, Exception`; every key is listed, zero included, as ADR-0036 already does for the legacy catalog. `by_type["Procedure"]` is **not** the same count as `procedures.total`, below: `checkRecords` ([recordcheck.go](../../../internal/knowledge/recordcheck.go)) requires every `TypeProcedure` typed record to name a real procedure document, but not the reverse, so a procedure without a Corpus Logic typed record (not yet given reasoning facts) is in `procedures.total` and not in `by_type["Procedure"]`.
- **`by_domain`** groups every typed record by the `Domain` record(s) it is directly `classified_as` ([recordcheck.go](../../../internal/knowledge/recordcheck.go) already requires a `classified_as` target to be a `Domain`). This is a direct-edge count only, not a transitive closure through `broader`: a record classified under a narrower domain is not also counted under its ancestor domains in `v1`. A record with no `classified_as` counts under `"unclassified"`. A record classified under more than one domain counts once under each.
- **`procedures`** is `Catalog.procs` (the ones `discover(kind="procedures")` matches against; also what `read` serves): `total`, the count, and `by_repository`, its `Applicability` ([knowledge.go](../../../internal/knowledge/knowledge.go)), the same three-way split ADR-0036 already uses for the legacy catalog's `by_scope`: `"shared"` for `ScopeShared`, `"unspecified"` for a procedure with no declared applicability, and the repository identifier for each `ScopeLocal` one. Typed Corpus Logic records other than `Procedure` carry no per-repository applicability today — only `Rule`/`Exception` authority names a `Repository` for governance, a different concept — so `by_type` and `by_domain` are never split by repository in `v1`.

**`kind="tools"`** is a static self-description of agent mode's own tool set, built from the same registrations `newAgentServerV2` already makes, not a new source of truth:

```json
{"kind": "tools", "tools": [
  {"name": "discover", "description": "…", "read_only": true},
  {"name": "read", "description": "…", "read_only": true},
  {"name": "feedback", "description": "…", "read_only": false},
  {"name": "resolve", "description": "…", "read_only": true}
]}
```

It is offered for parity with the original idea behind this ADR and for clients that only see the result of one tool call rather than the surrounding MCP session; it is explicitly not a substitute for the protocol's own `tools/list`, which every MCP client already has available without calling `discover` at all.

**Left out, on purpose** (mirroring how ADR-0036 scoped the legacy statistics):

- **Provenance** (authored locally vs. fetched from a remote via `knowledgesync`, trust state, divergence) is a different axis from type/domain/repository counts and is not part of `"stats"`. It belongs with the operator surface (`internal/knowledgesync`), not agent mode, if it is ever built.
- **Transitive domain rollups**, trends over time, and per-record detail: counts only, the same bound ADR-0036 drew for the legacy catalog.
- **Pagination:** `"tools"` never grows past agent mode's own small, fixed tool count; `"stats"` is always a complete summary, never a list of records.

**Contract and test updates**, made together as every prior tool-argument change in this file requires: `docs/architecture/mcp.md`'s agent-mode table (both the v1 and the v2 section, which says v1 and v2 schemas are identical), `internal/transport/mcp/testdata/agent-mode-tools.json`, `TestAgentModeToolContract` and `TestAgentModeContractIsDocumented`, `cmd/polaroidd/e2e_corpus_test.go`'s v1/v2 schema-identity assertion (unaffected in substance, since both still share one handler, but exercised with the new argument), new counting logic in `internal/knowledge` (nothing computes `by_type`/`by_domain`/`procedures` today) exercised against real Git-native fixtures, not mocks, and `internal/knowledgeops.Agent`'s `Discover`, which now branches on `in.Kind`.

## Consequences

- `/mcp/agent` stays a single entry point for everyday procedural-knowledge use (discover, read, feedback, resolve) while gaining a self-serve view of the corpus and of itself, without adding tool count.
- The one non-additive change — `task`'s requiredness now depends on `kind` — means a client reading only the schema's `required` list, not this decision, could call `discover` with neither `task` nor `kind` and get `invalid_request` where it previously got the same error for a different reason (a missing required field); behavior for every existing caller that always sent `task` is unchanged.
- `get_catalog_statistics` on `/mcp` is untouched and keeps summarizing only the SQLite catalog; the two stores remain, as ADR-0049 put it, "the permanent division of responsibility," now each with its own statistics surface instead of one trying to cover both.
- A future need for provenance/sync statistics, or for transitive domain rollups, is a new decision, not an extension of this one.
