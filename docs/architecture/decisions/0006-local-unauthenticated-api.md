# 0006. A loopback-only, unauthenticated API until access control exists

**Status:** Accepted
**Date:** 2026-10-09

## Context

Polaroid stores instructions that agents follow. Anyone who can write a procedure can steer agents, so write access is security-sensitive. Access control is planned but not designed. Until it exists, the first increment must be safe to run on a developer machine. A daemon on `localhost` can still be reached by web pages in the user's browser through cross-site requests and DNS rebinding.

## Decision

- `polaroidd` listens on `127.0.0.1:7417` by default. A non-loopback `-addr` is allowed but logs a warning that the API has no authentication.
- When the daemon listens on loopback, requests whose `Host` header is not `localhost`, `127.0.0.0/8` or `::1` are rejected with `403`. This defeats DNS rebinding.
- Unsafe cross-origin browser requests are rejected with `403`, using `net/http.CrossOriginProtection`. Writes require `Content-Type: application/json`.
- Bodies are limited to 1 MiB, and the server sets read, header, write and idle timeouts. Internal errors are never returned to clients.

## Consequences

- Polaroid is safe to run locally for one user. It is not safe to expose on a shared network.
- Access control (identity, authorization of writes, possibly signed records) remains a roadmap item. It will need a new ADR, and possibly a new API version.
- Clients that reach the daemon through a proxy must forward a loopback `Host` header, or the loopback check must be made configurable. That would be a deliberate future change.
