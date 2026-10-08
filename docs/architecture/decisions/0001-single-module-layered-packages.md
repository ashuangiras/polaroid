# 0001. One Go module with layered internal packages

**Status:** Accepted
**Date:** 2026-10-09

## Context

Polaroid needs a domain model (procedures and versions, and later bindings, references and executions), persistence, an HTTP API and two binaries. It will be developed mostly by coding agents. Agents work best when a boundary is enforced mechanically rather than by convention.

## Decision

- Use one repository and one Go module, with these packages:
  - `internal/memory`: the domain.
  - `internal/storage/sqlite`: persistence.
  - `internal/transport/http`: the API.
  - `cmd/polaroidd` and `cmd/polaroid`: the binaries.
- Dependencies point inward. `memory` uses only the standard library. Storage and transport depend on `memory`, never on each other. Only `cmd/polaroidd` wires them together.
- `memory` defines the one interface it needs, `Store`. No other abstractions are added until a second implementation or a test needs one.
- `internal/archtest` enforces the import rules with `go list -deps`, and the boundary violation test has been shown to fail.

## Consequences

- Each package is tested on its own: domain rules in `memory`, persistence in storage, and the full stack through HTTP.
- New transports, such as MCP, and new storage engines plug in at the existing boundaries.
- There are no separate versioned modules. Internal APIs can change freely; the `/v1` API and the record contracts are the compatibility surface.
