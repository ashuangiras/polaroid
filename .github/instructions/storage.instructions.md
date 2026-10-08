---
applyTo: "internal/storage/**"
description: "Use when changing SQLite storage, migrations, transactions or schema triggers in internal/storage."
---
# Storage rules

- Make a schema change only by adding the next numbered file, `internal/storage/sqlite/migrations/NNNN_name.sql`. Applied migrations are history. Never edit or renumber one. `PRAGMA user_version` counts the applied migrations. `Open` refuses a database whose schema is newer than the build.
- The triggers that reject `UPDATE` and `DELETE`, and gaps in version numbers, are part of the immutability guarantee. Removing or weakening them needs an ADR.
- Writes go through `Store.write`, one transaction that takes SQLite's write lock at `BEGIN` (`_txlock=immediate`). Each read is a single statement, so it sees one consistent snapshot. Do not split a read that must be consistent across several queries.
- Map driver errors to `memory` errors, such as `ErrNotFound`, `ErrCanonicalKeyExists` and `*VersionConflictError`, using result codes, not message text. Any other error is internal.
- Tests use real database files in `t.TempDir()`. Concurrency tests release goroutines from a shared barrier and never sleep. Cover close and reopen for anything that persists.
