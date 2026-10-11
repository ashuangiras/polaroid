# 0039. Multi-statement reads use read-only snapshot transactions

**Status:** Accepted. Amends [ADR-0003](0003-sqlite-with-modernc-driver.md) (connections) and [ADR-0009](0009-reference-graph-rules.md) (graph reads). Changes no stored record, request or response.
**Date:** 2026-10-11

## Context

Graph walks, binding resolution and verification need one query per node, so they read inside one transaction to see one snapshot. That transaction was the write transaction, `Store.write`. Every connection begins with `_txlock=immediate`, so each of these reads took SQLite's write lock at `BEGIN`. That had two effects:

- The reads queued with every writer, and writers queued behind them.
- While another program held the lock, or the daemon wrote continuously, a read waited out the 5-second `busy_timeout` and failed with `500 internal`.

The sustained run for `v1.0.0` showed this. With 4 clients writing without pause, one of 29,390 binding resolutions failed ([#111](https://github.com/ashuangiras/polaroid/issues/111)).

The driver (`modernc.org/sqlite` v1.60.1, `tx.go`) begins a transaction with `sql.TxOptions{ReadOnly: true}` as a plain `BEGIN`, ignoring `_txlock`. In WAL mode, such a deferred transaction takes no lock other than a WAL read mark. Its snapshot is fixed by its first statement and held until it ends.

## Decision

- **Read helper.** Multi-statement reads (`Resolve`, `ExecutionVerification`, `Verifications`) run in `Store.read`, which works in four steps:
  1. it begins a read-only transaction;
  2. it reads `PRAGMA schema_version` at once, so the snapshot is the moment the read began;
  3. it runs the walk;
  4. it always rolls back, on success, error or cancellation.
- **Writes are unchanged.** `Store.write` keeps `_txlock=immediate` and `busy_timeout` 5 s. Conflict detection ("base equals latest" inside the write) is unchanged.
- **The graph check on write stays in the write.** The graph check that runs when a version is written (ADR-0009) stays inside its write transaction: it must see the version it checks.
- **Single-statement reads are unchanged.** All other reads are single statements on the pool, which SQLite already runs as one snapshot without a lock.
- **No retries, no longer timeouts.** Neither is a substitute for the snapshot.

## Consequences

- **Reads beside a held lock.** A graph, resolution or verification answers while another connection holds the write lock, without waiting. It does not see that connection's uncommitted rows.
- **One snapshot.** A write committed while a walk runs is invisible to the whole walk, and visible to the next read. Tests show this with a barrier inside the open snapshot.
- **Readers no longer block writers.** A reader holds no lock that a writer waits for. A long read still holds its WAL read mark, which delays a full checkpoint until it ends, as any SQLite reader does.
- **Unchanged.** Writes that cannot take the lock within 5 seconds still fail with `500 internal` and store nothing. Each write still waits for the previous one.
- **Compatibility.** No API, record, schema or migration change. Responses are byte-for-byte what they were for the same catalog.
- **Supersedes** ADR-0009's consequence that "graph reads take the write lock for their snapshot", and ADR-0003's implication that every transaction takes the write lock: now every *write* transaction does.
