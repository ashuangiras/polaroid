# HTTP API (v1)

This page is the contract of the API that `polaroidd` serves. It covers only what is implemented: procedure identity and immutable versions, and repository bindings with immutable revisions. Field rules are defined in [records.md](records.md).

- **Base URL:** `http://127.0.0.1:7417` by default (`polaroidd -addr`).
- **Bodies:** every request and response body is UTF-8 JSON. Requests with a body must send `Content-Type: application/json` and stay under 1 MiB.
- **Strict decoding:** member names are case-sensitive. Unknown members, duplicate members and trailing data are rejected. This includes server-assigned fields and fields of planned capabilities, such as `references`.
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
| `POST /v1/procedures/{id}/versions` | `201` version, with a `Location` header | Append a version derived from `base_version`. |
| `POST /v1/bindings` | `201` binding history, with a `Location` header | Create a binding and its revision 1. |
| `GET /v1/bindings?repository={repository}` | `200 {"bindings":[...]}` | List one repository's bindings, ordered by name. Not paginated. |
| `GET /v1/bindings/{id}` | `200` binding history | A binding and all of its revisions, oldest first. |
| `GET /v1/bindings/{id}/revisions/{n}` | `200` binding revision | One revision. |
| `POST /v1/bindings/{id}/revisions` | `201` binding revision, with a `Location` header | Append a revision derived from `base_revision`. |

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

A **history** has the fields `id`, `canonical_key`, `created_at`, `latest_version` and `versions`. A list item has the same fields without `versions`. A **version** has the fields `procedure_id`, `version`, `philosophy`, `method`, `contract`, `instructions`, `revision_reason` and `created_at`. `contract` and `instructions` are returned with insignificant whitespace removed. Otherwise they are exactly as submitted.

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

A **binding history** has the fields `id`, `repository`, `name`, `procedure_id`, `created_at`, `latest_revision` and `revisions`. A list item has the same fields without `revisions`. A **binding revision** has the fields `binding_id`, `revision`, `inputs`, `version_policy`, `revision_reason` and `created_at`. `version_policy` is returned exactly as accepted: `{"pin":N}` or `{"contextual":{}}`. A contextual policy is not resolved, and no response names a selected version.

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

## Errors

Every error is a JSON object of this form:

```json
{"error": {"code": "…", "message": "…", "fields": [{"field": "…", "message": "…"}], "latest_version": 2, "latest_revision": 2}}
```

`fields` appears only with `invalid_request` errors that come from field validation. `latest_version` appears only with `version_conflict`, and `latest_revision` only with `revision_conflict`. Messages are for humans and may change; branch on `code`.

| Status | `code` | When |
| --- | --- | --- |
| 400 | `invalid_request` | Malformed JSON, unknown or duplicate members, wrong JSON types, or a field that fails validation (listed in `fields`). Also a version or revision path segment that is not a positive integer, a pinned version the procedure does not have, and a missing, repeated, invalid or unknown query parameter when listing bindings. |
| 403 | `forbidden` | The `Host` header does not name a loopback address while the daemon listens on loopback, or a browser sent an unsafe cross-origin request. |
| 404 | `not_found` | Unknown procedure (also as a binding's `procedure_id`), canonical key, version, binding, binding revision or endpoint. |
| 405 | `method_not_allowed` | The endpoint exists, but not for this method. The `Allow` header lists the methods it accepts. |
| 409 | `canonical_key_exists` | Another procedure already uses the canonical key. |
| 409 | `version_conflict` | `base_version` is not the latest version. `latest_version` is included. |
| 409 | `binding_exists` | The repository already has a binding with this `name`. |
| 409 | `revision_conflict` | `base_revision` is not the latest revision. `latest_revision` is included. |
| 413 | `request_too_large` | The body exceeds 1 MiB. |
| 415 | `unsupported_media_type` | A request with a body that is not `application/json`. |
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
| `polaroid revise ID [FILE]` | `POST /v1/procedures/{id}/versions` |
| `polaroid bindings REPOSITORY` | `GET /v1/bindings?repository={repository}` |
| `polaroid bind [FILE]` | `POST /v1/bindings` |
| `polaroid get-binding ID` | `GET /v1/bindings/{id}` |
| `polaroid get-binding-revision ID N` | `GET /v1/bindings/{id}/revisions/{n}` |
| `polaroid revise-binding ID [FILE]` | `POST /v1/bindings/{id}/revisions` |

`FILE` defaults to stdin, and so does `-`. The server is `-server URL`, else `$POLAROID_URL`, else `http://127.0.0.1:7417`.

## Compatibility

Within `/v1`, changes are additive only: new endpoints, or new optional response fields. Clients must ignore response fields they do not know. Requests stay strict, so a client sending a field the server does not yet support gets `400`, not silent data loss. A breaking change needs a new version prefix and an ADR.

Listing is unbounded today. Pagination, if added, will be opt-in through new query parameters, so existing clients keep receiving complete lists. Until then, `GET /v1/bindings` rejects any query parameter other than `repository`.
