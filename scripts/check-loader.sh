#!/usr/bin/env bash
# Checks how scripts/load-fixtures.sh loads binding revisions, in an isolated
# catalog (polaroidd on a free loopback port, temporary database and HOME):
# creation of every represented revision in order, appending to an existing
# binding, a second load that writes nothing, refusal of a differing stored
# revision, validation of every binding fixture before any binding write, a
# stale base caused by a concurrent writer, revisions the store has beyond the
# fixtures, and procedure keys resolved to the catalog's own IDs. Each case
# uses its own repository and procedure, so cases do not interact.
#
# Usage: scripts/check-loader.sh   (run `make loader-check`, which builds bin/
#   first; needs jq)
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2
command -v jq >/dev/null 2>&1 || { echo "loader-check: jq is required" >&2; exit 2; }

work="$(mktemp -d)"
pid=""
cleanup() {
	if [[ -n "$pid" ]]; then
		kill "$pid" 2>/dev/null
		wait "$pid" 2>/dev/null
	fi
	rm -rf "$work"
}
trap cleanup EXIT
mkdir -p "$work/home"
unset POLAROID_DB LOAD_FIXTURES_CLI
HOME="$work/home" bin/polaroidd -addr 127.0.0.1:0 -db "$work/polaroid.db" 2>"$work/polaroidd.log" &
pid=$!
addr=""
for _ in $(seq 1 100); do
	addr="$(sed -n 's/.*msg="polaroidd listening" addr=\([^ ]*\).*/\1/p' "$work/polaroidd.log")"
	[[ -n "$addr" ]] && break
	kill -0 "$pid" 2>/dev/null || { cat "$work/polaroidd.log" >&2; exit 1; }
	sleep 0.1
done
[[ -n "$addr" ]] || { echo "loader-check: polaroidd did not start" >&2; exit 1; }
export POLAROID_URL="http://$addr"
cli=bin/polaroid

passed=0 failed=0
check() { # check DESCRIPTION COMMAND...
	if "${@:2}"; then
		passed=$((passed + 1))
	else
		failed=$((failed + 1))
		echo "loader-check: FAIL: $1" >&2
		tail -2 "$work/load.err" 2>/dev/null | sed 's/^/  last load: /' >&2
	fi
}

# fixture CASE [LATER_REVISIONS_JSON]: a directory with procedure loader.CASE
# (versions 1 and 2) and binding "check" in example.com/loader/CASE.
fixture() {
	local d="$work/$1-$RANDOM" key="loader.$1"
	mkdir -p "$d/procedures/p" "$d/bindings"
	jq -n --arg k "$key" '{canonical_key: $k, version: {philosophy: "p", method: "m", contract: {}, instructions: {}, revision_reason: "v1"}}' >"$d/procedures/p/v1.create.json"
	jq -n '{base_version: 1, version: {philosophy: "p", method: "m", contract: {}, instructions: {}, revision_reason: "v2"}}' >"$d/procedures/p/v2.revise.json"
	jq -n --arg k "$key" --arg r "example.com/loader/$1" --argjson later "${2:-null}" \
		'{repository: $r, name: "check", procedure_id: $k,
		  revision: {inputs: {n: 1}, version_policy: {contextual: {}}, revision_reason: "r1"}}
		 + (if $later then {later_revisions: $later} else {} end)' >"$d/bindings/check.json"
	echo "$d"
}
rev() { jq -cn --argjson b "$1" --argjson p "$2" --arg why "$3" '{base_revision: $b, revision: {inputs: {n: ($b + 1)}, version_policy: $p, revision_reason: $why}}'; }
later23="[$(rev 1 '{"pin": 2}' r2),$(rev 2 '{"contextual": {}}' r3)]"
load() { scripts/load-fixtures.sh "$1" 2>"$work/load.err"; }
binding_of() { "$cli" bindings "example.com/loader/$1" | jq -r '.bindings[] | select(.name == "check") | .id'; }
history_of() { "$cli" get-binding "$1" | jq -c '[.revisions[] | {revision, inputs, version_policy, revision_reason}]'; }
latest_of() { "$cli" get-binding "$1" | jq -r .latest_revision; }
fixture_history() { jq -c '[.revision] + [(.later_revisions // [])[].revision] | to_entries | map({revision: (.key + 1)} + .value)' "$1"; }

# 1. Empty catalog: the binding and every represented revision, in order.
d1="$(fixture s1 "$later23")"
out1="$(load "$d1")"
check "an empty catalog gets revisions 1..3" [ "$?" = 0 ]
b1="$(binding_of s1)"
check "the stored history equals the fixture, in order" [ "$(history_of "$b1")" = "$(fixture_history "$d1/bindings/check.json")" ]
check "the output reports 3 represented and latest revision 3" [ "$(jq -c '.bindings[0] | [.fixture_revisions, .latest_revision]' <<<"$out1")" = "[3,3]" ]
check "the binding references this catalog's procedure ID, from the key" \
	[ "$("$cli" get-binding "$b1" | jq -r .procedure_id)" = "$(jq -r '.procedures["loader.s1"]' <<<"$out1")" ]

# 2. A catalog with revision 1 only: revision 2 is appended to the same binding.
d2a="$(fixture s2)"
load "$d2a" >/dev/null
b2="$(binding_of s2)"
d2="$(fixture s2 "[$(rev 1 '{"pin": 2}' r2)]")"
out2="$(load "$d2")"
check "revision 2 is appended to the existing binding" [ "$(latest_of "$b2")" = 2 ]
check "the binding keeps its ID" [ "$(jq -r '.bindings[0].id' <<<"$out2")" = "$b2" ]
check "the loader reports the append" grep -q 'check: appended revision 2' "$work/load.err"

# 3. A matching catalog: a second load writes nothing.
again="$(load "$d1")"
check "a second load of the complete fixture succeeds" [ "$?" = 0 ]
check "a second load prints the same output" [ "$again" = "$out1" ]
# shellcheck disable=SC2016 # expanded by the inner bash
check "a second load writes nothing" bash -c '! grep -qE "created|appended|recorded|registered|added" "$1"' _ "$work/load.err"
check "a second load leaves revision 3 the latest" [ "$(latest_of "$b1")" = 3 ]

# 4. A stored revision that differs from its fixture is refused, never overwritten.
before4="$(history_of "$b1")"
jq '.later_revisions[0].revision.revision_reason = "edited"' "$d1/bindings/check.json" >"$work/c4.json" && cp "$work/c4.json" "$d1/bindings/check.json"
load "$d1" >/dev/null
check "a differing revision 2 fails the load" [ "$?" != 0 ]
check "the refusal names the revision" grep -q 'revision 2 in the store differs' "$work/load.err"
check "the stored history is unchanged" [ "$(history_of "$b1")" = "$before4" ]
d1="$(fixture s1 "$later23")"

# 5. Malformed fixtures are refused before any binding is written, even valid ones.
for bad in \
	"[$(rev 2 '{"contextual": {}}' gap)]" \
	"[$(rev 1 '{"contextual": {}}' x | jq -c 'del(.revision.revision_reason)')]" \
	"[$(rev 1 '{"pin": 9}' too-far)]" \
	"[$(rev 1 '{"pin": 1.5}' fraction)]" \
	"[$(rev 1 '{"contextual": {}}' x | jq -c '.revision.extra = 1')]"; do
	d5="$(fixture s5 "$bad")"
	jq '.repository = "example.com/loader/s5-valid" | .name = "valid" | del(.later_revisions)' "$d5/bindings/check.json" >"$d5/bindings/a-valid.json"
	load "$d5" >/dev/null
	check "malformed later revisions $bad fail the load" [ "$?" != 0 ]
	check "nothing was bound for $bad" [ -z "$(binding_of s5)$("$cli" bindings example.com/loader/s5-valid | jq -r '.bindings[].id')" ]
done
check "a gap names the expected base" grep -q 'must be {"base_revision": 1' <(d="$(fixture s5 "[$(rev 2 '{"contextual": {}}' gap)]")"; scripts/load-fixtures.sh "$d" 2>&1 >/dev/null)

# 6. A stale base: another writer appends between the loader's read and its revise.
d6a="$(fixture s6)"
load "$d6a" >/dev/null
b6="$(binding_of s6)"
cat >"$work/racing-cli" <<EOF
#!/usr/bin/env bash
if [[ \$1 == revise-binding && ! -e "$work/raced" ]]; then
	touch "$work/raced"
	echo '{"base_revision": 1, "revision": {"inputs": {"n": 99}, "version_policy": {"contextual": {}}, "revision_reason": "concurrent writer"}}' | "$PWD/bin/polaroid" revise-binding "\$2" >/dev/null
fi
exec "$PWD/bin/polaroid" "\$@"
EOF
chmod +x "$work/racing-cli"
d6="$(fixture s6 "$later23")"
LOAD_FIXTURES_CLI="$work/racing-cli" scripts/load-fixtures.sh "$d6" >/dev/null 2>"$work/load.err"
check "a stale base fails the load" [ "$?" != 0 ]
check "the failure says the base is stale and names the store's latest revision" grep -q 'base_revision 1 is stale, the store.s latest revision is now 2' "$work/load.err"
check "the concurrent revision is kept and nothing after it was appended" \
	[ "$("$cli" get-binding "$b6" | jq -c '[.latest_revision, (.revisions[] | select(.revision == 2) | .revision_reason)]')" = '[2,"concurrent writer"]' ]
load "$d6" >/dev/null
check "a rerun refuses the concurrent revision instead of merging it" grep -q 'revision 2 in the store differs' "$work/load.err"

# 7. Revisions beyond the fixtures are kept and reported; the store's latest is reported.
d7="$(fixture s7 "[$(rev 1 '{"pin": 2}' r2)]")"
load "$d7" >/dev/null
b7="$(binding_of s7)"
rev 2 '{"contextual": {}}' "added outside the fixtures" | "$cli" revise-binding "$b7" >/dev/null
out7="$(load "$d7")"
check "a store with an extra revision still loads" [ "$?" = 0 ]
check "the extra revision is reported" grep -q 'beyond the fixtures; its latest revision is 3, not the fixtures. 2' "$work/load.err"
check "the output reports 2 represented and latest revision 3" [ "$(jq -c '.bindings[0] | [.fixture_revisions, .latest_revision]' <<<"$out7")" = "[2,3]" ]
# shellcheck disable=SC2016 # expanded by the inner bash
check "the extra revision is kept and nothing is written" bash -c '[ "$1" = 3 ] && ! grep -qE "created|appended" "$2"' _ "$(latest_of "$b7")" "$work/load.err"

if ((failed > 0)); then
	echo "loader-check: FAIL (passed=$passed failed=$failed)"
	exit 1
fi
echo "loader-check: PASS (passed=$passed failed=0)"
