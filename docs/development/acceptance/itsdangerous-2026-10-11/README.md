# External acceptance trials on ItsDangerous (2026-10-11)

Three agent-written reports from trials of Polaroid in an existing open-source repository, [pallets/itsdangerous](https://github.com/pallets/itsdangerous). The owner ran them with Codex agents and supplied the reports afterwards. They concern the published **`v0.7.0-verify.1`** linux/amd64 build, not `v1.0.0` or `v1.0.1`.

| File | Trial | Agent |
| --- | --- | --- |
| [report-1-initial-agent.md](report-1-initial-agent.md) | Task A and Task B: expiry-boundary tests | The initial agent. It had prior Polaroid context (per the owner) and kept its conversation across both tasks, so it is not a fresh independent agent |
| [report-2-fresh-agent.md](report-2-fresh-agent.md) | Zero-second expiry | A fresh agent with no inherited conversation (as reported) |
| [report-3-second-fresh-agent.md](report-3-second-fresh-agent.md) | One-second expiry | A second fresh agent (as reported) |

## Provenance

- **Supplied:** the three Markdown reports, copied here with only the edits listed below.
- **Not supplied, so not inspected:**
  - the `evidence/` directories the reports link to: API responses, command output, `cli-friction.json` and `boundary-tests.patch`;
  - the trial catalog and its backups;
  - the agents' transcripts;
  - the ItsDangerous task commits, which were never pushed.
- **Nothing was imported.** No record from the trial catalog is in the shared catalog. The execution and procedure IDs in the reports belong to the trial's own catalog.

**Edits to the copies:**

- Links to the missing `evidence/` paths are replaced with plain text that says so.
- One absolute path from the agents' workspace (`/workspace/scratch/<id>/acceptance-trial/release/…`) is replaced with the release directory's name.
- The tab-separated table in report 1 is rendered as a Markdown table.

Nothing else is changed: numbers, IDs, commits and wording are as supplied.

## What was checked here

| Claim | Status | How |
| --- | --- | --- |
| The trials used the published `v0.7.0-verify.1` linux/amd64 archive, SHA-256 `74c01ed9cf823f7e99e2f3af651b388b2d631e4f10469c4bb55abde766295ea3`, source `7f4ab44` | **Checked here** | This is the digest the GitHub release records for `polaroid_v0.7.0-verify.1_linux_amd64.tar.gz`, and `7f4ab44` is the commit of tag `v0.7.0-verify.1` |
| The ItsDangerous base commit `672971d66a2ef9f85151e53283113f33d642dabd` is upstream history | **Checked here** | The GitHub API returns this commit in `pallets/itsdangerous` ("Merge branch 'stable'", 2025-06-14) |
| Task commits `4435998`, `6666071`, `bda2c77` and `dba7446` | **Not checkable** | The GitHub API finds none of them upstream, which is consistent with the reports' local-only commits; their contents were not supplied |
| `polaroid discover` rejected a task containing `max_age=0` with exit 2 (report 2) | **Reproduced here**, independently | The same defect was reproduced with the real `v0.7.0-verify.1` binary in [#102](https://github.com/ashuangiras/polaroid/issues/102) and fixed in `v1.0.0` (PR #103) |
| Procedures created, revised to v3 and reused unchanged; bindings, executions, feedback, invalidations; no duplicate identities; test counts (297, 300, 312, 322, 337); mutation results; backup and restore | **Agent report, not inspected** | The reports say these; their supporting records were not supplied |
| The fresh agents had no inherited conversation history and did not read earlier reports | **Agent report, not inspected** | Stated in reports 2 and 3. The harness, and whether it kept any persistent memory, were not observed here |

## What the reports say, by trial

- **Initial agent (report 1).**
  - On an empty isolated catalog, it created a shared `python.pytest.verify` v1 and a local `itsdangerous.expiry.boundary.test` v1.
  - It bound the local procedure, recorded verified executions for Task A, revised the procedure to v2 after its own feedback, and reused v2 for Task B.
  - It also took a backup, restored it into a separate catalog and compared the two.
  - Its own decision: this is "not completion of the independent-agent acceptance criterion".
- **Fresh agent (report 2).**
  - It discovered the existing procedure and judged v2 inapplicable to zero-second expiry.
  - It filed feedback and revised the same identity to v3. It reused the pytest subprocedure unchanged and appended binding revision 3.
  - It committed, then recorded verified executions at that commit. 322 tests passed.
  - It hit the CLI `=` defect and continued through HTTP.
- **Second fresh agent (report 3).**
  - It reused v3 unchanged for one-second expiry and appended binding revision 4. 337 tests passed.
  - It invalidated its own two executions, which had a wrong Python version in the environment label, and recorded accurate replacements.
- **Scope.** All three were bounded Python test-coverage tasks in one repository, one environment (Linux, CPython 3.12.14) and one isolated catalog, on `v0.7.0-verify.1`.

How this evidence bears on the `v1` acceptance criterion is in [release readiness](../../release-readiness.md#acceptance-by-independent-agents).
