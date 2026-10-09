#!/usr/bin/env bash
# Demonstrates procedure identity, immutable versions, repository bindings and
# execution records against a real polaroidd process: create a procedure,
# read version 1, append a corrected version 2, confirm both versions remain,
# confirm a stale revision is rejected, bind the procedure in two
# repositories, revise one binding and reject a stale binding revision,
# record an execution under that binding.
#
# Then it replays the procedural-memory loop (#28) on Polaroid's development
# procedures: load the fixtures with scripts/load-fixtures.sh, resolve the
# binding over MCP, fail the labelled stale version, append the exported
# correction and refuse a stale base, and follow verification as the
# selected child version changes, the parent is re-recorded, a later failure
# withdraws it, and a second repository stays unverified. Outcomes are
# scripted: this is a regression check of Polaroid, not agent evidence.
# Finally it restarts the daemon and confirms every record is unchanged.
#
# Usage: scripts/demo.sh   (run `make build` first, or use `make demo`)
set -euo pipefail

cd "$(dirname "$0")/.."
command -v jq >/dev/null 2>&1 || { echo "demo: jq is required" >&2; exit 1; }
[[ -x bin/polaroidd && -x bin/polaroid ]] || { echo "demo: run 'make build' first" >&2; exit 1; }

example=examples/procedures/go-dependency-add
work="$(mktemp -d)"
db="$work/polaroid.db"
pid=""
starts=0

cleanup() {
	if [[ -n "$pid" ]]; then
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT

fail() {
	echo "demo: FAIL: $*" >&2
	exit 1
}

step() {
	printf '\n==> %s\n' "$*"
}

# start_daemon launches polaroidd on a free loopback port and waits until it
# logs the address it is listening on. Each start gets its own log file.
start_daemon() {
	starts=$((starts + 1))
	local log="$work/polaroidd.$starts.log"
	bin/polaroidd -addr 127.0.0.1:0 -db "$db" 2>"$log" &
	pid=$!
	local addr=""
	for _ in $(seq 1 100); do
		addr="$(sed -n 's/.*msg="polaroidd listening" addr=\([^ ]*\).*/\1/p' "$log")"
		[[ -n "$addr" ]] && break
		kill -0 "$pid" 2>/dev/null || { cat "$log" >&2; fail "polaroidd exited during start-up"; }
		sleep 0.1
	done
	[[ -n "$addr" ]] || fail "polaroidd did not report a listening address"
	export POLAROID_URL="http://$addr"
	echo "polaroidd pid $pid listening on $POLAROID_URL, database $db"
}

# stop_daemon sends SIGTERM and requires a clean (exit 0) shutdown.
stop_daemon() {
	kill -TERM "$pid"
	local status=0
	wait "$pid" || status=$?
	pid=""
	[[ $status -eq 0 ]] || fail "polaroidd exited with status $status on SIGTERM"
	echo "polaroidd stopped cleanly"
}

step "Start polaroidd"
start_daemon
bin/polaroid health

step "1. Create a procedure from $example/v1.create.json"
created="$(bin/polaroid create "$example/v1.create.json")"
id="$(jq -r .id <<<"$created")"
key="$(jq -r .canonical_key <<<"$created")"
jq -c '{id, canonical_key, latest_version}' <<<"$created"

step "2. Retrieve version 1"
v1="$(bin/polaroid get-version "$id" 1)"
jq -c '{version, license_step: (.instructions.steps[] | select(.id == "license") | .action)}' <<<"$v1"

step "3. Append corrected instructions as version 2"
v2="$(bin/polaroid revise "$id" "$example/v2.revise.json")"
jq -c '{version, license_step: (.instructions.steps[] | select(.id == "license") | .action)}' <<<"$v2"
jq -r '"revision_reason: " + .revision_reason' <<<"$v2"

step "4. Both versions remain available, and version 1 is unchanged"
history="$(bin/polaroid get "$id")"
jq -e '.latest_version == 2 and ([.versions[].version] == [1, 2])' <<<"$history" >/dev/null ||
	fail "history does not hold versions 1 and 2"
[[ "$(bin/polaroid get-version "$id" 1)" == "$v1" ]] || fail "version 1 changed after revision"
jq -c '{latest_version, versions: [.versions[] | {version, revision_reason}]}' <<<"$history"

step "5. A stale revision (base_version 1, latest is 2) is rejected"
status=0
stale="$(bin/polaroid revise "$id" "$example/v2.revise.json" 2>/dev/null)" || status=$?
[[ $status -eq 1 ]] || fail "stale revision exited $status, want 1"
jq -e '.error.code == "version_conflict" and .error.latest_version == 2' <<<"$stale" >/dev/null ||
	fail "unexpected stale-revision response: $stale"
[[ "$(bin/polaroid get "$id")" == "$history" ]] || fail "history changed after a rejected revision"
echo "exit status $status: $(jq -c .error <<<"$stale")"

# bind_request prints a create-binding request for this procedure.
bind_request() {
	jq -n --arg repository "$1" --arg procedure_id "$id" --argjson policy "$2" \
		'{repository: $repository, name: "add-dependency", procedure_id: $procedure_id,
		  revision: {inputs: {module: "modernc.org/sqlite"}, version_policy: $policy,
		             revision_reason: "Use the shared dependency procedure."}}'
}

step "6. Bind the procedure in two repositories without copying it"
repo_a=github.com/example/service-a
repo_b=scratch
binding_a="$(bind_request "$repo_a" '{"pin": 2}' | bin/polaroid bind)"
binding_b="$(bind_request "$repo_b" '{"contextual": {}}' | bin/polaroid bind)"
binding_a_id="$(jq -r .id <<<"$binding_a")"
binding_b_id="$(jq -r .id <<<"$binding_b")"
for binding in "$binding_a" "$binding_b"; do
	jq -e --arg id "$id" '.procedure_id == $id and .latest_revision == 1' <<<"$binding" >/dev/null ||
		fail "binding does not reference procedure $id: $binding"
	jq -c '{repository, name, procedure_id, version_policy: .revisions[0].version_policy}' <<<"$binding"
done
[[ "$(bin/polaroid get "$id")" == "$history" ]] || fail "binding changed the procedure's history"
jq -e --arg id "$binding_a_id" '[.bindings[].id] == [$id]' <<<"$(bin/polaroid bindings "$repo_a")" >/dev/null ||
	fail "$repo_a does not list exactly its own binding"
echo "both bindings reference procedure $id; its history is unchanged"

step "7. Revise the $repo_a binding, then reject a stale binding revision"
revise_binding='{"base_revision": 1, "revision": {"inputs": {"module": "modernc.org/sqlite"},
  "version_policy": {"pin": 1}, "revision_reason": "Pin version 1 until version 2 is verified here."}}'
revision="$(bin/polaroid revise-binding "$binding_a_id" <<<"$revise_binding")"
jq -c '{revision, version_policy, revision_reason}' <<<"$revision"
status=0
stale="$(bin/polaroid revise-binding "$binding_a_id" <<<"$revise_binding" 2>/dev/null)" || status=$?
[[ $status -eq 1 ]] || fail "stale binding revision exited $status, want 1"
jq -e '.error.code == "revision_conflict" and .error.latest_revision == 2' <<<"$stale" >/dev/null ||
	fail "unexpected stale binding revision response: $stale"
binding_a_history="$(bin/polaroid get-binding "$binding_a_id")"
binding_b_history="$(bin/polaroid get-binding "$binding_b_id")"
jq -e '[.revisions[].revision] == [1, 2]' <<<"$binding_a_history" >/dev/null ||
	fail "binding history does not hold revisions 1 and 2"
echo "exit status $status: $(jq -c .error <<<"$stale")"

step "8. Record an execution of version 1 under the $repo_a binding's revision 2"
commit=0123456789abcdef0123456789abcdef01234567
execution="$(jq -n --arg id "$id" --arg binding "$binding_a_id" --arg repository "$repo_a" --arg commit "$commit" \
	'{procedure_id: $id, version: 1, binding_id: $binding, binding_revision: 2, repository: $repository, commit: $commit,
	  environment: {name: "demo.local", attributes: {os: "any"}}, inputs: {module: "modernc.org/sqlite"},
	  outcome: "succeeded", evidence: {commands: [{run: "go list -deps -test ./...", exit: 0}]}}' |
	bin/polaroid record)"
execution_id="$(jq -r .id <<<"$execution")"
jq -c '{id, version, binding_revision, repository, outcome}' <<<"$execution"
[[ "$(bin/polaroid get-execution "$execution_id")" == "$execution" ]] || fail "execution reads back differently"
jq -e --arg id "$execution_id" '[.executions[].id] == [$id]' <<<"$(bin/polaroid executions "$id" "$repo_a")" >/dev/null ||
	fail "$repo_a does not list exactly its execution"
status=0
mismatch="$(jq '.version = 2' <<<"$(jq '{procedure_id, version, binding_id, binding_revision, repository, commit, environment, inputs, outcome, evidence}' <<<"$execution")" |
	bin/polaroid record 2>/dev/null)" || status=$?
jq -e '.error.fields[0].field == "version"' <<<"$mismatch" >/dev/null || fail "a version outside the binding's pin was recorded: $mismatch"
[[ "$(bin/polaroid get "$id")" == "$history" ]] || fail "recording an execution changed the procedure"
echo "version 2 under a revision that pins 1: exit status $status: $(jq -c .error.fields <<<"$mismatch")"

# The rest replays the procedural-memory loop of #28 on Polaroid's own
# development procedures (examples/development, ADR-0017). Every outcome
# below is scripted: it proves Polaroid's lifecycle, not an agent's
# reasoning. The live agent session is in docs/development/procedural-loop.md.
dev=examples/development
dev_repo=github.com/ashuangiras/polaroid
fixture_repo=example.com/fixtures/go-service
dev_commit=89abcdef0123456789abcdef0123456789abcdef
scripted='make demo replays the loop with fixed outcomes; a regression check, not an agent run'

# mcp_tool NAME ARGS calls one MCP tool (protocol 2026-07-28) and prints its
# result text, which is the HTTP API's document or error.
mcp_tool() {
	curl -sS -X POST "$POLAROID_URL/mcp" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
		-H 'Mcp-Protocol-Version: 2026-07-28' -H 'Mcp-Method: tools/call' -H "Mcp-Name: $1" \
		--data-binary "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2,\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\"2026-07-28\",\"io.modelcontextprotocol/clientCapabilities\":{},\"io.modelcontextprotocol/clientInfo\":{\"name\":\"demo\",\"version\":\"1\"}}}}" |
		jq -r '.result.content[0].text'
}

# One line per graph node: name@version selected_by verified_by.
selection_filter='"root@\(.graph.version) \(.selected_by) \(.graph.verified_by // "-")",
  (.graph.references[] | "\(.name)@\(.node.version) \(.selected_by) \(.node.verified_by // "-")")'
selection() { bin/polaroid resolve "$1" demo.ci | jq -r "$selection_filter"; }
expect_selection() { # expect_selection BINDING_ID WANT
	local got
	got="$(selection "$1")"
	[[ "$got" == "$2" ]] || fail "selection is
$got
want
$2"
	printf '%s\n' "$got"
}

# record_dev PROCEDURE_ID VERSION OUTCOME INPUTS [CHILDREN] prints the new execution's ID.
record_dev() {
	jq -n --arg p "$1" --argjson v "$2" --arg o "$3" --argjson in "$4" --argjson ch "${5:-[]}" \
		--arg r "$dev_repo" --arg c "$dev_commit" --arg s "$scripted" \
		'{procedure_id: $p, version: $v, repository: $r, commit: $c,
		  environment: {name: "demo.ci", attributes: {runner: "scripts/demo.sh"}},
		  inputs: $in, outcome: $o, evidence: {scripted: $s}}
		 + (if ($ch | length) > 0 then {children: $ch} else {} end)' |
		bin/polaroid record | jq -r .id
}
children() { jq -cn --arg b "$1" --arg c "$2" '[{reference: "build", execution_id: $b}, {reference: "checks", execution_id: $c}]'; }

# combinations prints dev.change.verify v1's combinations: the checks version, status and number of executions.
combinations() {
	bin/polaroid verifications "$verify_id" 1 |
		jq -c '[.verifications[] | {checks: (.combination.children[] | select(.reference == "checks") | .version), verified, runs: (.execution_ids | length)}]'
}
expect_combinations() {
	local got
	got="$(combinations)"
	[[ "$got" == "$1" ]] || fail "combinations are $got, want $1"
	echo "combinations: $got"
}

step "9. Load Polaroid's development procedures from $dev as first seeded (version 1 only)"
loaded="$(scripts/load-fixtures.sh -n 1 "$dev" 2>/dev/null)"
verify_id="$(jq -r '.procedures["dev.change.verify"]' <<<"$loaded")"
build_id="$(jq -r '.procedures["go.module.build"]' <<<"$loaded")"
checks_id="$(jq -r '.procedures["go.module.checks"]' <<<"$loaded")"
dev_binding="$(jq -r --arg r "$dev_repo" '.bindings[] | select(.repository == $r) | .id' <<<"$loaded")"
fixture_binding="$(jq -r --arg r "$fixture_repo" '.bindings[] | select(.repository == $r) | .id' <<<"$loaded")"
jq -e --arg b "$build_id" --arg c "$checks_id" '[.references[] | {name, procedure_id}] == [{name: "build", procedure_id: $b}, {name: "checks", procedure_id: $c}]' \
	<<<"$(bin/polaroid get-version "$verify_id" 1)" >/dev/null || fail "dev.change.verify does not reference the stored build and checks procedures"
for binding in "$dev_binding" "$fixture_binding"; do
	jq -e --arg id "$verify_id" '.procedure_id == $id and (any(paths; .[-1] == "instructions") | not)' <<<"$(bin/polaroid get-binding "$binding")" >/dev/null ||
		fail "binding $binding does not reference dev.change.verify by ID alone"
done
store="$(bin/polaroid list)"
[[ "$(scripts/load-fixtures.sh -n 1 "$dev" 2>/dev/null)" == "$loaded" && "$(bin/polaroid list)" == "$store" ]] || fail "loading the fixtures again changed the store"
jq -c '.procedures' <<<"$loaded"
echo "$dev_repo and $fixture_repo bind procedure $verify_id without copying it; a second load changed nothing"

step "10. Over MCP, resolve the $dev_repo binding: nothing is verified yet, so every version is the latest"
resolved="$(mcp_tool resolve_binding "{\"binding_id\":\"$dev_binding\",\"environment\":\"demo.ci\"}")"
want="root@1 latest -
build@1 latest -
checks@1 latest -"
[[ "$(jq -r "$selection_filter" <<<"$resolved")" == "$want" ]] || fail "unexpected MCP resolution: $resolved"
checks_v1="$(bin/polaroid get-version "$checks_id" 1)"
jq -e '(.revision_reason | contains("DEMONSTRATION SEED")) and any(.instructions.steps[]; .action | contains("./test/..."))' <<<"$checks_v1" >/dev/null ||
	fail "go.module.checks version 1 is not the labelled seed"
printf '%s\n' "$want"
echo "go.module.checks version 1 is the labelled demonstration seed (step unit runs go test ./test/...)"

step "11. A scripted run of the seeded version fails, and the parent is not verified"
dev_inputs="$(bin/polaroid get-binding-revision "$dev_binding" 1 | jq -c '.inputs + {working_tree: "clean"}')"
build_inputs="$(jq -c '{build_command, artifacts, working_tree}' <<<"$dev_inputs")"
checks_inputs="$(jq -c '{gate_commands, working_tree}' <<<"$dev_inputs")"
build1="$(record_dev "$build_id" 1 succeeded "$build_inputs")"
checks1="$(record_dev "$checks_id" 1 failed "$checks_inputs")"
parent1="$(record_dev "$verify_id" 1 failed "$dev_inputs" "$(children "$build1" "$checks1")")"
verification="$(bin/polaroid verification "$parent1")"
jq -e --arg c "$checks1" '.verified == false and [.problems[] | [.code, .reference // ""]] == [["outcome_failed", ""], ["child_not_verified", "checks"]]' \
	<<<"$verification" >/dev/null || fail "unexpected verification of the failed parent: $verification"
jq -c '{verified, problems}' <<<"$verification"

step "12. Append the correction (exported fixture v2.revise.json, replayed by the loader); a stale base is refused over MCP"
scripts/load-fixtures.sh "$dev" >/dev/null 2>&1 || fail "the loader did not append version 2"
jq -e '.latest_version == 2' <<<"$(bin/polaroid get "$checks_id")" >/dev/null || fail "go.module.checks has no version 2"
[[ "$(bin/polaroid get-version "$checks_id" 1)" == "$checks_v1" ]] || fail "version 1 changed"
conflict="$(mcp_tool revise_procedure "$(jq -c --arg id "$checks_id" '{procedure_id: $id, base_version: 1} + .version' "$dev/procedures/go-module-checks/v2.revise.json")")"
jq -e '.error.code == "version_conflict" and .error.latest_version == 2' <<<"$conflict" >/dev/null || fail "unexpected stale revision response: $conflict"
jq -e '.latest_version == 2' <<<"$(bin/polaroid get "$checks_id")" >/dev/null || fail "a stale revision was stored"
echo "stale base_version 1 over MCP: $(jq -c .error <<<"$conflict"); version 1 is unchanged"

step "13. A verified new child version changes the selected child, but the parent stays unverified"
expect_selection "$dev_binding" "root@1 latest -
build@1 evidence $build1
checks@2 latest -"
checks2="$(record_dev "$checks_id" 2 succeeded "$checks_inputs")"
expect_selection "$dev_binding" "root@1 latest -
build@1 evidence $build1
checks@2 evidence $checks2"
expect_combinations '[{"checks":1,"verified":false,"runs":1}]'

step "14. Recording the parent with the new combination verifies it; reordered input members are the same combination"
build2="$(record_dev "$build_id" 1 succeeded "$build_inputs")"
parent2="$(record_dev "$verify_id" 1 succeeded "$dev_inputs" "$(children "$build2" "$checks2")")"
jq -e '.verified and .combination.children == [{reference: "build", version: 1}, {reference: "checks", version: 2}]' \
	<<<"$(bin/polaroid verification "$parent2")" >/dev/null || fail "the parent with build v1 and checks v2 is not verified"
expect_selection "$dev_binding" "root@1 evidence $parent2
build@1 evidence $build2
checks@2 evidence $checks2"
reverse='to_entries | reverse | from_entries'
parent3="$(record_dev "$verify_id" 1 succeeded "$(jq -c "$reverse" <<<"$dev_inputs")" "$(children \
	"$(record_dev "$build_id" 1 succeeded "$(jq -c "$reverse" <<<"$build_inputs")")" \
	"$(record_dev "$checks_id" 2 succeeded "$(jq -c "$reverse" <<<"$checks_inputs")")")")"
expect_combinations '[{"checks":1,"verified":false,"runs":1},{"checks":2,"verified":true,"runs":2}]'

step "15. A later failure in that combination withdraws its verification; history keeps every execution"
checks4="$(record_dev "$checks_id" 2 failed "$checks_inputs")"
parent4="$(record_dev "$verify_id" 1 succeeded "$dev_inputs" "$(children "$(record_dev "$build_id" 1 succeeded "$build_inputs")" "$checks4")")"
jq -e '.verified == false and [.problems[].code] == ["child_not_verified"]' <<<"$(bin/polaroid verification "$parent4")" >/dev/null ||
	fail "a parent that claims success over a failed child was verified"
expect_combinations '[{"checks":1,"verified":false,"runs":1},{"checks":2,"verified":false,"runs":3}]'
expect_selection "$dev_binding" "root@1 latest -
build@1 evidence $(jq -r '.children[0].execution_id' <<<"$(bin/polaroid get-execution "$parent4")")
checks@2 latest -"
for run in "$parent2" "$parent3"; do
	jq -e '.verified' <<<"$(bin/polaroid verification "$run")" >/dev/null || fail "earlier verified execution $run is no longer verified"
done
jq -e '.outcome == "failed"' <<<"$(bin/polaroid get-execution "$checks1")" >/dev/null || fail "the failed run of version 1 is gone"
[[ "$(bin/polaroid get-version "$checks_id" 1)" == "$checks_v1" ]] || fail "version 1 changed"
echo "the original successful executions $parent2 and $parent3 remain verified; version 1 and its failed run remain readable"

step "16. The second repository shares the procedure, not its verification"
expect_selection "$fixture_binding" "root@1 latest -
build@1 latest -
checks@2 latest -"
jq -e '.executions == []' <<<"$(bin/polaroid executions "$verify_id" "$fixture_repo")" >/dev/null || fail "$fixture_repo has executions"
echo "$fixture_repo has no executions, so nothing is verified there"

dev_snapshot() {
	bin/polaroid get "$verify_id"
	bin/polaroid get "$checks_id"
	bin/polaroid executions "$verify_id"
	bin/polaroid verifications "$verify_id" 1
	bin/polaroid get-binding "$dev_binding"
}
dev_before="$(dev_snapshot)"

step "17. Restart polaroidd and confirm every record persisted"
stop_daemon
start_daemon
[[ "$(bin/polaroid get-by-key "$key")" == "$history" ]] || fail "history differs after restart"
[[ "$(bin/polaroid get-binding "$binding_a_id")" == "$binding_a_history" ]] || fail "$repo_a binding differs after restart"
[[ "$(bin/polaroid get-binding "$binding_b_id")" == "$binding_b_history" ]] || fail "$repo_b binding differs after restart"
[[ "$(bin/polaroid get-execution "$execution_id")" == "$execution" ]] || fail "execution differs after restart"
[[ "$(dev_snapshot)" == "$dev_before" ]] || fail "the development procedures, executions or verifications differ after restart"
echo "every history, binding, execution and verification is byte-for-byte identical after restart"
stop_daemon

printf '\ndemo: PASS\n'
