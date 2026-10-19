# Copilot instructions

Follow [AGENTS.md](../AGENTS.md). It is the canonical rule set for this repository, and it takes precedence over anything in this file. This file adds only Copilot-specific guidance.

- Path-specific rules in `.github/instructions/*.instructions.md` apply automatically to matching files: storage, and the HTTP API and CLI.
- Copilot Chat in VS Code: the workspace's `.vscode/mcp.json` names the MCP servers.
  - `polaroid-agent` at `/mcp/agent/v8` is the procedural knowledge interface. Begin each task by discovering `polaroid.using` for `github.com/ashuangiras/polaroid`, then reading it at the returned snapshot even if another result ranks higher. Start a dynamic parent there, discover/read and immediately attach `polaroid.session-close-capture` at the same snapshot, and keep it pending while attaching and completing task-specific children. Review verified work for reusable, evidence-backed learning; a no-op is correct when nothing qualifies. After the verified service is upgraded and the workspace selects v8, bind each capture proposal to the parent's walkthrough token at `propose_start`, review every field, and call `propose_publish_capture` with the exact `candidate_hash` while capture is the active final step. Other proposals need authenticated operator approval of the exact ready candidate. An installed older service does **not** enforce capture authorization server-side; do not assume v8 tools are available until upgrade. Never push or sync automatically. Complete capture last, then the parent; an open parent can accept later task children. Use `resolve` for typed relationships and `feedback` only for deficiencies in existing procedures. [docs/architecture/knowledge-guide.md](../docs/architecture/knowledge-guide.md) documents the interface. The legacy `/mcp` is retired; use the `polaroid` CLI or JSON HTTP API for operator and evidence work.
  - `dispatch` runs commands and keeps their output.

  `polaroid-agent` and `dispatch` are local entries. If a server's tools are missing or stale after the service is upgraded, restart that MCP server and open a new chat: a chat keeps the tool list it started with.
- Copilot cloud agent: the Go toolchain comes from `go.mod` (the `toolchain` line), and golangci-lint must be 2.14.0. Before you push, run the gate of the binding that matches your change (AGENTS.md, "Before you call it done"). The `ci` workflow runs only when dispatched by hand; do not wait for it. If a tool is missing in your environment, report that check as not run. Never report it as passed.
- Copilot code review: flag any change that does one of these things:
  - rewrites stored history, or edits an applied migration;
  - adds task-specific behavior to code;
  - imports HTTP or storage packages into `internal/memory`;
  - returns internal error text to clients;
  - changes the API or record contract without updating `docs/architecture/`;
  - adds behavior without tests;
  - reports a scoped verification (`verify-docs`, `verify-records`, `verify-focused`) as verification of the commit, or uses one for a change it excludes.
