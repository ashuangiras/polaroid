# Example procedure records

These files are task knowledge stored as records. Polaroid does not interpret them. Adding a task means adding records like these; it never needs a code change.

Each directory holds one procedure:

- `v1.create.json`: the request body for `POST /v1/procedures`, which creates the procedure and its version 1.
- `vN.revise.json` (N = 2, 3, …): the request body for `POST /v1/procedures/{id}/versions`, with `"base_version": N-1`.

| Directory | Canonical key | Versions | Shows |
| --- | --- | --- | --- |
| [go-dependency-add](procedures/go-dependency-add) | `go.dependency.add` | 2 | A corrected instruction appended as version 2, with the reason in `revision_reason` |
| [sqlite-schema-migrate](procedures/sqlite-schema-migrate) | `sqlite.schema.migrate` | 1 | A second, independent procedure |

To load them into a running daemon:

```sh
bin/polaroid create examples/procedures/go-dependency-add/v1.create.json   # prints the procedure, including its id
bin/polaroid revise <id> examples/procedures/go-dependency-add/v2.revise.json
bin/polaroid create examples/procedures/sqlite-schema-migrate/v1.create.json
```

`make demo` runs the first two commands against a temporary daemon and checks the results. `TestExampleRecordsAreAccepted` in [internal/transport/http](../internal/transport/http) submits every file in `procedures/` through the API, so an example that stops matching the record contract fails `make test`.

The `contract` and `instructions` members are free-form JSON objects. The shapes used here, such as `inputs`, `outputs` and `steps`, are conventions of these examples, not rules enforced by Polaroid.

## Discovering them before writing a new one

With the examples loaded, an agent describes its task in its own words and checks a proposal before creating it ([ADR-0032](../docs/architecture/decisions/0032-lexical-discovery-and-duplicate-suggestions.md)). Both are lexical, read-only, and leave the decision to the agent:

```sh
bin/polaroid discover "Adding a new module dependency with a permissive license" limit=3
# go.dependency.add first, with the words that matched per field (key, philosophy, ...)
bin/polaroid duplicates <<'EOF'
{"canonical_key": "go.module.require", "goal": "Require a third-party module only after checking its license.",
 "method": "Justify why the standard library is not enough, pin an explicit version, verify the license and record it in the dependency inventory.",
 "philosophy": "Every dependency is a liability: its code, license and maintenance become yours."}
EOF
# suggestions: go.dependency.add, with its similarity and shared words; no key_collision
```

`make e2e` runs these against the loaded example and checks the results; `make demo` step 24 does the same over Polaroid's development procedures.

## Task-aware verification

[task-aware/](task-aware) holds `change.verify.scoped`, which always runs `repo.checks.fast` and runs `repo.checks.integration` only when its `condition` holds: when the change can affect behaviour the integration suite observes ([ADR-0029](../docs/architecture/decisions/0029-conditional-references-and-applicability-decisions.md)). Its steps follow the [instruction-step convention](../docs/architecture/records.md#instruction-steps-recommended-convention) (`when`, `required_by`, `satisfied_when`, `done_when`, `escalate_when`). Load them with `scripts/load-fixtures.sh examples/task-aware`; `make demo` step 23 records a code change that runs both, a documentation change that skips integration with its reason, and an omitted decision and a missing child that are never verified.

## Polaroid's development procedures

[development/](development) holds the procedures used to develop Polaroid itself ([ADR-0017](../docs/architecture/decisions/0017-development-procedures-as-records.md)): `go.module.build`, `go.module.checks` and `dev.change.verify`, which references the other two, plus bindings in `github.com/ashuangiras/polaroid` and in a fixture-only second repository. Version 1 of `go.module.checks` is a labelled demonstration seed with one deliberately stale instruction; version 2 is the correction an agent appended. Version 2 of `dev.change.verify` teaches target verification ([#33](https://github.com/ashuangiras/polaroid/issues/33)): it was appended through MCP and exported here verbatim. For [#35](https://github.com/ashuangiras/polaroid/issues/35) the repository is registered (`repositories/`), each procedure records its origin (`origin.json`), and the latest versions of the three procedures above repeat their predecessors with a goal and shared applicability. `polaroid.record-model.change`, created through MCP during #35, is local to Polaroid: it changes Polaroid's own record model and references `dev.change.verify`. Version 4 of it, appended during [#50](https://github.com/ashuangiras/polaroid/issues/50), adds a conditional `lifecycle` reference to `polaroid.lifecycle.check`, also local to Polaroid, which runs `make lifecycle` against the real service manager only when the change can alter how Polaroid is installed or run as a service; these two were written here and loaded into the live development store. So were three created during [#52](https://github.com/ashuangiras/polaroid/issues/52), also local: `polaroid.archive.check` checks one packaged archive as users receive it (checksums, manifest commit, and its own `smoke-test.sh` under the platform's normal temporary directory, one with spaces, and failing runs); `polaroid.packaging.change` changes packaging, with `dev.change.verify` and that archive check required and the lifecycle check conditional; and `polaroid.release.publish` publishes a prerelease and checks its downloaded assets, with an upgrade of the shared instance (`polaroid.service.manage`) as a conditional reference. During [#56](https://github.com/ashuangiras/polaroid/issues/56), `polaroid.operations.change` was added for changes to how an operator manages a catalog (backup and restore): `dev.change.verify` is required, and the lifecycle and archive checks are conditional on whether the change touches the managed service or the packaged archive; version 3 of `polaroid.service.manage` backs up with `polaroid backup` and restores with `polaroid restore`. Version 4 ([#59](https://github.com/ashuangiras/polaroid/issues/59)) no longer assumes that a failed start means the daemon stopped: it states when restore rolls back and how to recover from `rollback-blocked`. The rest were exported from it. [procedural-loop.md](../docs/development/procedural-loop.md) has the story and the evidence.

For [#65](https://github.com/ashuangiras/polaroid/issues/65) ([ADR-0031](../docs/architecture/decisions/0031-scoped-verification-and-local-only-ci.md)), version 4 of `dev.change.verify` takes a `scope`, builds only when its inputs name a build, pins version 4 of `go.module.checks` (each gate command once, Go's test cache where the gate allows it), and references the new shared `dev.ci.hosted` conditionally, decided from the repository's recorded policy: the `hosted-ci` binding, `local-only` for Polaroid. Three procedures local to Polaroid call it at a narrower scope, each with a binding: `polaroid.change.docs` (`verify-docs`), `polaroid.change.records` (`verify-records`) and `polaroid.change.focused` (`verify-focused`). `polaroid.record-model.change` v5, `polaroid.packaging.change` v2 and `polaroid.release.publish` v2 no longer upgrade the shared instance on completion, or rely on pull-request and push CI. `make records-check` loads these fixtures into an isolated catalog twice and resolves every binding.

These fixtures name referenced procedures by canonical key instead of ID, so they are loaded with the loader rather than the commands above:

```sh
scripts/load-fixtures.sh examples/development        # every version
scripts/load-fixtures.sh -n 1 examples/development   # version 1 of each: the seed, before the corrections
```

The loader reuses existing identities, appends missing versions with the right `base_version`, refuses to overwrite a stored version that differs, and prints the IDs it used. `make demo` loads these fixtures and checks the results.

Fixtures may also register repositories (`repositories/*.json`: a register request plus optional `aliases`), record an origin once (`procedures/<name>/origin.json`), and name a repository by identifier wherever an ID is expected (`version.applicability.repository`, `origin.repository_id`); the loader replaces it with the registered ID, as it does canonical keys.

A binding fixture (`bindings/*.json`) is the create-binding request: `repository` (an identifier), `name`, `procedure_id` (a canonical key) and `revision`, which is revision 1. Its later revisions follow, in order, in `later_revisions`, each one the revise-binding request body: entry *i* (from 0) is `{"base_revision": i+1, "revision": {inputs, version_policy, revision_reason}}` and is revision *i*+2. A pin names a version number of the bound procedure, so it is portable between catalogs. Revisions are only appended, in the fixture as in the store; a fixture without `later_revisions` means revision 1 only, as before. The loader:

- checks every binding fixture (shape, numbering, the bound procedure and every pinned version) before it writes any binding;
- creates a missing binding with revision 1, compares each stored revision the fixture represents, and appends the missing ones in order;
- refuses a stored revision that differs, and stops on a stale base (another writer appended first), naming the store's latest revision; it never overwrites, recreates or merges;
- keeps revisions the store has beyond the fixture and reports them, with the store's `latest_revision` in its output.

A load is a series of API calls, not one transaction: if it stops, what it already wrote stays, and running it again after fixing the cause continues where it stopped. `make loader-check` exercises each of these cases in an isolated catalog.

## Repositories A and B

[multi-repository/](multi-repository) is a fixture-only pair of repositories for [#35](https://github.com/ashuangiras/polaroid/issues/35). Neither repository exists. Repository A (with a mirror alias) and B both bind the shared `go.test.run` with different `packages`; A also has `service-a.release`, local to A, which composes `go.test.run`. `make demo` loads them and shows that B cannot bind A's local procedure, that a run in A verifies nothing in B, and how feedback and paged lists find their records.

The procedures in `procedures/` above declare no applicability: they are *unspecified* examples, bindable anywhere.
