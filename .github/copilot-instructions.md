# Copilot instructions

Follow [AGENTS.md](../AGENTS.md). It is the canonical rule set for this repository, and it takes precedence over anything in this file. This file adds only Copilot-specific guidance.

- Path-specific rules in `.github/instructions/*.instructions.md` apply automatically to matching files: storage, and the HTTP API and CLI.
- Copilot Chat in VS Code: the workspace's `.vscode/mcp.json` names the MCP servers.
  - `polaroid-agent` (the endpoint configured in `.vscode/mcp.json`) is the procedural knowledge interface. Begin each task in this workspace by calling `discover` with repository `github.com/ashuangiras/polaroid` and a task string containing `polaroid.using`, then `read` that candidate at the returned snapshot. For discovery context, use the task in your own words and the canonical repository identifier when known. Treat the root as the advisory parent workflow. Next, discover procedures for the actual task and read the relevant candidates at that same snapshot; discovery is lexical, so judge relevance rather than assuming every match applies. Use `resolve` when typed-record reasoning matters. Use `start`/`advance` for a composed workflow when the configured endpoint exposes them and their guided steps help; otherwise `discover`/`read` are enough. Report only genuine procedural problems with `feedback`. When the configured endpoint exposes the field-complete proposal tools, note that the final `propose_advance` publishes immediately; do not take that step without the owner's approval for the exact change, and follow [AGENTS.md](../AGENTS.md). This is an advisory workflow, not an MCP-enforced gate. The root procedure discovers task procedures dynamically; it does not statically compose every procedure. [docs/architecture/knowledge-guide.md](../docs/architecture/knowledge-guide.md) is the full usage guide. This is the only Polaroid server configured here: the legacy `/mcp` (42 tools) is deliberately not wired into this workspace, so it cannot be reached by accident. Operator and evidence work it would have served — binding resolution, execution recording, feedback triage — is done with the `polaroid` CLI instead.
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
