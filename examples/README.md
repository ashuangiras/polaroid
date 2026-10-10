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

## Task-aware verification

[task-aware/](task-aware) holds `change.verify.scoped`, which always runs `repo.checks.fast` and runs `repo.checks.integration` only when its `condition` holds: when the change can affect behaviour the integration suite observes ([ADR-0029](../docs/architecture/decisions/0029-conditional-references-and-applicability-decisions.md)). Its steps follow the [instruction-step convention](../docs/architecture/records.md#instruction-steps-recommended-convention) (`when`, `required_by`, `satisfied_when`, `done_when`, `escalate_when`). Load them with `scripts/load-fixtures.sh examples/task-aware`; `make demo` step 23 records a code change that runs both, a documentation change that skips integration with its reason, and an omitted decision and a missing child that are never verified.

## Polaroid's development procedures

[development/](development) holds the procedures used to develop Polaroid itself ([ADR-0017](../docs/architecture/decisions/0017-development-procedures-as-records.md)): `go.module.build`, `go.module.checks` and `dev.change.verify`, which references the other two, plus bindings in `github.com/ashuangiras/polaroid` and in a fixture-only second repository. Version 1 of `go.module.checks` is a labelled demonstration seed with one deliberately stale instruction; version 2 is the correction an agent appended. Version 2 of `dev.change.verify` teaches target verification ([#33](https://github.com/ashuangiras/polaroid/issues/33)): it was appended through MCP and exported here verbatim. For [#35](https://github.com/ashuangiras/polaroid/issues/35) the repository is registered (`repositories/`), each procedure records its origin (`origin.json`), and the latest versions of the three procedures above repeat their predecessors with a goal and shared applicability. `polaroid.record-model.change`, created through MCP during #35, is local to Polaroid: it changes Polaroid's own record model and references `dev.change.verify`. Version 4 of it, appended during [#50](https://github.com/ashuangiras/polaroid/issues/50), adds a conditional `lifecycle` reference to `polaroid.lifecycle.check`, also local to Polaroid, which runs `make lifecycle` against the real service manager only when the change can alter how Polaroid is installed or run as a service; these two were written here and loaded into the live development store. The rest were exported from it. [procedural-loop.md](../docs/development/procedural-loop.md) has the story and the evidence.

These fixtures name referenced procedures by canonical key instead of ID, so they are loaded with the loader rather than the commands above:

```sh
scripts/load-fixtures.sh examples/development        # every version
scripts/load-fixtures.sh -n 1 examples/development   # version 1 of each: the seed, before the corrections
```

The loader reuses existing identities, appends missing versions with the right `base_version`, refuses to overwrite a stored version that differs, and prints the IDs it used. `make demo` loads these fixtures and checks the results.

Fixtures may also register repositories (`repositories/*.json`: a register request plus optional `aliases`), record an origin once (`procedures/<name>/origin.json`), and name a repository by identifier wherever an ID is expected (`version.applicability.repository`, `origin.repository_id`); the loader replaces it with the registered ID, as it does canonical keys.

## Repositories A and B

[multi-repository/](multi-repository) is a fixture-only pair of repositories for [#35](https://github.com/ashuangiras/polaroid/issues/35). Neither repository exists. Repository A (with a mirror alias) and B both bind the shared `go.test.run` with different `packages`; A also has `service-a.release`, local to A, which composes `go.test.run`. `make demo` loads them and shows that B cannot bind A's local procedure, that a run in A verifies nothing in B, and how feedback and paged lists find their records.

The procedures in `procedures/` above declare no applicability: they are *unspecified* examples, bindable anywhere.
