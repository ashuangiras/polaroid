# Copilot instructions

Follow [AGENTS.md](../AGENTS.md). It is the canonical rule set for this repository, and it takes precedence over anything in this file. This file adds only Copilot-specific guidance.

- Path-specific rules in `.github/instructions/*.instructions.md` apply automatically to matching files: storage, and the HTTP API and CLI.
- Copilot cloud agent: CI runs `make ci` on `ubuntu-latest`, with the Go toolchain from `go.mod` (the `toolchain` line) and golangci-lint 2.14.0. Run `make check` before you push. If a tool is missing in your environment, report that check as not run. Never report it as passed.
- Copilot code review: flag any change that does one of these things:
  - rewrites stored history, or edits an applied migration;
  - adds task-specific behavior to code;
  - imports HTTP or storage packages into `internal/memory`;
  - returns internal error text to clients;
  - changes the API or record contract without updating `docs/architecture/`;
  - adds behavior without tests.
