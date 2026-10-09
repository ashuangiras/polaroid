#!/usr/bin/env bash
# Demonstrates procedure identity, immutable versions, repository bindings and
# execution records against a real polaroidd process: create a procedure,
# read version 1, append a corrected version 2, confirm both versions remain,
# confirm a stale revision is rejected, bind the procedure in two
# repositories, revise one binding and reject a stale binding revision,
# record an execution under that binding, restart the daemon, and confirm
# every record is unchanged.
#
# This verifies storage and versioning only. Contextual binding policies are
# stored but not resolved, and nothing verifies executions yet
# (docs/development/roadmap.md).
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
step "9. Restart polaroidd and confirm every record persisted"
stop_daemon
start_daemon
[[ "$(bin/polaroid get-by-key "$key")" == "$history" ]] || fail "history differs after restart"
[[ "$(bin/polaroid get-binding "$binding_a_id")" == "$binding_a_history" ]] || fail "$repo_a binding differs after restart"
[[ "$(bin/polaroid get-binding "$binding_b_id")" == "$binding_b_history" ]] || fail "$repo_b binding differs after restart"
[[ "$(bin/polaroid get-execution "$execution_id")" == "$execution" ]] || fail "execution differs after restart"
echo "history for $key, both bindings and the execution are byte-for-byte identical after restart"
stop_daemon

printf '\ndemo: PASS\n'
