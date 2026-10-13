#!/usr/bin/env python3
"""Benchmarks agent-facing context cost of a representative Polaroid MCP/HTTP
workflow, without routing any payload through an LLM (issue #131).

It starts an isolated polaroidd (temporary database and loopback port),
loads the development fixtures, and replays the "verify-change" workflow
documented in AGENTS.md and docs/development/procedural-loop.md: resolve the
binding, read the selected versions, record the build and checks children,
record the parent, confirm verification, and resolve again. Every HTTP
request and response is captured verbatim and sized; nothing is sent to a
model. Token counts are a deterministic, local estimate (see
`approx_tokens`), not a model's real tokenization: they are for comparing
interface designs against each other and against the 70% target in #131, not
for billing.

Usage: scripts/benchmark-context.py [-o OUT_DIR]
  Needs bin/polaroid and bin/polaroidd (make build). Writes OUT_DIR (default
  bin/benchmark/<timestamp>, ignored by git) with one file per captured call
  plus manifest.json and summary.json; prints the summary path.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import urlencode

REPO_ROOT = Path(__file__).resolve().parent.parent
TOKEN_PATTERN = re.compile(r"[A-Za-z0-9]+|[^\sA-Za-z0-9]")


def approx_tokens(text: str) -> int:
    """A local, deterministic token estimate: runs of letters/digits count as
    one token each, and every other non-space character (JSON punctuation
    included) counts as one. This is not a real tokenizer; it is applied
    identically to every payload so relative comparisons (today's interface
    vs. a proposed one) are meaningful without calling a model."""
    return len(TOKEN_PATTERN.findall(text))


@dataclass
class Call:
    step: str
    method: str
    path: str
    request_body: str | None
    status: int
    response_body: str

    @property
    def request_text(self) -> str:
        return self.path + ("\n" + self.request_body if self.request_body else "")

    def as_record(self) -> dict:
        return {
            "step": self.step,
            "method": self.method,
            "path": self.path,
            "status": self.status,
            "request_bytes": len(self.request_text.encode("utf-8")),
            "response_bytes": len(self.response_body.encode("utf-8")),
            "request_tokens_est": approx_tokens(self.request_text),
            "response_tokens_est": approx_tokens(self.response_body),
        }


@dataclass
class Recorder:
    base_url: str
    calls: list[Call] = field(default_factory=list)

    def call(self, step: str, method: str, path: str, body: dict | None = None) -> dict:
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req = urllib.request.Request(self.base_url + path, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req) as resp:
                status = resp.status
                raw = resp.read().decode("utf-8")
        except urllib.error.HTTPError as e:
            status = e.code
            raw = e.read().decode("utf-8")
        self.calls.append(Call(step, method, path, json.dumps(body) if body is not None else None, status, raw))
        parsed = json.loads(raw) if raw else {}
        if status >= 400:
            print(f"benchmark-context: {step} -> HTTP {status}: {raw}", file=sys.stderr)
        return parsed


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


def free_loopback_port_addr() -> str:
    # polaroidd itself picks the free port with -addr 127.0.0.1:0; this just
    # names the bind address, matching scripts/check-records.sh's pattern.
    return "127.0.0.1:0"


def run(cmd: list[str], **kw) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, cwd=REPO_ROOT, capture_output=True, text=True, check=True, **kw)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
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
        [str(polaroidd), "-addr", free_loopback_port_addr(), "-db", str(db_path)],
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
        procedures = fixtures["procedures"]  # canonical_key -> id
        binding_id = next(
            b["id"] for b in fixtures["bindings"]
            if b["name"] == "verify-change" and b["repository"] == "github.com/ashuangiras/polaroid"
        )

        rec = Recorder(base_url)
        environment = {"name": "benchmark.local", "attributes": {"os": "linux", "go": "1.27.2"}}
        commit = "b" * 40
        inputs = {
            "build_command": "make build",
            "artifacts": ["bin/polaroidd", "bin/polaroid"],
            "gate_commands": ["make check", "make vuln", "make demo", "make e2e", "make e2e-mcp E2E_INTEROP=0"],
            "working_tree": "clean",
        }
        decisions = {"build": True, "hosted-ci": False}

        def resolution_path(target: bool) -> str:
            q = {"environment": environment["name"]}
            if target:
                q["commit"] = commit
                q["inputs"] = json.dumps(inputs, separators=(",", ":"))
                q["decisions"] = json.dumps(decisions, separators=(",", ":"))
            return f"/v1/bindings/{binding_id}/resolution?{urlencode(q)}"

        # 1. resolve before anything ran (what an agent does first).
        rec.call("resolve_binding_pre", "GET", resolution_path(target=True))

        # 2. read the selected versions in full (root, build, checks), as
        #    the procedure's own "read" step requires.
        root_id = procedures["dev.change.verify"]
        build_id = procedures["go.module.build"]
        checks_id = procedures["go.module.checks"]
        rec.call("read_version_root", "GET", f"/v1/procedures/{root_id}/versions/4")
        rec.call("read_version_build", "GET", f"/v1/procedures/{build_id}/versions/3")
        rec.call("read_version_checks", "GET", f"/v1/procedures/{checks_id}/versions/4")

        # 3. record the build child.
        build_inputs = {"build_command": inputs["build_command"], "artifacts": inputs["artifacts"], "working_tree": "clean"}
        build_exec = rec.call("record_execution_build", "POST", "/v1/executions", {
            "procedure_id": build_id, "version": 3,
            "repository": "github.com/ashuangiras/polaroid", "commit": commit,
            "environment": environment, "inputs": build_inputs,
            "outcome": "succeeded",
            "evidence": {"commands": [{"run": "make build", "exit": 0}], "artifacts": inputs["artifacts"]},
        })

        # 4. record the checks child.
        checks_inputs = {"gate_commands": inputs["gate_commands"], "working_tree": "clean"}
        checks_exec = rec.call("record_execution_checks", "POST", "/v1/executions", {
            "procedure_id": checks_id, "version": 4,
            "repository": "github.com/ashuangiras/polaroid", "commit": commit,
            "environment": environment, "inputs": checks_inputs,
            "outcome": "succeeded",
            "evidence": {"commands": [
                {"run": "make check", "exit": 0, "excerpt": "0 issues."},
                {"run": "make vuln", "exit": 0, "excerpt": "No vulnerabilities found."},
                {"run": "make demo", "exit": 0, "excerpt": "demo: PASS"},
                {"run": "make e2e", "exit": 0, "excerpt": "passed=302 failed=0"},
                {"run": "make e2e-mcp E2E_INTEROP=0", "exit": 0, "excerpt": "passed=129 failed=0"},
            ]},
        })

        # 5. record the parent, linking both children; hosted-ci is decided
        #    not_applicable (this repository's recorded policy is local-only).
        parent_exec = rec.call("record_execution_parent", "POST", "/v1/executions", {
            "procedure_id": root_id, "version": 4,
            "binding_id": binding_id, "binding_revision": 1,
            "repository": "github.com/ashuangiras/polaroid", "commit": commit,
            "environment": environment, "inputs": inputs,
            "outcome": "succeeded",
            "children": [
                {"reference": "build", "execution_id": build_exec["id"]},
                {"reference": "checks", "execution_id": checks_exec["id"]},
            ],
            "decisions": [
                {"reference": "build", "applicable": True, "rationale": "effective inputs name build_command"},
                {"reference": "hosted-ci", "applicable": False, "rationale": "hosted-ci binding policy is local-only"},
            ],
            "evidence": {"scope": "full", "children": ["build", "checks"], "hosted_ci": "not_applicable (local-only policy)"},
        })

        # 6. confirm.
        rec.call("get_verification_parent", "GET", f"/v1/executions/{parent_exec['id']}/verification")
        rec.call("resolve_binding_post", "GET", resolution_path(target=True))

        manifest = [c.as_record() for c in rec.calls]
        (out_dir / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        for i, c in enumerate(rec.calls, start=1):
            prefix = out_dir / f"{i:02d}-{c.step}"
            if c.request_body is not None:
                (prefix.with_suffix(".request.json")).write_text(c.request_body + "\n")
            (prefix.with_suffix(".response.json")).write_text(c.response_body + "\n")

        total_req_tokens = sum(r["request_tokens_est"] for r in manifest)
        total_resp_tokens = sum(r["response_tokens_est"] for r in manifest)
        total_req_bytes = sum(r["request_bytes"] for r in manifest)
        total_resp_bytes = sum(r["response_bytes"] for r in manifest)

        pre = next(c for c in rec.calls if c.step == "resolve_binding_pre")
        post = next(c for c in rec.calls if c.step == "resolve_binding_post")
        # A concrete, same-workflow repetition: the graph tree returned by
        # resolve_binding before and after recording evidence. Measured as
        # the length of the longest common substring-free LCS ratio via
        # difflib, over the compact JSON text.
        import difflib
        sm = difflib.SequenceMatcher(a=pre.response_body, b=post.response_body, autojunk=False)
        unchanged_bytes = sum(block.size for block in sm.get_matching_blocks())
        resolve_repeat_ratio = unchanged_bytes / max(1, len(pre.response_body))

        summary = {
            "workflow": "verify-change (dev.change.verify v4: build conditional, checks pinned v4, hosted-ci not_applicable)",
            "calls": len(manifest),
            "total_request_bytes": total_req_bytes,
            "total_response_bytes": total_resp_bytes,
            "total_request_tokens_est": total_req_tokens,
            "total_response_tokens_est": total_resp_tokens,
            "resolve_binding_pre_vs_post": {
                "pre_response_bytes": len(pre.response_body.encode("utf-8")),
                "post_response_bytes": len(post.response_body.encode("utf-8")),
                "unchanged_bytes_vs_pre": unchanged_bytes,
                "unchanged_ratio_vs_pre": round(resolve_repeat_ratio, 4),
            },
            "largest_responses": sorted(
                ({"step": r["step"], "response_bytes": r["response_bytes"], "response_tokens_est": r["response_tokens_est"]} for r in manifest),
                key=lambda r: -r["response_bytes"],
            )[:5],
        }
        (out_dir / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
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
