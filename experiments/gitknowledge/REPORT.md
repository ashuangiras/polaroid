# Git-native living knowledge: research report

Local experiment, not a production change. Branch `exp/git-native-knowledge` (local only, from `main` at `c929222`); PR #132, #128 and #130 untouched. Everything here lives in its own Go module, so Polaroid's `go.mod`, storage and APIs are unchanged.

## Verdict

**Git works, but it does not make Polaroid simpler.** Most of the complexity Polaroid carries today comes from its version, binding and execution model, not from SQLite. Moving to active knowledge (one stable key, one published state, atomic publication, history by snapshot) removes it whichever store sits underneath. On SQLite that takes 297 lines and 4 tables. On go-git the same contract takes 730 lines and is slower on every local operation measured.

What Git uniquely adds is **knowledge exchange between independent instances**: fetching only new objects, telling divergence from progress, and keeping every snapshot readable on both sides. SQLite would need a new history model and a sync protocol to match it.

**Recommendation: option 2, Git as the authoritative knowledge store, if exchange between autonomous instances is a real requirement** (the brief says it is). Execution, feedback and audit records stay in SQLite. If exchange is not needed soon, take option 1 instead: the new semantics on SQLite.

## 1. Current responsibilities

Today's production storage has 18 tables, 61 triggers and 10 migrations, with 2,665 lines in `internal/storage/sqlite` and 5,483 in `internal/memory`.

| Capability | Current owner | Git-native candidate | Keep, replace or retire |
| --- | --- | --- | --- |
| Active procedure | latest version, or a binding's pin, contextual or evidence policy | the tree at `refs/heads/active` | **Replace.** One active state, no version selection |
| Historical versions | `procedure_versions` rows plus immutability triggers | immutable blob, tree and commit objects | **Replace.** Snapshot = commit id |
| Knowledge revision | `revise_procedure` with `base_version` | a new commit | **Replace** |
| Knowledge snapshot | a resolved graph per call | the commit id | **Replace.** One id fixes every procedure a task reads |
| Concurrent changes | `version_conflict` on a stale base | compare-and-swap of the active ref (expected snapshot) | **Replace** (same semantics) |
| Knowledge transfer | none (backup and restore of a whole catalog) | fetch, then validate, then explicit accept | **New capability** |
| Discovery | SQL over the latest versions | in-memory index of the active tree, rebuildable | **Replace** |
| Bindings and version policies | `bindings`, `binding_revisions`, pin, contextual, evidence | repository inputs only; versions disappear | **Retire** version selection; keep repository inputs (future work) |
| Composition | references by procedure id and policy | references by stable key, resolved at one snapshot | **Replace** |
| Repository identity and applicability | `repositories`, `repository_identifiers`, per-version scope | `applicability` in the document, validated on publish | **Keep the rule, move the data** |
| Execution history and verification | `executions`, children, decisions, invalidations | external or audit store | **Preserve for now** (SQLite) |
| Feedback and toil | `feedback`, `toil_*` | not knowledge | **Preserve** (SQLite); feedback cites key and snapshot |
| Lifecycle (retire, reactivate) | lifecycle events | deleting the key from the active tree (history keeps it) | **Replace** |

## 2. What was built

These packages live in the experiment module; reproduction commands are in section 9:

- **`knowledge`** (443 lines of code):
  - the document;
  - canonical encoding with strict decoding, so non-canonical bytes and unknown members are refused;
  - the `Store` contract: `Head`, `ReadActive`, `ReadAt`, `Keys`, `Publish(expected, changes)`, `History`;
  - `Validate`, which checks key and path identity, required goal and method, sorted inputs, reference existence, input compatibility (a child dropping an input its parent maps is refused), applicability, required shared guidance, cycles, depth, size and count limits;
  - `Resolve`, which reads a whole graph at one snapshot;
  - `Revert`, which is a new publication.
- **`sqlitestore`** (297 lines): same driver and pragmas as production (`modernc.org/sqlite` v1.60.1, WAL, `synchronous=full`). Four tables (`blobs`, `snapshots`, `changes`, `head`) and 6 immutability triggers. Linear history. Snapshot id = sha256 of parent, changes and note. Writes are serialised by `BEGIN IMMEDIATE`.
- **`gitstore`** (730 lines, plus 275 for transfer): go-git v5.19.3 on a bare repository.
  - Documents are blobs at `procedures/<key segments>.json`, a snapshot is a commit, and `refs/heads/active` is the active snapshot.
  - The index is in memory and rebuilt from the tree; there is no second database.
  - Two write modes: loose objects (go-git's default) and one packfile per publication.
- **`knowledge/storetest`**: the shared behavioural suite. Both adapters pass it unchanged, and the git adapter passes it in both write modes.

**Shared behaviour, steps 1–12 of the brief** (`storetest.workflow`):

1. Empty store.
2. Publish the initial tree: `agent.guidance.shared`, `go.module.verify` → `build` and `checks`.
3. Read the active root.
4. Resolve the root at that snapshot.
5–6. Improve `checks` and publish.
7. New tasks see the improved child; the unchanged `build` keeps its hash.
8. The old snapshot still resolves to the old child.
9. A stale update is refused and the head does not move.
10. A bad change is reverted by a new publication; the bad snapshot stays auditable.
11. The full history is retrieved with the keys each publication changed.
12. After a restart, the head, old snapshots, history and the next publish all work.

**Other shared checks:**

- **Concurrency:** 8 racing writers from one snapshot produce exactly one winner. This holds within one handle and across two independently opened handles on the same data, standing in for two processes.
- **Reads during publication:** readers of an old snapshot and of active knowledge run while another handle publishes 20 times, and they always see complete, unchanged snapshots.
- **Invalid publications:** 12 cases are refused with the reason logged and the head unmoved.

**Transfer tests** (git only):

- **Initial transfer:** fetched knowledge is invisible until accepted, then B resolves exactly what A does, with the same commit ids.
- **Incremental transfer:** one changed procedure sends exactly 6 objects (about 730 bytes).
- **Divergence:** both sides report it with the merge base, and neither active state moves. An explicit `Reconcile` on B records both parents, and A then fast-forwards to it.
- **Untrusted remote:** C can fetch but `Accept` returns `ErrUntrusted`.
- **Rejected remote knowledge:** 9 hostile trees are refused, B's head stays put, and the rejected snapshot stays unreadable:
  - a missing reference;
  - a non-canonical document;
  - a document with an unknown member (`"run":"rm -rf /"`);
  - a symlink;
  - an executable file;
  - an extra root file;
  - a `..` path (go-git itself refuses it);
  - a 1 MiB blob;
  - a document under the wrong identity.
- **Tainted history:** a valid tip on top of an invalid intermediate snapshot is refused, because accepting it would make that snapshot readable.
- **Size limit:** a fetch over the limit is refused before validation.
- **Backup and damage:** a backup made by copying the directory restores everything. A damaged object reads as `zlib: invalid checksum`, never as wrong content. A replica can rebuild an empty store.
- **Interrupted publish:** orphan objects and a temp file left by a crash do not change the active knowledge. An emptied ref is an error, never "empty store".

**Mutation checks** (temporary copies, source untouched): removing compare-and-swap, the reachability guard, trust, import validation, divergence detection, or the storage lock each makes a test fail. Without the storage lock, the race detector reported 64 races inside go-git in pack-per-publication mode.

## 3. go-git findings (v5.19.3)

- **Version and licence:** Apache-2.0. The module requires `go 1.26.0` and supports "the last 3 stable Go versions"; v6 is still a beta.
- **Pure Go:** bare repositories, filesystem and in-memory storage, building and reading blobs, trees and commits, tree diff, ancestry and merge base, pack encoding, and in-process upload-pack all work in pure Go.
  - Its `file://` transport runs the `git-upload-pack` binary instead, so the prototype registers an in-process scheme built on go-git's server.
  - Networked instances would use go-git's https or ssh clients.
- **Durability and atomicity are not provided:**
  - **No fsync anywhere** in object, pack or ref writes.
  - **Refs are rewritten in place:** go-git updates a ref under flock by truncating and rewriting the file, so a crash can leave an empty ref, and a concurrent reader can see it. The prototype writes `active` itself: temp file, fsync, rename, fsync of the directory, under a flock that dies with its process.
  - **Objects are fsynced by Polaroid:** loose objects or the publication's pack, before the ref moves.
- **Not safe for concurrent use with packs:** the pack index map and pack handle cache are built lazily and replaced by `Reindex` and by pack writes with no synchronisation. All object access in the prototype goes through one mutex (`lockedStorage`). A pack written by another process is invisible until `Reindex`, so a miss is retried once after reindexing.
- **Fetch fails on local-only commits:** go-git's own upload-pack server returns `object not found` when a client advertises a commit the server lacks. The prototype advertises only the remote's last fetched state. A standard Git server ignores such commits.
- **Loose objects are expensive:** one file open per object read, even when the object is cached, and one file plus temp file plus rename per object written. Pack-per-publication cuts initial publish 42× and steady publish about 2×, but packs accumulate: periodic `RepackObjects` (which works) and pruning become an operational duty.
- **Security:**
  - Validation never executes content, and there is no working tree, so no path ever reaches the disk. go-git refuses `..` tree entries itself.
  - Commit hashes prove content identity, not authenticity: trust is a separate policy (`TrustedRemotes`), and nothing is activated without `Accept`. Signature verification (`Commit.Verify`) is available but not used.
  - A fetch's size can only be checked after it lands; a byte cap during transfer needs a limiting transport or a deadline.
  - govulncheck v1.8.0: no vulnerability reachable from the code. The modules go-git requires carry 6 vulnerabilities: 5 in `x/net` v0.57.0 (fixed in v0.60.0) and 1 in `x/crypto` with no fix. Adopting go-git would mean managing those versions.

## 4. Measurements

**Setup:**

- **Host:** Apple M1 Pro, 32 GiB, macOS 26.6.1, go1.27.2.
- **Runs:** 3 per case; the table shows the median of runs and, inside each run, the median and p99 of the repetitions.
- **Data:** about 1.3 KiB per document, groups of 10 with an 11-node resolution of depth 4, plus the guidance. Publish timings are measured over 30 single-procedure and 10 ten-procedure publications.
- **Host costs, which inflate every file-per-object design:** open+close 153 µs, create+write+rename 533 µs, create+write+fsync 5.3 ms.
- **Not measured:** Linux.

| | n | SQLite | git (loose) | git (pack per publish) |
| --- | ---: | ---: | ---: | ---: |
| Initial publish, ms | 100 / 1k / 10k | 5 / 37 / 427 | 726 / 4,792 / 57,113 | 45 / 172 / 1,374 |
| Publish 1 procedure, p50 ms | 100 / 1k / 10k | 0.42 / 1.6 / 14.8 | 99 / 110 / 121 | 44 / 49 / 65 |
| Publish 1, worst (first after load, cold validation), ms | 10k | 369 | 3,641 | 400 |
| Publish 10 procedures, p50 ms | 10k | 15.6 | 374 | 74 |
| Read active (warm), p50 / p99 ms | 10k | 0.008 / 0.016 | 0.153 / 0.572 | 0.146 / 0.356 |
| Read at an old snapshot (warm), p50 ms | 10k | < 0.001 | 0.001 | < 0.001 |
| Cold read after restart, p50 / p99 ms | 10k | 0.049 / 0.098 | 0.421 / 1.36 | 0.250 / 0.676 |
| Resolve 11 nodes (warm), p50 ms | 10k | 0.004 | 0.008 | 0.008 |
| Stale-publish detection, p50 ms | 10k | 0.035 | 0.347 | 0.314 |
| Restart plus first read, ms | 100 / 10k | 3.4 / 24.0 | 12.6 / 219.5 | 25.4 / 40.3 |
| Storage after 41 publications, KiB (files) | 10k | 22,284 (1) | 5,089 (11,538) | 2,418 (86) |
| … after `RepackObjects`, KiB | 10k | – | 2,266 | 2,219 |
| Go heap / peak RSS, MiB | 10k | 60 / 148 | 88 / 202 | 92 / 221 |
| Initial transfer, ms / KiB | 10k | 84 / 22,100 (`VACUUM INTO` copy) | 17,773 / 1,945 | 5,136 / 1,899 |
| Incremental transfer after one change | 10k | **no mechanism** | 818 ms, 3,698 B, 7 objects | 728 ms, 3,699 B, 7 objects |

The figures above are medians from `bench/durable.json`; the same three adapters without fsync, measured only to attribute cost, are in `bench/nosync.json`. Both are written by the commands in section 9 and are not committed.

**Where the time goes:**

- **Git publishing:** 34 of the 65 ms per pack-mode publish at 10k remain with fsync off, so roughly half is this host's fsync and the rest is file operations and validation.
- **SQLite publishing:** 14.4 of 14.8 ms remain with fsync off; that time is whole-tree validation, which both adapters share and which could be made incremental for both.
- **Transfer:** mostly validating every imported snapshot (41 commits × 10k entries for the initial transfer), not moving bytes.
- **Storage:** Git compresses documents (zlib, deltas on repack); the SQLite adapter stores them raw and could compress too.

**Maintainability:**

| | SQLite adapter | go-git adapter |
| --- | ---: | ---: |
| Code for the shared contract, non-blank lines | 297 | 730 |
| Transfer code | none (no mechanism) | 275 |
| Dependencies Polaroid does not already have | 0 | 18 modules (go-git, go-billy, ProtonMail/go-crypto, circl, ssh libraries…) |
| Storage invariants Polaroid itself must enforce, beyond shared validation | 2: compare-and-swap inside `BEGIN IMMEDIATE`, immutability triggers | 9, listed below |
| Operational duties | `VACUUM INTO` backups | repack and prune, copies only while idle (or by fetch), monitoring go-git's required module versions |

The nine go-git invariants are:

1. A durable, atomic active ref.
2. fsync of objects before the ref moves.
3. A cross-process flock.
4. One lock around all go-git object access.
5. Reindex when another process has written a pack.
6. Fetch advertising only commits the remote is known to hold.
7. Fetched commits stay unreadable until accepted (reachability guard).
8. Structural validation of trees (paths, modes, root entries).
9. Validation of every imported snapshot, not only the tip.

## 5. Portability and durability, answered

1. **Backup by copying or mirroring the repository?** Yes, if no publication is in progress; a copy taken during one may hold objects without the ref, which is harmless, or the old ref. A fetch into another store is always consistent. Tested.
2. **Restore without SQLite?** Yes: open the copy, or fetch and accept into an empty store. Tested.
3. **Transfer between disconnected systems?** Yes, by carrying the repository directory and fetching from it. go-git v5 has no `git bundle` support.
4. **Exchange without sharing a database?** Yes: fetch, validate, then explicit `Accept`, with divergence reported and explicit `Reconcile`. Tested.
5. **Reproducible snapshots?** Content identity, yes: a commit id fixes every byte of every procedure (SHA-1 with collision detection, `sha1cd`). Both instances read identical hashes. Availability is a separate matter: an object that is damaged or pruned is lost unless a replica holds it.
6. **Interrupted writes, corruption, failed synchronisation?**
   - **Interrupted publish:** leaves only unreachable objects; the active snapshot is unchanged. Tested.
   - **Damaged ref:** reported as an error. Tested.
   - **Damaged object:** detected on read in the test (zlib checksum). Whether go-git re-hashes objects on read was not established. Tested.
   - **Failed or rejected fetch:** leaves an incoming ref and its objects, never an active change. Tested.
7. **Recovery procedure:**
   - Stop the daemon.
   - Restore the directory from a copy, or fetch from a trusted replica into an empty store and `Accept`.
   - For a damaged ref, set it to the last known snapshot from a backup or replica.
   - Prune unreachable objects after a repack.
8. **What stays outside Git:**
   - fsync and atomic refs (Polaroid's code);
   - locking;
   - validation and the trust and publication policy;
   - object retention (repack and prune);
   - size limits during transfer;
   - a reflog of who accepted what (not built);
   - execution, feedback and audit records.

## 6. Agent-facing interface

This experiment leaves `discover`, `read` and `feedback` unchanged. In the proposed model:

- **`read`:** returns active knowledge plus one compact snapshot id. The agent passes that id back to read referenced children consistently. It never sees branches, commits or version numbers.
- **`feedback`:** cites key and snapshot.
- **`discover`:** indexes the active tree.

## 7. Recommendation and its risks

**Option 2: Git as the authoritative knowledge store.** SQLite stays for executions, feedback and toil, and is not needed as a knowledge index at the measured sizes: rebuilding the index in memory takes 40 ms at 10k procedures in pack mode.

**Conditions:**

- One daemon process writes.
- Each publication writes one pack, with scheduled repacking.
- Polaroid owns the durable ref writes and the fsync layer, as built here.
- Snapshots are validated before they become readable.
- An explicit trust and acceptance policy decides what gets published.

**Why not option 3 (Git only):** executions and audit records are not knowledge, and nothing measured favours moving them.

**Why not option 1 (SQLite with Git-inspired semantics), despite better local numbers:**

- SQLite has the same active-knowledge semantics in less code, but its history is linear. Exchange with divergence needs a DAG of snapshots, merge bases, negotiation of what each side has, and delta transfer: Git's core, which would have to be rebuilt.
- At the real catalog's scale (tens of procedures, a few publications a day), Git's costs are irrelevant to agents: about 45 ms per publication and 0.15 ms per active read.
- **If exchange is not a near-term need, choose option 1:** less code, no new dependencies, faster.

**Risks:**

- **go-git:** its concurrency and durability gaps must stay wrapped. v6 may change the APIs. Its required `x/net` and `x/crypto` versions must be bumped and monitored.
- **Size and scale:**
  - Whole-tree validation is O(n) per publication, for both adapters; it could be made incremental.
  - Import validation of long histories is slow (5 s for 41 snapshots at 10k).
  - Pack accumulation needs maintenance.
- **Not tested:** behaviour on Linux and on network filesystems; Git-level authentication (signed commits) for trust beyond remote names.
- **Divergence handling:** it needs an operator, or an explicit `Reconcile`; automatic merge is intentionally absent.

## 8. Migration assessment (not implemented)

1. **Export, read-only.** Replay every stored procedure version in creation order as one commit per version: key = canonical key; author, time and message from the version. Record a deterministic map from (procedure id, version) to commit, for audit. Re-running the export must produce identical commit ids, which is the test.
2. **Translate composition.** Pins and contextual policies become references by key. A pin to an old child version conflicts with "active" semantics. Each one is listed for an explicit owner decision before the first active snapshot is published; the export refuses to guess.
3. **Shadow.** Serve `/mcp/agent` `read` from the Git store alongside SQLite and compare answers (key, content hash) on every read, until they agree on the whole catalog.
4. **Switch writes.** New publications go to Git through `Publish`. The legacy version and binding endpoints become read-only and are answered from the frozen SQLite history.
5. **Re-point records.** New executions and feedback cite (key, snapshot). Old records keep (procedure id, version) and are joined through the map. Nothing is rewritten.
6. **Retire.** Removing the legacy write endpoints is a breaking API change (ADR-0038): it needs a major version and a deprecation window.

## 9. Reproduce

From `experiments/gitknowledge` (needs go1.27.2):

```sh
go test -race ./...                       # shared behaviour (both adapters, both git modes), transfer, damage
go test -v -run 'TestTransfer|TestRejected' ./gitstore/   # logs object counts and every rejection reason
go build -o bench/knowbench ./cmd/knowbench
./bench/knowbench -sizes 100,1000,10000 -runs 3 -adapters sqlite,git,git-pack -out bench/durable.json
./bench/knowbench -sizes 100,1000,10000 -runs 1 -adapters sqlite-nosync,git-nosync,git-pack-nosync -out bench/nosync.json
```
