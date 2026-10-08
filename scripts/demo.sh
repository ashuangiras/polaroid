#!/usr/bin/env bash
# Demonstrates procedure identity and immutable version storage against a real
# polaroidd process: create a procedure, read version 1, append a corrected
# version 2, confirm both versions remain, confirm a stale revision is
# rejected, restart the daemon, and confirm the history is unchanged.
#
# This verifies storage and versioning only. Execution evidence and contextual
# resolution are not implemented yet (docs/development/roadmap.md).
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

step "6. Restart polaroidd and confirm the history persisted"
stop_daemon
start_daemon
[[ "$(bin/polaroid get-by-key "$key")" == "$history" ]] || fail "history differs after restart"
echo "history for $key is byte-for-byte identical after restart"
stop_daemon

printf '\ndemo: PASS\n'
