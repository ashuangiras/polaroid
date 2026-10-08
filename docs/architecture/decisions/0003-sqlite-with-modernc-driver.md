# 0003. SQLite through the pure-Go `modernc.org/sqlite` driver

**Status:** Accepted
**Date:** 2026-10-09

## Context

Polaroid needs durable, transactional local storage with uniqueness constraints and safe concurrent writers. It must work in a single-binary deployment, and the build should be straightforward on developer machines and in CI. The candidate drivers:

- `github.com/mattn/go-sqlite3` (MIT) requires cgo and a C toolchain.
- `github.com/ncruces/go-sqlite3` (MIT) runs SQLite compiled to WebAssembly through wazero.
- `modernc.org/sqlite` (BSD-3-Clause) is SQLite transpiled to Go.

## Decision

- Use `modernc.org/sqlite` v1.60.1, which bundles SQLite 3.53.4, through `database/sql`.
- Connections set `journal_mode=WAL`, `synchronous=FULL`, `foreign_keys=on`, `busy_timeout=5000` and `_txlock=immediate`. Every write transaction therefore takes the write lock at `BEGIN`, and concurrent writers queue instead of failing mid-transaction.
- The schema is a sequence of embedded SQL migrations, counted by `PRAGMA user_version`.

## Consequences

- No cgo is needed. `go build` works anywhere Go does, and the race detector runs with this driver: verified with `go test -race ./...` on darwin/arm64.
- Ten modules are compiled into `polaroidd`, all MIT or BSD-3-Clause. They are listed and checked in [dependencies.md](../../development/dependencies.md).
- The database is a single file and a single writer. Horizontal scaling would need another `Store` implementation behind the same interface.
- Upgrading the driver upgrades the bundled SQLite. Re-run the storage tests, including the trigger and concurrency tests, when upgrading.
