# HTTP API (v1)

This page is the contract of the API that `polaroidd` serves. It covers only what is implemented: procedure identity and immutable versions with subprocedure references, repository bindings with immutable revisions, execution records, and feedback reports about Polaroid itself. Field rules are defined in [records.md](records.md). The same operations are available to MCP clients at `/mcp`; see [mcp.md](mcp.md).

- **Base URL:** `http://127.0.0.1:7417` by default (`polaroidd -addr`).
- **Bodies:** every request and response body is UTF-8 JSON. Requests with a body must send `Content-Type: application/json` and stay under 1 MiB.
- **Strict decoding:** member names are case-sensitive. Unknown members, duplicate members and trailing data are rejected. This includes server-assigned fields and fields of planned capabilities.
- **Timestamps:** RFC 3339 in UTC.

## Endpoints

| Method and path | Success | Purpose |
| --- | --- | --- |
| `GET /healthz` | `200 {"status":"ok"}` | The daemon and its database are reachable. |
| `POST /v1/procedures` | `201` history, with a `Location` header | Create a procedure and its version 1. |
| `GET /v1/procedures` | `200 {"procedures":[...]}` | List all procedures, ordered by canonical key. Not paginated. |
| `GET /v1/procedures/{id}` | `200` history | A procedure and all of its versions, oldest first. |
| `GET /v1/procedures/by-key/{canonical_key}` | `200` history | The same history, looked up by canonical key. |
| `GET /v1/procedures/{id}/versions/{n}` | `200` version | One version. |
| `GET /v1/procedures/{id}/versions/{n}/graph` | `200` graph node | The version's composition graph, with the exact version each reference selects. |
| `GET /v1/procedures/{id}/versions/{n}/verifications[?repository=…][&commit=…][&environment=…]` | `200 {"verifications":[...]}` | The version's execution combinations and whether each is verified. Not paginated. |
| `POST /v1/procedures/{id}/versions` | `201` version, with a `Location` header | Append a version derived from `base_version`. |
| `POST /v1/bindings` | `201` binding history, with a `Location` header | Create a binding and its revision 1. |
| `GET /v1/bindings?repository={repository}` | `200 {"bindings":[...]}` | List one repository's bindings, ordered by name. Not paginated. |
| `GET /v1/bindings/{id}` | `200` binding history | A binding and all of its revisions, oldest first. |
| `GET /v1/bindings/{id}/revisions/{n}` | `200` binding revision | One revision. |
| `POST /v1/bindings/{id}/revisions` | `201` binding revision, with a `Location` header | Append a revision derived from `base_revision`. |
| `POST /v1/executions` | `201` execution, with a `Location` header | Record one finished run. |
| `GET /v1/executions?procedure_id={id}[&version={n}][&repository={repository}]` | `200 {"executions":[...]}` | List a procedure's executions, oldest first, without `inputs` and `evidence`. Not paginated. |
| `GET /v1/executions/{id}` | `200` execution | One execution, in full. |
| `GET /v1/executions/{id}/verification` | `200` verification | Whether one execution is verified, and its combination. |
| `POST /v1/feedback` | `201` feedback report, with a `Location` header | Report a problem with Polaroid, or suggest an improvement. |
| `GET /v1/feedback[?kind={kind}]` | `200 {"feedback":[...]}` | List feedback reports, oldest first, in full. Not paginated. |
| `GET /v1/feedback/{id}` | `200` feedback report | One feedback report. |

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

A **history** has the fields `id`, `canonical_key`, `created_at`, `latest_version` and `versions`. A list item has the same fields without `versions`. A **version** has the fields `procedure_id`, `version`, `philosophy`, `method`, `contract`, `instructions`, `references` (only when the version has references), `revision_reason` and `created_at`. `contract` and `instructions` are returned with insignificant whitespace removed. Otherwise they are exactly as submitted.

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

`GET /v1/procedures/{id}/versions/{n}/graph[?repository=…&environment=…]` returns a nested tree.

- Each node has `procedure_id`, `canonical_key`, `version` (the exact version selected), `verified_by` (only when the node has evidence in the context) and `references`, which is empty for a leaf.
- Each reference has its stored `name` and `version_policy`, then `selected_by` (`pin`, `evidence` or `latest`), its stored `inputs`, and the selected child `node`.
- Without `repository` and `environment`, pinned references select the pin and contextual references the target's latest version at the moment of the read.
- With them, the graph is resolved from evidence in that repository and environment ([records.md](records.md#contextual-resolution-implemented)). They must be given together. Each may appear once, and any other query parameter is `400`.
- The whole walk reads one consistent snapshot.

```json
{"procedure_id":"…root","canonical_key":"compose.root","version":1,"verified_by":"…run","references":[
  {"name":"old-leaf","version_policy":{"pin":1},"selected_by":"pin","inputs":{},"node":{"procedure_id":"…leaf","canonical_key":"go.dependency.add","version":1,"verified_by":"…leaf-run","references":[]}},
  {"name":"mid","version_policy":{"contextual":{}},"selected_by":"evidence","inputs":{"module":{"input":"driver"}},"node":{"procedure_id":"…mid","canonical_key":"compose.mid","version":1,"verified_by":"…mid-run","references":[
    {"name":"leaf","version_policy":{"contextual":{}},"selected_by":"evidence","inputs":{},"node":{"procedure_id":"…leaf","canonical_key":"go.dependency.add","version":2,"verified_by":"…leaf2-run","references":[]}}]}}]}
```

A missing procedure or version is `404`. A cycle is `409 reference_cycle`. It is possible only in versions stored before cycle checking existed, or when evidence selects an older version whose references lead back. A graph over the limits is `422 graph_too_large`. No partial graph is ever returned.

### Resolve a binding

`GET /v1/bindings/{id}/resolution?environment=ci.ubuntu-latest` resolves the binding's latest revision in the binding's repository and that environment. A pin selects its version. A contextual policy selects the highest version verified there, or else the latest version.

```json
{"binding_id":"…","binding_revision":2,"repository":"github.com/ashuangiras/polaroid","environment":{"name":"ci.ubuntu-latest"},
 "version_policy":{"contextual":{}},"selected_by":"evidence","graph":{"procedure_id":"…","canonical_key":"go.dependency.add","version":2,"verified_by":"…","references":[]}}
```

`environment` is required, must appear once and must be valid. Any other parameter is `400`. An unknown binding is `404`. Graph errors are as for the graph endpoint.

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

`GET /v1/bindings?repository=github.com/ashuangiras/polaroid`. The `repository` parameter is required, must appear once and must be a valid repository identifier. Any other query parameter is rejected with `400`, so a filter this server does not support is never silently ignored. A repository without bindings gets `{"bindings":[]}`.

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

A parent execution adds `"children": [{"reference": "pinned-child", "execution_id": "…"}, …]`, listing child executions recorded earlier. They are returned in the same order, and the field is absent when there are none. A link to an unknown reference or execution is `400` naming `children[i].reference` or `children[i].execution_id`. So is a child that ran another procedure, a version other than the pin, another repository or commit, or a child already linked to another parent. See [records.md](records.md#subprocedure-execution-implemented).

### List executions

`GET /v1/executions?procedure_id=…` lists one procedure's executions, oldest first, optionally filtered by `version` and `repository`.

- `procedure_id` is required.
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

- `verified` is true when the execution succeeded and every reference of its version has a linked child that is itself verified.
- `problems` is absent when `verified` is true. Otherwise it lists the direct reasons in this order: `outcome_failed`, then one per reference in version order, either `missing_child` (with `reference`) or `child_not_verified` (with `reference` and the child's `execution_id`). A child's own problems are read from the child.
- `combination` has `repository`, `commit`, `environment.name`, the canonical `inputs`, and `children`. `children` lists each linked child's `reference`, `version` and own `children`, in the version's reference order, and is absent when there are none.
- An unknown execution is `404`.

`GET /v1/procedures/{id}/versions/{n}/verifications` lists the version's combinations, ordered by their first execution:

```json
{"verifications":[{"combination":{…},"verified":true,"latest_execution_id":"…c","execution_ids":["…a","…b","…c"]}]}
```

- `verified` is the verification of `latest_execution_id`, the newest execution in the combination. `execution_ids` lists all of them, oldest first.
- `repository`, `commit` (full hash) and `environment` (name) optionally filter the executions. Each may appear once, must be valid, and any other parameter is rejected with `400`.
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
| 409 | `binding_exists` | The repository already has a binding with this `name`. |
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
| `polaroid list` | `GET /v1/procedures` |
| `polaroid create [FILE]` | `POST /v1/procedures` |
| `polaroid get ID` | `GET /v1/procedures/{id}` |
| `polaroid get-by-key KEY` | `GET /v1/procedures/by-key/{key}` |
| `polaroid get-version ID N` | `GET /v1/procedures/{id}/versions/{n}` |
| `polaroid graph ID N [REPO ENV]` | `GET /v1/procedures/{id}/versions/{n}/graph[?repository=…&environment=…]` |
| `polaroid revise ID [FILE]` | `POST /v1/procedures/{id}/versions` |
| `polaroid bindings REPOSITORY` | `GET /v1/bindings?repository={repository}` |
| `polaroid bind [FILE]` | `POST /v1/bindings` |
| `polaroid get-binding ID` | `GET /v1/bindings/{id}` |
| `polaroid get-binding-revision ID N` | `GET /v1/bindings/{id}/revisions/{n}` |
| `polaroid revise-binding ID [FILE]` | `POST /v1/bindings/{id}/revisions` |
| `polaroid resolve BINDING_ID ENV` | `GET /v1/bindings/{id}/resolution?environment={env}` |
| `polaroid record [FILE]` | `POST /v1/executions` |
| `polaroid get-execution ID` | `GET /v1/executions/{id}` |
| `polaroid executions PROCEDURE_ID [REPOSITORY]` | `GET /v1/executions?procedure_id={id}[&repository={repository}]` |
| `polaroid verification ID` | `GET /v1/executions/{id}/verification` |
| `polaroid verifications ID N [REPO [COMMIT [ENV]]]` | `GET /v1/procedures/{id}/versions/{n}/verifications[?repository=…][&commit=…][&environment=…]` |
| `polaroid feedback [FILE]` | `POST /v1/feedback` |
| `polaroid feedbacks [KIND]` | `GET /v1/feedback[?kind={kind}]` |
| `polaroid get-feedback ID` | `GET /v1/feedback/{id}` |

`FILE` defaults to stdin, and so does `-`. The server is `-server URL`, else `$POLAROID_URL`, else `http://127.0.0.1:7417`.

## Compatibility

Within `/v1`, changes are additive only: new endpoints, or new optional response fields. Clients must ignore response fields they do not know. Requests stay strict, so a client sending a field the server does not yet support gets `400`, not silent data loss. A breaking change needs a new version prefix and an ADR.

Listing is unbounded today. Pagination, if added, will be opt-in through new query parameters, so existing clients keep receiving complete lists. Until then, the list endpoints for bindings, executions and feedback reject any query parameter they do not document.
