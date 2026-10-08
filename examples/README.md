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

`make demo` runs the first two commands against a temporary daemon and checks the results. `TestExampleRecordsAreAccepted` in [internal/transport/http](../internal/transport/http) submits every file here through the API, so an example that stops matching the record contract fails `make test`.

The `contract` and `instructions` members are free-form JSON objects. The shapes used here, such as `inputs`, `outputs` and `steps`, are conventions of these examples, not rules enforced by Polaroid.
