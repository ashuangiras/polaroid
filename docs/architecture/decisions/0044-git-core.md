# 0044. Git-core: a Git repository as the durable store of living knowledge

**Status:** Accepted (owner direction, 2026-10-13). Milestones A–C implemented (B: [ADR-0045](0045-living-knowledge-kernel.md); C: [ADR-0046](0046-git-knowledge-synchronisation.md)); milestones D–E planned. Polaroid's behaviour, APIs and SQLite catalog are unchanged until milestone D connects the kernel.
**Date:** 2026-10-13

## Context

Polaroid is moving from versioned procedure records toward living procedural knowledge. Each procedure has one stable key and one published definition, and a snapshot of the whole tree fixes what a task reads. Independent instances should keep, exchange and selectively activate knowledge through ordinary Git repositories.

A prototype compared SQLite and go-git behind the same interface ([experiment report](https://github.com/ashuangiras/polaroid/blob/exp/git-native-knowledge/experiments/gitknowledge/REPORT.md), branch `exp/git-native-knowledge`). Git matched SQLite's behaviour and was slower on this machine. It was the only one that could exchange history between instances, with divergence detection. The prototype also found that go-git:

- never fsyncs;
- rewrites refs in place;
- is not safe for concurrent use once packs exist;
- fails as an upload-pack server when a client offers commits it lacks.

## Decision

- **Git is the target authoritative store of active procedural knowledge.** The work is staged:
  - A: Git-core (this record);
  - B: the knowledge kernel on it;
  - C: remote synchronisation;
  - D: the agent interface on it;
  - E: migration from the SQLite catalog.

  Execution, feedback and verification records stay in SQLite. SQLite never also holds the living knowledge.
- **Git-core (`internal/gitcore`) stores files, not procedures.**
  - **Contents:** a bare repository holds paths and contents, snapshots (commit ids), history, and one active ref, `refs/heads/active`, with `HEAD` pointing to it.
  - **Validation by the caller:** procedure rules belong to the caller, through `Publication.Validate`, which sees the complete proposed tree before anything is written.
  - **Boundary:** `internal/archtest` keeps Git-core free of `memory`, transports, SQL and recovery, and keeps `memory` free of Git-core.
- **Publication protocol.** Every step runs under an exclusive lock: a mutex within the process, `flock` across processes, released by the kernel on any exit.
  1. Compare the expected snapshot with the active one; a mismatch is `ErrStale`.
  2. Build the proposed tree, check it (safe paths, size and count limits, no file that is also a directory, no paths that differ only in case), and pass it to `Validate`.
  3. Write each new object as a loose object: temp file, fsync, rename, fsync of the directory.
  4. Replace the active ref the same way.
  5. Append Git's reflog.

  Go's `File.Sync` is `fsync` on Linux and `F_FULLFSYNC` on macOS. A crash before step 4 leaves the previous snapshot active, plus unreachable objects. A crash after it leaves the new snapshot active, possibly without its reflog line, which `Verify` reports. Temp files left by a crash are removed by the next publication, under the lock.
- **Git-core writes objects and the active ref itself; go-git only reads and decodes.**
  - Writes through go-git's storage are refused.
  - Every go-git object access goes through one mutex.
  - A missed object triggers one reindex, so packs written by Git tools or another process are found.
  - Loose objects (one file per object) keep publications independent and immediately visible to other processes; packs arrive only from Git tools or, later, fetch.
- **Reads.** `Tree(snapshot)` serves only published snapshots (the active one and its ancestors), so fetched or orphaned commits are invisible. Blob content is rehashed against its id on every uncached read, because go-git does not check it. `packed-refs` is read when a Git tool has moved the ref there.
- **Provenance** is the commit: author (who authorised), subject, body, and `Key: Value` trailers that any Git tool reads.
- **Integrity and recovery.**
  - `Verify` rehashes every object reachable from the active snapshot, checks paths and modes, and compares the reflog with the ref.
  - `Recover` makes the newest snapshot in the reflog that verifies completely active again, and logs the move.

## Consequences

- **Dependencies:** go-git v5.19.3 and the 12 other modules its plumbing, object and storage packages compile (listed in [dependencies.md](../../development/dependencies.md)); `golang.org/x/net` is raised to v0.60.0. No transport is compiled yet. govulncheck finds no reachable vulnerability; it lists one advisory, GO-2026-5932, for the uncompiled `x/crypto/openpgp`.
- **Measured on the development Mac (Apple M1 Pro, `F_FULLFSYNC` about 5 ms):**
  - one publication: about 120 ms with 100 files, about 160 ms with 1,000;
  - an active read (ref plus tree plus file): about 0.4 ms warm;
  - opening and reading one file at 1,000 files: about 24 ms.

  Bulk loading costs one fsync per object, which matters for a large migration (milestone E).
- **Not established:**
  - behaviour after real power loss (only process crashes are tested);
  - network filesystems (unsupported);
  - Windows (publication needs `flock`).
- **Open for later milestones:** object retention and garbage collection, pruning unreachable objects, signatures and trust, remotes and their credentials, size caps during transfer, and how existing procedure versions map to keys and snapshots.
