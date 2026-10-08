# Dependencies and licenses

Polaroid keeps third-party code to a minimum. The `polaroid` CLI links only the Go standard library. `polaroidd` links one direct dependency, the SQLite driver, and the modules that the driver needs.

`make deps-check` ([scripts/check-deps.sh](../../scripts/check-deps.sh)) enforces this file. The check fails if any of these is true:

- A module compiled into a package or test on linux or darwin (amd64 or arm64) is missing from the table below.
- A module is listed at a version other than the one in use.
- A row lists a module that is no longer compiled.
- A module's license file does not read as the license listed for it.
- A listed license is not one of MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0 or ISC.

To add a dependency, follow the `go.dependency.add` example procedure in [examples/procedures](../../examples/procedures). Then add a row here in the same change.

## Go modules compiled into Polaroid

Licenses were verified on 2026-10-09. Each module's own LICENSE file in the Go module cache was read and classified, and the result was cross-checked against the `LICENSE-3RD-PARTY.md` notice that modernc.org/sqlite ships.

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

The module graph also references modules that are never compiled into Polaroid. modernc.org/sqlite's notice lists them, for example `github.com/hashicorp/golang-lru/v2` (MPL-2.0). `go.sum` holds checksums for some of their `go.mod` files. They carry no license obligation for Polaroid's binaries, and the check above ignores them by design.

Redistributing a `polaroidd` binary requires reproducing the notices of the modules above. No release process exists yet, so nothing automates this.

## Development and CI tools

These tools are not Go module dependencies. They are not linked into or shipped with Polaroid.

| Tool | Version | License | Used for |
| --- | --- | --- | --- |
| Go toolchain | 1.27.2 (minimum 1.27.1) | BSD-3-Clause | Build, test, `go vet`. Taken from the `toolchain` and `go` lines in `go.mod`. 1.27.1 is affected by 9 reachable standard-library vulnerabilities; see [ADR-0002](../architecture/decisions/0002-go-toolchain-and-provisional-module-path.md). |
| golangci-lint | 2.14.0 | GPL-3.0 | `make lint`. Runs as a separate binary, installed locally or by the CI action. Its source is never linked into Polaroid. Its official binaries are built with Go 1.27.0. Version 2.12.2 cannot lint with the Go 1.27.2 toolchain. |
| govulncheck (`golang.org/x/vuln`) | v1.8.0 | BSD-3-Clause | `make vuln`, run with `go run` at a pinned version. |
| jq | any | MIT | `scripts/demo.sh` only. GitHub reports the license as NOASSERTION; jq's `COPYING` file states MIT. |
| `actions/checkout` | v7.0.1 (`3d3c42e5aac5ba805825da76410c181273ba90b1`) | MIT | CI |
| `actions/setup-go` | v7.0.0 (`b7ad1dad31e06c5925ef5d2fc7ad053ef454303e`) | MIT | CI |
| `golangci/golangci-lint-action` | v9.3.0 (`ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a`) | MIT | CI. Installs golangci-lint. |

Each action is pinned to the full commit SHA of its release tag. Tags and SHAs were resolved with the GitHub API on 2026-10-09.
