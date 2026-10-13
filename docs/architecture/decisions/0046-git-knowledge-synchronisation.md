# 0046. Git-native knowledge synchronisation

**Status:** Accepted (owner direction, 2026-10-13). Milestone C of [ADR-0044](0044-git-core.md), implemented on branch `git-core`. No daemon, API, MCP tool, CLI command, record or schema changes: synchronisation is a library that nothing outside its tests calls until milestone D.
**Date:** 2026-10-13

## Context

[ADR-0044](0044-git-core.md) chose Git so that independent Polaroid instances can exchange knowledge through ordinary Git repositories. [ADR-0045](0045-living-knowledge-kernel.md) put validated, composable knowledge on Git-core. Milestone C has to make the exchange safe. Polaroid A publishes knowledge; a remote Git repository preserves it; Polaroid B fetches it, validates it and explicitly activates it.

These requirements shape the design:

- Incoming data must never become active just because a fetch succeeded.
- Transfers must be bounded.
- Knowledge must not bypass the kernel.
- Divergence must be explicit and lose nothing.
- Credentials must stay out of history, logs and responses.
- GitHub must work as an ordinary remote, with no GitHub API.
- No LLM may sit in the synchronisation loop.

Several go-git limits found by the earlier experiment affect the design:

- it does not fsync;
- its in-process server does not compare the old value on a push;
- its upload-pack server fails on unknown client haves;
- `Remote.Fetch` writes a pack into storage with no bound.

## Decision

- **Three layers, each with one job.**
  - **`internal/gitcore`** handles Git objects, refs, transport and integrity.
  - **`internal/knowledge`** handles knowledge validity and activation.
  - **`internal/knowledgesync`** is the synchronisation layer. It holds the trust policy, validates candidates, classifies divergence and calls activation.
  - The agent MCP interface is unchanged and exposes no Git operation.
  - `internal/archtest` enforces the layering:
    - Git-core imports no knowledge package;
    - the kernel imports no Git;
    - synchronisation imports no transport, legacy record or SQL;
    - no legacy package imports any of the three.
- **Transports.** go-git's client transports are used directly, at the protocol level.
  - **`file`:** an absolute local path. It runs the installed `git-upload-pack` and `git-receive-pack`, so the remote's ref updates are Git's own, atomic and compare-and-swap.
  - **`https`:** basic authentication, with a token as the password. An optional PEM CA bundle is supported per remote. Plain `http` is refused.
  - **`ssh`:** an explicit private key, plus a `known_hosts` file that must verify the server.
  - `git://` and go-git's in-process server are not used.
  - The remote's branch is configurable; the default is `main`.
- **Credentials** are a value passed to each call.
  - They are never stored, logged or written into a URL; their `String` form is redacted.
  - A URL that holds a password or token, or any user name except in an SSH URL, is refused before any connection.
  - Error text never contains a credential; the tests check this.
- **Fetch quarantines.**
  - `Repository.Fetch` asks for the remote branch's commit. Its "haves" are the active and tracked snapshots, all verified when stored.
  - The request removes thin-pack, side-band and multi-ack, so the remote sends one plain, self-contained pack.
  - The pack is spooled into a private quarantine directory, `polaroid-quarantine/q-*`, held by an `flock`. A crashed fetch's quarantine is removed by the next fetch; a live one is never removed.
  - The transfer bounds apply before go-git indexes anything:
    - spooling stops at `MaxPackBytes`;
    - every object header is then checked against `MaxObjects` and `MaxObjectBytes`;
    - every delta's result size is also checked against `MaxObjectBytes`, and the inflated total against `MaxInflatedBytes`;
    - the pack checksum is verified.
  - Only then is the pack indexed into the quarantine's own storage. The repository's object store and refs are untouched, and a failed fetch leaves nothing behind.
- **Git integrity (`Candidate.Verify`).** Verify walks the new commits from the remote's snapshot to the first stored commit, or to a root, and checks:
  - at most `MaxCommits` commits;
  - at most one parent per commit (knowledge history is linear);
  - a well-formed message within the limits and a well-formed author;
  - for every received object, its content recomputed against its id;
  - every new snapshot's tree:
    - safe path segments only;
    - regular files only (no executables, symlinks or submodules);
    - entries in Git's order without duplicates;
    - no paths that differ only in case;
    - depth, path and file-count limits;
    - each file within `MaxFileBytes`;
  - that the pack holds no object outside these snapshots. This keeps unverified objects out of the store, so a stored commit always means a verified commit and everything it reaches.

  Verify reads no content as knowledge and executes nothing.
- **Validation and acceptance (`Syncer.Validate`).** After Verify, every new publication must have:
  - an author listed among the source's publishers;
  - a rationale (a commit body);
  - a valid complete catalog under `knowledge.Load`, the kernel's own read validation.

  Any failure **rejects** the candidate: the quarantine is discarded and nothing changes. A valid candidate is **accepted**:
  - `Repository.Import` moves its pack into the object store, syncing each file and moving the pack before its index;
  - it records the remote's snapshot in the tracking ref `refs/remotes/<source>/<branch>`, by compare-and-swap against the value seen at fetch (a concurrent import is `ErrStale`).

  Accepted knowledge is stored, but not active and not readable through the kernel.
- **Relation to the local knowledge.** It is compared along first parents and decides the candidate's status:
  - `identical`: no action;
  - `fast-forward`: the remote descends from the active snapshot; the only activatable status;
  - `local-ahead`: nothing to do, and nothing is overwritten;
  - `diverged`: an explicit conflict. Both histories are kept, the local one active and the remote one under its tracking ref, for a later reconciliation. Nothing is merged.

  An invalid catalog and an unauthorised publisher or source are both `rejected`.
- **Activation is the only change to the active knowledge.**
  1. `Syncer.Activate` accepts only a `fast-forward` candidate and calls `Kernel.Activate`.
  2. The kernel calls the store's new `Advance` operation. Under Git-core's writer lock, `Advance`:
     - refuses unless the active snapshot is still the one the candidate was validated against (`ErrStale`, so activation never overwrites a newer local publication);
     - refuses unless the target was imported (it must be reachable from a tracking ref);
     - refuses unless the target descends from the active snapshot (`ErrDiverged`);
     - **validates the target's complete catalog again** through the kernel;
     - then moves the active ref.
  3. The incoming commits keep their identities, authors, rationale and `Polaroid-*` trailers.
  4. The local decision is recorded in two places: in the reflog, and as a commit in a local activation journal, `refs/polaroid/activations`. That journal is never pushed; its commits have the empty tree. A journal entry holds:
     - who accepted the knowledge (the commit author) and why (the summary and rationale);
     - `Polaroid-Source` (the source name and URL);
     - `Polaroid-Remote-Snapshot`;
     - `Polaroid-Publishers`;
     - `Polaroid-Accepted` (the validation the kernel performed);
     - Git-core's `Activated` and `Previous` trailers.

  `Kernel.Activations` reads the journal.
- **Publishing.** `Syncer.Publish` pushes the active snapshot only to a source whose policy says `Publish`, and only as a fast-forward:
  - `ErrBehind` when the remote holds a snapshot not stored here (fetch first);
  - `ErrDiverged` when the histories diverge.

  The push names the remote's previous commit, so the remote's own compare-and-swap refuses a concurrent update (`ErrStale`). Nothing is ever forced, and no ref is ever deleted.
- **Trust.** The policy (`knowledgesync.Policy`) is explicit and deterministic:
  - Only listed sources are contacted; any other name is `ErrUnknownSource`, before any network.
  - Each source lists its authorised publishers.
  - A commit's hash identifies its content, not its trust.
  - An author name is an unverified claim. It narrows what is accepted and never grants trust; authorisation rests on the source's own access control, meaning who can push to it.
  - Signature verification is not implemented. It would be a separate capability with explicit key management; signed commits get no extra trust.
- **Restoring from a remote** uses the same path. A fresh or empty instance fetches, validates and activates; activation from the empty snapshot is a fast-forward.
- **The Store port grows by two operations:** `Advance` and `Activations` ([ADR-0045](0045-living-knowledge-kernel.md)'s port, extended), and the kernel gains `Load`, `Activate` and `Activations`. No other versioning or persistence code is added.

## Consequences

- **Tests.** Every network test uses a real Git server:
  - a bare repository made by `git init`, through `git-upload-pack` and `git-receive-pack`;
  - `git http-backend` behind TLS with basic authentication;
  - an in-process SSH server that runs the same Git programs.

  They show the 14-step exchange between two independent instances and a fresh one, plus:
  - interrupted, corrupted and truncated transfers;
  - wrong tokens, unknown SSH keys and untrusted host keys;
  - unsafe trees: hidden names, executables, symlinks, submodules, unsorted or duplicate entries, case collisions, oversized files and merges;
  - unrequested objects, every transfer limit, stale activation and import, a racing activation and local publication, and six racing pushers with one winner.

  Mutation checks fail the tests when any of these guards is removed: the transfer bounds, the merge, mode, order, case and size checks, the unrequested-object check, import and activation compare-and-swap, the tracked and descends checks, push fast-forward and behind checks, the credential-URL check, host-key verification, the publisher, rationale and catalog checks, activation eligibility, unknown sources, and the kernel's re-validation.
- **Measured on the development Mac.** The figures below come from `POLAROID_SYNC_MEASURE=1 go test -run TestMeasureExchange ./internal/knowledgesync/`, over a local bare remote with one procedure changed incrementally.

  | Catalog | Transfer | Objects | Pack bytes | Fetch | Validate | Activate |
  | --- | --- | --- | --- | --- | --- | --- |
  | 100 procedures | initial | 104 | 10,724 | 75 ms | 100 ms | 76 ms |
  | 100 procedures | incremental | 5 | 3,065 | 67 ms | 48 ms | 52 ms |
  | 1,000 procedures | initial | 1,004 | 118,950 | 104 ms | 567 ms | 314 ms |
  | 1,000 procedures | incremental | 5 | 25,481 | 79 ms | 117 ms | 97 ms |

  - **Storage growth:** the receiving store grew by 4,923 bytes (100 procedures) and 27,338 bytes (1,000) per incremental activation.
  - **Incremental size:** it is dominated by the directory tree that holds the changed document. All 1,000 test keys share one first segment, so each change resends that directory's tree object, which lists all 1,000 documents.
  - **Push time:** pushing the initial 1,000-procedure catalog took 3.9 s, mostly go-git's delta search when packing.
- **Not established or not supported:**
  - **Unbounded steps:** reference advertisements before the pack, and the go-git parser's memory while it resolves deltas inside the bounds, are not bounded separately. Network deadlines come from the caller's context.
  - **SSH settings:** go-git's SSH transport reads `~/.ssh/config` host settings.
  - **Garbage collection:** unaccepted or diverged objects are kept, and nothing is collected yet.
  - **Reconciliation:** there is no reconciliation of diverged histories.
  - **Secrets:** no secret detection in published knowledge.
  - **Signatures:** none, as above.
  - **GitHub:** the only GitHub check is the documented [smoke test](../../development/git-remote-smoke.md), which needs a test repository and token supplied by the owner.
