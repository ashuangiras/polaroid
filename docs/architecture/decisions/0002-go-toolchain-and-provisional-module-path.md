# 0002. Go 1.27.1 minimum, Go 1.27.2 toolchain, and the provisional module path `example.com/polaroid`

**Status:** Accepted
**Date:** 2026-10-09

## Context

- The bootstrap environment has Go 1.27.1 installed, from Homebrew, with `GOTOOLCHAIN=local` set in the shell, so no other toolchain is downloaded automatically.
- On 2026-10-09, `go.dev/dl` lists 1.27.2 (published 2026-10-02) and 1.26.9 as the supported releases.
- `make vuln` with Go 1.27.1 reports **9 standard-library vulnerabilities reachable from Polaroid's code**, in `net/http`, its internal HTTP/2 package, `mime/multipart` and `crypto/tls` (GO-2026-6603 through GO-2026-6617). All are fixed in 1.27.2. With 1.27.2, govulncheck reports none.
- Go 1.27 adds two standard-library packages that Polaroid uses: `uuid` (for UUIDv7 IDs) and `encoding/json/v2` with `encoding/json/jsontext` ([ADR-0005](0005-strict-json-with-encoding-json-v2.md)).
- golangci-lint 2.12.2, which is installed locally and self-built with Go 1.27.1, cannot read Go 1.27.2 export data ("export data version 5 is greater than maximum supported version 4"). Its official binaries were built with Go 1.26, before Go 1.27 existed. The official golangci-lint 2.14.0 binary, built with Go 1.27.0, lints the code cleanly under both 1.27.1 and 1.27.2.
- The repository has no Git remote, so it has no canonical import path.

## Decision

- `go.mod` declares `go 1.27.1` as the minimum and `toolchain go1.27.2` as the toolchain to build with.
  - `actions/setup-go` reads the `toolchain` line, so CI builds, tests and scans with 1.27.2.
  - With Go's default `GOTOOLCHAIN=auto`, local `go` commands switch to 1.27.2 automatically.
  - With `GOTOOLCHAIN=local`, as in this environment, 1.27.1 keeps working, but `make vuln` fails until Go is upgraded. That is the correct signal: binaries built with 1.27.1 contain the vulnerabilities.
- golangci-lint is pinned to 2.14.0, in the Makefile and in CI.
- The module path is `example.com/polaroid`. `example.com` is reserved for documentation, so this path can never collide with a real module, and it is plainly a placeholder.

## Consequences

- Locally, upgrade to Go 1.27.2 (for example, `brew upgrade go`), or unset `GOTOOLCHAIN=local`. After that, `make ci` matches CI exactly. Until then, run `GOTOOLCHAIN=go1.27.2 make ci`, which downloads that toolchain into the module cache.
- When a new patch release appears, bump the `toolchain` line. Raise the `go` line only when a newer language or standard-library feature is needed.
- Once the repository has a remote, replace the module path in one commit (`OWNER` is a placeholder):

  ```sh
  go mod edit -module github.com/OWNER/polaroid
  grep -rl 'example.com/polaroid' --include='*.go' . | xargs perl -pi -e 's#example\.com/polaroid#github.com/OWNER/polaroid#g'
  make check
  ```

  Also update this ADR's status and any documentation that shows the path. No configuration file references the module path.
