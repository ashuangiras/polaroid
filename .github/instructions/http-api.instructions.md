---
applyTo: "internal/transport/http/**,cmd/polaroid/**"
description: "Use when changing HTTP endpoints, request or response bodies, API errors, or the polaroid CLI."
---
# HTTP API and CLI rules

- [docs/architecture/http-api.md](../../docs/architecture/http-api.md) is the API contract. Update it in the same change as any endpoint, field, status code or error code.
- Within `/v1`, only make additive, backward-compatible changes. A breaking change needs a new version prefix and an ADR.
- Every response, errors included, is JSON. Error bodies are `{"error":{"code","message",...}}`, using the codes listed in the contract. Unrecognised errors are logged and returned as `internal`, without detail.
- Request decoding is strict. Unknown or duplicate members, a wrong `Content-Type` and oversized bodies are all rejected. Do not relax this to accept planned fields early.
- The `*JSON` structs in the tests restate the documented response shapes, and they decode with unknown members rejected. Update them when the contract changes, not to silence a failure.
- The CLI writes each response body to stdout, also on failure, and a diagnostic to stderr. Its exit codes are 0 for success, 1 for a failed request and 2 for a usage error. Treat them as a compatibility contract.
