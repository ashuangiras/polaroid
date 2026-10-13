#!/usr/bin/env python3
"""One small real-agent comparison of the full MCP interface (/mcp) and agent
mode (/mcp/agent) on the same task (PR #132 review).

A real LLM agent, the GitHub Copilot CLI in non-interactive mode
(`copilot -p`), does the same task once per interface, each against its own
fresh, isolated polaroidd loaded with examples/development and a binding
`check-small` of dev.change.verify whose gate is `gofmt -l .` and
`go vet ./...`. The CLI runs with no custom instructions (AGENTS.md), none of
the user's MCP servers, only the Polaroid server under test, permission for
that server and for the gate's commands, and every other tool on its default
permission (denied without a prompt).

Tokens are the model API's own usage counts, read from the CLI's debug log
(`prompt_tokens`, `cached_tokens`, `completion_tokens`, `reasoning_tokens`
per model call); the tool-schema tokens are the CLI's own `tool_tokens`. No
number here is estimated. Each run is also graded mechanically from its
transcript and the catalog afterwards, and the final answer is kept for
reading.

Usage: scripts/agent-compare.py [-o OUT_DIR] [--model MODEL] [--runs N]
  Needs make build and an authenticated `copilot` CLI with network; spends
  real premium requests. Writes the transcripts, logs and summary.json to
  OUT_DIR (default bin/benchmark/agent-compare-<timestamp>, git-ignored).
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
REPO = "github.com/ashuangiras/polaroid"
GATE = ["gofmt -l .", "go vet ./..."]
TASK = ("Use the Polaroid MCP server to check the current commit of this repository ({repo}) with its binding "
        "`check-small`, in environment `local.dev`: find and follow the procedure Polaroid gives you, running its gate "
        "commands in the current directory. Do not modify any file. Finish with a short report: the procedure and "
        "version you followed, each command with its exit status, and what, if anything, is verified.")


def http(base: str, method: str, path: str, body: dict | None = None) -> dict:
    req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body else None, method=method)
    if body:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read().decode())


def daemon(work: Path) -> tuple[subprocess.Popen, str]:
    log = work / "polaroidd.log"
    proc = subprocess.Popen([str(REPO_ROOT / "bin/polaroidd"), "-addr", "127.0.0.1:0", "-db", str(work / "p.db")],
                            env={**os.environ, "HOME": str(work)}, stdout=subprocess.DEVNULL, stderr=open(log, "w"))
    for _ in range(200):
        m = re.search(r'msg="polaroidd listening" addr=(\S+)', log.read_text()) if log.exists() else None
        if m:
            return proc, "http://" + m.group(1)
        time.sleep(0.05)
    raise RuntimeError("polaroidd did not start")


def user_mcp_servers() -> list[str]:
    config = Path.home() / ".copilot/mcp-config.json"
    return list(json.loads(config.read_text()).get("mcpServers", {})) if config.exists() else []


def usage(log_dir: Path) -> dict:
    """Sums the API-reported usage of every model call in the CLI's debug log:
    each chat completion's pretty-printed "usage" object, counted once."""
    totals = {"model_calls": 0, "prompt_tokens": 0, "cached_tokens": 0, "completion_tokens": 0, "reasoning_tokens": 0}
    for f in sorted(log_dir.glob("*")):
        text = f.read_text(errors="replace")
        for m in re.finditer(r'"usage": (\{\n.*?\n  \})', text, re.S):
            try:
                u = json.loads(m.group(1))
            except json.JSONDecodeError:
                continue
            if "prompt_tokens" not in u or "completion_tokens" not in u:
                continue
            totals["model_calls"] += 1
            totals["prompt_tokens"] += u["prompt_tokens"]
            totals["completion_tokens"] += u["completion_tokens"]
            totals["cached_tokens"] += (u.get("prompt_tokens_details") or {}).get("cached_tokens") or 0
            totals["reasoning_tokens"] += (u.get("completion_tokens_details") or {}).get("reasoning_tokens") or 0
    return totals


def run(mode: str, path: str, model: str | None, out: Path, n: int) -> dict:
    work = Path(tempfile.mkdtemp(prefix=f"polaroid-agent-{mode}-"))
    proc, base = daemon(work)
    try:
        env = {**os.environ, "HOME": str(work), "POLAROID_URL": base, "LOAD_FIXTURES_CLI": str(REPO_ROOT / "bin/polaroid")}
        keys = json.loads(subprocess.run(["scripts/load-fixtures.sh", "examples/development"], cwd=REPO_ROOT, env=env,
                                         capture_output=True, text=True, check=True).stdout)["procedures"]
        http(base, "POST", "/v1/bindings", {"repository": REPO, "name": "check-small", "procedure_id": keys["dev.change.verify"], "revision": {
            "inputs": {"gate_commands": GATE}, "version_policy": {"contextual": {}}, "revision_reason": "Agent comparison gate."}})
        log_dir = out / f"{mode}-{n}-logs"
        shutil.rmtree(log_dir, ignore_errors=True)
        cmd = ["copilot", "-p", TASK.format(repo=REPO), "--output-format", "json", "--log-level", "all", "--log-dir", str(log_dir),
               "--no-custom-instructions", "--disable-builtin-mcps", "--allow-all-mcp-server-instructions", "--no-auto-update",
               "--additional-mcp-config", json.dumps({"mcpServers": {"polaroid": {"type": "http", "url": base + path, "tools": ["*"]}}}),
               "--allow-tool", "polaroid", "--allow-tool", "shell(gofmt:*)", "--allow-tool", "shell(go vet:*)",
               "--allow-tool", "shell(git rev-parse:*)", "--allow-tool", "shell(git status:*)"]
        for name in user_mcp_servers():
            cmd += ["--disable-mcp-server", name]
        if model:
            cmd += ["--model", model]
        started = time.time()
        res = subprocess.run(cmd, cwd=REPO_ROOT, capture_output=True, text=True, timeout=1200)
        transcript = out / f"{mode}-{n}.jsonl"
        transcript.write_text(res.stdout)
        events = [json.loads(line) for line in res.stdout.splitlines() if line.startswith("{")]
        tools, answer, models, schema = [], "", set(), {}
        for e in events:
            d = e.get("data") or {}
            if e.get("type") == "assistant.message":
                models.add(d.get("model"))
                answer = d.get("content") or answer
                for t in d.get("toolRequests") or []:
                    tools.append({"name": t.get("name"), "arguments": t.get("arguments")})
            if e.get("type") == "session.usage_checkpoint":
                for state in d.get("promptCacheBreakState") or []:
                    for m in (state.get("models") or {}).values():
                        schema = {"tool_count": m.get("tool_count"), "tool_tokens": m.get("tool_tokens")}
        mcp = [t["name"] for t in tools if "polaroid" in (t["name"] or "")]
        shell = [json.dumps(t["arguments"]) for t in tools if t["name"] not in mcp]
        executions = http(base, "GET", "/v1/executions?repository=" + REPO)["executions"]
        verified = [e["id"] for e in executions if http(base, "GET", f"/v1/executions/{e['id']}/verification")["verified"]]
        records = http(base, "GET", "/v1/statistics")["records"]
        result = next((e for e in reversed(events) if e.get("type") == "result"), {})
        return {
            "mode": mode, "path": path, "run": n, "models": sorted(m for m in models if m), "exit_code": res.returncode,
            "seconds": round(time.time() - started, 1), "premium_requests": (result.get("usage") or {}).get("premiumRequests"),
            **usage(log_dir), **schema, "mcp_calls": mcp, "other_tool_calls": shell,
            "gate_ran": all(any(g in c for c in shell) for g in GATE),
            "executions_written": records["executions"], "executions_verified": len(verified), "feedback_written": records["feedback"],
            "answer": answer, "transcript": str(transcript.relative_to(REPO_ROOT)) if transcript.is_relative_to(REPO_ROOT) else str(transcript),
        }
    finally:
        proc.terminate()
        proc.wait(timeout=5)
        shutil.rmtree(work, ignore_errors=True)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("-o", "--out", type=Path)
    ap.add_argument("--model")
    ap.add_argument("--runs", type=int, default=1)
    args = ap.parse_args()
    out = (args.out or REPO_ROOT / "bin/benchmark" / time.strftime("agent-compare-%Y%m%dT%H%M%SZ", time.gmtime())).resolve()
    out.mkdir(parents=True, exist_ok=True)
    rows = [run(mode, path, args.model, out, n) for n in range(1, args.runs + 1) for mode, path in (("full", "/mcp"), ("agent", "/mcp/agent"))]
    (out / "summary.json").write_text(json.dumps(rows, indent=2) + "\n")
    for r in rows:
        print(f"{r['mode']:<5} run {r['run']} {r['models']}: model calls {r['model_calls']}, prompt {r['prompt_tokens']} (cached {r['cached_tokens']}), "
              f"completion {r['completion_tokens']} (reasoning {r['reasoning_tokens']}), tools {r.get('tool_count')} / {r.get('tool_tokens')} schema tokens, "
              f"mcp calls {len(r['mcp_calls'])}, gate ran {r['gate_ran']}, executions {r['executions_written']} ({r['executions_verified']} verified), "
              f"feedback {r['feedback_written']}, exit {r['exit_code']}, {r['seconds']}s")
    print(out / "summary.json")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
