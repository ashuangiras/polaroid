#!/usr/bin/env python3
"""Benchmarks the knowledge-first workflow of ADR-0043 against the legacy
execution-recording workflow, over the real MCP transport, with no LLM in the
loop (issue #131, PR #132).

  discover -> read -> execute locally -> verify locally -> finish

It starts an isolated polaroidd, loads examples/development and
examples/knowledge-first, binds two small, real gates in
github.com/ashuangiras/polaroid (`gofmt -l .` and `go vet ./...`): one to
dev.change.verify v4 (an existing procedure, unchanged) and one to
dev.change.check (the representative knowledge-first example, whose
universal rules live once in agent.guidance.shared). The gate commands are
run for real, at this checkout's real HEAD, from the inputs the reads return.

Scenarios (each counted separately; nothing is summed across them):
  legacy-full        full mode (/mcp): resolve_binding, get_version x2, run,
                     record_execution x2, get_verification, resolve_binding.
  knowledge-cold     agent mode (/mcp/agent), dev.change.verify v4: discover,
                     read binding, read checks; run; no Polaroid write.
  knowledge-cold-shared  the same with dev.change.check, plus one read of the
                     shared guidance.
  knowledge-warm / knowledge-warm-shared  CONDITIONAL: read binding only,
                     assuming the session already holds the child and shared
                     guidance it read earlier (immutable versions); not a
                     measured session saving.
  knowledge-gap      knowledge-cold-shared plus one classified feedback call
                     (a scripted finding, labelled as such).

Session setup (server/discover instructions + tools/list schemas) is
measured per mode and reported apart from, and added once to, each total.
The script exits non-zero if any guidance is incomplete, a mapped input
differs from the full API's derivation, a real command fails, the legacy
run is not verified, a knowledge scenario writes an execution, or feedback
is not stored with its classification.

Tokens: tiktoken's o200k_base when installed, else a labelled deterministic
estimate (a run of letters/digits is one token, every other non-space
character one). Usage: scripts/benchmark-knowledge.py [-o OUT_DIR]; needs
make build. Raw requests and responses go to OUT_DIR (default
bin/benchmark/knowledge-<timestamp>, git-ignored).
"""
from __future__ import annotations

import argparse
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
REPO = "github.com/ashuangiras/polaroid"
PROTOCOL = "2026-07-28"
GATE = ["gofmt -l .", "go vet ./..."]
TOKEN_PATTERN = re.compile(r"[A-Za-z0-9]+|[^\sA-Za-z0-9]")
try:
    import tiktoken  # type: ignore

    _ENC = tiktoken.get_encoding("o200k_base")
    TOKENIZER = "tiktoken:o200k_base"
except Exception:  # environment-dependent
    _ENC = None
    TOKENIZER = "approx (tiktoken unavailable; deterministic estimate)"


def tokens(text: str) -> int:
    return len(_ENC.encode(text)) if _ENC else len(TOKEN_PATTERN.findall(text))


class Failure(Exception):
    pass


def check(cond: bool, message: str) -> None:
    if not cond:
        raise Failure(message)


class Client:
    def __init__(self, base: str, path: str, out: Path):
        self.url, self.out, self.calls = base + path, out, []

    def post(self, method: str, name: str, params: dict) -> tuple[str, str]:
        params = {**params, "_meta": {"io.modelcontextprotocol/protocolVersion": PROTOCOL,
                                      "io.modelcontextprotocol/clientCapabilities": {},
                                      "io.modelcontextprotocol/clientInfo": {"name": "benchmark-knowledge", "version": "1"}}}
        body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, separators=(",", ":"))
        req = urllib.request.Request(self.url, data=body.encode(), method="POST")
        for k, v in {"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
                     "Mcp-Protocol-Version": PROTOCOL, "Mcp-Method": method}.items():
            req.add_header(k, v)
        if name:
            req.add_header("Mcp-Name", name)
        try:
            with urllib.request.urlopen(req) as resp:
                raw = resp.read().decode()
        except urllib.error.HTTPError as e:
            raw = e.read().decode()
        return body, raw

    def setup(self) -> dict:
        _, discover = self.post("server/discover", "", {})
        _, listed = self.post("tools/list", "", {})
        names = [t["name"] for t in json.loads(listed)["result"]["tools"]]
        return {"tools": len(names), "names": names, "instructions_tokens": tokens(discover),
                "schema_tokens": tokens(listed), "schema_bytes": len(listed.encode())}

    def tool(self, scenario: str, name: str, args: dict) -> dict:
        body, raw = self.post("tools/call", name, {"name": name, "arguments": args})
        result = json.loads(raw).get("result", {})
        text = result["content"][0]["text"] if result.get("content") else raw
        check(not result.get("isError"), f"{scenario}: {name} failed: {text}")
        n = len(self.calls) + 1
        (self.out / f"{n:02d}-{scenario}-{name}.request.json").write_text(body + "\n")
        (self.out / f"{n:02d}-{scenario}-{name}.response.json").write_text(raw + "\n")
        self.calls.append({"scenario": scenario, "tool": name, "request_tokens": tokens(body), "response_tokens": tokens(text)})
        return json.loads(text)


def http(base: str, method: str, path: str, body: dict | None = None) -> dict:
    req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body else None, method=method)
    if body:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req) as resp:
        return json.loads(resp.read().decode())


def run_gate(commands: list[str]) -> list[dict]:
    results = []
    for cmd in commands:
        proc = subprocess.run(cmd.split(), cwd=REPO_ROOT, capture_output=True, text=True)
        out = (proc.stdout + proc.stderr).strip()
        ok = proc.returncode == 0 and not (cmd.startswith("gofmt") and out)
        results.append({"run": cmd, "exit": proc.returncode, "excerpt": out[:200] or "(no output)", "ok": ok})
    check(all(r["ok"] for r in results), f"a real gate command failed: {results}")
    return results


def complete(guidance: dict, keys: list[str]) -> None:
    check(bool(guidance.get("instructions")) and bool(guidance.get("contract")), f"guidance without instructions or contract: {guidance.get('canonical_key')}")
    missing = [k for k in keys if k not in guidance["contract"]]
    check(not missing, f"{guidance['canonical_key']}: contract lacks {missing}")


def knowledge(agent: Client, scenario: str, binding: str, shared: bool, gap: bool, full_plan_checks: dict) -> None:
    agent.tool(scenario, "discover", {"task": "check a change to this repository by running its gates", "repository": REPO})
    root = agent.tool(scenario, "read", {"repository": REPO, "binding": binding})
    check(root["inputs"] == {"gate_commands": GATE}, f"{scenario}: binding inputs {root['inputs']}")
    complete(root, ["inputs", "expected_outcome", "verification", "boundaries", "preconditions"])
    if shared:
        ref = root["contract"]["shared_guidance"]
        rules = agent.tool(scenario, "read", {"canonical_key": ref["canonical_key"], "version": ref["version"]})
        check(len(rules["contract"]["rules"]) >= 8, f"{scenario}: shared guidance incomplete")
    refs = {r["name"]: r for r in root["references"]}
    check("build_command" not in root["inputs"], "build must be decided not applicable here")
    checks = refs["checks"]
    check(checks["inputs"] == full_plan_checks, f"{scenario}: checks inputs {checks['inputs']} != full API {full_plan_checks}")
    child = agent.tool(scenario, "read", {"procedure_id": checks["procedure_id"], "version": checks["version"], "inputs": checks["inputs"]})
    complete(child, ["inputs"])
    results = run_gate(child["inputs"]["gate_commands"])
    if gap:
        fb = agent.tool(scenario, "feedback", {
            "procedure_id": root["procedure_id"], "version": root["version"], "classification": "missing_precondition",
            "summary": "The checks gate assumes go vet's toolchain matches go.mod",
            "details": "Scripted finding for the benchmark: the contract names no toolchain check before go vet.",
            "reporter": "benchmark.knowledge", "repository": REPO,
            "evidence": {"step": "checks", "command": results[1]["run"], "exit": results[1]["exit"]}})
        check(fb["classification"] == "missing_precondition", f"{scenario}: feedback {fb}")
        agent.feedback_id = fb["id"]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("-o", "--out", type=Path)
    args = ap.parse_args()
    out = args.out or REPO_ROOT / "bin" / "benchmark" / time.strftime("knowledge-%Y%m%dT%H%M%SZ", time.gmtime())
    out.mkdir(parents=True, exist_ok=True)
    polaroidd, polaroid = REPO_ROOT / "bin" / "polaroidd", REPO_ROOT / "bin" / "polaroid"
    if not (polaroidd.exists() and polaroid.exists()):
        print("benchmark-knowledge: run make build first", file=sys.stderr)
        return 2

    work = Path(tempfile.mkdtemp(prefix="polaroid-knowledge-"))
    env = {**os.environ, "HOME": str(work)}
    env.pop("POLAROID_DB", None)
    log = work / "polaroidd.log"
    proc = subprocess.Popen([str(polaroidd), "-addr", "127.0.0.1:0", "-db", str(work / "p.db")], env=env,
                            stdout=subprocess.DEVNULL, stderr=open(log, "w"))
    try:
        base = ""
        for _ in range(200):
            m = re.search(r'msg="polaroidd listening" addr=(\S+)', log.read_text()) if log.exists() else None
            if m:
                base = "http://" + m.group(1)
                break
            check(proc.poll() is None, f"polaroidd exited: {log.read_text()}")
            time.sleep(0.05)
        check(bool(base), "polaroidd did not start")
        load_env = {**env, "POLAROID_URL": base, "LOAD_FIXTURES_CLI": str(polaroid)}
        keys = {}
        for d in ("examples/development", "examples/knowledge-first"):
            loaded = subprocess.run(["scripts/load-fixtures.sh", d], cwd=REPO_ROOT, env=load_env, capture_output=True, text=True, check=True)
            keys.update(json.loads(loaded.stdout)["procedures"])
        bindings = {}
        for name, key in (("check-small", "dev.change.verify"), ("check-small-kf", "dev.change.check")):
            bindings[name] = http(base, "POST", "/v1/bindings", {"repository": REPO, "name": name, "procedure_id": keys[key], "revision": {
                "inputs": {"gate_commands": GATE}, "version_policy": {"contextual": {}}, "revision_reason": "Benchmark gate."}})["id"]

        full, agent = Client(base, "/mcp", out), Client(base, "/mcp/agent", out)
        setup = {"full": full.setup(), "agent": agent.setup()}
        check(setup["agent"]["tools"] == 3 and setup["full"]["tools"] == 39, f"tool counts {setup['full']['tools']}/{setup['agent']['tools']}")

        commit = subprocess.run(["git", "rev-parse", "HEAD"], cwd=REPO_ROOT, capture_output=True, text=True, check=True).stdout.strip()
        target = {"environment": "benchmark.local", "commit": commit, "inputs": {"gate_commands": GATE}, "decisions": {"build": False, "hosted-ci": False}}
        plan = full.tool("reference", "prepare_execution", {"binding_id": bindings["check-small"], **target})
        full.calls.pop()  # a cross-check for correctness, not part of any scenario
        full_plan_checks = next(s["inputs"] for s in plan["steps"] if s["path"] == "checks")

        # legacy-full: today's recording workflow, real commands, real verification.
        s = "legacy-full"
        res = full.tool(s, "resolve_binding", {"binding_id": bindings["check-small"], **target})
        graph = res["graph"]
        checks_ref = next(r for r in graph["references"] if r["name"] == "checks")
        full.tool(s, "get_version", {"procedure_id": graph["procedure_id"], "version": graph["version"]})
        full.tool(s, "get_version", {"procedure_id": checks_ref["node"]["procedure_id"], "version": checks_ref["node"]["version"]})
        results = run_gate(GATE)
        evidence = {"commands": [{k: r[k] for k in ("run", "exit", "excerpt")} for r in results]}
        envr = {"name": "benchmark.local", "attributes": {}}
        child = full.tool(s, "record_execution", {"procedure_id": checks_ref["node"]["procedure_id"], "version": checks_ref["node"]["version"],
                                                  "repository": REPO, "commit": commit, "environment": envr, "inputs": full_plan_checks,
                                                  "outcome": "succeeded", "evidence": evidence})
        parent = full.tool(s, "record_execution", {"procedure_id": graph["procedure_id"], "version": graph["version"], "binding_id": bindings["check-small"],
                                                   "binding_revision": 1, "repository": REPO, "commit": commit, "environment": envr,
                                                   "inputs": {"gate_commands": GATE}, "outcome": "succeeded", "evidence": {"scope": "benchmark gate"},
                                                   "children": [{"reference": "checks", "execution_id": child["id"]}],
                                                   "decisions": [{"reference": "build", "applicable": False, "rationale": "no build_command"},
                                                                 {"reference": "hosted-ci", "applicable": False, "rationale": "local-only policy"}]})
        check(full.tool(s, "get_verification", {"execution_id": parent["id"]})["verified"], "legacy run not verified")
        full.tool(s, "resolve_binding", {"binding_id": bindings["check-small"], **target})

        executions_before = len(http(base, "GET", "/v1/executions")["executions"])
        knowledge(agent, "knowledge-cold", "check-small", False, False, full_plan_checks)
        knowledge(agent, "knowledge-cold-shared", "check-small-kf", True, False, full_plan_checks)
        agent.tool("knowledge-warm", "read", {"repository": REPO, "binding": "check-small"})
        run_gate(GATE)
        agent.tool("knowledge-warm-shared", "read", {"repository": REPO, "binding": "check-small-kf"})
        run_gate(GATE)
        knowledge(agent, "knowledge-gap", "check-small-kf", True, True, full_plan_checks)
        check(len(http(base, "GET", "/v1/executions")["executions"]) == executions_before, "a knowledge scenario wrote an execution")
        stored = http(base, "GET", f"/v1/feedback/{agent.feedback_id}")
        check(stored["context"]["classification"] == "missing_precondition", f"feedback stored as {stored}")

        calls = full.calls + agent.calls
        rows = {}
        for c in calls:
            r = rows.setdefault(c["scenario"], {"calls": 0, "request_tokens": 0, "response_tokens": 0})
            r["calls"] += 1
            r["request_tokens"] += c["request_tokens"]
            r["response_tokens"] += c["response_tokens"]
        for name, r in rows.items():
            mode = setup["full" if name == "legacy-full" else "agent"]
            r["setup_tokens"] = mode["schema_tokens"] + mode["instructions_tokens"]
            r["total_tokens"] = r["setup_tokens"] + r["request_tokens"] + r["response_tokens"]
            r["conditional"] = name.startswith("knowledge-warm")
        summary = {"tokenizer": TOKENIZER, "commit": commit, "setup": setup, "scenarios": rows, "calls": calls}
        (out / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")

        print(f"tokenizer: {TOKENIZER}")
        for m in ("full", "agent"):
            print(f"setup {m:<5}: {setup[m]['tools']:>2} tools, schema {setup[m]['schema_tokens']:>6} tok, instructions {setup[m]['instructions_tokens']:>5} tok")
        print(f"{'scenario':<22} {'calls':>5} {'req':>6} {'resp':>6} {'setup':>6} {'total':>7}")
        for name, r in rows.items():
            flag = "  (conditional)" if r["conditional"] else ""
            print(f"{name:<22} {r['calls']:>5} {r['request_tokens']:>6} {r['response_tokens']:>6} {r['setup_tokens']:>6} {r['total_tokens']:>7}{flag}")
        print("correctness: PASS (guidance complete, inputs match the full API, real gate passed, legacy verified, no knowledge writes, feedback classified)")
        print(out / "summary.json")
        return 0
    except Failure as e:
        print(f"benchmark-knowledge: FAIL: {e}", file=sys.stderr)
        return 1
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    raise SystemExit(main())
