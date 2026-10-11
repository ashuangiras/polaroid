# HTTP API (v1)

This page is the contract of the API that `polaroidd` serves. It covers only what is implemented: procedure identity and immutable versions with subprocedure references and applicability, lexical procedure discovery and duplicate suggestions, the repository registry, repository bindings with immutable revisions, execution records, feedback reports, toil items with append-only revisions, the catalog lifecycle: execution-evidence invalidation and procedure retirement, and read-only catalog statistics. Field rules are defined in [records.md](records.md). The same operations are available to MCP clients at `/mcp`; see [mcp.md](mcp.md).

- **Base URL:** `http://127.0.0.1:7417` by default (`polaroidd -addr`).
- **Bodies:** every request and response body is UTF-8 JSON. Requests with a body must send `Content-Type: application/json` and stay under 1 MiB. Each stored field also has a [size limit](#write-limits).
- **Strict decoding:** member names are case-sensitive. Unknown members, duplicate members and trailing data are rejected. This includes server-assigned fields and fields of planned capabilities.
- **Timestamps:** RFC 3339 in UTC.

## Endpoints

| Method and path | Success | Purpose |
| --- | --- | --- |
| `GET /healthz` | `200 {"status":"ok"}` | The daemon and its database are reachable. |
| `POST /v1/procedures` | `201` history, with a `Location` header | Create a procedure and its version 1, with an optional `origin`. |
| `GET /v1/procedures[?repository=…][&scope=…][&q=…][&lifecycle=…][&limit=…&after=…][&snapshot=true]` | `200 {"procedures":[...]}` | List procedures, ordered by canonical key, optionally [filtered and paged](#discovery-filters-and-pages); retired ones only with `lifecycle`. |
| `GET /v1/procedures/{id}` | `200` history | A procedure and all of its versions, oldest first, retired or not. |
| `GET /v1/procedures/by-key/{canonical_key}` | `200` history | The same history, looked up by canonical key. |
| `GET /v1/procedures/discovery?task=…[&repository=…][&lifecycle=…][&limit=…]` | `200` discovery | Procedures whose latest version shares words with a task, [ranked and explained](#discover-procedures-for-a-task). Read-only. |
| `POST /v1/procedures/duplicates` | `200` duplicates | [Check a proposed procedure](#check-a-proposal-for-duplicates) for an existing canonical key and overlapping content, retired procedures included. Read-only: stores nothing. |
| `POST /v1/procedures/{id}/origin` | `201` history | Record where and why the procedure was first created, once. |
| `GET /v1/procedures/{id}/lifecycle/events` | `200` lifecycle | The procedure's [lifecycle](#procedure-lifecycle) state and every retirement and reactivation. |
| `POST /v1/procedures/{id}/retirement` | `201` lifecycle, with a `Location` header | Retire the procedure, against `base_event`. |
| `POST /v1/procedures/{id}/reactivation` | `201` lifecycle, with a `Location` header | Reactivate a retired procedure, against `base_event`. |
| `GET /v1/procedures/{id}/versions/{n}` | `200` version | One version. |
| `GET /v1/procedures/{id}/versions/{n}/graph` | `200` graph node | The version's composition graph, with the exact version each reference selects. |
| `GET /v1/procedures/{id}/versions/{n}/verifications[?repository=…][&commit=…][&environment=…]` | `200 {"verifications":[...]}` | The version's execution combinations and whether each is verified. Not paginated. |
| `POST /v1/procedures/{id}/versions` | `201` version, with a `Location` header | Append a version derived from `base_version`. |
| `POST /v1/bindings` | `201` binding history, with a `Location` header | Create a binding and its revision 1. |
| `GET /v1/bindings?repository={repository}[&limit=…&after=…][&snapshot=true]` | `200 {"bindings":[...]}` | List one repository's bindings, ordered by name; a registered identifier covers every identifier of its repository. |
| `GET /v1/bindings/{id}` | `200` binding history | A binding and all of its revisions, oldest first. |
| `GET /v1/bindings/{id}/revisions/{n}` | `200` binding revision | One revision. |
| `POST /v1/bindings/{id}/revisions` | `201` binding revision, with a `Location` header | Append a revision derived from `base_revision`. |
| `POST /v1/executions` | `201` execution, with a `Location` header | Record one finished run. |
| `GET /v1/executions[?procedure_id=…[&version=…]][&repository=…][&commit=…][&evidence_status=…][&limit=…&after=…]` | `200 {"executions":[...]}` | List executions, oldest first, without `inputs` and `evidence`. |
| `GET /v1/executions/{id}` | `200` execution | One execution, in full, with its evidence status. |
| `GET /v1/executions/{id}/verification` | `200` verification | Whether one execution is verified, and its combination. |
| `POST /v1/executions/{id}/invalidation` | `201` invalidation state, with a `Location` header | [Withdraw the execution as evidence](#execution-evidence-invalidation), permanently. |
| `GET /v1/executions/{id}/invalidation` | `200` invalidation state | Whether the execution is still valid evidence. |
| `POST /v1/feedback` | `201` feedback report, with a `Location` header | Report a problem or a suggestion, optionally naming its subject. |
| `GET /v1/feedback[?kind=…][&subject_type=…[&subject_id=…[&subject_version=…]]][&repository=…][&limit=…&after=…]` | `200 {"feedback":[...]}` | List feedback reports, oldest first, in full. |
| `GET /v1/feedback/{id}` | `200` feedback report | One feedback report. |
| `POST /v1/repositories` | `201` repository, with a `Location` header | Register a repository under its canonical identifier. |
| `GET /v1/repositories[?limit=…&after=…]` | `200 {"repositories":[...]}` | List registered repositories, oldest first. |
| `GET /v1/repositories/{id}` | `200` repository | One repository with its aliases. |
| `GET /v1/repositories/by-identifier/{identifier}` | `200` repository | The repository an identifier, canonical or alias, is registered to. |
| `POST /v1/repositories/{id}/aliases` | `201` repository | Register another identifier of the same repository. |
| `POST /v1/toil-items` | `201` toil item history, with a `Location` header | Create a [toil item](#toil-items) and its revision 1. |
| `GET /v1/toil-items[?status=…][&repository=…][&procedure_id=…][&limit=…&after=…][&snapshot=true]` | `200 {"toil_items":[...]}` | List toil item summaries by their latest revision, ordered by canonical key. |
| `GET /v1/toil-items/{id}` | `200` toil item history | A toil item and all of its revisions, oldest first. |
| `GET /v1/toil-items/by-key/{canonical_key}` | `200` toil item history | The same history, looked up by canonical key. |
| `GET /v1/toil-items/{id}/revisions/{n}` | `200` toil revision | One revision. |
| `POST /v1/toil-items/{id}/revisions` | `201` toil revision, with a `Location` header | Append a revision derived from `base_revision`. |
| `GET /v1/statistics` | `200` statistics | [Catalog statistics](#catalog-statistics): counts, payload sizes and storage. Read-only; takes no parameters. |

`HEAD` is accepted wherever `GET` is.

### Create a procedure

```http
POST /v1/procedures
Content-Type: application/json

{
  "canonical_key": "demo.key",
  "version": {
    "philosophy": "…",
    "method": "…",
    "contract": {"inputs": {}},
    "instructions": {"steps": ["a"]},
    "revision_reason": "Initial version."
  }
}
```

```http
HTTP/1.1 201 Created
Location: /v1/procedures/01a11de2-5b69-705a-a457-278000c106be

{"id":"01a11de2-5b69-705a-a457-278000c106be","canonical_key":"demo.key","created_at":"2026-10-08T23:38:56.233022Z","latest_version":1,
 "versions":[{"procedure_id":"01a11de2-5b69-705a-a457-278000c106be","version":1,"philosophy":"…","method":"…",
   "contract":{"inputs":{}},"instructions":{"steps":["a"]},"revision_reason":"Initial version.","created_at":"2026-10-08T23:38:56.233022Z"}]}
```

A **history** has the fields `id`, `canonical_key`, `created_at`, `latest_version`, `scope`, `lifecycle_status`, `lifecycle_event`, `goal`, `applicability` and `origin` (the last four only when present) and `versions`. A list item has the same fields without `lifecycle_event` and `versions`. `scope`, `goal` and `applicability` describe version `latest_version` only: `scope` is its applicability, `shared`, `local` or `unspecified` ([ADR-0023](decisions/0023-pagination-guarantees-and-scope-labels.md)). `lifecycle_status` is the identity's state, `active` or `retired`, and `lifecycle_event` its latest [lifecycle event](#procedure-lifecycle), omitted when there is none ([ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md)). A **version** has the fields `procedure_id`, `version`, `philosophy`, `method`, `scope` (this version's own declaration, always present), `goal` and `applicability` (only when given), `contract`, `instructions`, `references` (only when the version has references), `revision_reason` and `created_at`. `contract` and `instructions` are returned with insignificant whitespace removed. Otherwise they are exactly as submitted.

A create request may add `"origin": {"repository_id": …, "reason": …}`, and a version may add `"goal"` and `"applicability": {"shared": {}}` or `{"repository": "<repository id>"}` ([records.md](records.md#applicability-and-origin-implemented)). `POST /v1/procedures/{id}/origin` with `{"repository_id", "reason"}` records an origin later, once: a second one is `409 origin_exists`, and an unregistered repository is `400` naming `origin.repository_id`.

### References

A version may list the procedures it composes, in `version.references` on create and revise:

```json
"references": [
  {"name": "add-driver", "procedure_id": "01a11de2-5b69-705a-a457-278000c106be", "version_policy": {"pin": 2},
   "inputs": {"module": {"input": "driver_module"}, "strict": {"value": true}}}
]
```

They are returned in the same order and form, compacted. A version without references has no `references` field, so versions written before references existed are served unchanged. An unknown target, a missing pinned version, a duplicate name or a malformed `inputs` mapping is `400 invalid_request`. Each failing reference is named in `fields`, for example `version.references[0].procedure_id` or `version.references[1].inputs.module`.

A reference may add `"condition": "<text>"`, which makes it **conditional**: it applies only when the condition holds, which the agent decides and records ([ADR-0029](decisions/0029-conditional-references-and-applicability-decisions.md)). Without `condition` (or with `null`) a reference is required, as every reference before it was. A blank condition is `400` on `version.references[i].condition`. The condition is returned with the reference, and omitted for a required one; Polaroid never reads it.

```json
{"name": "integration", "procedure_id": "…", "version_policy": {"contextual": {}}, "inputs": {},
 "condition": "The change can affect behaviour the integration suite observes: code, build files, schema or interfaces."}
```

A version whose graph would contain a cycle is rejected with `409 reference_cycle`. The `cycle` field lists the path: each step is a node and the reference followed out of it, and the last step is the repeated procedure, which has no `reference`.

```json
{"error":{"code":"reference_cycle","message":"references form a cycle: A@2 -[b]-> B@1 -[a]-> A@2",
  "cycle":[{"procedure_id":"A","version":2,"reference":"b"},{"procedure_id":"B","version":1,"reference":"a"},{"procedure_id":"A","version":2}]}}
```

A graph deeper than 32 references or larger than 2048 nodes is rejected with `422 graph_too_large`. See [records.md](records.md#composition-graph-implemented).

### Composition graph

`GET /v1/procedures/{id}/versions/{n}/graph[?repository=…&environment=…[&commit=…&inputs=…[&decisions=…]]]` returns a nested tree.

- Each node has `procedure_id`, `canonical_key`, `version` (the exact version selected), `scope` (that version's own declaration), `applicability` (when declared), `retirement` (only when the procedure is retired: its latest lifecycle event, `{"event", "status", "reason", "actor", "created_at"}`), `verified_by` and `selection_evidence` (only when the node has evidence in the context), `target_verification` (only with a target) and `references`, which is empty for a leaf. A retired node is selected exactly as before, and keeps its evidence and target verification: retirement says the procedure should not be used for new work, not that its runs did not happen.
- Each reference has its stored `name` and `version_policy`, `condition` (conditional references only), then `selected_by` (`pin`, `evidence` or `latest`), `skipped_by` (only when the node's evidence recorded the reference as not applicable: that execution's ID, so the child was selected without it), `decision` (only with a target, for conditional references: `applicable`, `not_applicable` or `undecided`), its stored `inputs`, and the selected child `node`. Every reference is listed, conditional ones included, whatever evidence decided.
- Without `repository` and `environment`, pinned references select the pin and contextual references the target's latest version at the moment of the read.
- With them, the graph is resolved from evidence in that repository and environment ([records.md](records.md#contextual-resolution-implemented)). They must be given together.
- `commit` and `inputs` (a JSON object, URL-encoded) add a target ([records.md](records.md#selection-evidence-and-target-verification-implemented)). They must be given together, and only with `repository` and `environment`; otherwise each missing one is `400`. A malformed commit, or `inputs` that is not a JSON object with unique member names, is `400` naming the field.
- `decisions` (a JSON object, URL-encoded) gives the target's applicability decisions for conditional references, keyed by reference path: the names from the root joined by `/`, for example `{"integration": false, "verify/lifecycle": true}`. It needs `commit` and `inputs`. A value that is not a boolean, a malformed path, or a path that does not reach a conditional reference of the selected graph outside a reference decided not applicable, is `400` on `decisions`.
- Each parameter may appear once, and any other query parameter is `400`.
- The whole walk reads one consistent snapshot.

`selection_evidence` is `{"execution_id", "repository", "repository_id", "commit", "environment": {"name"}}`: the execution that selected the node, which may have run at any commit and with any inputs, with the identifier it was recorded under and, when registered, its repository (`repository_id`, [ADR-0022](decisions/0022-repository-identity-in-evidence.md)). `verified_by` repeats its ID, for compatibility. Neither says anything about another commit.

`target_verification` has the shape of an entry of the [verifications list](#verification): `combination` is the node's exact selected combination at the target (repository, `commit`, environment, the node's effective `inputs` in canonical form, and the selected child-version tree), `verified` is the status of its latest valid execution there, `latest_execution_id` and `execution_ids` list those executions, and `invalidated_execution_ids` (when any) the invalidated ones, which never decide. If no valid execution has run in that combination, `verified` is `false` and `latest_execution_id` is omitted; `execution_ids` is `[]` when nothing ran.

With conditional references, the expected child tree follows the target's `decisions`: a reference decided not applicable is expected as `{"reference": "…", "skipped": true}` and gets no `target_verification` below it; an applicable one is expected executed. A node with a conditional reference below it that `decisions` does not decide reports `"undecided": ["<path>", …]`, `verified: false` and `execution_ids: []`: no execution is looked up, and nothing is inferred from earlier runs ([ADR-0029](decisions/0029-conditional-references-and-applicability-decisions.md)).

```json
{"procedure_id":"…root","canonical_key":"compose.root","version":1,"verified_by":"…run","selection_evidence":{"execution_id":"…run","repository":"github.com/ashuangiras/polaroid","commit":"0123…","environment":{"name":"ci.ubuntu-latest"}},"references":[
  {"name":"old-leaf","version_policy":{"pin":1},"selected_by":"pin","inputs":{},"node":{"procedure_id":"…leaf","canonical_key":"go.dependency.add","version":1,"verified_by":"…leaf-run","selection_evidence":{…},"references":[]}},
  {"name":"mid","version_policy":{"contextual":{}},"selected_by":"evidence","inputs":{"module":{"input":"driver"}},"node":{"procedure_id":"…mid","canonical_key":"compose.mid","version":1,"verified_by":"…mid-run","selection_evidence":{…},"references":[
    {"name":"leaf","version_policy":{"contextual":{}},"selected_by":"evidence","inputs":{},"node":{"procedure_id":"…leaf","canonical_key":"go.dependency.add","version":2,"verified_by":"…leaf2-run","selection_evidence":{…},"references":[]}}]}}]}
```

A missing procedure or version is `404`. A cycle is `409 reference_cycle`. It is possible only in versions stored before cycle checking existed, or when evidence selects an older version whose references lead back. A graph over the limits is `422 graph_too_large`. No partial graph is ever returned.

### Resolve a binding

`GET /v1/bindings/{id}/resolution?environment=ci.ubuntu-latest[&commit=…&inputs=…[&decisions=…]]` resolves the binding's latest revision in the binding's repository and that environment. A pin selects its version. A contextual policy selects the highest version verified there, at any commit, or else the latest version. `commit` and `inputs` add a target, exactly as for the graph endpoint; `inputs` are the root's effective inputs, usually the revision's `inputs` plus anything the run adds.

```json
{"binding_id":"…","binding_revision":2,"repository":"github.com/ashuangiras/polaroid","environment":{"name":"ci.ubuntu-latest"},
 "version_policy":{"contextual":{}},"selected_by":"evidence","graph":{"procedure_id":"…","canonical_key":"go.dependency.add","version":2,"verified_by":"…a-run",
  "selection_evidence":{"execution_id":"…a-run","repository":"github.com/ashuangiras/polaroid","commit":"0123…(A)","environment":{"name":"ci.ubuntu-latest"}},
  "target_verification":{"combination":{"repository":"github.com/ashuangiras/polaroid","commit":"89ab…(B)","environment":{"name":"ci.ubuntu-latest"},"inputs":{"module":"modernc.org/sqlite"}},
   "verified":false,"execution_ids":[]},"references":[]}}
```

Here version 2 was selected because it succeeded at commit A, and it is not verified at commit B, the target. `environment` is required, and each parameter must appear once and be valid. Any other parameter is `400`. An unknown binding is `404`. Graph errors are as for the graph endpoint.

### Append a version

```http
POST /v1/procedures/{id}/versions
Content-Type: application/json

{"base_version": 1, "version": {"philosophy": "…", "method": "…", "contract": {…}, "instructions": {…}, "revision_reason": "Why this changed."}}
```

The response is `201`, with the new version as the body and `Location: /v1/procedures/{id}/versions/2`. If `base_version` is not the latest version, the response is `409 version_conflict` and nothing is stored:

```json
{"error":{"code":"version_conflict","message":"base version 1 is not the latest version (latest is 2)","latest_version":2}}
```

### Create a binding

```http
POST /v1/bindings
Content-Type: application/json

{
  "repository": "github.com/ashuangiras/polaroid",
  "name": "add-dependency",
  "procedure_id": "01a11de2-5b69-705a-a457-278000c106be",
  "revision": {
    "inputs": {"module": "modernc.org/sqlite"},
    "version_policy": {"pin": 2},
    "revision_reason": "Use the shared dependency procedure."
  }
}
```

```http
HTTP/1.1 201 Created
Location: /v1/bindings/01a12033-e0f1-7b6c-8f5e-3d1c2b4a5968

{"id":"01a12033-e0f1-7b6c-8f5e-3d1c2b4a5968","repository":"github.com/ashuangiras/polaroid","name":"add-dependency",
 "procedure_id":"01a11de2-5b69-705a-a457-278000c106be","created_at":"2026-10-09T14:02:11.418903Z","latest_revision":1,
 "revisions":[{"binding_id":"01a12033-e0f1-7b6c-8f5e-3d1c2b4a5968","revision":1,"inputs":{"module":"modernc.org/sqlite"},
   "version_policy":{"pin":2},"revision_reason":"Use the shared dependency procedure.","created_at":"2026-10-09T14:02:11.418903Z"}]}
```

A **binding history** has the fields `id`, `repository`, `name`, `procedure_id`, `created_at`, `latest_revision` and `revisions`. A list item has the same fields without `revisions`. A **binding revision** has the fields `binding_id`, `revision`, `inputs`, `version_policy`, `revision_reason` and `created_at`. `version_policy` is returned exactly as accepted: `{"pin":N}` or `{"contextual":{}}`. Binding reads never name a selected version; [resolving a binding](#resolve-a-binding) does.

An unknown `procedure_id` gets `404 not_found`. A pinned version the procedure does not have gets `400 invalid_request` with the field `revision.version_policy.pin`. A repository that already has a binding with that `name` gets `409 binding_exists`.

### List a repository's bindings

`GET /v1/bindings?repository=github.com/ashuangiras/polaroid`. The `repository` parameter is required, must appear once and must be a valid repository identifier. If it is registered, the list covers every identifier of its repository; each binding keeps the `repository` it was created with. `limit` and `after` [page](#discovery-filters-and-pages) the list. Any other query parameter is rejected with `400`, so a filter this server does not support is never silently ignored. A repository without bindings gets `{"bindings":[]}`.

A binding of a version local to another repository is `400` naming `procedure_id` (contextual policy) or `revision.version_policy.pin`; so is a binding revision, naming `revision.version_policy` or its `pin` ([records.md](records.md#applicability-and-origin-implemented)).

### Repositories

```http
POST /v1/repositories
Content-Type: application/json

{"identifier": "github.com/ashuangiras/polaroid", "name": "Polaroid"}
```

The response is `201` with the repository and `Location: /v1/repositories/{id}`:

```json
{"id":"01a1…","name":"Polaroid","identifier":"github.com/ashuangiras/polaroid","aliases":[],"created_at":"2026-10-09T19:00:00Z"}
```

`POST /v1/repositories/{id}/aliases` with `{"identifier", "reason"}` adds an alias and returns the repository (`201`). An identifier that is already registered, either way, is `409 repository_identifier_exists`. An alias whose bindings would give the repository two bindings of one name is `409 binding_exists`. `GET /v1/repositories/by-identifier/github.com/ashuangiras/polaroid` finds a repository by its canonical identifier or any alias; an unregistered identifier is `404`. Nothing is registered by any read.

### Discovery, filters and pages

`GET /v1/procedures` accepts optional filters, which combine:

- `repository`: only procedures whose latest version applies there: shared, unspecified, or local to that repository (by identity);
- `scope`: `shared`, `local` or `unspecified`;
- `q`: the canonical key or the latest goal contains this text, ignoring ASCII case;
- `lifecycle`: `active` (the default: retired procedures are left out), `retired` or `all` ([ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md)).

The procedure, repository, binding, execution, feedback and toil item lists take **opt-in pagination** ([ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md), [ADR-0023](decisions/0023-pagination-guarantees-and-scope-labels.md)):

- `limit` (1 to 500) returns at most that many items. If more remain, the body adds `"next"`, an opaque cursor; pass it back as `after`, with a `limit` (which may change) and **the same other parameters**, to continue. The last page has no `next`.
- Without `limit` the list is complete, as before, and has no `next`. `after` without `limit` is `400`, and so is an `after` that is not a cursor, or a cursor issued by another list or with other parameters (`400` on `after`). Cursors issued before ADR-0023 are still accepted, as positions without that check.
- **Order** is stable, with unique tie-breakers: procedures and toil items by `canonical_key` (unique); bindings by `name`, then `id`; repositories, executions and feedback by `created_at`, then `id`.
- **A page is a fresh read; the default traversal is live, not a snapshot.** A cursor means "after this item, in this order".
  - Time-ordered lists (repositories, executions, feedback): `created_at` is assigned inside the write transaction, strictly after every stored one, so a record committed while you page sorts after your cursor and a later page returns it. Continuing never skips or repeats one.
  - Key-ordered lists (procedures, bindings, toil items): a record created while you page is returned only if its key sorts after the cursor. None is repeated.
  - Filters are evaluated on each page against the state then: a procedure's latest version (for `scope`, `q` and `repository`), a toil item's latest revision (for every toil filter) and the repository registry (for every `repository` filter). A record whose membership changes between pages may be missed, or returned late. Item contents, such as `latest_version`, are as of the page that returned them.
  - To see what a traversal could not return, start again without `after`.
- **Snapshot traversal** (`snapshot=true`, with `limit`, on `GET /v1/procedures`, `GET /v1/bindings` and `GET /v1/toil-items`): the first page fixes a boundary, carried by its cursors, and every page reads only records committed before it.
  - **Membership and contents are fixed as of the first page.** For procedures: which exist, each one's latest version (so `latest_version`, `scope`, `goal`, `applicability`), its origin, its lifecycle state, and repository identity for `repository`. For bindings: which exist, `latest_revision`, and the identity for `repository`. For toil items: which exist, each one's latest revision (so `latest_revision`, `status`, `activity`, `scope`, `applicability`, `updated_at` and the sources `procedure_id` matches), and the identity for `repository`.
  - **Every record within the boundary is returned exactly once**, wherever its key sorts. Records committed later are excluded: start a new snapshot to see them.
  - **Nothing else is a snapshot:** reading a record by ID, or any other list, returns current state. `snapshot` must be given on every page; it is `400` without `limit`, with `true` or `false` as its only values, and an unknown parameter on the other lists.
- A filter given but empty is `400`.

### Discover procedures for a task

`GET /v1/procedures/discovery?task=publish+a+prerelease+build&repository=github.com/ashuangiras/polaroid&limit=1` returns procedures that share words with the task, best first ([ADR-0032](decisions/0032-lexical-discovery-and-duplicate-suggestions.md), ranked as in [ADR-0033](decisions/0033-discovery-ranking-rarity-purpose-detail.md)). Matching is **lexical**: it compares words, not meaning.

```json
{"terms":["publish","prerelease","build"],"repository":"github.com/ashuangiras/polaroid","considered":16,
 "rarity":[{"term":"publish","procedures":9,"rarity":1.02},{"term":"prerelease","procedures":1,"rarity":2.83},{"term":"build","procedures":12,"rarity":0.85}],
 "detail_cap":5.67,"matched":14,
 "candidates":[{"procedure_id":"…","canonical_key":"polaroid.release.publish","version":2,"scope":"local","applicability":{"repository":"…"},
   "goal":"…","method":"…","matched_terms":3,"score":17.11,"detail_points":0,
   "contributions":[{"term":"publish","field":"canonical_key","points":4.09},{"term":"prerelease","field":"goal","points":11.33},{"term":"build","field":"method","points":1.69}],
   "matches":[{"field":"canonical_key","terms":["publish"]},{"field":"goal","terms":["publish","prerelease"]},{"field":"method","terms":["publish","build"]}, …]}]}
```

- **Terms.** The task is lowercased and split at every character that is not a letter or digit. Words shorter than two characters and common English function words (`a`, `an`, `and`, `are`, `as`, `at`, `be`, `but`, `by`, `can`, `do`, `does`, `for`, `from`, `has`, `have`, `if`, `in`, `into`, `is`, `it`, `its`, `may`, `must`, `no`, `not`, `of`, `on`, `or`, `our`, `should`, `so`, `than`, `that`, `the`, `their`, `then`, `there`, `these`, `this`, `those`, `to`, `was`, `we`, `were`, `when`, `where`, `which`, `while`, `who`, `will`, `with`, `without`, `you`, `your`) are dropped. Each word loses at most one suffix, the first of `ies`/`ied` (→ `y`), `ing`, `ed`, `es`, `s` (not after `s`) and `e` that leaves at least three characters, so `adding` meets `add` and `dependencies` meets `dependency`. `terms` lists the task's distinct terms, each as the first word that produced it; explanations use the same words.
- **What is searched.** The **latest version** of each procedure, as stored when the request is read: the canonical key (split at its separators), `goal`, `method`, `philosophy`, and every string value in `contract` and `instructions`. Member names, numbers, IDs, timestamps, `revision_reason`, references, origins, bindings and executions are never searched. A procedure that others reference is a candidate like any other.
- **Candidates.** With `repository` (registered or not), only procedures whose latest version applies there: shared, unspecified, or local to that repository, matched by identity; procedures local to other repositories are left out. Without it, every procedure in the catalog is a candidate, local ones included, each labelled with its `scope` and `applicability`. `lifecycle` is `active` (the default), `retired` or `all`: retired procedures are candidates only when it asks for them ([ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md)).
- **Score and order** ([ADR-0033](decisions/0033-discovery-ranking-rarity-purpose-detail.md)). The corpus is the candidates above, `considered` of them: a repository-scoped request uses only procedures that apply there. Each task term's `rarity` is ln(1 + `considered` ÷ `procedures`), where `procedures` is how many candidates contain it, so words common to the catalog count less. Fields weigh 4 (`canonical_key`, `goal`: the stated purpose), 2 (`method`) or 1 (`philosophy`, `contract`, `instructions`); a `goal`, `method` or `philosophy` longer than that field's average among the candidates weighs less (divided by max(1, 0.25 + 0.75 × its distinct terms ÷ the average)). A matched term earns its rarity times the best such weight among the fields containing it, credited to that field in `contributions`. Terms credited to `contract` or `instructions` add together at most `detail_cap`, twice the task's highest rarity; their uncapped total is `detail_points`. `score` is the other terms' points plus min(`detail_points`, `detail_cap`), rounded to two decimals. Candidates with at least one term are ranked by `score`, then `matched_terms`, both descending, then by `canonical_key`. `matches` lists, per field in the order above, every task term found there. Terms are distinct: repeating a word, in a procedure or in the task, changes nothing. A score ranks the candidates of one task over one corpus; it is not a confidence, and it changes when the catalog changes.
- **Bounds.** `limit` is 1 to 50 (default 10). `matched` counts every procedure that matched, before the limit. There is no cursor: reword the task to see others.
- **Each candidate** gives `procedure_id`, `canonical_key`, `version` (the latest version, the one searched), `scope`, `lifecycle_status`, `applicability` (omitted when unspecified), `goal` (omitted when absent) and `method`. Fetch the version for its contract and instructions. A candidate is not a recommendation and says nothing about verification: resolution may select another version from evidence, and nothing is verified for your target until you run it.
- **Errors (`400`):** a missing or blank `task`, one longer than 4096 bytes or without searchable words (only short words or function words), an invalid or empty `repository`, a `lifecycle` other than `active`, `retired` or `all`, a `limit` outside 1 to 50, a repeated or unknown parameter. A task whose words match nothing returns `200` with `"matched":0,"candidates":[]`.

### Check a proposal for duplicates

```http
POST /v1/procedures/duplicates
Content-Type: application/json

{"canonical_key": "golang.compile", "goal": "Compile a Go module with its declared toolchain.",
 "method": "Check the toolchain against go.mod and run the build command from the module root.",
 "philosophy": "A build is evidence only with the declared toolchain.",
 "repository": "github.com/ashuangiras/polaroid", "exclude_procedure_id": "…", "limit": 5}
```

`method` and `philosophy` are required, as in a version; `goal`, `canonical_key`, `repository`, `exclude_procedure_id` (the procedure you are revising) and `limit` (1 to 50, default 10; `0` means the default) are optional. Nothing is stored, merged, refused, revised or bound. The response is `200`:

```json
{"proposal_terms":31,"repository":"github.com/ashuangiras/polaroid",
 "key_collision":{"procedure_id":"…","canonical_key":"golang.compile","latest_version":1},
 "matched":1,
 "suggestions":[{"procedure_id":"…","canonical_key":"go.module.build","version":3,"scope":"shared","applicability":{"shared":{}},
   "goal":"…","method":"…","similarity":0.52,"shared_terms":17,
   "matches":[{"field":"canonical_key","terms":["go","module","build"]},{"field":"goal","terms":[…]}, …]}]}
```

- **`key_collision`** (omitted when none) is the procedure that already has the proposed `canonical_key`, in any repository, with its `lifecycle_status`. Creating the proposal would be `409 canonical_key_exists`, also when that procedure is retired: reactivate and revise it instead. When it is the excluded procedure, it is not reported: a revision keeps its key.
- **Retired procedures are always compared**, and each suggestion has `lifecycle_status`: retiring knowledge must not invite a second identity for it ([ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md)).
- **`suggestions`** are advisory content overlaps. The proposal's terms (as for discovery) from `canonical_key`, `goal`, `method` and `philosophy` are compared with the same fields of each candidate's latest version; contract and instructions are not compared. `similarity` is 2 × `shared_terms` ÷ (`proposal_terms` + the candidate's terms), rounded to two decimals, and a candidate is suggested when it is at least 0.35 (compared exactly). Suggestions are ranked by similarity, then `shared_terms`, then `canonical_key`; `matches` lists, per field of the candidate, the proposal's terms found there. `matched` counts every suggestion before the limit.
- **Candidates** follow discovery's `repository` rule, so a shared procedure is suggested in any repository as an alternative to a local copy. The excluded procedure is never a candidate.
- **Errors (`400`):** a blank `method` or `philosophy`, a multi-line or blank `goal`, an invalid `canonical_key` or `repository`, a proposal without searchable words (on `method`), a `limit` outside 1 to 50, an `exclude_procedure_id` that names no procedure, and any unknown member, such as `contract`.

### Append a binding revision

```http
POST /v1/bindings/{id}/revisions
Content-Type: application/json

{"base_revision": 1, "revision": {"inputs": {…}, "version_policy": {"contextual": {}}, "revision_reason": "Why this changed."}}
```

The response is `201`, with the new revision as the body and `Location: /v1/bindings/{id}/revisions/2`. If `base_revision` is not the latest revision, the response is `409 revision_conflict` and nothing is stored:

```json
{"error":{"code":"revision_conflict","message":"base revision 1 is not the latest revision (latest is 2)","latest_revision":2}}
```

### Record an execution

```http
POST /v1/executions
Content-Type: application/json

{
  "procedure_id": "01a11de2-5b69-705a-a457-278000c106be", "version": 2,
  "binding_id": "01a12033-e0f1-7b6c-8f5e-3d1c2b4a5968", "binding_revision": 1,
  "repository": "github.com/ashuangiras/polaroid",
  "commit": "0123456789abcdef0123456789abcdef01234567",
  "environment": {"name": "ci.ubuntu-latest", "attributes": {"os": "linux", "go": "1.27.2"}},
  "inputs": {"module": "modernc.org/sqlite"},
  "outcome": "succeeded",
  "evidence": {"commands": [{"run": "make ci", "exit": 0}]}
}
```

- The response is `201`, with the execution as the body and `Location: /v1/executions/{id}`. The body is the request plus `id`, `created_at` and `evidence_status` (`valid`; after an [invalidation](#execution-evidence-invalidation), `invalidated` with `invalidation`). `binding_id` and `binding_revision` are absent when no binding was given.
- `GET /v1/executions/{id}` returns the same bytes.
- An unknown procedure or binding is `404`.
- These are `400`, each naming its field:
  - a missing version or binding revision;
  - a binding of another procedure or repository;
  - a version other than the binding revision's pin;
  - an abbreviated commit;
  - empty `evidence`.

A parent execution adds `"children": [{"reference": "pinned-child", "execution_id": "…"}, …]`, listing child executions recorded earlier. They are returned in the same order, and the field is absent when there are none. A link to an unknown reference or execution is `400` naming `children[i].reference` or `children[i].execution_id`. So is a child that ran another procedure, a version other than the pin, another repository or commit, or a child already linked to another parent. So is a child whose `inputs` differ from those the reference maps from the parent's `inputs`, compared canonically; the message gives both ([ADR-0024](decisions/0024-child-inputs-follow-the-reference-mapping.md)), for example `ran with inputs {"service":"b"}, but reference "build" maps the parent's inputs to {"service":"a"} (ADR-0024)`. See [records.md](records.md#subprocedure-execution-implemented).

An execution of a version with conditional references adds `"decisions"`, one per conditional reference, returned in the same order and absent when there are none ([ADR-0029](decisions/0029-conditional-references-and-applicability-decisions.md)):

```json
"decisions": [{"reference": "integration", "applicable": false,
                "rationale": "Only docs/guide.md changed (git diff --name-only); the integration suite reads no Markdown.",
                "evidence": {"changed_files": ["docs/guide.md"]}}]
```

- `reference` names a conditional reference; `applicable` (boolean) and `rationale` (not blank) are required; `evidence` is an optional JSON object, stored compacted and never interpreted.
- These are `400`, naming `decisions[i].reference` or `decisions[i].applicable`: a reference the version does not have; a required reference, which cannot be skipped; a second decision for the same reference; and `applicable: false` while `children` links a child for that reference. A missing `applicable` or a blank `rationale` is `400` on that field, and an unknown member is `400`.
- A missing decision, or an applicable one without a linked child, is accepted, so failed and partial runs stay recordable; such an execution is never verified.
- Polaroid checks the decision's structure. Whether the rationale is true is the recording agent's claim.

### List executions

`GET /v1/executions` lists executions, oldest first, optionally filtered by `procedure_id`, `version` (which needs `procedure_id`), `repository` (by identity: every identifier of a registered repository), `commit` and `evidence_status` (`valid` or `invalidated`), and paged with `limit` and `after`. Without filters it lists every execution; `procedure_id` used to be required, and still works as before.

- Each parameter may appear once.
- Any other parameter is rejected with `400`.
- List items have every execution field except `inputs`, `evidence`, `children`, `decisions` and `invalidation`; `evidence_status` is included.

### Verification

Verification is derived from stored executions on every read; nothing is stored ([ADR-0012](decisions/0012-derived-verification.md), [records.md](records.md#verification-implemented)). Both endpoints are read-only and read one consistent snapshot.

`GET /v1/executions/{id}/verification` judges one execution:

```json
{"execution_id":"…parent","procedure_id":"…","version":1,"verified":false,
 "problems":[{"code":"missing_child","reference":"pinned-child"},{"code":"child_not_verified","reference":"latest-child","execution_id":"…child"}],
 "combination":{"repository":"github.com/ashuangiras/polaroid","commit":"0123456789abcdef0123456789abcdef01234567",
   "environment":{"name":"ci.ubuntu-latest"},"inputs":{"module":"modernc.org/sqlite"},
   "children":[{"reference":"latest-child","version":3}]}}
```

- `verified` is true when the execution is valid evidence (not [invalidated](#execution-evidence-invalidation)), succeeded, and every reference of its version has a linked child that ran with the inputs the reference maps and is itself verified, except a conditional reference the execution decided not applicable; every conditional reference needs a decision. `evidence_status` is the execution's own, `valid` or `invalidated`.
- `problems` is absent when `verified` is true. Otherwise it lists the direct reasons in this order: `invalidated` (the execution itself; any other problem listed with it is structural, so `invalidated` alone means it satisfied its contract as recorded), `outcome_failed`, then per reference in version order, either `missing_decision` (a conditional reference without a decision, with `reference`), `missing_child` (with `reference`), or `child_inputs_mismatch` and either `child_invalidated` (the child is invalidated) or `child_not_verified`, each with `reference` and the child's `execution_id`, when they apply. `child_inputs_mismatch` reports a stored link whose child ran with other inputs than the reference maps; recording one is refused now, but links stored before the rule, or with direct SQL, are served as stored. A child's own problems are read from the child.
- `combination` has `repository`, `commit`, `environment.name`, the canonical `inputs`, and `children`. `children` lists each linked child's `reference`, `version` and own `children`, and each conditional reference decided not applicable as `{"reference": "…", "skipped": true}`, in the version's reference order, and is absent when there are none. A rationale's wording is not part of the combination.
- An unknown execution is `404`.

`GET /v1/procedures/{id}/versions/{n}/verifications` lists the version's combinations, ordered by their first execution:

```json
{"verifications":[{"combination":{…},"verified":true,"latest_execution_id":"…c","execution_ids":["…a","…b","…c"]}]}
```

- `verified` is the verification of `latest_execution_id`, the newest valid execution in the combination. `execution_ids` lists all of them, oldest first; `invalidated_execution_ids` (omitted when none) lists those that are invalidated, which never decide. When every execution of a combination is invalidated, `verified` is `false` and `latest_execution_id` is omitted.
- A combination's `repository` is the repository identity: the canonical identifier of a registered repository, with its `repository_id`, or else the unregistered identifier. Executions recorded under any identifier of a registered repository share its combinations ([ADR-0022](decisions/0022-repository-identity-in-evidence.md)).
- `repository` (matched by identity), `commit` (full hash) and `environment` (name) optionally filter the executions. Each may appear once, must be valid, and any other parameter is rejected with `400`.
- An unknown procedure or version is `404`. A version without executions gets `{"verifications":[]}`.

### Feedback

```http
POST /v1/feedback
Content-Type: application/json

{
  "kind": "problem",
  "summary": "record_execution rejected a short commit without saying how long it must be",
  "details": "I passed a 7-character hash.\nThe error named the field but not the rule.",
  "reporter": "copilot.vscode",
  "context": {"tool": "record_execution"}
}
```

- The response is `201`, with the report as the body and `Location: /v1/feedback/{id}`. The body is the request plus `id` and `created_at`, with `context` compacted, or `{}` when it was absent.
- `GET /v1/feedback/{id}` returns the same bytes. An unknown report is `404`.
- An unknown `kind`, a blank or multi-line `summary`, blank `details`, an invalid `reporter` and a `context` that is not an object are each `400`, naming the field.
- `GET /v1/feedback` lists every report, oldest first, with every field. `kind=problem` or `kind=suggestion` filters the list. `kind` may appear once and must be valid. Any other parameter is `400`. Without reports, the body is `{"feedback":[]}`.

A report may also name its `subject`, the `repository` it was made in, and a related `execution_id` ([records.md](records.md#feedback-report-implemented)):

```json
{"kind": "problem", "summary": "…", "details": "…", "reporter": "copilot.vscode",
 "subject": {"type": "procedure", "procedure_id": "…", "version": 2}, "repository": "github.com/ashuangiras/polaroid", "execution_id": "…"}
```

An unknown subject record or execution, a subject member of another type, and a repository or execution that disagrees with the subject are `400` naming the field. The list adds the filters `subject_type`, `subject_id` (needs `subject_type`), `subject_version` (needs a procedure or binding `subject_id`) and `repository` (the report's repository, or a repository subject, by identity), plus `limit` and `after`. `subject_type=service` includes reports without a subject. For example, reports about version 2 of a procedure in one repository: `GET /v1/feedback?subject_type=procedure&subject_id=…&subject_version=2&repository=github.com/ashuangiras/polaroid`.

Reports are never changed or deleted, and Polaroid tracks no triage state ([records.md](records.md#feedback-report-implemented)).

### Toil items

A toil item is an agent's versioned assessment of repetitive work, with a proposal and an assessment lifecycle ([ADR-0034](decisions/0034-toil-items.md), field rules in [records.md](records.md#toil-item-implemented)). Its status is never execution verification: Polaroid checks structure and links, and derives nothing.

```http
POST /v1/toil-items
Content-Type: application/json

{"canonical_key": "polaroid.change-classification.manual",
 "revision": {
   "activity": "Classify the files a change touches by hand",
   "applicability": {"repository": "01a12236-…"},
   "sources": [{"procedure_id": "…", "version": 3, "steps": ["files"]}],
   "observations": [{"execution_id": "…", "note": "#81: the changed files classified by hand."}],
   "assessment": {"repetition": {"status": "observed", "evidence": "…"}, "burden": {"status": "hypothesis", "note": "…"}},
   "proposal": {"inputs": "…", "outputs": "…", "remaining_judgment": "…", "failure_and_recovery": "…", "acceptance_criteria": ["…"]},
   "status": "candidate", "status_rationale": "A proposal awaits the owner's decision.",
   "revision_reason": "Promoted from #85's extraction evidence.", "author": "copilot.vscode"}}
```

- The response is `201` with the history and `Location: /v1/toil-items/{id}`. The history is the item's summary (`id`, `canonical_key`, `created_at`, `latest_revision`, and the latest revision's `activity`, `status`, `scope`, `applicability`, plus `updated_at`, its `created_at`) and `revisions`, oldest first. `GET /v1/toil-items/{id}` and `GET /v1/toil-items/by-key/{key}` return the same document.
- A revision document is the revision's content plus `toil_item_id`, `revision`, `scope` and `created_at`. `observations` is always present (`[]` when none); `proposal`, `replacement` and `validation` are omitted when absent, and a stored `validation` always lists `executions` and `references`. `steps` and a reference's `note` are omitted when absent.
- `POST /v1/toil-items/{id}/revisions` with `{"base_revision": 1, "revision": {…}}` appends a complete revision: `201`, `Location: /v1/toil-items/{id}/revisions/2`. A stale base is `409 revision_conflict` with `latest_revision`, and nothing is stored. A status that may not follow the base revision's is `400` on `revision.status`, naming the allowed ones.
- Revision 1 is `observed` or `candidate`. Each status requires and allows: `candidate` a `proposal`; `implemented` a `proposal` and a `replacement` (`summary` and at least one `references` entry `{uri, note?}`); `validated` those and a `validation` (`rationale`, and `executions` `{execution_id, note}`, `references`, or both); `observed` and `retired` neither require them, and only `retired` allows a `replacement` or `validation`; a field a status does not allow is `400`. The allowed transitions are in [records.md](records.md#toil-item-implemented).
- `400`, naming the field: an invalid `canonical_key`; a blank or multi-line `activity`; a missing `applicability` (it is required) or an unregistered repository in it; no `sources`, an unknown procedure or version, or a duplicate; an `observations` or `validation.executions` entry naming an unknown execution, or one cited twice; `assessment` without `repetition` and `burden` objects whose `status` is `observed`, `hypothesis` or `unknown`; a `proposal` missing a member or with empty `acceptance_criteria`; a blank `status_rationale` or `revision_reason`; an invalid `author`. For a local item, a source version not applicable in its repository and an execution that ran elsewhere (by identity) are `400`.
- References and step IDs are declared: they are stored and returned, and never fetched, opened, executed or checked against instructions. Failed executions are valid observations.
- `GET /v1/toil-items` lists summaries. Its filters apply to each item's latest revision: `status`; `repository`, an identifier, for shared items and items local to the repository it belongs to (aliases included; an unregistered identifier sees shared items only); `procedure_id`, items whose sources cite that procedure at any version (an unknown ID matches nothing). With `limit`, `after` and `snapshot` as [above](#discovery-filters-and-pages). An invalid `status` or `repository` is `400`.
- An unknown item, key or revision is `404`.

### Execution-evidence invalidation

An execution that did not do what its version claims, for example one recorded against the wrong version, is withdrawn as evidence without changing it ([ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md), [records.md](records.md#execution-evidence-invalidation-implemented)).

```http
POST /v1/executions/{id}/invalidation
Content-Type: application/json

{"reason": "Recorded against extract v2, but it only promoted an assessment recorded earlier by v1.", "actor": "copilot.vscode"}
```

```http
HTTP/1.1 201 Created
Location: /v1/executions/{id}/invalidation

{"execution_id":"…","evidence_status":"invalidated","invalidation":{"reason":"Recorded against extract v2, …","actor":"copilot.vscode","created_at":"2026-10-11T13:00:00Z"}}
```

- `reason` (not blank) and `actor` (canonical-key format, as a feedback `reporter`) are required; each is `400` otherwise. An unknown execution is `404`.
- It is **irreversible**: a second invalidation, also a concurrent one, is `409 execution_invalidated`, and nothing changes. Replace the evidence by recording a new, genuine execution.
- `GET /v1/executions/{id}/invalidation` returns the same document, or `{"execution_id","evidence_status":"valid"}` without `invalidation`. The execution itself adds `evidence_status` and `invalidation`; every recorded field reads back unchanged.
- Effects: the execution is never verified (`invalidated`); a parent linking it reports `child_invalidated` and is not verified, nor are its ancestors, and no other child is substituted; combination statuses and target verification are decided by the latest **valid** execution; contextual selection uses only valid executions, so it falls back to the previous valid run of the version, another version, or no evidence. A parent run linking an invalidated child stays valid and unverified, so it withdraws its version in the context rather than bringing older runs back; invalidate it too to use an older run. Nothing else changes: not the version, not unrelated executions.

### Procedure lifecycle

A procedure identity is `active` until it is retired ([ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md), [records.md](records.md#procedure-lifecycle-implemented)).

```http
POST /v1/procedures/{id}/retirement
Content-Type: application/json

{"base_event": 0, "reason": "Superseded by go.module.add, which covers private modules.", "actor": "copilot.vscode"}
```

```http
HTTP/1.1 201 Created
Location: /v1/procedures/{id}/lifecycle/events

{"procedure_id":"…","status":"retired","latest_event":1,"events":[{"event":1,"status":"retired","reason":"Superseded by …","actor":"copilot.vscode","created_at":"2026-10-11T13:00:00Z"}]}
```

- `POST /v1/procedures/{id}/reactivation` takes the same body. `base_event` is the latest event you read (`lifecycle_event` of the procedure, `0` without one); a stale one is `409 lifecycle_conflict` with `latest_event`, and of concurrent changes from one base exactly one is stored. Retiring a retired procedure, or reactivating an active one, is `409 lifecycle_unchanged`. A negative `base_event`, a blank `reason` or an invalid `actor` is `400`; an unknown procedure is `404`.
- `GET /v1/procedures/{id}/lifecycle/events` returns the state, `latest_event` and every event, oldest first (`"events":[]` for a procedure never retired). Events are never changed or deleted.
- **While retired:** the procedure list and discovery leave it out unless `lifecycle` is `retired` or `all`; reads by ID, key and version, graphs, resolution, bindings, executions and verification keep working, and graph nodes of it carry `retirement`. These writes are `409 procedure_retired`, whose message says to reactivate it first: a new version of it; a new version (of any procedure) with a reference to it, pinned or contextual, naming each `version.references[i].procedure_id` in `fields`; a binding of it (`fields`: `procedure_id`); a binding revision of a binding of it. Recording executions, origins, feedback and toil items that cite it is still accepted. Retirement never changes a parent, a reference or a binding, and never cascades.

### Catalog statistics

`GET /v1/statistics` summarizes the whole catalog ([ADR-0036](decisions/0036-read-only-catalog-statistics.md)). It writes nothing, takes no query parameter (any is `400 invalid_request`), and returns no record content, ID or filesystem path. A real response from the synthetic catalog of the HTTP tests (two repositories, five procedures, nine executions), with eight payload categories left out where marked:

```json
{"statistics_version":1,"generated_at":"2026-10-11T14:50:55.299568Z",
 "records":{"procedures":5,"procedure_versions":6,"procedure_references":3,"procedure_origins":1,"procedure_lifecycle_events":3,
   "repositories":2,"repository_aliases":1,"bindings":1,"binding_revisions":2,"executions":9,"execution_children":4,
   "execution_decisions":2,"execution_invalidations":5,"feedback":1,"toil_items":1,"toil_revisions":2,"toil_revision_sources":2,
   "toil_revision_executions":2},
 "procedures":{"by_lifecycle":{"active":4,"retired":1},"by_scope":{"shared":1,"local":1,"unspecified":3}},
 "executions":{"by_outcome":{"succeeded":8,"failed":1},"by_evidence_status":{"valid":4,"invalidated":5},
   "invalidated_by_outcome":{"succeeded":4,"failed":1},"valid_with_invalidated_child":1},
 "toil_items":{"by_status":{"observed":0,"candidate":1,"implemented":0,"validated":0,"retired":0}},
 "payloads":{"procedure_contracts":{"count":6,"total_bytes":57,"mean_bytes":9,"max_bytes":47},
   "execution_evidence":{"count":9,"total_bytes":105,"mean_bytes":11,"max_bytes":25},
   "toil_proposals":{"count":1,"total_bytes":273,"mean_bytes":273,"max_bytes":273}, …},
 "storage":{"schema_version":10,"page_size":4096,"page_count":70,"freelist_count":0,"database_bytes":286720,
   "files":{"database":{"present":true,"bytes":4096},"wal":{"present":true,"bytes":947632},"shm":{"present":true,"bytes":32768}}}}
```

Here every page is still in the WAL, so the database file is one page while `database_bytes` is 70 pages.

- **`records`** counts every stored record kind: identities (`procedures`, `repositories`, `bindings`, `executions`, `feedback`, `toil_items`), their history (`procedure_versions`, `procedure_lifecycle_events`, `binding_revisions`, `toil_revisions`) and relationship records (`procedure_references`, `procedure_origins`, `repository_aliases`, `execution_children`, `execution_decisions`, `execution_invalidations`, `toil_revision_sources`, `toil_revision_executions`).
- **`procedures`**: `by_lifecycle` from each procedure's latest lifecycle event, `by_scope` from its latest version. Each adds up to `records.procedures`.
- **`executions`**: `by_outcome` is the recorded outcome of every execution, invalidated ones included; it is not verification. `by_evidence_status` is `valid` or `invalidated`; `invalidated_by_outcome` splits the invalidated ones by outcome. `valid_with_invalidated_child` counts valid executions that directly link at least one invalidated child (they report `child_invalidated`); their ancestors are not counted. Verified executions are not counted: that needs every execution tree walked.
- **`toil_items`**: `by_status` of each item's latest revision.
- **`payloads`**: for `procedure_contracts`, `procedure_instructions`, `reference_inputs`, `binding_revision_inputs`, `execution_inputs`, `execution_evidence`, `execution_environment_attributes`, `decision_evidence`, `feedback_contexts`, `toil_assessments` and `toil_proposals`, over every version and revision: `count` of stored values, `total_bytes`, `mean_bytes` (rounded down, 0 without values) and `max_bytes`. A size is the UTF-8 length of the value's stored JSON text (compact, escapes as received); a response writes an escaped character as itself, so it can be shorter. `{}` counts 2 bytes; an absent optional value (`proposal`, a decision's `evidence`) is not counted.
- **`storage`**: `schema_version`, `page_size`, `page_count`, `freelist_count` and `database_bytes` (page size × page count) describe the database as of the same snapshot. `files` are the sizes of the database file and SQLite's `wal` and `shm` files, each `{"present":true,"bytes":N}` or `{"present":false}`.
- **Consistency:** everything except `storage.files` comes from one SQL statement, so one committed state; `generated_at` is taken just before it. `files` are read just after it and may disagree: a commit grows the WAL, a checkpoint moves it into the database file, and SQLite deletes `wal` and `shm` when its last connection closes. A file that does not exist is `present: false`; another failure to read a file's size is `500 internal`.
- Members may be added within `statistics_version` 1; removing one or changing its meaning increments it.
- **`write_limits`** (since [#98](https://github.com/ashuangiras/polaroid/issues/98)) is the policy that validation enforces, read from the same table: `policy_version`, `write_max_bytes`, `request_max_bytes` and `fields`, each `{"record","field","kind","max_bytes"}` ([write limits](#write-limits)).

### Write limits

Every persisted, user-controlled text and JSON field has a byte limit, and so has each write as a whole ([ADR-0037](decisions/0037-size-limits-on-new-writes.md)). The limits are fixed, the same on every transport, and reported by `GET /v1/statistics` in `write_limits`.

| Tier | Bytes | Fields |
| --- | --- | --- |
| document | 65,536 | `version.contract`, `version.instructions`; execution `evidence`; toil `revision.assessment`, `revision.proposal` |
| object | 16,384 | `version.references[].inputs`; binding `revision.inputs`; execution `inputs`, `environment.attributes`, `decisions[].evidence`; feedback `context` |
| text | 8,192 | `version.philosophy`, `version.method`, `version.revision_reason`, `version.references[].condition`, `origin.reason`; binding `revision.revision_reason`; `decisions[].rationale`; invalidation, lifecycle and alias `reason`; feedback `details`; toil `revision.status_rationale`, `revision.revision_reason`, `revision.replacement.summary`, `revision.validation.rationale`, and every `note` |
| line | 2,048 | `version.goal`; feedback `summary`; repository `name`; toil `revision.activity`, `revision.sources[].steps[]`, each `uri` |
| whole write | 262,144 | the sum of all bounded fields of one request |

- **Measurement:** JSON is measured after the compaction used for storage (whitespace removed, member order and string escapes as sent, so `"\u00e9"` counts 6 bytes and `"é"` 2); text is measured in UTF-8 bytes as stored. Exactly the limit is accepted; one byte more is refused. Absent optional values are not counted; feedback without `context` stores and counts `{}`.
- **Order:** the 1 MiB body limit applies first, to the raw bytes (`request_too_large`); then the usual validation (`invalid_request`); then the limits. Conflicts, existence and retirement are checked after them.
- **Error:** `413 value_too_large` lists every value over its limit; field `*` is the whole write. The message names fields and sizes, never content. Nothing is stored.

```json
{"error":{"code":"value_too_large","message":"value too large: evidence is 70010 bytes, at most 65536",
  "limits":[{"field":"evidence","bytes":70010,"max_bytes":65536}]}}
```

```json
{"error":{"code":"value_too_large","message":"value too large: the whole write is 266502 bytes, at most 262144",
  "limits":[{"field":"*","bytes":266502,"max_bytes":262144}]}}
```

- **Stored records are never checked.** Records written before the limits, of any size, read, list, verify, back up and restore as before. A new write is always checked, also when it repeats a stored value: revise an oversized procedure by shortening the value. A retry of a write accepted before the limits that is over them now gets `value_too_large` instead of its conflict; nothing is stored either way.
- **For agents:** record what a run showed (results, short excerpts) and point to the rest (commits, URLs, log locations). Never paste whole logs. The limits bound one write, not the catalog's growth.

## Errors

Every error is a JSON object of this form:

```json
{"error": {"code": "…", "message": "…", "fields": [{"field": "…", "message": "…"}], "latest_version": 2, "latest_revision": 2}}
```

`fields` appears only with `invalid_request` errors that come from field validation, and with `procedure_retired`. `limits` appears only with `value_too_large`. `latest_version` appears only with `version_conflict`, `latest_revision` only with `revision_conflict`, `latest_event` only with `lifecycle_conflict`, and `cycle` only with `reference_cycle`. Messages are for humans and may change; branch on `code`.

| Status | `code` | When |
| --- | --- | --- |
| 400 | `invalid_request` | Malformed JSON, unknown or duplicate members, wrong JSON types, or a field that fails validation (listed in `fields`). Also a version or revision path segment that is not a positive integer, a pinned version the procedure does not have, a reference to an unknown procedure, an execution that does not match its version or binding, and a missing, repeated, invalid or unknown query parameter when listing bindings, executions, verifications, feedback or toil items. Also a toil revision whose status may not follow its base revision's. |
| 403 | `forbidden` | The `Host` header does not name a loopback address while the daemon listens on loopback, or a browser sent an unsafe cross-origin request. |
| 404 | `not_found` | Unknown procedure (also as a binding's or execution's `procedure_id`), canonical key, version, binding (also as an execution's `binding_id`), binding revision, execution, feedback report, toil item, toil item key or toil revision, or endpoint. |
| 405 | `method_not_allowed` | The endpoint exists, but not for this method. The `Allow` header lists the methods it accepts. |
| 409 | `canonical_key_exists` | Another procedure already uses the canonical key, or, when creating a toil item, another toil item does. The two namespaces are separate. |
| 409 | `version_conflict` | `base_version` is not the latest version. `latest_version` is included. |
| 409 | `binding_exists` | The repository already has a binding with this `name`, under this or another of its identifiers, or an alias would give it two. |
| 409 | `repository_identifier_exists` | The identifier is already registered, as a canonical identifier or an alias. |
| 409 | `origin_exists` | The procedure's origin is already recorded. |
| 409 | `revision_conflict` | `base_revision` is not the latest revision of the binding or toil item. `latest_revision` is included. |
| 409 | `reference_cycle` | The version's references would form a cycle, or a stored graph contains one. `cycle` is included. |
| 409 | `execution_invalidated` | The execution is already invalidated; invalidation is irreversible. |
| 409 | `lifecycle_conflict` | `base_event` is not the procedure's latest lifecycle event. `latest_event` is included. |
| 409 | `lifecycle_unchanged` | Retiring a retired procedure, or reactivating an active one. |
| 409 | `procedure_retired` | A new version of a retired procedure, a new version referencing one (`fields` names each reference), or a new binding or binding revision of one. Reactivate it first. |
| 413 | `request_too_large` | The body exceeds 1 MiB. |
| 413 | `value_too_large` | A stored field, or the whole write, exceeds its [write limit](#write-limits). `limits` lists each one: `{"field","bytes","max_bytes"}`. |
| 415 | `unsupported_media_type` | A request with a body that is not `application/json`. |
| 422 | `graph_too_large` | A composition graph, written or read, is deeper than 32 references or has more than 2048 nodes. |
| 500 | `internal` | An unexpected failure. The details are logged by `polaroidd` and never returned. |
| 503 | `unavailable` | Only from `/healthz`, when the database is unreachable. |

Example `invalid_request` response:

```json
{"error":{"code":"invalid_request","message":"invalid input: canonical_key must be lowercase letters and digits joined by single '.', '-' or '_' characters; version.method is required",
  "fields":[{"field":"canonical_key","message":"must be lowercase letters and digits joined by single '.', '-' or '_' characters"},
            {"field":"version.method","message":"is required"}]}}
```

## CLI mapping

`polaroid` is a thin client for this API. It prints each response body to stdout, also when the request fails. It exits with 0 for success, 1 for a failed request (any non-2xx status, or a transport error) and 2 for a usage error.

| CLI | Request |
| --- | --- |
| `polaroid health` | `GET /healthz` |
| `polaroid list [NAME=VALUE...]` | `GET /v1/procedures[?…]` |
| `polaroid create [FILE]` | `POST /v1/procedures` |
| `polaroid get ID` | `GET /v1/procedures/{id}` |
| `polaroid get-by-key KEY` | `GET /v1/procedures/by-key/{key}` |
| `polaroid discover TASK [NAME=VALUE...]` | `GET /v1/procedures/discovery?task={task}[&…]` |
| `polaroid duplicates [FILE]` | `POST /v1/procedures/duplicates` |
| `polaroid origin ID [FILE]` | `POST /v1/procedures/{id}/origin` |
| `polaroid get-version ID N` | `GET /v1/procedures/{id}/versions/{n}` |
| `polaroid get-procedure-lifecycle ID` | `GET /v1/procedures/{id}/lifecycle/events` |
| `polaroid retire-procedure ID [FILE]` | `POST /v1/procedures/{id}/retirement` |
| `polaroid reactivate-procedure ID [FILE]` | `POST /v1/procedures/{id}/reactivation` |
| `polaroid graph ID N [REPO ENV [COMMIT INPUTS [DECISIONS]]]` | `GET /v1/procedures/{id}/versions/{n}/graph[?repository=…&environment=…[&commit=…&inputs=…[&decisions=…]]]` |
| `polaroid revise ID [FILE]` | `POST /v1/procedures/{id}/versions` |
| `polaroid bindings REPOSITORY [NAME=VALUE...]` | `GET /v1/bindings?repository={repository}[&…]` |
| `polaroid bind [FILE]` | `POST /v1/bindings` |
| `polaroid get-binding ID` | `GET /v1/bindings/{id}` |
| `polaroid get-binding-revision ID N` | `GET /v1/bindings/{id}/revisions/{n}` |
| `polaroid revise-binding ID [FILE]` | `POST /v1/bindings/{id}/revisions` |
| `polaroid resolve BINDING_ID ENV [COMMIT INPUTS [DECISIONS]]` | `GET /v1/bindings/{id}/resolution?environment={env}[&commit=…&inputs=…[&decisions=…]]` |
| `polaroid record [FILE]` | `POST /v1/executions` |
| `polaroid get-execution ID` | `GET /v1/executions/{id}` |
| `polaroid executions [PROCEDURE_ID [REPOSITORY]] [NAME=VALUE...]` | `GET /v1/executions[?procedure_id={id}][&repository={repository}][&…]` |
| `polaroid verification ID` | `GET /v1/executions/{id}/verification` |
| `polaroid invalidate-execution ID [FILE]` | `POST /v1/executions/{id}/invalidation` |
| `polaroid get-execution-invalidation ID` | `GET /v1/executions/{id}/invalidation` |
| `polaroid verifications ID N [REPO [COMMIT [ENV]]]` | `GET /v1/procedures/{id}/versions/{n}/verifications[?repository=…][&commit=…][&environment=…]` |
| `polaroid feedback [FILE]` | `POST /v1/feedback` |
| `polaroid feedbacks [KIND] [NAME=VALUE...]` | `GET /v1/feedback[?kind={kind}][&…]` |
| `polaroid get-feedback ID` | `GET /v1/feedback/{id}` |
| `polaroid register [FILE]` | `POST /v1/repositories` |
| `polaroid alias ID [FILE]` | `POST /v1/repositories/{id}/aliases` |
| `polaroid repository ID` | `GET /v1/repositories/{id}` |
| `polaroid repository-by-identifier IDENTIFIER` | `GET /v1/repositories/by-identifier/{identifier}` |
| `polaroid repositories [NAME=VALUE...]` | `GET /v1/repositories[?…]` |
| `polaroid toil-items [NAME=VALUE...]` | `GET /v1/toil-items[?…]` |
| `polaroid create-toil-item [FILE]` | `POST /v1/toil-items` |
| `polaroid get-toil-item ID` | `GET /v1/toil-items/{id}` |
| `polaroid get-toil-item-by-key KEY` | `GET /v1/toil-items/by-key/{key}` |
| `polaroid get-toil-revision ID N` | `GET /v1/toil-items/{id}/revisions/{n}` |
| `polaroid revise-toil-item ID [FILE]` | `POST /v1/toil-items/{id}/revisions` |
| `polaroid statistics` | `GET /v1/statistics` |

`FILE` defaults to stdin, and so does `-`. Trailing `NAME=VALUE` arguments of the list commands become query parameters, for example `polaroid list repository=github.com/ashuangiras/polaroid scope=shared limit=20`. The server is `-server URL`, else `$POLAROID_URL`, else `http://127.0.0.1:7417`.

### Local lifecycle commands

`install`, `start`, `stop`, `restart`, `status`, `uninstall` and `version` send no request: they manage this user's installation and service ([ADR-0026](decisions/0026-per-user-installation-and-managed-service.md), README "Run Polaroid as a service"). Each prints the resulting status as one JSON object on stdout and a one-line summary on stderr; `version` prints the build, `{"name", "version", "revision", "modified", "time", "go"}`, where `version` is the main module version Go embeds from the VCS: the tag at the built commit, a pseudo-version, or `(devel)` ([ADR-0028](decisions/0028-prerelease-verification-builds.md)); `polaroidd -version` prints the same. The status object has `state` and, where they apply, `action`, `detail`, `manager`, `service`, `definition`, `pid`, `build`, `binaries`, `endpoint`, `database`, `diagnostics`, `conflict` (`{pid, command}` of a process holding the endpoint that is not the managed one), `previous` and `problems`.

| Command | Exit status |
| --- | --- |
| `status` | 0 `running` (managed process owns the endpoint and is healthy), 1 `failed` (crashed, unreachable, conflicting, or the last start failed), 3 `stopped`, 4 `not-installed`, 5 `starting` |
| `install`, `start`, `restart` | 0 when the service is running and healthy; 1 otherwise; 4 (`start`, `restart`) when not installed |
| `stop`, `uninstall` | 0 when stopped or uninstalled (also when it already was); 1 otherwise; 4 (`stop`) when not installed |

All of them exit 2 for a usage error.

## Compatibility

Within `/v1`, changes are additive only: new endpoints, or new optional response fields. Clients must ignore response fields they do not know. Requests stay strict, so a client sending a field the server does not yet support gets `400`, not silent data loss. A breaking change needs a new version prefix and an ADR.

Pagination is opt-in through `limit` and `after`, so a client that sends neither keeps receiving complete lists ([ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md)). The list endpoints still reject any query parameter they do not document.

Changes for [#35](https://github.com/ashuangiras/polaroid/issues/35), all additive for existing clients:

- New endpoints for repositories and origins; new optional request fields `origin`, `goal`, `applicability`, `subject`, `repository` and `execution_id`; new optional query parameters; the new response field `scope` on procedures, and `next` on paged lists only.
- `GET /v1/executions` without `procedure_id`, which used to be `400`, now lists every execution. `limit` on a list, which used to be an unknown parameter (`400`), now pages.
- Writes that a declared applicability forbids are `400`. No stored version declares one, so no existing binding, execution or reference is affected.

Changes for [#37](https://github.com/ashuangiras/polaroid/issues/37) ([ADR-0022](decisions/0022-repository-identity-in-evidence.md), [ADR-0023](decisions/0023-pagination-guarantees-and-scope-labels.md)):

- New response fields: `scope` on versions and graph nodes; `repository_id` on executions, execution summaries, selection evidence and combinations of registered identifiers. New query parameter `snapshot` on the procedure and binding lists.
- Verification, verification lists, target verification and resolution match evidence by repository identity. Unregistered identifiers, and repositories without aliases, behave as before. Executions recorded under an alias now join their repository's combinations (whose `repository` is the canonical identifier), which may change those combinations' status. No execution changes.
- A cursor reused with another list or other parameters, which used to continue from an arbitrary position, is now `400`. Cursors issued before the change are still accepted.

Changes for [#39](https://github.com/ashuangiras/polaroid/issues/39) ([ADR-0024](decisions/0024-child-inputs-follow-the-reference-mapping.md)), a correctness fix that tightens one rule within `/v1`:

- `POST /v1/executions` with a child whose inputs differ from those its reference maps, which used to be accepted, is `400` naming `children[i].execution_id`.
- Verification can report the new problem code `child_inputs_mismatch`. A parent whose stored link has such a child, previously verified, is now unverified, and so are its ancestors; verification lists, contextual resolution and target verification follow. Executions and links read back unchanged. No migration.

Changes for [#50](https://github.com/ashuangiras/polaroid/issues/50) ([ADR-0029](decisions/0029-conditional-references-and-applicability-decisions.md)), all additive; migration 8:

- New optional request and response fields: `condition` on references, `decisions` on executions. New query parameter `decisions` on the graph and resolution endpoints. New response fields: `skipped` entries in combination `children` (whose `version` is then absent), `condition`, `skipped_by` and `decision` on graph edges, and `undecided` on `target_verification`. New problem code `missing_decision`.
- Existing references are required and existing executions have no decisions, so every stored record, verification, combination, selection and target verification is unchanged. Requests without the new fields behave as before.

Changes for [#79](https://github.com/ashuangiras/polaroid/issues/79) ([ADR-0032](decisions/0032-lexical-discovery-and-duplicate-suggestions.md)) and [#81](https://github.com/ashuangiras/polaroid/issues/81) ([ADR-0033](decisions/0033-discovery-ranking-rarity-purpose-detail.md)), additive; no migration:

- New read-only endpoints `GET /v1/procedures/discovery` and `POST /v1/procedures/duplicates`. Procedure IDs are UUIDs, so `GET /v1/procedures/{id}` never meant `discovery` or `duplicates`; `GET /v1/procedures/duplicates` is still `404` as an unknown procedure.
- #81 replaced the discovery ranking before the endpoints were released: `score` became a non-integer ranking value, and `considered`, `rarity`, `detail_cap`, `detail_points` and `contributions` were added.
- No record, response field or existing request changes; listing, verification and resolution behave as before.

Changes for [#87](https://github.com/ashuangiras/polaroid/issues/87) ([ADR-0034](decisions/0034-toil-items.md)), additive; migration 9:

- New endpoints under `/v1/toil-items`, the error cases above for them, and `snapshot` on their list. `canonical_key_exists` and `revision_conflict` can now come from toil items too, with the same shape.
- No existing endpoint, request, response or record changes: procedures, bindings, executions, verification, resolution, discovery and feedback read and behave as before, and toil items never affect them. The message of a `snapshot` refused on another list now names the toil item list among those that offer it.
- A schema-9 catalog cannot be opened by earlier builds, and their `inspect-backup` and `restore` refuse a schema-9 backup.

Changes for [#91](https://github.com/ashuangiras/polaroid/issues/91) ([ADR-0035](decisions/0035-evidence-invalidation-and-procedure-retirement.md)); migration 10:

- New endpoints: `POST`/`GET /v1/executions/{id}/invalidation`, `GET /v1/procedures/{id}/lifecycle/events`, `POST /v1/procedures/{id}/retirement` and `/reactivation`. New query parameters: `lifecycle` on the procedure list and discovery, `evidence_status` on the execution list. New error codes: `execution_invalidated`, `lifecycle_conflict` (with `latest_event`), `lifecycle_unchanged`, `procedure_retired`. New problem codes: `invalidated`, `child_invalidated`.
- New response fields, always present: `evidence_status` on executions, execution list items and verifications; `lifecycle_status` on procedures, list items, discovery candidates, duplicate suggestions and `key_collision`. Present only when they apply: `invalidation`, `lifecycle_event`, `retirement` on graph nodes, `invalidated_execution_ids` on combination statuses and target verification.
- Until something is invalidated or retired, every existing response is the same apart from those fields, and every request behaves as before. Afterwards: the procedure list and discovery leave retired procedures out by default (`lifecycle=all` lists everything, as before); `latest_execution_id` is the latest **valid** execution and is omitted when there is none; verification, combination statuses, contextual selection and target verification ignore invalidated executions as evidence.
- A procedure-list snapshot cursor issued by an earlier build is still accepted, and sees no lifecycle event. A schema-10 catalog cannot be opened by earlier builds, and their `inspect-backup` and `restore` refuse a schema-10 backup.

Changes for [#96](https://github.com/ashuangiras/polaroid/issues/96) ([ADR-0036](decisions/0036-read-only-catalog-statistics.md)), additive; no migration:

- New read-only endpoint `GET /v1/statistics`, MCP tool `get_catalog_statistics` and CLI command `statistics`. No existing endpoint, request, response, record or schema changes, and a schema-10 catalog stays readable by `v0.6.0-verify.1`.

Changes for [#98](https://github.com/ashuangiras/polaroid/issues/98) ([ADR-0037](decisions/0037-size-limits-on-new-writes.md)), a tightening of writes within `/v1`; no migration:

- Every write endpoint refuses a value over its [write limit](#write-limits), or a write over 256 KiB in total, with the new error `413 value_too_large` (member `limits`). Such writes used to be accepted. No stored record, read, list, verification, selection, backup or restore changes; the largest live values are at most a quarter of their limits.
- `GET /v1/statistics` adds `write_limits`; `statistics_version` stays 1.
