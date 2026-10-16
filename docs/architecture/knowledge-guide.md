# Using Git-native knowledge: a guide for agents and operators

This is the practical guide for an agent, or an operator acting on an agent's behalf, that wants to use Polaroid's Git-native knowledge in a repository: connecting to it, retrieving what applies to a task, and storing new knowledge. It consolidates the contract that [mcp.md](mcp.md), [http-api.md](http-api.md#git-native-knowledge-operator-api) and [ADR-0045](decisions/0045-living-knowledge-kernel.md) through [ADR-0050](decisions/0050-corpus-logic.md) define, with worked examples. Read those for the full normative detail and the reasoning behind each rule; this page is where to start and what to come back to while working.

If you are developing Polaroid itself, read [AGENTS.md](../../AGENTS.md) instead; it is the contributor rule set, not a usage guide.

## Two knowledge authorities

Polaroid runs two separate procedural-knowledge stores side by side, on the same daemon, never mixed:

| | Legacy catalog | Git-native knowledge |
| --- | --- | --- |
| Storage | SQLite | A bare Git repository (`internal/gitcore`), by default `~/.polaroid/data/knowledge` |
| Identity | Procedure ID + version number | Canonical key, read at an opaque snapshot token |
| Selection | Bindings, version policies, evidence-based resolution from recorded executions | None: one definition per key per snapshot |
| Endpoint | `/mcp` (40 tools), `/v1/procedures/*` | `/mcp/agent` (3 tools), `/mcp/agent/v2` (+1 tool), `/mcp/agent/v3` (+2 tools), `/v1/knowledge/*` |
| Records it serves | Procedures, bindings, executions, feedback, toil items | Procedures (guidance only) and, with Corpus Logic ([ADR-0050](decisions/0050-corpus-logic.md)), typed records: domains, concepts, claims, rules, procedure semantics, evidence, exceptions |
| Who writes | Any MCP client with `create_procedure` / `revise_procedure` | Only the explicit operator surface, `/v1/knowledge/*` or `polaroid knowledge-*`; never agent mode |

**This guide is entirely about the second column, and a new agent should connect to it only.** Git-native knowledge is the one procedural-memory authority this project is moving all task knowledge into; new knowledge should be authored here, not in the legacy catalog. The two stores share no record and never migrate into each other automatically ([ADR-0049](decisions/0049-agent-mode-finalized-no-migration.md)). The legacy catalog and its 40 `/mcp` tools are listed above only so you recognize them if you see them mentioned elsewhere in this repository's history or docs; do not connect to `/mcp` or call its tools (`discover_procedures`, `create_procedure`, and so on) as a new agent. Everything you need — discovering, reading, reasoning over and reporting on task knowledge — is served by the two endpoints below, and the one capability neither serves, storing new knowledge, is done with the `polaroid` CLI, not by falling back to a legacy MCP tool.

Within Git-native knowledge, two agent-facing MCP endpoints exist, both under the same v1 compatibility guarantee ([ADR-0038](decisions/0038-stable-releases-and-compatibility.md)):

- **`/mcp/agent`**: `discover`, `read`, `feedback`. Works with Procedure documents alone (format 1), and still works unchanged once typed records exist (format 2).
- **`/mcp/agent/v2`**: the same three tools, plus `resolve`, which reasons over typed records: obligations, specialization, exceptions, composition. Use it once the Corpus holds domains, concepts and rules worth reasoning over, not just procedures to read verbatim.
- **`/mcp/agent/v3`** ([ADR-0053](decisions/0053-guided-walkthroughs.md)): the same four tools, plus `start` and `advance`: a token-identified, expiring walkthrough of a procedure's ordered composed steps, remembered across turns and sessions without resending a `completed` list. Not yet finalized. Reach for it only when a procedure is long enough, and your work on it spans enough separate turns or sessions, that resuming matters; `discover`/`read`/`resolve` alone are enough for everyday, single-session task guidance.

**Connect to `/mcp/agent/v2` and use only that**, since it is a strict superset of `/mcp/agent` (the same three tools plus `resolve`); there is no reason for a new agent to pin to the smaller v1 surface instead. Reach for `resolve` specifically when you need to know what a goal obligates, whether an exception applies, or whether one procedure's output satisfies another's precondition; use `discover`/`read` alone for everyday task guidance. Connect to `/mcp/agent/v3` instead only when you specifically need `start`/`advance`'s stateful walkthrough.

## Set up for a repository

**There is no per-repository registration step in Git-native knowledge.** Unlike the legacy catalog's `register_repository`/alias system, a repository here is just a string: a procedure's or a context assertion's `repository` is compared by exact equality against what you pass as `repository` to `discover`, `read`, `resolve` or `feedback`. Pick one canonical form and use it consistently across every call and every record — the host/owner/repo form already used elsewhere in Polaroid, for example `github.com/ashuangiras/polaroid`, is a sensible default.

**Initializing a repository's knowledge means nothing more than running the daemon and, optionally, publishing its first records.** Concretely:

1. **Have a running `polaroidd`.** See the [README](../../README.md#quick-start) for installing and starting it; this guide assumes `http://127.0.0.1:7417` (the default) throughout.
2. **Connect an MCP client to `/mcp/agent/v2` only.** For VS Code's `.vscode/mcp.json`:

   ```json
   {
     "servers": {
       "polaroid-agent": { "type": "http", "url": "http://127.0.0.1:7417/mcp/agent/v2" }
     }
   }
   ```

   Do not also add an entry for `/mcp`: a new agent has no use for the legacy catalog, and adding it only invites reaching for the wrong tool.
3. **Check readiness.** An agent's `discover` tells you whether anything has been published yet: an empty catalog is a normal, well-formed state (`{"empty": true}`), not an error, and agents should treat it as "nothing to discover yet," not a failure. (`polaroid knowledge-status`, below, is the operator's equivalent check, and also reports whether a remote source is configured.)
4. **If this repository's procedures or rules do not exist yet, publish them with the `polaroid` CLI** (see "Storing knowledge" below) — there is no MCP tool for this, by design (storing is an explicit, approved operator action, never something an agent triggers on its own). There is nothing else to configure. A remote for exchanging knowledge between instances is optional and separate; see [knowledge-remotes.md](../development/knowledge-remotes.md) if you need it.

## Retrieving knowledge

These four tools are safe for any agent to call freely: `discover`, `read` and `resolve` are read-only, and `feedback` only ever adds an immutable report, never changes what is served. None of them need the operator token.

### `discover`: find what applies to a task

```json
{"task": "validate a slug-style identifier", "repository": "github.com/ashuangiras/polaroid", "limit": 5}
```

```json
{
  "snapshot": "b143067b3b89432271b1d83eb18e57eb94b95dca",
  "candidates": [
    {"key": "go.validate.slug-string", "goal": "Validate a slug-style identifier...", "score": 10}
  ]
}
```

`task` is free text; matching is lexical term scoring, not embeddings (deterministic, no network). `repository` narrows results to what applies there; omit it only when you have none. Carry the returned `snapshot` forward if you want every later call in this session to see exactly this state, even if something is published in the meantime.

### `read`: get one key's guidance

```json
{"key": "go.validate.slug-string"}
```

```json
{
  "snapshot": "b143067b3b89432271b1d83eb18e57eb94b95dca",
  "key": "go.validate.slug-string",
  "scope": "shared",
  "goal": "Validate a slug-style identifier...",
  "method": "Write one exported function, ValidateSlug(s string) error, ...",
  "contract": {"inputs": {"s": "string, the candidate slug"}, "expected_outcome": "..."},
  "instructions": {"steps": ["1. If s is empty, return an error wrapping ErrEmpty...", "..."]}
}
```

Pass `snapshot` (from an earlier `discover` or `read`) to pin exactly that published state; omit it to read the active knowledge. Pass `repository` to have it checked against the procedure's applicability (`not_applicable` if it is local to another repository). Pass `inputs` (your own effective inputs) when you need `references[].inputs` computed for a child call. `read` resolves references only one level — read a listed reference's own `key` with a further `read` if you need to follow it. Add `full: true` to also get `philosophy`.

### `resolve` (`/mcp/agent/v2`): what a goal obligates, and whether an exception applies

```json
{"goal": "go.verify", "context": {"concurrency": "true"}}
```

```json
{
  "version": "corpus-logic/1",
  "snapshot": "b143067b3b89432271b1d83eb18e57eb94b95dca",
  "goal": "go.verify",
  "at": "2026-10-14T18:00:00Z",
  "complete": true,
  "applicable": ["change.verify", "go.verify", "rule.go.vet", "rule.software.tests"],
  "obligations": [
    {"requirement": "check.tests", "status": "required", "derivations": [{"id": "…", "source": "rule.software.tests", "source_type": "rule", "via": "change.verify", "path": ["go.verify", "change.verify"], "status": "active"}]},
    {"requirement": "check.vet", "status": "required", "derivations": [{"id": "…", "source": "rule.go.vet", "source_type": "rule", "via": "go.verify", "status": "active"}]}
  ],
  "exceptions": [],
  "conflicts": [],
  "unknowns": [],
  "evidence": []
}
```

`goal` is a record key: a procedure, concept or claim. `context` is what *you* assert about your situation (string name/value pairs); it selects which rules apply, but by itself it **never authorises an exception** — a project- or condition-scoped exception needs a signed `assertions` entry instead (below). Add `then` (another procedure key) to also get a `composition` verdict (`compatible`, `incompatible`, `undetermined`) for running `goal` then `then`. Add `at` (RFC 3339) to resolve as of an earlier time; omit it for now.

Read `complete` before trusting an absence: `false` means some combination of missing context could not all be examined (`incomplete` says which names or why), and a conclusion that depends on context you did not give shows up in `unknowns` with the `when` values that would decide it — it is never silently treated as "no." `superseded` lists any rule or exception whose contribution was silenced by a `supersedes` relationship, so you can tell a retired rule from one that never applied.

To use a signed exception, pass `assertions`, each `{reference, key, repository, facts, valid_from, valid_until, signature}` as issued to you (for example by your CI):

```json
{"goal": "go.verify", "assertions": [
  {"reference": "assert-team.ci-1", "key": "team.ci", "repository": "github.com/team/app",
   "facts": {"ci": "true"}, "valid_from": "2026-10-14T11:00:00Z", "valid_until": "2026-10-14T13:00:00Z",
   "signature": "base64…"}
]}
```

Resolution is fully deterministic: the same snapshot, `context`, `assertions`, `at` and trust material always give byte-identical results, and every `derivations[].id` and `unknowns[].id`-equivalent is independently recomputable from the snapshot. See [mcp.md](mcp.md#agent-mode-v2-mcpagentv2) for the complete result shape and error codes (`not_found`, `invalid_request`, `budget_exceeded`).

### `feedback`: report a problem or a suggestion

```json
{"key": "go.validate.slug-string", "snapshot": "b143067b3b89432271b1d83eb18e57eb94b95dca",
 "classification": "incorrect_instruction", "summary": "Step 3 allows uppercase on Windows paths",
 "reporter": "copilot.agent"}
```

`classification` is one of `incorrect_applicability`, `missing_precondition`, `incorrect_instruction`, `inadequate_verification` (problems), `improved_method` (a suggestion), or `product_defect`/`environment_problem`/`transient_failure` (not procedural knowledge; these fail with `invalid_request` here — report them another way). `key` and `snapshot` must be a pair that was actually served; a mismatch is `not_found`. Feedback never changes what is served: Git-native knowledge is not itself the feedback store, so your report is recorded as an ordinary feedback record ([ADR-0047](decisions/0047-knowledge-identities-and-origin.md)), which an operator reads and triages with `polaroid feedbacks` / `get-feedback` / `resolve-feedback`, then decides whether to publish a fix — optionally citing your report's id as `finding` so publishing reconciles it automatically.

## The record contract

Git-native knowledge holds two kinds of document, sharing one key namespace (at most 128 lowercase letters and digits, joined by single `.`, `-` or `_`; a key never changes and names exactly one thing):

- **Procedure documents** (`procedures/<first key segment>/<key>.json`): what `read` serves. Always present for anything an agent should be told how to do.
- **Typed records** (`records/<type>/<first key segment>/<key>.jsonld`, [ADR-0050](decisions/0050-corpus-logic.md)): what `resolve` reasons over. Optional — a snapshot with none of these is format 1, read exactly as before; one with any is format 2 and carries a `corpus.json` manifest.

**A Procedure document and a typed `Procedure` record are two different things that may share one key.** The document (below) is what an agent reads for guidance. The record declares that same key's classification, specialization, dependencies and formal contract so that *rules* can match it — `rule(P, R) :- record(P, "Procedure"), in_domain(P, D)` only fires for a key that has a typed `Procedure` record, whatever guidance its document carries. Publish a typed `Procedure` record only for the keys you actually want reasoning to see; a document with no matching typed record is still perfectly readable by `discover`/`read`, just invisible to `resolve`'s rules. When a key does have both, keep them consistent (see the standing rule under "Storing knowledge" below): a typed record must not omit a relationship — most importantly `composes`, mirroring the document's own `references` — that the document already states.

### Procedure document

| Field | Meaning |
| --- | --- |
| `key` | Canonical key. |
| `goal` | One line, at most 2 KiB. |
| `philosophy`, `method` | At most 8 KiB each. |
| `applicability` | `{"shared": {}}` or `{"repository": "<repository>"}` (local to one repository). |
| `contract`, `instructions` | Free-form JSON objects, at most 64 KiB each; Polaroid never interprets their members. |
| `references` | `[{"name", "key", "condition", "inputs"}]`: `name` unique within the procedure, `key` the target procedure, `condition` free text Polaroid never evaluates, `inputs` a mapping of `{"input": "<parent input>"}` or `{"value": <JSON>}` per child input, at most 16 KiB. The child must apply everywhere the parent does: a shared procedure may reference only shared children, and a local procedure only local children of its exact same repository (never a shared child, and never a local child of another repository). Cycles, a depth beyond 32 and more than 2,048 procedures in one expansion are refused. |

### Typed records

Every typed record shares `{"@context": "urn:polaroid:context:v1", "id": "k:<key>", "type": "<Type>", "label": ...}` plus the fields its type admits, below. Relationships hold other records' keys (written `k:<key>` in the document; give the bare key when publishing through the operator API — Polaroid adds the prefix). Sets are stored sorted and deduplicated; a relationship from a record to itself is refused. `classified_as` targets Domain and is admitted for every type; it is omitted from the per-type list below.

| Type | Scalar fields | Relationships (target type, where not same-type) |
| --- | --- | --- |
| **Domain** | `label`, `definition` | `broader` (acyclic) |
| **Concept** | `label`, `definition` | `broader` (acyclic), `specializes` (acyclic), `uses` (→ Concept/Claim/Rule/Procedure), `contradicts` (→ Claim/Concept), `supersedes` (acyclic) |
| **Claim** | `label`, `statement` (required), `asserts: [{subject, predicate, object}]` | `uses` (→ Concept/Claim/Rule/Procedure), `supports` (→ Claim), `contradicts` (→ Claim/Concept), `supersedes` (acyclic) |
| **Evidence** | `label`, `statement` (required), `source` | `supports` (→ Claim), `contradicts` (→ Claim/Concept) |
| **Rule** | `label`, `statement` (required), `program` (required, ≤16 KiB, safe Datalog subset below), `revision` (required, > 0), `protected`, `waivable` | `uses` (→ Concept/Claim/Rule/Procedure), `supersedes` (acyclic; only a Rule may supersede a Rule, only a protected Rule may supersede a protected one) |
| **Procedure** (record) | `label`, `definition`, `inputs`/`outputs: [{name, concept}]` (→ Concept), `outputs_complete`, `preconditions`/`postconditions`/`invariants` (→ Concept), `informal_conditions` | `specializes` (acyclic; a child's applicability must lie within its parent's), `uses` (→ Concept/Claim/Rule/Procedure), `composes` (acyclic, → Procedure), `precedes` (acyclic, → Procedure; every target must be a sibling — another key in the same parent's `composes` set; not fed to rule programs, read only by `resolve`'s `plan`, [ADR-0052](decisions/0052-ordered-sibling-steps.md)), `requires` (→ Concept), `supersedes` (acyclic) |
| **Exception** | `revision` (required), `rule_digest`, `waives` (→ Concept), `replacement` (→ Concept), `justification` (required), `authority` (required), `approval` (required) | `excepts` (required, exactly one, → Rule), `applies_to` (→ Procedure/Concept) |

**Rule programs** are positive Datalog only: atoms, `=`/`!=` over variables and string constants. Negation, aggregation, functions, temporal clauses, declarations and packages are refused before evaluation runs. A rule's head may only be `obligation(Subject, Requirement)` or `derived(Subject, Predicate, Object)`; `Requirement` must name a published Concept (checked at publication when it is a constant, reported as an `undefined_requirement` conflict at resolve time when it is derived through a variable). Built-in facts available to a program: `record(Key, Type)`, `classified_as`, `broader`, `specializes`, `uses`, `composes`, `requires`, `supports`, `contradicts`, `supersedes`, `excepts`, `precondition`, `postcondition`, `invariant`, `asserts(Key, Subject, Predicate, Object)`, `input_type`/`output_type`, `context(Name, Value)` (what the caller asserted), `goal(Key)`, plus the derived `in_domain`, `specializes_star`, `broader_star`, `relevant`. See [ADR-0050](decisions/0050-corpus-logic.md) for the full built-in program.

**Exceptions** are the only way a rule's obligation is waived, and only by an operator-held Ed25519 key with the matching authority (`corpus`, one `domain`, or one project `repository`) — never by caller-asserted context alone, and never for a `protected` rule at any authority. An exception names the rule's `revision` and `rule_digest` (the SHA-256 of its normative semantics: key, statement, program, protected, waivable, classified_as, supersedes, uses, canonical JSON), so a later change to any of those invalidates the exception until it is approved again. The approval is signed over `{purpose: "polaroid.corpus.exception.approval/v1", exception, rule, revision, rule_digest, applies_to, conditions, waives, replacement, justification_sha256, authority, reference, key, valid_from, valid_until}`, canonical JSON, RFC 8785. **Polaroid signs nothing itself**: the authority holding the private key signs the exception record offline, with whatever tool it trusts, before the record is published.

## Storing knowledge

**Storing knowledge is an explicit, token-guarded operator action, never something agent mode can do.** `/mcp/agent`, `/mcp/agent/v2` and `/mcp/agent/v3` have no tool that stores knowledge content beside `feedback` — there is no MCP tool for publishing, by design, because storing knowledge is an owner-approved operator decision, not something any MCP client should be able to trigger. (`/mcp/agent/v3`'s `start`/`advance` are write tools too, but they only remember one agent's own runtime progress through a procedure ([ADR-0053](decisions/0053-guided-walkthroughs.md)); they never store or publish knowledge content, and an expired walkthrough leaves no trace.) Use the **`polaroid` CLI** for it: `polaroid knowledge-*`, authenticated by `$POLAROID_KNOWLEDGE_TOKEN` (the token at `<knowledge-dir>-operator-token`). It is a thin client over `/v1/knowledge/*`; reach for the raw HTTP endpoints directly only if you are scripting something the CLI does not cover. **An agent must not run these commands on its own initiative.** Draft the change, but publish it only when acting explicitly as the operator with the owner's approval for that exact change — the same rule [AGENTS.md](../../AGENTS.md) states for this project's own development procedures applies to any repository's Corpus.

### Publish

Write the request as JSON and run `polaroid knowledge-publish FILE` (or `- ` for stdin):

```json
{
  "base": "",
  "changes": [
    {"key": "go.verify", "scope": "shared", "goal": "Verify a Go change.",
     "philosophy": "Verify what you change.", "method": "Follow the steps.",
     "contract": {}, "instructions": {"steps": []}}
  ],
  "records": [
    {"key": "domain.software", "document": {"@context": "urn:polaroid:context:v1", "id": "k:domain.software", "type": "Domain", "label": "Software engineering"}},
    {"key": "domain.software.go", "document": {"@context": "urn:polaroid:context:v1", "id": "k:domain.software.go", "type": "Domain", "label": "Go software", "broader": ["k:domain.software"]}},
    {"key": "check.vet", "document": {"@context": "urn:polaroid:context:v1", "id": "k:check.vet", "type": "Concept", "label": "go vet passes", "classified_as": ["k:domain.software.go"]}},
    {"key": "go.verify", "document": {"@context": "urn:polaroid:context:v1", "id": "k:go.verify", "type": "Procedure", "label": "go.verify", "classified_as": ["k:domain.software.go"]}},
    {"key": "rule.go.vet", "document": {"@context": "urn:polaroid:context:v1", "id": "k:rule.go.vet", "type": "Rule", "label": "rule.go.vet",
       "statement": "Every Go procedure requires go vet.", "revision": 1, "waivable": true, "classified_as": ["k:domain.software.go"],
       "program": "obligation(P, \"check.vet\") :- record(P, \"Procedure\"), in_domain(P, \"domain.software.go\")."}}
  ],
  "author": "operator.alice", "summary": "Seed Go verification rules", "rationale": "Establish the baseline go vet obligation for Go procedures."
}
```

`base` is the active snapshot from `polaroid knowledge-status` (empty string for a fresh, empty catalog); a stale `base` is `409 stale_snapshot` — refresh and reapply. `changes` and `records` together need at least one entry; `{"key": ..., "remove": true}` deletes a key (a procedure change) or a record. `author` must be a name an eventual remote's `publishers` list accepts if this will ever be pushed; `summary` and `rationale` are both required, and `rationale` is what justifies the change to a reviewer or another instance validating it later. Add `finding` with a feedback report id to reconcile it (mark it addressed) immediately after publishing.

A refused publication is `400 invalid_request` with one problem per field: an unsafe rule program, an undeclared requirement, a protected rule made waivable, a published rule removed or retyped, a semantics change without a revision rise, or any of the structural checks (key syntax, relationship target types, cycles, size and count limits) in the record and relationship tables above.

**Standing rule: a record must state every relationship its own content already makes true.** Every admitted field splits into four categories, and only one of them is ever acceptably blank without a tracked reason:

- **Schema-inapplicable.** A field the record's type does not admit at all (a `Domain` has no `statement` or `program`). Publishing one is rejected outright. Nothing to do.
- **Genuinely empty.** The field is admitted, but no true relationship or content of that kind exists yet (a root domain's `broader`; a concept with no narrower or broader concept in a small corpus). Leaving it blank states the correct, complete fact — adding a value here would be fabricating a relationship that is not real.
- **Known but unstated — always fix this, in the same publication that would otherwise leave it inconsistent.** The record's own content, or another record's content it is clearly tied to, already makes a field's value plain, and it was simply not carried over:
  - A Procedure document's `references` names another procedure, and a typed `Procedure` record exists for the same key → mirror it as `composes` (Procedure→Procedure, acyclic; the relation `resolve`'s `relevant` traversal and `then` composition both read).
  - A Rule's `program` names a concept in its `obligation`/`derived` head → mirror it as `uses` on that rule. **Note:** for a `Rule`, this is a governed semantics change ([ADR-0050](decisions/0050-corpus-logic.md)) — `uses` is part of `RuleDigest`, so adding or changing it needs a revision rise like any other semantics edit, not a silent patch. Stating it is still correct even when it currently has no observable effect on `resolve` (e.g. `uses` only feeds the `relevant` traversal, which does not yet reach obligation-derivation rules for any goal) — a true relationship is worth stating for reasoning and RDF-projection consistency regardless of what reads it today.
  - The same reasoning applies to any other relationship a document's or another record's structure makes true. Do not add or leave a typed record with less relational information than is already known.
- **Needs new content, not extraction.** The field is admitted, nothing states its correct value yet, and filling it correctly means authoring something new — a `definition` gloss, or formal `inputs`/`outputs`/`preconditions`/`postconditions` backed by Concepts that do not exist yet. This is a real decision (what to name a new Concept, how to phrase a definition), not a mechanical mirroring, and must not be invented casually. File a GitHub issue on this repository describing the gap (which key, which field, what the Concept or prose would need to say) instead of leaving it silently blank with no record that it was ever considered.

### Attach a governed exception

Publish the `Exception` record the same way, as a `records` entry, once it is signed (see above). It must name the rule's **current** `revision` and `rule_digest` — a stale digest (the rule changed after the approval was signed) is refused, not silently ignored.

### Rollback

`polaroid knowledge-rollback FILE`, with `{"base", "to", "author", "summary", "rationale"}`, restores snapshot `to`'s knowledge as a new publication on top of `base`. History is never rewritten; a restored rule whose semantics differ from the active one takes a revision above both, so no approval given for an earlier revision becomes valid again. A rollback that would remove a published rule, unprotect a protected one, or restore a rule this instance never governed (not in the active knowledge) is refused, and so is one that changes nothing.

### Exchange knowledge between instances

Publishing only changes this instance. To share knowledge with another Polaroid instance over an ordinary Git remote (`polaroid knowledge-fetch`/`-validate`/`-activate` on the receiving side, `polaroid knowledge-push` from the authoring side), follow [knowledge-remotes.md](../development/knowledge-remotes.md) — it covers configuring a source, credentials, review before pushing, and what each `409` result (`behind`, `diverged`, `stale_snapshot`) means.

### Trust material

Two separate, operator-held files the daemon reads at start (never from the knowledge they authorise):

- `<knowledge-dir>-sources.json`: remotes and their trusted publishers ([knowledge-remotes.md](../development/knowledge-remotes.md)).
- `<knowledge-dir>-authorities.json`: `{"keys": [...], "revoked": [...]}`. Each key holds exactly one role — `"authority": {"level", "domain", "repository"}` to sign exception approvals, or `"attests": {"repository", "names"}` to sign context assertions for `resolve` — never both. A missing file means no exception ever applies and no assertion is ever accepted; this fails closed, not open.

## Troubleshooting

| Result | Likely cause |
| --- | --- |
| `discover`/`read` `{"empty": true}` | Nothing published or activated yet. Normal; not an error. |
| `read`/`feedback` `not_applicable` | The key's applicability is local to another repository. |
| `feedback` `not_found` | The `key`/`snapshot` pair was never actually served, or names an unpublished key. |
| `resolve` `not_found` | `goal` (or `then`) is not a record of the snapshot. |
| `resolve` `invalid_request` | A malformed or expired assertion, disagreeing assertions, context that contradicts a trusted fact, more than 16 assertions, or a `context`/`trusted` conflict. |
| `resolve` `budget_exceeded` (422) | The evaluation exceeded its time or fact-creation limit; no partial result is returned. |
| An exception shows `not_applicable` though you expected `applied` | You passed `context`, not `assertions`. Caller-asserted context never authorises a waiver — only a signed assertion from a trusted attestation key does. |
| Publish `409 stale_snapshot` | `base` is not the current active snapshot; re-run `polaroid knowledge-status` and retry. |
| Publish `400 invalid_request` naming a revision | A rule's semantics changed without its `revision` rising, or the change would unprotect/remove/retype a published rule. |
| `knowledge-*` fails with `401 unauthorized` | Missing or wrong `$POLAROID_KNOWLEDGE_TOKEN`. |

## See also

- [mcp.md](mcp.md#agent-mode-mcpagent) and [mcp.md](mcp.md#agent-mode-v2-mcpagentv2) — the full tool contract, pinned and tested.
- [http-api.md](http-api.md#git-native-knowledge-operator-api) — the operator HTTP surface, in full.
- [ADR-0044](decisions/0044-git-core.md) through [ADR-0050](decisions/0050-corpus-logic.md) — why each rule exists.
- [knowledge-remotes.md](../development/knowledge-remotes.md) — exchanging knowledge between instances.
- [AGENTS.md](../../AGENTS.md) — if you are developing Polaroid itself, not just using it.
