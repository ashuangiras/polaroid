#!/usr/bin/env python3
"""A small, fast, REAL executable workflow check (PR #132 review finding 2):
unlike scripts/benchmark-context.py (which measures token cost with
synthetic, pre-written evidence strings and is kept as-is for that purpose),
this script actually runs real commands against this repository's real
current commit and working-tree state, through the compact interface
(prepare_execution, a selective fields= read, format=plan to confirm), and
records their real exit codes and real output excerpts as evidence. If a
real command actually failed, this script reports that honestly: nothing
here is a canned "succeeded".

It also exercises finding 3 (selective field retrieval): it reads the
`checks` procedure version with `fields=instructions` instead of in full,
and reports the size difference, while making explicit that the commands it
actually runs come from `prepare_execution`'s own `inputs` (computed,
mechanical reference-mapping output), never from parsing `instructions`
text (AGENTS.md: contract and instructions are opaque, free-form, never
interpreted by Polaroid or by this script).

To keep this fast, it binds a deliberately small, real gate (`gofmt -l .`,
`go vet ./...`), not the full `make check`/`demo`/`e2e` gate: `build` and
`hosted-ci` are decided not_applicable (the effective inputs name no
build_command, and this repository's hosted-ci policy is local-only), so
only `checks` actually runs.

Usage: scripts/verify-change-real-check.py
  Needs bin/polaroidd, bin/polaroid (make build), and a clean git checkout
  of this repository (uses its real HEAD commit and working-tree state).
  Exits non-zero on any real command failure or verification mismatch.
"""
from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
MCP_PROTOCOL = "2026-07-28"


def meta() -> dict:
    return {
        "io.modelcontextprotocol/protocolVersion": MCP_PROTOCOL,
        "io.modelcontextprotocol/clientCapabilities": {},
        "io.modelcontextprotocol/clientInfo": {"name": "verify-change-real-check", "version": "1"},
    }


def mcp_call(base_url: str, method: str, name: str, params: dict) -> dict:
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, separators=(",", ":"))
    req = urllib.request.Request(base_url + "/mcp", data=body.encode("utf-8"), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json, text/event-stream")
    req.add_header("Mcp-Protocol-Version", MCP_PROTOCOL)
    req.add_header("Mcp-Method", method)
    if name:
        req.add_header("Mcp-Name", name)
    try:
        with urllib.request.urlopen(req) as resp:
            raw = resp.read().decode("utf-8")
    except urllib.error.HTTPError as e:
        raw = e.read().decode("utf-8")
    return json.loads(raw)


def tool(base_url: str, name: str, arguments: dict) -> tuple[dict, bool]:
    envelope = mcp_call(base_url, "tools/call", name, {"name": name, "arguments": arguments, "_meta": meta()})
    result = envelope.get("result", envelope.get("error", {}))
    is_error = bool(result.get("isError", False))
    text = result["content"][0]["text"] if result.get("content") else json.dumps(result)
    return json.loads(text), is_error


def run_real(cmd: list[str]) -> tuple[int, str]:
    """Actually runs cmd against this repository and returns its real exit
    code and a short, deterministic excerpt of its combined output."""
    proc = subprocess.run(cmd, cwd=REPO_ROOT, capture_output=True, text=True)
    out = (proc.stdout + proc.stderr).strip()
    excerpt = out if out else "(no output)"
    if len(excerpt) > 300:
        excerpt = excerpt[:300] + "…"
    return proc.returncode, excerpt


def wait_for_listen(log_path: Path, proc: subprocess.Popen) -> str:
    pattern = re.compile(r'msg="polaroidd listening" addr=(\S+)')
    for _ in range(200):
        if proc.poll() is not None:
            raise RuntimeError(f"polaroidd exited during start-up (code {proc.returncode}); log: {log_path.read_text()}")
        if log_path.exists():
            m = pattern.search(log_path.read_text())
            if m:
                return m.group(1)
        time.sleep(0.05)
    raise RuntimeError("polaroidd did not report a listening address in time")


def git(*args: str) -> str:
    return subprocess.run(["git", *args], cwd=REPO_ROOT, capture_output=True, text=True, check=True).stdout.strip()


def main() -> int:
    polaroidd, polaroid = REPO_ROOT / "bin" / "polaroidd", REPO_ROOT / "bin" / "polaroid"
    if not (polaroidd.exists() and polaroid.exists()):
        print("verify-change-real-check: run `make build` first", file=sys.stderr)
        return 2

    commit = git("rev-parse", "HEAD")
    dirty = git("status", "--porcelain")
    working_tree = "clean" if not dirty else "modified"
    print(f"real commit: {commit}")
    print(f"working tree: {working_tree}")

    work = Path(tempfile.mkdtemp(prefix="polaroid-real-check-"))
    log_path, db_path = work / "polaroidd.log", work / "polaroid.db"
    env = dict(os.environ)
    env.pop("POLAROID_DB", None)
    env["HOME"] = str(work / "home")
    (work / "home").mkdir()

    proc = subprocess.Popen([str(polaroidd), "-addr", "127.0.0.1:0", "-db", str(db_path)],
                             cwd=REPO_ROOT, env=env, stdout=subprocess.DEVNULL, stderr=open(log_path, "w"))
    try:
        base_url = f"http://{wait_for_listen(log_path, proc)}"
        load_env = dict(env)
        load_env["POLAROID_URL"], load_env["LOAD_FIXTURES_CLI"] = base_url, str(polaroid)
        loaded = subprocess.run(["scripts/load-fixtures.sh", "examples/development"], cwd=REPO_ROOT, env=load_env,
                                 capture_output=True, text=True, check=True)
        fixtures = json.loads(loaded.stdout)
        procedures = fixtures["procedures"]
        root_id, checks_id = procedures["dev.change.verify"], procedures["go.module.checks"]
        binding_id = next(b["id"] for b in fixtures["bindings"] if b["name"] == "verify-change" and b["repository"] == "github.com/ashuangiras/polaroid")

        # A small, real, fast gate: no build_command, so `build` is not
        # applicable; `hosted-ci` is not applicable (local-only policy).
        # Only `checks` actually runs, with two real, quick commands.
        environment = "real-check.local"
        inputs = {"gate_commands": ["gofmt -l .", "go vet ./..."], "working_tree": working_tree}
        decisions = {"build": False, "hosted-ci": False}

        plan, err = tool(base_url, "prepare_execution", {
            "binding_id": binding_id, "environment": environment, "commit": commit, "inputs": inputs, "decisions": decisions,
        })
        if err:
            raise RuntimeError(f"prepare_execution failed: {plan}")
        by_path = {s["path"]: s for s in plan["steps"]}
        if by_path["build"]["status"] != "skipped" or by_path["hosted-ci"]["status"] != "skipped":
            raise RuntimeError(f"build/hosted-ci should be skipped (no build_command; local-only policy): {plan['steps']}")
        if by_path["checks"]["status"] != "pending":
            raise RuntimeError(f"checks should be pending (nothing has run yet): {by_path['checks']}")
        if plan.get("next_action") != "checks":
            raise RuntimeError(f"next_action = {plan.get('next_action')!r}, want checks")
        print(f"prepare_execution: next_action=checks, effective gate_commands={by_path['checks'].get('inputs')} (from inputs, not parsed from instructions)")
        effective_gate_commands = by_path["checks"]["inputs"]["gate_commands"]

        # Finding 3: a selective read, not the complete version. The
        # commands above came entirely from prepare_execution's own
        # inputs, never from this (or any) content.
        full_version, _ = tool(base_url, "get_version", {"procedure_id": checks_id, "version": 4})
        selective_version, _ = tool(base_url, "get_version", {"procedure_id": checks_id, "version": 4, "fields": "instructions"})
        full_bytes, selective_bytes = len(json.dumps(full_version)), len(json.dumps(selective_version))
        print(f"selective fields=instructions read: {selective_bytes} bytes vs {full_bytes} bytes in full "
              f"({100 * (1 - selective_bytes / full_bytes):.0f}% smaller); the workflow below never needed more than this.")

        # Run the real commands for real.
        commands = []
        all_ok = True
        for cmd_str in effective_gate_commands:
            code, excerpt = run_real(cmd_str.split())
            ok = code == 0 and (cmd_str != "gofmt -l ." or excerpt == "(no output)")
            all_ok = all_ok and ok
            commands.append({"run": cmd_str, "exit": code, "excerpt": excerpt})
            print(f"  ran: {cmd_str!r} -> exit {code}, {'ok' if ok else 'FAILED'}: {excerpt}")
        outcome = "succeeded" if all_ok else "failed"

        checks_exec, err = tool(base_url, "record_execution", {
            "procedure_id": checks_id, "version": 4, "repository": "github.com/ashuangiras/polaroid", "commit": commit,
            "environment": {"name": environment, "attributes": {}}, "inputs": {"gate_commands": effective_gate_commands, "working_tree": working_tree},
            "outcome": outcome, "evidence": {"commands": commands},
        })
        if err:
            raise RuntimeError(f"record_execution (checks) failed: {checks_exec}")

        parent_exec, err = tool(base_url, "record_execution", {
            "procedure_id": root_id, "version": 4, "binding_id": binding_id, "binding_revision": 1,
            "repository": "github.com/ashuangiras/polaroid", "commit": commit,
            "environment": {"name": environment, "attributes": {}}, "inputs": inputs, "outcome": outcome,
            "children": [{"reference": "checks", "execution_id": checks_exec["id"]}],
            "decisions": [
                {"reference": "build", "applicable": False, "rationale": "effective inputs do not name a build_command"},
                {"reference": "hosted-ci", "applicable": False, "rationale": "hosted-ci binding policy is local-only"},
            ],
            "evidence": {"scope": "checks-only (small, fast real-check; not the full make check/demo/e2e gate)"},
        })
        if err:
            raise RuntimeError(f"record_execution (parent) failed: {parent_exec}")

        verification, err = tool(base_url, "get_verification", {"execution_id": parent_exec["id"]})
        if err:
            raise RuntimeError(f"get_verification failed: {verification}")
        if not all_ok:
            if verification.get("verified"):
                raise RuntimeError("a real command failed, but the parent is reported verified")
            print(f"REAL FAILURE: a real command failed; verified={verification.get('verified')} (correctly not verified). Problems: {verification.get('problems')}")
            return 1
        if not verification.get("verified"):
            raise RuntimeError(f"every real command succeeded, but the parent is not verified: {verification}")

        confirm, err = tool(base_url, "resolve_binding", {
            "binding_id": binding_id, "environment": environment, "commit": commit, "inputs": inputs, "decisions": decisions, "format": "plan",
        })
        if err or confirm.get("next_action") is not None:
            raise RuntimeError(f"confirm: next_action = {confirm.get('next_action')!r}, want none (everything is verified or skipped): {confirm}")

        print(f"REAL RESULT: verified=true at commit {commit[:12]}, {len(commands)} real command(s) run, outcome={outcome}, next_action=none")
        return 0
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    raise SystemExit(main())
