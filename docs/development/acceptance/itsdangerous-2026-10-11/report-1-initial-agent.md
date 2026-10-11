Polaroid external-repository acceptance trial
Date: 2026-10-11. Result: bounded adoption and procedure reuse passed; fresh-agent independence remains untested.
Setup
- Existing project: https://github.com/pallets/itsdangerous, shallow-cloned at 672971d66a2ef9f85151e53283113f33d642dabd.
- Local branch: polaroid-acceptance. No upstream push, PR, or service modification.
- Published Linux amd64 release: Polaroid v0.7.0-verify.1, source 7f4ab44c9b73beac5d3e317573686c95b5cada3a.
- Archive SHA-256: 74c01ed9cf823f7e99e2f3af651b388b2d631e4f10469c4bb55abde766295ea3. Archive and both per-binary hashes matched the public manifest.
- Unmodified packaged smoke test: 42 passed, 0 failed.
- Empty isolated SQLite catalog, explicit database path and loopback endpoint. No Polaroid development fixtures imported.
- Public README, CLI help, advertised MCP schemas and HTTP documentation used for onboarding.
- Test environment: CPython 3.12.14, pytest 9.1.1, freezegun 1.5.5, Ruff 0.17.0.
Two real changes
These changes add missing coverage to the existing project's tests. They do not fix a demonstrated production bug.
| Stage | Change | Full suite | Exact task commit |
| --- | --- | --- | --- |
| Baseline | Existing checkout | 297 passed | 672971d66a2ef9f85151e53283113f33d642dabd |
| Task A | TimestampSigner expiry at ages 0, 10 and 11; expired payload/date metadata | 300 passed | 443599806b9ed9095c9d6961003bde947e47b892 |
| Task B | TimedSerializer expiry boundaries across its four payload fixtures | 312 passed | 6666071409b087805cc23a25aa0eb3571189675a |


Task A focused module: 104 passed. Task B focused module: 107 passed.
Task B Ruff lint and formatting checks passed. Ruff warned that the upstream UP038 ignore rule has been removed; no configuration was modified.
A temporary mutation changed age > max_age to age >= max_age. The boundary tests caught it: 2 failed, 4 passed. Original source bytes were restored in a finally block; focused and full suites then passed. No production source change remains.
Procedural memory loop
1. Discovery against the clean catalog returned no candidates.
2. Duplicate suggestions were checked before creating records.
3. Created shared python.pytest.verify v1 and local itsdangerous.expiry.boundary.test v1, with a required reference to the shared leaf and explicit input mapping.
4. Bound the parent to the external repository and followed it for Task A.
5. Recorded child and parent executions at Task A's exact commit; verification returned true.
6. Filed suggestion feedback to add repository style checks and a cutoff mutation control. This was a refinement, not a claim that v1 failed.
7. Appended parent v2 and binding revision 2.
8. MCP discovery ranked the existing v2 procedure first for the serializer task. Retrieved the stored instructions through CLI and MCP, then followed them for Task B. No duplicate procedure identity was created.
9. Task B's target was unverified before recording and verified after recording. Task A's historical version still verifies at its own commit.
Parent execution A: 01a12be6-4436-7c2f-a7aa-9991b3168644.
Parent execution B: 01a12be7-6e1f-7d41-b804-b08b5cd1ea1e.
Final catalog: 1 repository, 2 procedure identities, 3 procedure versions, 1 binding with 2 revisions, 4 executions, 2 child links, 1 feedback record. The shared leaf assumes a compatible src-layout Python project; this trial does not demonstrate portability to other layouts.
Interfaces and recovery
- Real MCP initialize/tools/list succeeded: 38 tools. Real MCP calls included discovery, get_version and statistics.
- HTTP, CLI and MCP returned equal logical counts, payload summaries and write-limit policy.
- A deliberately oversized evidence write returned 413 value_too_large and did not change record counts.
- Supported online backup and integrity inspection succeeded.
- Supported restore created a separate catalog. Its procedure/execution/feedback/repository/binding inventories exactly matched the original. Both parent executions still verified through a real daemon serving the restored database.
- Backup database size: 294,912 bytes; schema 10.
Limitations and decision
The same Codex agent authored and reused the procedures. MCP sessions and daemon processes were fresh, but the agent's conversational memory was not reset. A fresh independent agent or VS Code Copilot session was not tested. Therefore this is evidence of external-repository adoption and functioning procedural reuse, not completion of the independent-agent acceptance criterion.
Only local test-coverage tasks and one Python version were exercised. Native systemd installation, long-term growth, concurrent agents, million-record discovery, failure-driven repair and upstream acceptance were not tested. No token or duration savings are claimed.
The execution environment isolates networking per command. The trial's generic harness started clients and the daemon in the same command context, preserving the database across restarts. This was an environment constraint, not a Polaroid defect.
No blocking Polaroid defect was observed in the exercised path. The next acceptance step is a fresh agent retrieving these records without the first agent's task history, completing another bounded task, and recording truthful evidence.
Evidence is in evidence/ (not supplied with this copy). The two test changes are available as boundary-tests.patch. The report/evidence commit is separate from the tested task commits.