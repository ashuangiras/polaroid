Second independent Polaroid reuse trial

Added six cases to the existing boundary parametrization, without duplicating existing zero/ten-second coverage: max_age=1 at ages 0, 1, and 2 for TimestampSigner and TimedSerializer. Existing assertions verify expired payload (decoded for serializer) and original UTC signing time. No production changes.

Task commit: dba7446a923e4d38bcf06cb6db7204573b9c90bf.

Discovery used /v1/procedures/discovery?task=TimestampSigner%20TimedSerializer%20expiry%20regression%20tests&repository=github.com%2Fpallets%2Fitsdangerous, then retrieved versions and binding revision. Reused itsdangerous.expiry.boundary.test identity 01a12be5-7952-7aee-9994-85c08dbcb1e1 version 3 and its pinned python.pytest.verify identity 01a12be5-7950-747f-8d16-43af447b5014 version 1. Both procedures apply without adaptation. No procedure revision, creation, or feedback needed. Active identity count before/after: 2/2. Binding 01a12be5-7954-7b57-9c1b-b2aa5de9f8ab appended revision 4 for max_age=1; retained identity and pin 3.

Ruff check --no-fix and format --check passed. Both emit pre-existing removed-rule UP038 warnings. PYTHONPATH=src import resolved to this checkout. Exact task-commit focused run: 117 passed in 0.28s; full run: 337 passed in 0.69s. Python 3.12.14 on Linux only.

Stored mutation requirement executed: temporarily changed age > max_age to age >= max_age; pytest -q tests/test_itsdangerous/test_timed.py -k one-boundary failed both target classes (2 failed, 115 deselected). Source bytes restored in finally; post-restore focused/full suites passed and tracked checkout was clean at evidence recording.

Child execution 01a12bf1-7e86-788d-8a71-9b7ef3c8f99f and parent execution 01a12bf1-7e8a-7241-a900-5c69af333496 both verify true. Graph target verification true for task commit, environment acceptance.linux-python3.12, exact inputs max_age=1, and pinned child v1. Parent links the mapped-input child at the same commit. Attached JSON records provide exact evidence and target combination.

Onboarding friction and corrections:

    Initial source search rg -n 'expiry|max_age|freeze|timestamp' tests/test_timed.py tests/test_signer.py tests/test_serializer.py returned missing-file errors; retrieved contract supplied the correct nested path.
    Accidental git show review-99:docs/architecture/http-api.md from the ItsDangerous repo failed invalid object name; successful documentation read used the Polaroid repo.
    POST /v1/executions initially omitted environment.attributes; server returned HTTP 400 invalid_request, environment.attributes is required. Added attributes and retried.
    The first environment label incorrectly said Python 3.13. Explicit --version showed 3.12.14. Invalidated parent 01a12bf1-4925-7256-a456-a121e713004f and child 01a12bf1-4922-7ad9-ba9c-8a533a02f08f with the correction reason, then re-recorded under the accurate environment above. No inaccurate records remain valid evidence.

Only existing source/tests and public API docs were inspected; no prior report/evidence directories or other-agent notes were read. Infrastructure saved under the independently assigned evidence path. No push or upstream contact. Catalog verification checks stored claims and composition, not test-log authenticity. No other interpreters or hosted CI tested. This documentation/evidence commit is separate from the task commit; verification targets the task commit.