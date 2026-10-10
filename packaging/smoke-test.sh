#!/bin/sh
# smoke-test.sh: checks the polaroid and polaroidd beside this script without
# installing anything. It runs polaroidd directly with an explicitly named
# temporary database on a free loopback port, and exercises health, procedure
# create, get, revise, stale-base rejection, persistence across a restart and
# one MCP tool call.
#
# Needs: a POSIX shell, curl, and mktemp, sed, grep, tr, sleep, kill and ls.
# No Go, no source checkout, no root, no service manager.
# It never registers a service, never reads or writes ~/.polaroid (the daemon
# runs with HOME set to an empty temporary directory), and removes its
# temporary directory at the end (set KEEP=1 to keep it for inspection).
# It does NOT test the managed service (polaroid install, launchd, systemd);
# see README.md for that separate test.
#
# Usage: ./smoke-test.sh        exit 0 when every check passes
set -u
DIR=$(cd "$(dirname "$0")" && pwd)
P="$DIR/polaroid"
PD="$DIR/polaroidd"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/polaroid-smoke.XXXXXX") || exit 2
DB="$WORK/data/smoke.db"
ISOLATED_HOME="$WORK/home"
mkdir -p "$WORK/data" "$ISOLATED_HOME"
PASS=0
FAIL=0
DPID=""
URL=""

cleanup() {
	if [ -n "$DPID" ]; then
		kill "$DPID" 2>/dev/null
		wait "$DPID" 2>/dev/null
	fi
	if [ "${KEEP:-0}" = 1 ]; then
		echo "kept $WORK"
	else
		rm -rf "$WORK"
	fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

ok() {
	PASS=$((PASS + 1))
	echo "PASS: $1"
}
bad() {
	FAIL=$((FAIL + 1))
	echo "FAIL: $1"
	[ -n "${2:-}" ] && echo "      $2"
}
# check DESCRIPTION COMMAND...: passes when COMMAND exits 0.
check() {
	desc=$1
	shift
	if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi
}
contains() { case $1 in *"$2"*) return 0 ;; esac; return 1; }
lacks() { ! contains "$1" "$2"; }

# start_daemon: runs polaroidd on 127.0.0.1:0 (the kernel picks a free port)
# and reads the address it bound from its start-up line.
start_daemon() {
	: >"$WORK/daemon.log"
	HOME="$ISOLATED_HOME" "$PD" -addr 127.0.0.1:0 -db "$DB" 2>>"$WORK/daemon.log" &
	DPID=$!
	i=0
	URL=""
	while [ $i -lt 100 ]; do
		addr=$(sed -n 's/.*msg="polaroidd listening" addr=\([^ ]*\).*/\1/p' "$WORK/daemon.log")
		if [ -n "$addr" ]; then
			URL="http://$addr"
			return 0
		fi
		kill -0 "$DPID" 2>/dev/null || break
		sleep 0.1
		i=$((i + 1))
	done
	echo "polaroidd did not start; its log:"
	cat "$WORK/daemon.log"
	return 1
}
stop_daemon() {
	kill -TERM "$DPID" 2>/dev/null
	wait "$DPID"
	rc=$?
	DPID=""
	return $rc
}
cli() { "$P" -server "$URL" "$@"; }

echo "polaroid smoke test: $DIR"
echo "temporary directory: $WORK"

echo "[1] Versions"
V1=$("$P" version 2>&1) && contains "$V1" '"name":"polaroid"' && ok "polaroid version: $V1" || bad "polaroid version" "$V1"
V2=$("$PD" -version 2>&1) && contains "$V2" '"name":"polaroidd"' && ok "polaroidd -version: $V2" || bad "polaroidd -version" "$V2"
R1=$(printf '%s' "$V1" | sed -n 's/.*"revision":"\([0-9a-f]*\)".*/\1/p')
R2=$(printf '%s' "$V2" | sed -n 's/.*"revision":"\([0-9a-f]*\)".*/\1/p')
check "both report the same full 40-character commit" test "${#R1}" -eq 40 -a "$R1" = "$R2"
check "both report an unmodified source tree" lacks "$V1$V2" '"modified":true'

echo "[2] Start polaroidd on a temporary database and a loopback port"
if ! start_daemon; then
	bad "polaroidd starts"
	echo "smoke test: passed=$PASS failed=$FAIL"
	exit 1
fi
ok "polaroidd listens on $URL with -db $DB"
check "the address is loopback" contains "$URL" "http://127.0.0.1:"
check "the start-up line names the temporary database" grep -qF "db=$DB db_source=flag" "$WORK/daemon.log"
check "GET /healthz answers" curl -sf "$URL/healthz"
check "polaroid health succeeds" cli health

echo "[3] Create, read and revise a procedure"
CREATED=$(printf '%s' '{"canonical_key":"smoke.check","version":{"philosophy":"A smoke test record.","method":"Create it.","contract":{},"instructions":{"steps":["create"]},"revision_reason":"First version."}}' | cli create 2>&1)
ID=$(printf '%s' "$CREATED" | grep -o '"id":"[0-9a-f-]*"' | head -n 1 | sed 's/"id":"\(.*\)"/\1/')
if [ -n "$ID" ]; then ok "create: procedure $ID"; else bad "create" "$CREATED"; fi
GOT=$(cli get "$ID" 2>&1)
check "get returns it by ID" contains "$GOT" '"canonical_key":"smoke.check"'
check "get-by-key returns it" cli get-by-key smoke.check
REVISED=$(printf '%s' '{"base_version":1,"version":{"philosophy":"A smoke test record.","method":"Revise it.","contract":{},"instructions":{"steps":["create","revise"]},"revision_reason":"Second version."}}' | cli revise "$ID" 2>&1)
check "revise from base 1 appends version 2" contains "$REVISED" '"version":2'
STALE=$(printf '%s' '{"base_version":1,"version":{"philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"Stale."}}' | cli revise "$ID" 2>&1)
STALE_RC=$?
if [ $STALE_RC -eq 1 ] && contains "$STALE" '"code":"version_conflict"' && contains "$STALE" '"latest_version":2'; then
	ok "a revision from stale base 1 is rejected: version_conflict, latest 2"
else
	bad "stale-base rejection (exit $STALE_RC)" "$STALE"
fi
BEFORE=$(cli get "$ID" 2>&1)
check "the history has versions 1 and 2 and no third" lacks "$BEFORE" '"version":3'
check "the history includes version 2" contains "$BEFORE" '"version":2'

echo "[4] MCP: one tools/call over HTTP (protocol 2026-07-28, docs/architecture/mcp.md)"
MCP=$(curl -sS -X POST "$URL/mcp" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
	-H 'Mcp-Protocol-Version: 2026-07-28' -H 'Mcp-Method: tools/call' -H 'Mcp-Name: get_procedure' \
	--data-binary '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_procedure","arguments":{"id":"'"$ID"'"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"smoke-test","version":"1"}}}}' 2>&1)
if contains "$MCP" 'smoke.check' && ! contains "$MCP" '"isError":true' && ! contains "$MCP" '"error":{'; then
	ok "MCP get_procedure returns the procedure"
else
	bad "MCP get_procedure" "$MCP"
fi

echo "[5] Persistence across a restart"
if stop_daemon; then ok "SIGTERM stops polaroidd with exit status 0"; else bad "SIGTERM stops polaroidd with exit status 0"; fi
if start_daemon; then
	ok "polaroidd restarts on the same database ($URL)"
	AFTER=$(cli get "$ID" 2>&1)
	if [ "$AFTER" = "$BEFORE" ]; then ok "the procedure and its history read back unchanged"; else bad "the procedure changed across the restart" "$AFTER"; fi
	stop_daemon
else
	bad "polaroidd restarts"
fi

echo "[6] All data stayed in the temporary directory"
check "the database is in the temporary directory" test -s "$DB"
check "the isolated HOME is still empty (no ~/.polaroid was created)" test -z "$(ls -A "$ISOLATED_HOME")"

echo "smoke test: passed=$PASS failed=$FAIL"
echo "Not tested here: the managed service (polaroid install, launchd, systemd); see README.md."
[ "$FAIL" -eq 0 ]
