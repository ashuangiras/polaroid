#!/usr/bin/env bash
# Checks the procedure, repository and binding fixtures (ADR-0017) in an
# isolated catalog: polaroidd on a free loopback port, with a temporary
# database and HOME. For each fixture directory it loads the records through
# scripts/load-fixtures.sh, so the service accepts every version, reference,
# origin and binding; loads them again and requires that nothing was written;
# and resolves every loaded binding, which must select a complete graph.
# With RECORDS_FROM=BACKUP (a `polaroid backup` directory, or a database
# file) the catalog starts as a copy of that backup, for example of the live
# catalog, so the check also shows that
# the fixtures extend its stored history instead of conflicting with it.
#
# Usage: scripts/check-records.sh [DIR...]   (default: examples/development
#   examples/task-aware; run `make records-check`, which builds bin/ first;
#   needs jq)
set -euo pipefail
cd "$(dirname "$0")/.."
command -v jq >/dev/null 2>&1 || { echo "records-check: jq is required" >&2; exit 2; }
(($# > 0)) || set -- examples/development examples/task-aware

work="$(mktemp -d)"
pid=""
cleanup() {
	if [[ -n "$pid" ]]; then
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT
fail() {
	echo "records-check: FAIL: $*" >&2
	exit 1
}

mkdir -p "$work/home"
unset POLAROID_DB
if [[ -n "${RECORDS_FROM:-}" ]]; then
	src=$RECORDS_FROM
	[[ -d "$src" ]] && src="$src/polaroid.db"
	cp "$src" "$work/polaroid.db" || fail "cannot copy $src"
	echo "records-check: catalog copied from $src"
fi
HOME="$work/home" bin/polaroidd -addr 127.0.0.1:0 -db "$work/polaroid.db" 2>"$work/polaroidd.log" &
pid=$!
addr=""
for _ in $(seq 1 100); do
	addr="$(sed -n 's/.*msg="polaroidd listening" addr=\([^ ]*\).*/\1/p' "$work/polaroidd.log")"
	[[ -n "$addr" ]] && break
	kill -0 "$pid" 2>/dev/null || { cat "$work/polaroidd.log" >&2; fail "polaroidd exited during start-up"; }
	sleep 0.1
done
[[ -n "$addr" ]] || fail "polaroidd did not report a listening address"
export POLAROID_URL="http://$addr"

# inventory OUT prints every procedure, repository and binding the loaded fixtures name.
inventory() {
	bin/polaroid list
	bin/polaroid repositories
	jq -r '.bindings[].repository' <<<"$1" | sort -u | while IFS= read -r r; do bin/polaroid bindings "$r"; done
	jq -r '.bindings[].id' <<<"$1" | while IFS= read -r b; do bin/polaroid get-binding "$b"; done
}

for dir in "$@"; do
	loaded="$(scripts/load-fixtures.sh "$dir" 2>"$work/load.log")" || { cat "$work/load.log" >&2; fail "loading $dir"; }
	before="$(inventory "$loaded")"
	again="$(scripts/load-fixtures.sh "$dir" 2>"$work/load.log")" || { cat "$work/load.log" >&2; fail "loading $dir again"; }
	[[ "$again" == "$loaded" && "$(inventory "$loaded")" == "$before" ]] || fail "loading $dir again wrote to the catalog"
	resolved=0
	while IFS= read -r b; do
		graph="$(bin/polaroid resolve "$b" records.check)" || fail "binding $b of $dir does not resolve: $graph"
		jq -e '[.graph | .. | objects | select(has("procedure_id") and has("version"))] | length > 0' <<<"$graph" >/dev/null ||
			fail "binding $b of $dir resolved without a graph: $graph"
		resolved=$((resolved + 1))
	done < <(jq -r '.bindings[].id' <<<"$loaded")
	echo "records-check: $dir: $(jq '.procedures | length' <<<"$loaded") procedures and $resolved bindings ($(jq '[.bindings[].latest_revision] | add // 0' <<<"$loaded") binding revisions) loaded and resolved; a second load wrote nothing"
done
kill -TERM "$pid"
wait "$pid" || fail "polaroidd did not stop cleanly"
pid=""
echo "records-check: PASS"
