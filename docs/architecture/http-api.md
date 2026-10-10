# HTTP API (v1)

This page is the contract of the API that `polaroidd` serves. It covers only what is implemented: procedure identity and immutable versions with subprocedure references and applicability, the repository registry, repository bindings with immutable revisions, execution records, and feedback reports. Field rules are defined in [records.md](records.md). The same operations are available to MCP clients at `/mcp`; see [mcp.md](mcp.md).

- **Base URL:** `http://127.0.0.1:7417` by default (`polaroidd -addr`).
- **Bodies:** every request and response body is UTF-8 JSON. Requests with a body must send `Content-Type: application/json` and stay under 1 MiB.
- **Strict decoding:** member names are case-sensitive. Unknown members, duplicate members and trailing data are rejected. This includes server-assigned fields and fields of planned capabilities.
- **Timestamps:** RFC 3339 in UTC.

## Endpoints

| Method and path | Success | Purpose |
| --- | --- | --- |
| `GET /healthz` | `200 {"status":"ok"}` | The daemon and its database are reachable. |
| `POST /v1/procedures` | `201` history, with a `Location` header | Create a procedure and its version 1, with an optional `origin`. |
| `GET /v1/procedures[?repository=…][&scope=…][&q=…][&limit=…&after=…][&snapshot=true]` | `200 {"procedures":[...]}` | List procedures, ordered by canonical key, optionally [filtered and paged](#discovery-filters-and-pages). |
| `GET /v1/procedures/{id}` | `200` history | A procedure and all of its versions, oldest first. |
| `GET /v1/procedures/by-key/{canonical_key}` | `200` history | The same history, looked up by canonical key. |
| `POST /v1/procedures/{id}/origin` | `201` history | Record where and why the procedure was first created, once. |
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
| `GET /v1/executions[?procedure_id=…[&version=…]][&repository=…][&commit=…][&limit=…&after=…]` | `200 {"executions":[...]}` | List executions, oldest first, without `inputs` and `evidence`. |
| `GET /v1/executions/{id}` | `200` execution | One execution, in full. |
| `GET /v1/executions/{id}/verification` | `200` verification | Whether one execution is verified, and its combination. |
| `POST /v1/feedback` | `201` feedback report, with a `Location` header | Report a problem or a suggestion, optionally naming its subject. |
| `GET /v1/feedback[?kind=…][&subject_type=…[&subject_id=…[&subject_version=…]]][&repository=…][&limit=…&after=…]` | `200 {"feedback":[...]}` | List feedback reports, oldest first, in full. |
| `GET /v1/feedback/{id}` | `200` feedback report | One feedback report. |
| `POST /v1/repositories` | `201` repository, with a `Location` header | Register a repository under its canonical identifier. |
| `GET /v1/repositories[?limit=…&after=…]` | `200 {"repositories":[...]}` | List registered repositories, oldest first. |
| `GET /v1/repositories/{id}` | `200` repository | One repository with its aliases. |
| `GET /v1/repositories/by-identifier/{identifier}` | `200` repository | The repository an identifier, canonical or alias, is registered to. |
| `POST /v1/repositories/{id}/aliases` | `201` repository | Register another identifier of the same repository. |

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

A **history** has the fields `id`, `canonical_key`, `created_at`, `latest_version`, `scope`, `goal`, `applicability` and `origin` (the last three only when present) and `versions`. A list item has the same fields without `versions`. `scope`, `goal` and `applicability` describe version `latest_version` only: `scope` is its applicability, `shared`, `local` or `unspecified` ([ADR-0023](decisions/0023-pagination-guarantees-and-scope-labels.md)). A **version** has the fields `procedure_id`, `version`, `philosophy`, `method`, `scope` (this version's own declaration, always present), `goal` and `applicability` (only when given), `contract`, `instructions`, `references` (only when the version has references), `revision_reason` and `created_at`. `contract` and `instructions` are returned with insignificant whitespace removed. Otherwise they are exactly as submitted.

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

A version whose graph would contain a cycle is rejected with `409 reference_cycle`. The `cycle` field lists the path: each step is a node and the reference followed out of it, and the last step is the repeated procedure, which has no `reference`.

```json
{"error":{"code":"reference_cycle","message":"references form a cycle: A@2 -[b]-> B@1 -[a]-> A@2",
  "cycle":[{"procedure_id":"A","version":2,"reference":"b"},{"procedure_id":"B","version":1,"reference":"a"},{"procedure_id":"A","version":2}]}}
```

A graph deeper than 32 references or larger than 2048 nodes is rejected with `422 graph_too_large`. See [records.md](records.md#composition-graph-implemented).

### Composition graph

`GET /v1/procedures/{id}/versions/{n}/graph[?repository=…&environment=…[&commit=…&inputs=…]]` returns a nested tree.

- Each node has `procedure_id`, `canonical_key`, `version` (the exact version selected), `scope` (that version's own declaration), `applicability` (when declared), `verified_by` and `selection_evidence` (only when the node has evidence in the context), `target_verification` (only with a target) and `references`, which is empty for a leaf.
- Each reference has its stored `name` and `version_policy`, then `selected_by` (`pin`, `evidence` or `latest`), its stored `inputs`, and the selected child `node`.
- Without `repository` and `environment`, pinned references select the pin and contextual references the target's latest version at the moment of the read.
- With them, the graph is resolved from evidence in that repository and environment ([records.md](records.md#contextual-resolution-implemented)). They must be given together.
- `commit` and `inputs` (a JSON object, URL-encoded) add a target ([records.md](records.md#selection-evidence-and-target-verification-implemented)). They must be given together, and only with `repository` and `environment`; otherwise each missing one is `400`. A malformed commit, or `inputs` that is not a JSON object with unique member names, is `400` naming the field.
- Each parameter may appear once, and any other query parameter is `400`.
- The whole walk reads one consistent snapshot.

`selection_evidence` is `{"execution_id", "repository", "repository_id", "commit", "environment": {"name"}}`: the execution that selected the node, which may have run at any commit and with any inputs, with the identifier it was recorded under and, when registered, its repository (`repository_id`, [ADR-0022](decisions/0022-repository-identity-in-evidence.md)). `verified_by` repeats its ID, for compatibility. Neither says anything about another commit.

`target_verification` has the shape of an entry of the [verifications list](#verification): `combination` is the node's exact selected combination at the target (repository, `commit`, environment, the node's effective `inputs` in canonical form, and the selected child-version tree), `verified` is the status of its latest execution there, and `latest_execution_id` and `execution_ids` list those executions. If nothing has run in that combination, `verified` is `false`, `execution_ids` is `[]` and `latest_execution_id` is omitted.

```json
{"procedure_id":"…root","canonical_key":"compose.root","version":1,"verified_by":"…run","selection_evidence":{"execution_id":"…run","repository":"github.com/ashuangiras/polaroid","commit":"0123…","environment":{"name":"ci.ubuntu-latest"}},"references":[
  {"name":"old-leaf","version_policy":{"pin":1},"selected_by":"pin","inputs":{},"node":{"procedure_id":"…leaf","canonical_key":"go.dependency.add","version":1,"verified_by":"…leaf-run","selection_evidence":{…},"references":[]}},
  {"name":"mid","version_policy":{"contextual":{}},"selected_by":"evidence","inputs":{"module":{"input":"driver"}},"node":{"procedure_id":"…mid","canonical_key":"compose.mid","version":1,"verified_by":"…mid-run","selection_evidence":{…},"references":[
    {"name":"leaf","version_policy":{"contextual":{}},"selected_by":"evidence","inputs":{},"node":{"procedure_id":"…leaf","canonical_key":"go.dependency.add","version":2,"verified_by":"…leaf2-run","selection_evidence":{…},"references":[]}}]}}]}
```

A missing procedure or version is `404`. A cycle is `409 reference_cycle`. It is possible only in versions stored before cycle checking existed, or when evidence selects an older version whose references lead back. A graph over the limits is `422 graph_too_large`. No partial graph is ever returned.

### Resolve a binding

`GET /v1/bindings/{id}/resolution?environment=ci.ubuntu-latest[&commit=…&inputs=…]` resolves the binding's latest revision in the binding's repository and that environment. A pin selects its version. A contextual policy selects the highest version verified there, at any commit, or else the latest version. `commit` and `inputs` add a target, exactly as for the graph endpoint; `inputs` are the root's effective inputs, usually the revision's `inputs` plus anything the run adds.

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
- `q`: the canonical key or the latest goal contains this text, ignoring ASCII case.

The procedure, repository, binding, execution and feedback lists take **opt-in pagination** ([ADR-0021](decisions/0021-targeted-feedback-and-bounded-lists.md), [ADR-0023](decisions/0023-pagination-guarantees-and-scope-labels.md)):

- `limit` (1 to 500) returns at most that many items. If more remain, the body adds `"next"`, an opaque cursor; pass it back as `after`, with a `limit` (which may change) and **the same other parameters**, to continue. The last page has no `next`.
- Without `limit` the list is complete, as before, and has no `next`. `after` without `limit` is `400`, and so is an `after` that is not a cursor, or a cursor issued by another list or with other parameters (`400` on `after`). Cursors issued before ADR-0023 are still accepted, as positions without that check.
- **Order** is stable, with unique tie-breakers: procedures by `canonical_key` (unique); bindings by `name`, then `id`; repositories, executions and feedback by `created_at`, then `id`.
- **A page is a fresh read; the default traversal is live, not a snapshot.** A cursor means "after this item, in this order".
  - Time-ordered lists (repositories, executions, feedback): `created_at` is assigned inside the write transaction, strictly after every stored one, so a record committed while you page sorts after your cursor and a later page returns it. Continuing never skips or repeats one.
  - Key-ordered lists (procedures, bindings): a record created while you page is returned only if its key sorts after the cursor. None is repeated.
  - Filters are evaluated on each page against the state then: a procedure's latest version (for `scope`, `q` and `repository`) and the repository registry (for every `repository` filter). A record whose membership changes between pages may be missed, or returned late. Item contents, such as `latest_version`, are as of the page that returned them.
  - To see what a traversal could not return, start again without `after`.
- **Snapshot traversal** (`snapshot=true`, with `limit`, on `GET /v1/procedures` and `GET /v1/bindings`): the first page fixes a boundary, carried by its cursors, and every page reads only records committed before it.
  - **Membership and contents are fixed as of the first page.** For procedures: which exist, each one's latest version (so `latest_version`, `scope`, `goal`, `applicability`), its origin, and repository identity for `repository`. For bindings: which exist, `latest_revision`, and the identity for `repository`.
  - **Every record within the boundary is returned exactly once**, wherever its key sorts. Records committed later are excluded: start a new snapshot to see them.
  - **Nothing else is a snapshot:** reading a record by ID, or any other list, returns current state. `snapshot` must be given on every page; it is `400` without `limit`, with `true` or `false` as its only values, and an unknown parameter on the other lists.
- A filter given but empty is `400`.

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

- The response is `201`, with the execution as the body and `Location: /v1/executions/{id}`. The body is the request plus `id` and `created_at`. `binding_id` and `binding_revision` are absent when no binding was given.
- `GET /v1/executions/{id}` returns the same bytes.
- An unknown procedure or binding is `404`.
- These are `400`, each naming its field:
  - a missing version or binding revision;
  - a binding of another procedure or repository;
  - a version other than the binding revision's pin;
  - an abbreviated commit;
  - empty `evidence`.

A parent execution adds `"children": [{"reference": "pinned-child", "execution_id": "…"}, …]`, listing child executions recorded earlier. They are returned in the same order, and the field is absent when there are none. A link to an unknown reference or execution is `400` naming `children[i].reference` or `children[i].execution_id`. So is a child that ran another procedure, a version other than the pin, another repository or commit, or a child already linked to another parent. So is a child whose `inputs` differ from those the reference maps from the parent's `inputs`, compared canonically; the message gives both ([ADR-0024](decisions/0024-child-inputs-follow-the-reference-mapping.md)), for example `ran with inputs {"service":"b"}, but reference "build" maps the parent's inputs to {"service":"a"} (ADR-0024)`. See [records.md](records.md#subprocedure-execution-implemented).

### List executions

`GET /v1/executions` lists executions, oldest first, optionally filtered by `procedure_id`, `version` (which needs `procedure_id`), `repository` (by identity: every identifier of a registered repository) and `commit`, and paged with `limit` and `after`. Without filters it lists every execution; `procedure_id` used to be required, and still works as before.

- Each parameter may appear once.
- Any other parameter is rejected with `400`.
- List items have every execution field except `inputs`, `evidence` and `children`.

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

- `verified` is true when the execution succeeded and every reference of its version has a linked child that ran with the inputs the reference maps and is itself verified.
- `problems` is absent when `verified` is true. Otherwise it lists the direct reasons in this order: `outcome_failed`, then per reference in version order, either `missing_child` (with `reference`), or `child_inputs_mismatch` and `child_not_verified`, each with `reference` and the child's `execution_id`, when they apply. `child_inputs_mismatch` reports a stored link whose child ran with other inputs than the reference maps; recording one is refused now, but links stored before the rule, or with direct SQL, are served as stored. A child's own problems are read from the child.
- `combination` has `repository`, `commit`, `environment.name`, the canonical `inputs`, and `children`. `children` lists each linked child's `reference`, `version` and own `children`, in the version's reference order, and is absent when there are none.
- An unknown execution is `404`.

`GET /v1/procedures/{id}/versions/{n}/verifications` lists the version's combinations, ordered by their first execution:

```json
{"verifications":[{"combination":{…},"verified":true,"latest_execution_id":"…c","execution_ids":["…a","…b","…c"]}]}
```

- `verified` is the verification of `latest_execution_id`, the newest execution in the combination. `execution_ids` lists all of them, oldest first.
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

## Errors

Every error is a JSON object of this form:

```json
{"error": {"code": "…", "message": "…", "fields": [{"field": "…", "message": "…"}], "latest_version": 2, "latest_revision": 2}}
```

`fields` appears only with `invalid_request` errors that come from field validation. `latest_version` appears only with `version_conflict`, `latest_revision` only with `revision_conflict`, and `cycle` only with `reference_cycle`. Messages are for humans and may change; branch on `code`.

| Status | `code` | When |
| --- | --- | --- |
| 400 | `invalid_request` | Malformed JSON, unknown or duplicate members, wrong JSON types, or a field that fails validation (listed in `fields`). Also a version or revision path segment that is not a positive integer, a pinned version the procedure does not have, a reference to an unknown procedure, an execution that does not match its version or binding, and a missing, repeated, invalid or unknown query parameter when listing bindings, executions, verifications or feedback. |
| 403 | `forbidden` | The `Host` header does not name a loopback address while the daemon listens on loopback, or a browser sent an unsafe cross-origin request. |
| 404 | `not_found` | Unknown procedure (also as a binding's or execution's `procedure_id`), canonical key, version, binding (also as an execution's `binding_id`), binding revision, execution, feedback report or endpoint. |
| 405 | `method_not_allowed` | The endpoint exists, but not for this method. The `Allow` header lists the methods it accepts. |
| 409 | `canonical_key_exists` | Another procedure already uses the canonical key. |
| 409 | `version_conflict` | `base_version` is not the latest version. `latest_version` is included. |
| 409 | `binding_exists` | The repository already has a binding with this `name`, under this or another of its identifiers, or an alias would give it two. |
| 409 | `repository_identifier_exists` | The identifier is already registered, as a canonical identifier or an alias. |
| 409 | `origin_exists` | The procedure's origin is already recorded. |
| 409 | `revision_conflict` | `base_revision` is not the latest revision. `latest_revision` is included. |
| 409 | `reference_cycle` | The version's references would form a cycle, or a stored graph contains one. `cycle` is included. |
| 413 | `request_too_large` | The body exceeds 1 MiB. |
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
| `polaroid origin ID [FILE]` | `POST /v1/procedures/{id}/origin` |
| `polaroid get-version ID N` | `GET /v1/procedures/{id}/versions/{n}` |
| `polaroid graph ID N [REPO ENV [COMMIT INPUTS]]` | `GET /v1/procedures/{id}/versions/{n}/graph[?repository=…&environment=…[&commit=…&inputs=…]]` |
| `polaroid revise ID [FILE]` | `POST /v1/procedures/{id}/versions` |
| `polaroid bindings REPOSITORY [NAME=VALUE...]` | `GET /v1/bindings?repository={repository}[&…]` |
| `polaroid bind [FILE]` | `POST /v1/bindings` |
| `polaroid get-binding ID` | `GET /v1/bindings/{id}` |
| `polaroid get-binding-revision ID N` | `GET /v1/bindings/{id}/revisions/{n}` |
| `polaroid revise-binding ID [FILE]` | `POST /v1/bindings/{id}/revisions` |
| `polaroid resolve BINDING_ID ENV [COMMIT INPUTS]` | `GET /v1/bindings/{id}/resolution?environment={env}[&commit=…&inputs=…]` |
| `polaroid record [FILE]` | `POST /v1/executions` |
| `polaroid get-execution ID` | `GET /v1/executions/{id}` |
| `polaroid executions [PROCEDURE_ID [REPOSITORY]] [NAME=VALUE...]` | `GET /v1/executions[?procedure_id={id}][&repository={repository}][&…]` |
| `polaroid verification ID` | `GET /v1/executions/{id}/verification` |
| `polaroid verifications ID N [REPO [COMMIT [ENV]]]` | `GET /v1/procedures/{id}/versions/{n}/verifications[?repository=…][&commit=…][&environment=…]` |
| `polaroid feedback [FILE]` | `POST /v1/feedback` |
| `polaroid feedbacks [KIND] [NAME=VALUE...]` | `GET /v1/feedback[?kind={kind}][&…]` |
| `polaroid get-feedback ID` | `GET /v1/feedback/{id}` |
| `polaroid register [FILE]` | `POST /v1/repositories` |
| `polaroid alias ID [FILE]` | `POST /v1/repositories/{id}/aliases` |
| `polaroid repository ID` | `GET /v1/repositories/{id}` |
| `polaroid repository-by-identifier IDENTIFIER` | `GET /v1/repositories/by-identifier/{identifier}` |
| `polaroid repositories [NAME=VALUE...]` | `GET /v1/repositories[?…]` |

`FILE` defaults to stdin, and so does `-`. Trailing `NAME=VALUE` arguments of the list commands become query parameters, for example `polaroid list repository=github.com/ashuangiras/polaroid scope=shared limit=20`. The server is `-server URL`, else `$POLAROID_URL`, else `http://127.0.0.1:7417`.

### Local lifecycle commands

`install`, `start`, `stop`, `restart`, `status`, `uninstall` and `version` send no request: they manage this user's installation and service ([ADR-0026](decisions/0026-per-user-installation-and-managed-service.md), README "Run Polaroid as a service"). Each prints the resulting status as one JSON object on stdout and a one-line summary on stderr; `version` prints the build. The status object has `state` and, where they apply, `action`, `detail`, `manager`, `service`, `definition`, `pid`, `build`, `binaries`, `endpoint`, `database`, `diagnostics`, `conflict` (`{pid, command}` of a process holding the endpoint that is not the managed one), `previous` and `problems`.

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
