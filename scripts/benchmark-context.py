#!/usr/bin/env python3
"""Benchmarks agent-facing context cost of the "verify-change" workflow over
the real MCP transport (issue #131, corrected per the owner's P0 redirect):
no LLM is in the loop, every byte is captured to local files, and what is
measured is **total agent-visible tokens per successfully completed
workflow** - not raw transport bytes alone.

It starts an isolated polaroidd, loads the development fixtures, then drives
three variants of the same workflow through real `tools/call` JSON-RPC
requests (the exact envelope an MCP client sends: method, params, _meta,
headers), each at a fresh target commit:

  baseline        today's full interface: resolve_binding (full graph)
                  before and after, get_version in full for every node.
  optimized-cold  prepare_execution (compact) instead of resolve_binding
                  before running; format=plan instead of the full graph to
                  confirm; get_version is still read in full for every node,
                  because a cold agent has no cached content yet.
  optimized-warm  the same, but the three get_version reads are not made at
                  all: this variant simulates a session that has already
                  read these exact, immutable (procedure_id, version) pairs
                  before (ADR-0004 makes that content cacheable forever).
                  This is an assumption this script states explicitly, not a
                  claim that conditional fetch (ETag/If-None-Match) is
                  implemented: that remains a documented follow-up
                  (context-efficiency-proposal.md) to make the cache check
                  itself cheap over the wire, rather than trusted
                  client-side bookkeeping alone.

Every variant must reach the identical verified outcome, at the identical
target, with the identical selected (procedure_id, version) per reference
path, or the script exits non-zero: an optimization that is cheaper but
wrong is not reported as a saving.

Token counts: this script tries a real tokenizer (tiktoken's o200k_base)
first; when it is not installed (as in this environment: no network to
fetch it), it falls back to a local, deterministic estimate
(approx_tokens), clearly labelled, applied identically everywhere so
relative comparisons stay meaningful.

Usage: scripts/benchmark-context.py [-o OUT_DIR]
  Needs bin/polaroid and bin/polaroidd (make build). Writes OUT_DIR (default
  bin/benchmark/<timestamp>, git-ignored) with one file per captured call,
  manifest.json and summary.json; prints the summary path and a compact
  table. Nothing here is sent to a model.
"""
from __future__ import annotations

import argparse
import difflib
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
from dataclasses import dataclass, field
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
TOKEN_PATTERN = re.compile(r"[A-Za-z0-9]+|[^\sA-Za-z0-9]")
MCP_PROTOCOL = "2026-07-28"

try:
    import tiktoken  # type: ignore

    _ENC = tiktoken.get_encoding("o200k_base")
    TOKENIZER = "tiktoken:o200k_base"
except Exception:  # pragma: no cover - environment-dependent
    _ENC = None
    TOKENIZER = "approx (tiktoken unavailable in this environment)"


def count_tokens(text: str) -> int:
    if _ENC is not None:
        return len(_ENC.encode(text))
    return len(TOKEN_PATTERN.findall(text))


def approx_tokens(text: str) -> int:
    """The local, deterministic fallback estimate alone, always computed
    (even when tiktoken is available) so every record carries both numbers
    for comparison."""
    return len(TOKEN_PATTERN.findall(text))


def meta() -> dict:
    return {
        "io.modelcontextprotocol/protocolVersion": MCP_PROTOCOL,
        "io.modelcontextprotocol/clientCapabilities": {},
        "io.modelcontextprotocol/clientInfo": {"name": "benchmark-context", "version": "1"},
    }


@dataclass
class MCPCall:
    variant: str
    step: str
    tool: str
    request_body: str
    response_body: str
    result_text: str
    is_error: bool

    def as_record(self) -> dict:
        return {
            "variant": self.variant,
            "step": self.step,
            "tool": self.tool,
            "is_error": self.is_error,
            "request_bytes": len(self.request_body.encode("utf-8")),
            "response_envelope_bytes": len(self.response_body.encode("utf-8")),
            "response_tool_result_bytes": len(self.result_text.encode("utf-8")),
            "request_tokens": count_tokens(self.request_body),
            "response_envelope_tokens": count_tokens(self.response_body),
            "response_tool_result_tokens": count_tokens(self.result_text),
            "request_tokens_approx": approx_tokens(self.request_body),
            "response_envelope_tokens_approx": approx_tokens(self.response_body),
            "response_tool_result_tokens_approx": approx_tokens(self.result_text),
        }


@dataclass
class MCPClient:
    base_url: str
    calls: list = field(default_factory=list)

    def _post(self, method: str, name: str, params: dict) -> tuple[str, str]:
        body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, separators=(",", ":"))
        req = urllib.request.Request(self.base_url + "/mcp", data=body.encode("utf-8"), method="POST")
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
        return body, raw

    def tool(self, variant: str, step: str, name: str, arguments: dict) -> tuple[dict, bool]:
        req_body, raw = self._post("tools/call", name, {"name": name, "arguments": arguments, "_meta": meta()})
        envelope = json.loads(raw)
        result = envelope.get("result", envelope.get("error", {}))
        text = ""
        is_error = bool(result.get("isError", False)) if isinstance(result, dict) else False
        if isinstance(result, dict) and result.get("content"):
            text = result["content"][0].get("text", "")
        call = MCPCall(variant, step, name, req_body, raw, text, is_error)
        self.calls.append(call)
        return (json.loads(text) if text else {}), is_error

    def tools_list(self) -> tuple[str, str]:
        return self._post("tools/list", "", {"_meta": meta()})


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


def run(cmd: list[str], **kw) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, cwd=REPO_ROOT, capture_output=True, text=True, check=True, **kw)


def flatten_full_graph(node: dict, path: str = "") -> dict:
    """path -> (procedure_id, version) for a full (format=full) graph or
    resolve_binding "graph" subtree, for cross-variant comparison."""
    out = {path: (node["procedure_id"], node["version"])}
    for ref in node.get("references", []):
        child_path = ref["name"] if not path else path + "/" + ref["name"]
        out.update(flatten_full_graph(ref["node"], child_path))
    return out


def flatten_plan(plan: dict) -> dict:
    return {s["path"]: (s["procedure_id"], s["version"]) for s in plan["steps"]}


def run_variant(mcp: MCPClient, variant: str, binding_id: str, root_id: str, build_id: str, checks_id: str, commit: str) -> dict:
    """Runs one variant of the verify-change workflow and returns its
    selection map (path -> (procedure_id, version)) plus the parent
    execution id, for the cross-variant correctness check."""
    environment = "benchmark.local"
    inputs = {
        "build_command": "make build",
        "artifacts": ["bin/polaroidd", "bin/polaroid"],
        "gate_commands": ["make check", "make vuln", "make demo", "make e2e", "make e2e-mcp E2E_INTEROP=0"],
        "working_tree": "clean",
    }
    decisions = {"build": True, "hosted-ci": False}
    target = {"binding_id": binding_id, "environment": environment, "commit": commit, "inputs": inputs, "decisions": decisions}

    if variant == "baseline":
        pre, err = mcp.tool(variant, "resolve_binding_pre", "resolve_binding", dict(target))
        if err:
            raise RuntimeError(f"{variant}: resolve_binding_pre failed: {pre}")
        selection = flatten_full_graph(pre["graph"])
        mcp.tool(variant, "read_version_root", "get_version", {"procedure_id": root_id, "version": 4})
        mcp.tool(variant, "read_version_build", "get_version", {"procedure_id": build_id, "version": 3})
        mcp.tool(variant, "read_version_checks", "get_version", {"procedure_id": checks_id, "version": 4})
    else:  # optimized-cold, optimized-warm
        pre, err = mcp.tool(variant, "prepare_execution", "prepare_execution", dict(target))
        if err:
            raise RuntimeError(f"{variant}: prepare_execution failed: {pre}")
        selection = flatten_plan(pre)
        if variant == "optimized-cold":
            mcp.tool(variant, "read_version_root", "get_version", {"procedure_id": root_id, "version": 4})
            mcp.tool(variant, "read_version_build", "get_version", {"procedure_id": build_id, "version": 3})
            mcp.tool(variant, "read_version_checks", "get_version", {"procedure_id": checks_id, "version": 4})
        # optimized-warm: these three calls are not made at all (see module
        # docstring): the simulated session already holds this exact,
        # immutable content.

    build_inputs = {"build_command": inputs["build_command"], "artifacts": inputs["artifacts"], "working_tree": "clean"}
    build_exec, err = mcp.tool(variant, "record_execution_build", "record_execution", {
        "procedure_id": build_id, "version": 3, "repository": "github.com/ashuangiras/polaroid", "commit": commit,
        "environment": {"name": environment, "attributes": {"os": "linux", "go": "1.27.2"}}, "inputs": build_inputs,
        "outcome": "succeeded", "evidence": {"commands": [{"run": "make build", "exit": 0}], "artifacts": inputs["artifacts"]},
    })
    if err:
        raise RuntimeError(f"{variant}: record_execution_build failed: {build_exec}")

    checks_inputs = {"gate_commands": inputs["gate_commands"], "working_tree": "clean"}
    checks_exec, err = mcp.tool(variant, "record_execution_checks", "record_execution", {
        "procedure_id": checks_id, "version": 4, "repository": "github.com/ashuangiras/polaroid", "commit": commit,
        "environment": {"name": environment, "attributes": {"os": "linux", "go": "1.27.2"}}, "inputs": checks_inputs,
        "outcome": "succeeded", "evidence": {"commands": [
            {"run": "make check", "exit": 0, "excerpt": "0 issues."},
            {"run": "make vuln", "exit": 0, "excerpt": "No vulnerabilities found."},
            {"run": "make demo", "exit": 0, "excerpt": "demo: PASS"},
            {"run": "make e2e", "exit": 0, "excerpt": "passed=302 failed=0"},
            {"run": "make e2e-mcp E2E_INTEROP=0", "exit": 0, "excerpt": "passed=129 failed=0"},
        ]},
    })
    if err:
        raise RuntimeError(f"{variant}: record_execution_checks failed: {checks_exec}")

    parent_exec, err = mcp.tool(variant, "record_execution_parent", "record_execution", {
        "procedure_id": root_id, "version": 4, "binding_id": binding_id, "binding_revision": 1,
        "repository": "github.com/ashuangiras/polaroid", "commit": commit,
        "environment": {"name": environment, "attributes": {"os": "linux", "go": "1.27.2"}}, "inputs": inputs,
        "outcome": "succeeded",
        "children": [{"reference": "build", "execution_id": build_exec["id"]}, {"reference": "checks", "execution_id": checks_exec["id"]}],
        "decisions": [
            {"reference": "build", "applicable": True, "rationale": "effective inputs name build_command"},
            {"reference": "hosted-ci", "applicable": False, "rationale": "hosted-ci binding policy is local-only"},
        ],
        "evidence": {"scope": "full", "children": ["build", "checks"], "hosted_ci": "not_applicable (local-only policy)"},
    })
    if err:
        raise RuntimeError(f"{variant}: record_execution_parent failed: {parent_exec}")

    verification, err = mcp.tool(variant, "get_verification_parent", "get_verification", {"execution_id": parent_exec["id"]})
    if err or not verification.get("verified"):
        raise RuntimeError(f"{variant}: parent not verified after recording: {verification}")

    if variant == "baseline":
        post, err = mcp.tool(variant, "resolve_binding_post", "resolve_binding", dict(target))
        if err:
            raise RuntimeError(f"{variant}: resolve_binding_post failed: {post}")
        post_selection = flatten_full_graph(post["graph"])
    else:
        post, err = mcp.tool(variant, "resolve_binding_post", "resolve_binding", {**target, "format": "plan"})
        if err:
            raise RuntimeError(f"{variant}: resolve_binding_post (plan) failed: {post}")
        post_selection = flatten_plan(post)
    if post_selection != selection:
        raise RuntimeError(f"{variant}: selection changed between pre- and post-run reads: {selection} vs {post_selection}")

    return {"selection": selection, "parent_execution_id": parent_exec["id"], "outcome": parent_exec["outcome"]}


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("-o", "--out", type=Path, default=None)
    args = ap.parse_args()

    out_dir = args.out or (REPO_ROOT / "bin" / "benchmark" / time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()))
    out_dir.mkdir(parents=True, exist_ok=True)

    polaroidd = REPO_ROOT / "bin" / "polaroidd"
    polaroid = REPO_ROOT / "bin" / "polaroid"
    if not (polaroidd.exists() and polaroid.exists()):
        print("benchmark-context: run `make build` first (bin/polaroidd, bin/polaroid missing)", file=sys.stderr)
        return 2

    work = Path(tempfile.mkdtemp(prefix="polaroid-benchmark-"))
    log_path = work / "polaroidd.log"
    db_path = work / "polaroid.db"
    env = dict(os.environ)
    env.pop("POLAROID_DB", None)
    env["HOME"] = str(work / "home")
    (work / "home").mkdir()

    proc = subprocess.Popen(
        [str(polaroidd), "-addr", "127.0.0.1:0", "-db", str(db_path)],
        cwd=REPO_ROOT, env=env, stdout=subprocess.DEVNULL, stderr=open(log_path, "w"),
    )
    try:
        addr = wait_for_listen(log_path, proc)
        base_url = f"http://{addr}"

        load_env = dict(env)
        load_env["POLAROID_URL"] = base_url
        load_env["LOAD_FIXTURES_CLI"] = str(polaroid)
        loaded = run(["scripts/load-fixtures.sh", "examples/development"], env=load_env)
        fixtures = json.loads(loaded.stdout)
        procedures = fixtures["procedures"]
        binding_id = next(
            b["id"] for b in fixtures["bindings"]
            if b["name"] == "verify-change" and b["repository"] == "github.com/ashuangiras/polaroid"
        )
        root_id, build_id, checks_id = procedures["dev.change.verify"], procedures["go.module.build"], procedures["go.module.checks"]

        mcp = MCPClient(base_url)
        schema_req, schema_resp = mcp.tools_list()
        schema_record = {
            "request_bytes": len(schema_req.encode("utf-8")), "response_bytes": len(schema_resp.encode("utf-8")),
            "request_tokens": count_tokens(schema_req), "response_tokens": count_tokens(schema_resp),
            "note": "one-time per session/connection, not per workflow call; a conforming client fetches this once, not on every turn",
        }

        commits = {"baseline": "b" * 40, "optimized-cold": "c" * 40, "optimized-warm": "d" * 40}
        results = {}
        for variant, commit in commits.items():
            results[variant] = run_variant(mcp, variant, binding_id, root_id, build_id, checks_id, commit)

        base_sel = results["baseline"]["selection"]
        for variant in ("optimized-cold", "optimized-warm"):
            if results[variant]["selection"] != base_sel:
                raise RuntimeError(f"{variant} selected different (procedure_id, version) pairs than baseline: "
                                   f"{results[variant]['selection']} vs {base_sel}")
        for variant, r in results.items():
            if r["outcome"] != "succeeded":
                raise RuntimeError(f"{variant}: outcome {r['outcome']!r}, want succeeded")

        manifest = [c.as_record() for c in mcp.calls]
        (out_dir / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        (out_dir / "tools_list.json").write_text(json.dumps(schema_record, indent=2) + "\n")
        for i, c in enumerate(mcp.calls, start=1):
            prefix = out_dir / f"{i:02d}-{c.variant}-{c.step}"
            (prefix.with_suffix(".request.json")).write_text(c.request_body + "\n")
            (prefix.with_suffix(".response.json")).write_text(c.response_body + "\n")

        summary = {"tokenizer": TOKENIZER, "workflow": "verify-change (dev.change.verify v4)", "tool_schema_list": schema_record, "variants": {}}
        for variant in commits:
            calls = [r for r in manifest if r["variant"] == variant]
            summary["variants"][variant] = {
                "calls": len(calls),
                "request_bytes": sum(c["request_bytes"] for c in calls),
                "response_envelope_bytes": sum(c["response_envelope_bytes"] for c in calls),
                "response_tool_result_bytes": sum(c["response_tool_result_bytes"] for c in calls),
                "request_tokens": sum(c["request_tokens"] for c in calls),
                "response_envelope_tokens": sum(c["response_envelope_tokens"] for c in calls),
                "response_tool_result_tokens": sum(c["response_tool_result_tokens"] for c in calls),
                "by_step": [{"step": c["step"], "tool": c["tool"], "response_tool_result_tokens": c["response_tool_result_tokens"]} for c in calls],
            }
        base_tokens = summary["variants"]["baseline"]["response_tool_result_tokens"]
        for variant in ("optimized-cold", "optimized-warm"):
            v = summary["variants"][variant]
            v["response_tool_result_token_reduction_vs_baseline"] = round(1 - v["response_tool_result_tokens"] / base_tokens, 4) if base_tokens else None

        # Repeated content: byte-level overlap between each variant's two
        # resolution-ish calls (first and last), via longest-matching-blocks.
        for variant in commits:
            calls = [c for c in mcp.calls if c.variant == variant]
            first, last = calls[0], calls[-1]
            sm = difflib.SequenceMatcher(a=first.result_text, b=last.result_text, autojunk=False)
            unchanged = sum(b.size for b in sm.get_matching_blocks())
            summary["variants"][variant]["first_vs_last_call_unchanged_bytes"] = unchanged
            summary["variants"][variant]["first_vs_last_call_unchanged_ratio_vs_first"] = round(unchanged / max(1, len(first.result_text)), 4)

        (out_dir / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")

        print(f"tokenizer: {TOKENIZER}")
        print(f"{'variant':<16} {'calls':>5} {'req_tok':>9} {'resp_tok':>9} {'reduction_vs_baseline':>22}")
        for variant, v in summary["variants"].items():
            red = v.get("response_tool_result_token_reduction_vs_baseline")
            red_s = f"{red * 100:.1f}%" if red is not None else "-"
            print(f"{variant:<16} {v['calls']:>5} {v['request_tokens']:>9} {v['response_tool_result_tokens']:>9} {red_s:>22}")
        print(f"tool schema list (tools/list, one-time): {schema_record['response_tokens']} tokens, {schema_record['response_bytes']} bytes")
        print(str(out_dir / "summary.json"))
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
