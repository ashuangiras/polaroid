# Dependencies and licenses

Polaroid keeps third-party code to a minimum. The `polaroid` CLI links only the Go standard library. `polaroidd` links two direct dependencies, the SQLite driver and the MCP Go SDK, and the modules that they need.

`make deps-check` ([scripts/check-deps.sh](../../scripts/check-deps.sh)) enforces this file. The check fails if any of these is true:

- A module compiled into a package or test on linux or darwin (amd64 or arm64) is missing from the table below.
- A module is listed at a version other than the one in use.
- A row lists a module that is no longer compiled.
- A module's license file does not read as the license listed for it.
- A listed license is not one of MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0 or ISC. A license file holding both the Apache-2.0 and MIT texts is listed as `Apache-2.0 AND MIT`, and both parts must be allowed.

To add a dependency, follow the `go.dependency.add` example procedure in [examples/procedures](../../examples/procedures). Then add a row here in the same change.

## Go modules compiled into Polaroid

Licenses were verified on 2026-10-09. Each module's own LICENSE file in the Go module cache was read and classified. For the modernc modules, the result was cross-checked against the `LICENSE-3RD-PARTY.md` notice that modernc.org/sqlite ships.

| Module | Version | License | Compiled on | Why |
| --- | --- | --- | --- | --- |
| `modernc.org/sqlite` | `v1.60.1` | BSD-3-Clause | linux, darwin | Direct: the SQLite driver. It is pure Go, so builds need no C toolchain. Bundles SQLite 3.53.4, which is in the public domain. |
| `modernc.org/libc` | `v1.77.1` | BSD-3-Clause | linux, darwin | C runtime for the transpiled SQLite. Bundles musl, go-netdb and nixpkgs code (MIT) and Go code (BSD-3-Clause); see its `LICENSE-3RD-PARTY.md`. |
| `modernc.org/mathutil` | `v1.7.1` | BSD-3-Clause | linux, darwin | Needed by modernc.org/libc. |
| `modernc.org/memory` | `v1.12.1` | BSD-3-Clause | linux, darwin | Memory allocator for modernc.org/libc. Also ships `LICENSE-GO` and `LICENSE-MMAP-GO` (BSD-3-Clause) and a logo attribution. |
| `github.com/dustin/go-humanize` | `v1.0.1` | MIT | linux, darwin | Needed by modernc.org/libc. |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause | linux, darwin | Needed by modernc.org/libc. Polaroid's own IDs use the standard library `uuid` package. |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | BSD-3-Clause | linux, darwin | Needed by modernc.org/mathutil. |
| `golang.org/x/sys` | `v0.48.0` | BSD-3-Clause | linux, darwin | System calls for modernc.org/libc and modernc.org/memory. |
| `github.com/mattn/go-isatty` | `v0.0.24` | MIT | darwin | Needed by modernc.org/libc on darwin. |
| `github.com/ncruces/go-strftime` | `v1.0.0` | MIT | darwin | Needed by modernc.org/libc on darwin. |
| `github.com/modelcontextprotocol/go-sdk` | `v1.8.0` | Apache-2.0 AND MIT | linux, darwin | Direct: the official MCP Go SDK, for the `/mcp` endpoint ([ADR-0014](../architecture/decisions/0014-mcp-transport.md)). The project is relicensing from MIT to Apache-2.0. Its LICENSE holds both texts: new contributions are Apache-2.0, and older ones stay MIT until relicensed. |
| `github.com/google/jsonschema-go` | `v0.4.3` | MIT | linux, darwin | JSON Schema inference for tool input schemas; needed by the MCP SDK. |
| `github.com/segmentio/encoding` | `v0.5.4` | MIT | linux, darwin | JSON encoding inside the MCP SDK. |
| `github.com/segmentio/asm` | `v1.1.3` | MIT | linux, darwin | Needed by github.com/segmentio/encoding. |
| `github.com/yosida95/uritemplate/v3` | `v3.0.2` | BSD-3-Clause | linux, darwin | RFC 6570 URI templates for MCP resource templates; needed by the MCP SDK. |
| `golang.org/x/oauth2` | `v0.35.0` | BSD-3-Clause | linux, darwin | Compiled in through the MCP SDK's authorization support, which Polaroid does not use. |
| `golang.org/x/sync` | `v0.23.0` | BSD-3-Clause | linux, darwin | Needed by the MCP SDK. |
| `golang.org/x/time` | `v0.15.0` | BSD-3-Clause | linux, darwin | Rate limiting inside the MCP SDK. |

The module graph also references modules that are never compiled into Polaroid. modernc.org/sqlite's notice lists them, for example `github.com/hashicorp/golang-lru/v2` (MPL-2.0). `go.sum` holds checksums for some of their `go.mod` files. They carry no license obligation for Polaroid's binaries, and the check above ignores them by design.

Redistributing a `polaroidd` binary requires reproducing the notices of the modules above. No release process exists yet, so nothing automates this.

## Development and CI tools

These tools are not Go module dependencies. They are not linked into or shipped with Polaroid.

| Tool | Version | License | Used for |
| --- | --- | --- | --- |
| Go toolchain | 1.27.2 (minimum 1.27.1) | BSD-3-Clause | Build, test, `go vet`. Taken from the `toolchain` and `go` lines in `go.mod`. 1.27.1 is affected by 9 reachable standard-library vulnerabilities; see [ADR-0002](../architecture/decisions/0002-go-toolchain-and-provisional-module-path.md). |
| golangci-lint | 2.14.0 | GPL-3.0 | `make lint`. Runs as a separate binary, installed locally or by the CI action. Its source is never linked into Polaroid. Its official binaries are built with Go 1.27.0. Version 2.12.2 cannot lint with the Go 1.27.2 toolchain. |
| govulncheck (`golang.org/x/vuln`) | v1.8.0 | BSD-3-Clause | `make vuln`, run with `go run` at a pinned version. |
| jq | any | MIT | `scripts/demo.sh`, `scripts/e2e.sh`, `scripts/e2e-mcp.sh`. GitHub reports the license as NOASSERTION; jq's `COPYING` file states MIT. |
| sqlite3 CLI | any | Public domain | `scripts/e2e.sh` only: shows that raw SQL cannot change history. |
| `@modelcontextprotocol/sdk` (npm) | 1.32.1 | MIT | `scripts/e2e-mcp.sh` only: an independent MCP client. Installed into a temporary directory; the checks are skipped without npm. |
| `@modelcontextprotocol/inspector` (npm) | 2.10.1 | MIT and Apache-2.0 (relicensing in progress; both permissive) | `scripts/e2e-mcp.sh` only, as above. |
| `actions/checkout` | v7.0.1 (`3d3c42e5aac5ba805825da76410c181273ba90b1`) | MIT | CI |
| `actions/setup-go` | v7.0.0 (`b7ad1dad31e06c5925ef5d2fc7ad053ef454303e`) | MIT | CI |
| `golangci/golangci-lint-action` | v9.3.0 (`ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a`) | MIT | CI. Installs golangci-lint. |

Each action is pinned to the full commit SHA of its release tag. Tags and SHAs were resolved with the GitHub API on 2026-10-09.
