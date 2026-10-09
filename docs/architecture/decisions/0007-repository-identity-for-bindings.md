# 0007. Repositories are identified by a canonical path, and bindings by repository and local name

**Status:** Accepted
**Date:** 2026-10-09

## Context

A repository binding ([#1](https://github.com/ashuangiras/polaroid/issues/1)) records that a repository uses a shared procedure under a repository-local name. Polaroid has no repository records and no access to the repositories themselves, so a binding has to carry a repository identifier that clients supply. Binding identities never change after creation ([ADR-0004](0004-append-only-versions-with-expected-base.md)), so this format cannot be corrected later without a new identity scheme.

The identifier must let independent agents working in the same repository arrive at the same string, and must not need a remote, because local or scratch repositories have none. We considered:

- **A normalized remote URL**, such as `https://github.com/o/r.git`. Clients disagree on scheme, user, port and `.git` suffix, and repositories without a remote have no identifier.
- **An owner-chosen name plus an optional remote URL.** Two fields, and the optional one would be stored but never used.
- **A free-form string.** Case and spelling variants silently become different repositories.
- **A canonical path string.** One field, with one accepted spelling.

## Decision

- A repository identifier is a **canonical path**: one or more segments joined by `/`, 1–255 bytes. Each segment uses only lowercase ASCII letters, digits, `.`, `_` and `-`. A segment cannot be `.` or `..`, and the last segment cannot end in `.git`.
- For a repository with a remote, clients derive the identifier as the host and path of the remote, in lowercase, without scheme, user, port or `.git` suffix. For example, `git@github.com:Ashuangiras/Polaroid.git` becomes `github.com/ashuangiras/polaroid`. A repository without a remote uses a name its owner chooses, such as `scratch`.
- Polaroid validates the format and rejects anything else with `400`. It never rewrites an identifier, so normalization is the client's job, and the rejected spellings (uppercase, a scheme, a `.git` suffix) cannot be stored.
- A binding's **local name** follows the canonical-key format. `(repository, local name)` is unique, and a duplicate gets `409 binding_exists`. One repository may bind the same procedure under several names.
- Polaroid keeps no repository records. A repository exists only as the identifier its bindings share.

## Consequences

- Agents in the same repository find the same bindings without coordination, and repositories without a remote are supported.
- Hosts whose paths are case-sensitive, or that need a port to tell repositories apart, are folded into one lowercase spelling without a port. If that ever conflates two real repositories, the owner must choose a distinct name for one of them.
- Renaming or moving a repository does not move its bindings. Aliases or re-binding need new, explicit records in later work, as for procedure identities.
