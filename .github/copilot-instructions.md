# Copilot instructions

Follow [AGENTS.md](../AGENTS.md). It is the canonical rule set for this repository, and it takes precedence over anything in this file. This file adds only Copilot-specific guidance.

- Path-specific rules in `.github/instructions/*.instructions.md` apply automatically to matching files: storage, and the HTTP API and CLI.
- Copilot cloud agent: the Go toolchain comes from `go.mod` (the `toolchain` line), and golangci-lint must be 2.14.0. Before you push, run the gate of the binding that matches your change (AGENTS.md, "Before you call it done"). The `ci` workflow runs only when dispatched by hand; do not wait for it. If a tool is missing in your environment, report that check as not run. Never report it as passed.
- Copilot code review: flag any change that does one of these things:
  - rewrites stored history, or edits an applied migration;
  - adds task-specific behavior to code;
  - imports HTTP or storage packages into `internal/memory`;
  - returns internal error text to clients;
  - changes the API or record contract without updating `docs/architecture/`;
  - adds behavior without tests;
  - reports a scoped verification (`verify-docs`, `verify-records`, `verify-focused`) as verification of the commit, or uses one for a change it excludes.
