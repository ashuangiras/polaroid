#!/usr/bin/env bash
# End-to-end test of everything implemented so far. Runs real binaries
# against a temporary database, records every command with its real output,
# checks each claim, and writes bin/e2e/REPORT.md.
# Usage: make e2e (needs bash 4+, curl, jq and sqlite3). Exits non-zero if any
# check fails. It does not rerun `make check`; `make ci` runs both.
# Variables are read inside the single-quoted commands that show() evaluates.
# shellcheck disable=SC2034
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2

((BASH_VERSINFO[0] >= 4)) || { echo "e2e: bash 4 or newer is required, found $BASH_VERSION" >&2; exit 2; }
for tool in curl jq sqlite3 shasum; do
	command -v "$tool" >/dev/null || { echo "e2e: $tool is required" >&2; exit 2; }
done
OUT=bin/e2e
mkdir -p "$OUT"
REPORT=$OUT/REPORT.md
BODY=$OUT/.body.md
WORK="$(mktemp -d)"
DB="$WORK/polaroid.db"
EX=examples/procedures/go-dependency-add
PID="" URL="" LOG="" starts=0
PASS=0 FAIL=0 SEC_PASS=0 SEC_FAIL=0 STEP=0
SECTIONS=()
: >"$BODY"

cleanup() {
	[[ -n "$PID" ]] && { kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null; }
	rm -rf "$WORK"
}
trap cleanup EXIT

md() { printf '%s\n' "$@" >>"$BODY"; }

close_section() {
	((STEP == 0)) && return
	local result=PASS
	((SEC_FAIL > 0)) && result="**FAIL ($SEC_FAIL)**"
	SECTIONS+=("| [$STEP]($(anchor)) | $TITLE | $SEC_PASS | $result |")
}
anchor() { printf '#%s' "$(printf '%s-%s' "$STEP" "$TITLE" | tr 'A-Z' 'a-z' | tr -cd 'a-z0-9 -' | tr ' ' '-')"; }

# section TITLE WHAT HOWTO
section() {
	close_section
	STEP=$((STEP + 1)) TITLE="$1" SEC_PASS=0 SEC_FAIL=0
	md "" "## $STEP $TITLE" "" "**What it is:** $2" "" "**How to do it yourself:** $3" "" "**Proof:**" ""
	echo "[$STEP] $TITLE" >&2
}

# show CMD: runs CMD in this shell and records it, its output and exit status.
show() {
	LAST="$(eval "$1" 2>&1)"
	RC=$?
	md '```console' "\$ $1"
	[[ -n "$LAST" ]] && md "$LAST"
	md "[exit status $RC]" '```'
}

# check CLAIM COMMAND...: records whether COMMAND succeeds.
check() {
	local claim="$1"
	shift
	if "$@" >/dev/null 2>&1; then
		PASS=$((PASS + 1)) SEC_PASS=$((SEC_PASS + 1))
		md "- **PASS**: $claim"
	else
		FAIL=$((FAIL + 1)) SEC_FAIL=$((SEC_FAIL + 1))
		md "- **FAIL**: $claim"
		echo "    FAIL: $claim" >&2
	fi
}
note() { md "" "$@" ""; }

# Assertions over the last recorded output.
rc_is() { [[ "$RC" == "$1" ]]; }
status_is() { [[ "$(head -n1 <<<"$LAST")" == "HTTP/1.1 $1 "* ]]; }
header_is() { grep -qix "$1" <<<"$LAST"; }
body_has() { tail -n1 <<<"$LAST" | jq -e "$@"; } # curl output: body is the last line
json_has() { head -n1 <<<"$LAST" | jq -e "$@"; } # CLI output: body is the first line
out_has() { grep -qF -- "$1" <<<"$LAST"; }
count_is() { grep -Eq "^ *$1 $2\$" <<<"$LAST"; } # count_is N CODE, over `sort | uniq -c` output
equal() { [[ "$1" == "$2" ]]; }
matches() { [[ "$1" =~ $2 ]]; }

# api METHOD PATH [BODY|@FILE] [curl args...]: one HTTP request; prints the
# status line, the interesting headers and the body. CT overrides the
# Content-Type ("-" sends none).
api() {
	local m=$1 p=$2 b=${3-}
	shift $(($# < 3 ? $# : 3))
	local args=(-sS -i -X "$m")
	if [[ -n "$b" ]]; then
		[[ "${CT:-application/json}" != "-" ]] && args+=(-H "Content-Type: ${CT:-application/json}")
		args+=(--data-binary "$b")
	fi
	curl "${args[@]}" "$@" "$URL$p" | tr -d '\r' | grep -iE '^(HTTP/|location:|allow:|content-type:|\{)'
}
get() { curl -sS "$URL$1"; }
digest() { get "$1" | shasum -a 256 | cut -c1-16; }

# race N PATH BODY: N concurrent POSTs released together; prints status codes.
race() {
	local n=$1 p=$2 b=$3 i
	for i in $(seq 1 "$n"); do
		curl -sS -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' --data-binary "$b" "$URL$p" &
	done
	wait
}

bind_req() { # bind_req REPOSITORY POLICY [NAME] [PROCEDURE_ID]
	jq -cn --arg r "$1" --argjson p "$2" --arg n "${3:-add-dependency}" --arg id "${4:-$ID}" \
		'{repository: $r, name: $n, procedure_id: $id,
		  revision: {inputs: {module: "modernc.org/sqlite"}, version_policy: $p, revision_reason: "Use the shared procedure."}}'
}
rev_req() { # rev_req BASE POLICY
	jq -cn --argjson b "$1" --argjson p "$2" \
		'{base_revision: $b, revision: {inputs: {module: "example.com/other"}, version_policy: $p, revision_reason: "Changed the module."}}'
}

start_daemon() { # start_daemon DB [env|flags]
	local db=$1 how=${2:-flags} addr=""
	starts=$((starts + 1))
	LOG="$WORK/polaroidd.$starts.log"
	if [[ "$how" == env ]]; then
		POLAROID_ADDR=127.0.0.1:0 POLAROID_DB="$db" bin/polaroidd 2>"$LOG" &
		md '```console' "\$ POLAROID_ADDR=127.0.0.1:0 POLAROID_DB=$db bin/polaroidd &"
	else
		bin/polaroidd -addr 127.0.0.1:0 -db "$db" 2>"$LOG" &
		md '```console' "\$ bin/polaroidd -addr 127.0.0.1:0 -db $db &"
	fi
	PID=$!
	for _ in $(seq 1 100); do
		addr="$(sed -n 's/.*msg="polaroidd listening" addr=\([^ ]*\).*/\1/p' "$LOG")"
		[[ -n "$addr" ]] && break
		kill -0 "$PID" 2>/dev/null || break
		sleep 0.1
	done
	md "$(cat "$LOG")" '```'
	URL="http://$addr"
	export POLAROID_URL="$URL"
}

stop_daemon() {
	kill -TERM "$PID"
	wait "$PID"
	RC=$?
	PID=""
	md '```console' '$ kill -TERM <pid>; wait <pid>' "$(cat "$LOG")" "[exit status $RC]" '```'
}

########################################################################
section "Build" \
	"Two binaries: \`polaroidd\` (the daemon, which owns the SQLite file and serves the JSON API) and \`polaroid\` (a thin CLI over the API). Pure Go; no C toolchain." \
	"\`make build\`, then \`bin/polaroidd -h\` and \`bin/polaroid help\`."
show 'make build'
check "make build succeeds" rc_is 0
show 'bin/polaroidd -h'
check "polaroidd prints its flags (-addr, -db) and exits 0" rc_is 0
check "usage mentions -addr and -db" out_has "-db"

########################################################################
section "Start the daemon and check health" \
	"\`polaroidd\` listens on loopback (\`127.0.0.1:7417\` by default; this test uses port 0 to pick a free port). Configuration comes from flags or the \`POLAROID_ADDR\` / \`POLAROID_DB\` environment variables. \`GET /healthz\` reports whether the database is reachable." \
	"\`bin/polaroidd -db polaroid.db &\` then \`bin/polaroid health\` (or \`curl -i http://127.0.0.1:7417/healthz\`)."
start_daemon "$DB" env
check "daemon took its database path from POLAROID_DB" grep -q "db=$DB" "$LOG"
check "listening address is loopback" matches "$URL" '^http://127\.0\.0\.1:[0-9]+$'
show 'bin/polaroid health'
check "CLI health exits 0" rc_is 0
check "CLI health prints {\"status\":\"ok\"}" json_has '.status == "ok"'
show 'api GET /healthz'
check "GET /healthz is 200" status_is 200
check "responses are JSON (Content-Type: application/json)" header_is "content-type: application/json"
show 'curl -sS -I "$URL/healthz" | tr -d "\r" | head -n1'
check "HEAD is accepted wherever GET is" out_has "200 OK"

########################################################################
section "Create a procedure" \
	"A procedure is a shared, repository-independent identity: a server-assigned UUIDv7 \`id\` and a unique \`canonical_key\`. It is created together with version 1, which holds philosophy, method, contract (JSON object), instructions (JSON object) and a revision reason." \
	"\`bin/polaroid create $EX/v1.create.json\` (note the \`id\` in the output). Over HTTP: \`POST /v1/procedures\` with that file as the body."
show "api POST /v1/procedures @$EX/v1.create.json"
check "201 Created" status_is 201
ID="$(tail -n1 <<<"$LAST" | jq -r .id)"
KEY="$(tail -n1 <<<"$LAST" | jq -r .canonical_key)"
check "Location header points at the new procedure" header_is "location: /v1/procedures/$ID"
check "id is a UUIDv7 ($ID)" matches "$ID" '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
check "canonical_key is go.dependency.add, latest_version is 1, one version" body_has '.canonical_key == "go.dependency.add" and .latest_version == 1 and (.versions | length) == 1'
check "version 1 carries the revision reason" body_has '.versions[0].revision_reason == "Initial version."'
note "For the rest of this report: \`ID=$ID\`."

########################################################################
section "Read procedures: list, by ID, by key, one version" \
	"Every read returns the full, consistent history (one SQL statement per read). Lookup by ID and by canonical key return byte-identical documents." \
	"\`bin/polaroid list\`, \`bin/polaroid get \$ID\`, \`bin/polaroid get-by-key go.dependency.add\`, \`bin/polaroid get-version \$ID 1\`."
show 'bin/polaroid list'
check "list contains the procedure with latest_version 1" json_has --arg id "$ID" '.procedures | length == 1 and .[0].latest_version == 1'
show 'bin/polaroid get "$ID" | shasum -a 256; bin/polaroid get-by-key go.dependency.add | shasum -a 256'
check "get by ID and get by key are byte-identical" equal "$(sed -n 1p <<<"$LAST" | cut -c1-64)" "$(sed -n 2p <<<"$LAST" | cut -c1-64)"
show 'bin/polaroid get-version "$ID" 1 | jq -c "{procedure_id, version, license_step: (.instructions.steps[] | select(.id == \"license\") | .action)}"'
check "version 1 is retrievable on its own" json_has '.version == 1'
V1="$(digest "/v1/procedures/$ID/versions/1")"
note "Digest of version 1 before any revision: \`$V1\`."

########################################################################
section "Revise a procedure (append version 2)" \
	"A revision names the version it was derived from (\`base_version\`). It is stored only if that base is still the latest version; the new version is base + 1. Older versions are never changed." \
	"\`bin/polaroid revise \$ID $EX/v2.revise.json\` (the file has \`\"base_version\": 1\`). Over HTTP: \`POST /v1/procedures/\$ID/versions\`."
show "api POST \"/v1/procedures/\$ID/versions\" @$EX/v2.revise.json | sed 's/\"instructions\":.*\"revision_reason\"/\"instructions\":{…},\"revision_reason\"/'"
check "201 Created" status_is 201
check "Location is /v1/procedures/\$ID/versions/2" header_is "location: /v1/procedures/$ID/versions/2"
show 'bin/polaroid get "$ID" | jq -c "{latest_version, versions: [.versions[] | {version, revision_reason: .revision_reason[0:60]}]}"'
check "history holds versions 1 and 2" json_has '.latest_version == 2 and ([.versions[].version] == [1, 2])'
show 'digest "/v1/procedures/$ID/versions/1"'
check "version 1 is byte-for-byte unchanged after the revision ($V1)" equal "$LAST" "$V1"

########################################################################
section "Stale revisions are rejected" \
	"Submitting a revision based on an outdated version is a conflict: \`409 version_conflict\` with \`latest_version\`, and nothing is stored. Conflicts are never merged or overwritten; the agent re-reads and revises again." \
	"Run the same \`bin/polaroid revise \$ID $EX/v2.revise.json\` a second time: it exits 1 and prints the conflict."
BEFORE="$(digest "/v1/procedures/$ID")"
show "api POST \"/v1/procedures/\$ID/versions\" @$EX/v2.revise.json"
check "409 Conflict" status_is 409
check "error code version_conflict with latest_version 2" body_has '.error.code == "version_conflict" and .error.latest_version == 2'
check "history is unchanged (digest $BEFORE)" equal "$(digest "/v1/procedures/$ID")" "$BEFORE"

########################################################################
section "Concurrent revisions: exactly one wins" \
	"Writes take SQLite's write lock at BEGIN, and the base check and insert happen in one transaction. Of N concurrent revisions from the same base, exactly one succeeds; the rest get 409." \
	"Fire several \`curl -X POST .../versions\` with the same \`base_version\` in parallel (as below)."
REV3="$(jq -c '.base_version = 2' "$EX/v2.revise.json")"
show 'race 8 "/v1/procedures/$ID/versions" "$REV3" | sort | uniq -c'
check "exactly one 201" count_is 1 201
check "seven 409" count_is 7 409
show 'bin/polaroid get "$ID" | jq -c "[.versions[].version]"'
check "versions are contiguous: [1,2,3]" json_has '. == [1, 2, 3]'

########################################################################
section "Canonical keys are unique; listing is ordered" \
	"A canonical key has one accepted spelling (lowercase, single separators), so exact-match uniqueness rules out case and separator variants. A duplicate gets \`409 canonical_key_exists\`." \
	"Run \`bin/polaroid create $EX/v1.create.json\` again; then create the second example and \`bin/polaroid list\`."
show "api POST /v1/procedures @$EX/v1.create.json"
check "409 canonical_key_exists" body_has '.error.code == "canonical_key_exists"'
show 'bin/polaroid create examples/procedures/sqlite-schema-migrate/v1.create.json | jq -c "{id, canonical_key, latest_version}"'
show 'bin/polaroid list | jq -c "[.procedures[] | {canonical_key, latest_version}]"'
check "two procedures, ordered by canonical key" json_has '[.[].canonical_key] == ["go.dependency.add", "sqlite.schema.migrate"]'

########################################################################
section "Compose procedures with subprocedure references" \
	"A version can list the procedures it composes (ADR-0008). Each reference has a unique \`name\`, a target \`procedure_id\`, a \`version_policy\` (\`{\"pin\": N}\` or \`{\"contextual\": {}}\`) and \`inputs\` mapping each child input to \`{\"input\": \"<parent input>\"}\` or \`{\"value\": <JSON>}\`. Targets and pinned versions must exist; references are returned byte-for-byte and are immutable with their version. Versions without references have no \`references\` field." \
	"Add a \`references\` list to the \`version\` object of \`bin/polaroid create\` or \`revise\` (example below)."
REFS="$(jq -cn --arg id "$ID" '[{name: "pinned-child", procedure_id: $id, version_policy: {pin: 2}, inputs: {module: {input: "driver"}, strict: {value: true}}},
  {name: "latest-child", procedure_id: $id, version_policy: {contextual: {}}, inputs: {}}]')"
PARENT_REQ="$(jq -cn --argjson refs "$REFS" '{canonical_key: "compose.parent", version: {philosophy: "Compose, do not copy.", method: "Delegate to children.", contract: {inputs: {driver: "module path"}}, instructions: {steps: ["run children"]}, references: $refs, revision_reason: "Initial version."}}')"
show 'jq . <<<"$PARENT_REQ"'
CHILD_BEFORE="$(digest "/v1/procedures/$ID")"
show 'api POST /v1/procedures "$PARENT_REQ"'
check "201 Created" status_is 201
PARENT="$(tail -n1 <<<"$LAST" | jq -r .id)"
check "references returned in order with compacted inputs" body_has --argjson refs "$REFS" '.versions[0].references == $refs'
show 'get "/v1/procedures/$PARENT/versions/1" | jq -c .references'
check "GET returns the references byte-for-byte" equal "$LAST" "$(jq -c . <<<"$REFS")"
check "referencing a procedure does not change it" equal "$(digest "/v1/procedures/$ID")" "$CHILD_BEFORE"
show 'get "/v1/procedures/$ID/versions/1" | jq -c "has(\"references\")"'
check "a version without references has no references field" equal "$LAST" false
BAD="$(jq -cn --arg id "$ID" '[{name: "ghost", procedure_id: "0192f7e4-0000-7000-8000-000000000000", version_policy: {pin: 1}, inputs: {}},
  {name: "too-new", procedure_id: $id, version_policy: {pin: 99}, inputs: {}},
  {name: "too-new", procedure_id: $id, version_policy: {contextual: {}}, inputs: {module: "driver"}}]')"
show 'api POST "/v1/procedures/$PARENT/versions" "$(jq -cn --argjson refs "$BAD" "{base_version: 1, version: {philosophy: \"p\", method: \"m\", contract: {}, instructions: {}, references: \$refs, revision_reason: \"r\"}}")" | tail -n1 | jq -c "[.error.fields[].field]"'
check "duplicate name and malformed mapping are named" json_has '. == ["version.references[2].name", "version.references[2].inputs.module"]'
show 'api POST "/v1/procedures/$PARENT/versions" "$(jq -cn --argjson refs "$(jq -c ".[0:2]" <<<"$BAD")" "{base_version: 1, version: {philosophy: \"p\", method: \"m\", contract: {}, instructions: {}, references: \$refs, revision_reason: \"r\"}}")" | tail -n1 | jq -c "[.error.fields[] | [.field, .message]]"'
check "unknown target and missing pinned version are named" json_has '[.[][0]] == ["version.references[0].procedure_id", "version.references[1].version_policy.pin"]'
show 'bin/polaroid get "$PARENT" | jq -c "[.versions[].version]"'
check "rejected revisions stored nothing" json_has '. == [1]'
show 'sqlite3 "$DB" "UPDATE procedure_version_references SET pinned_version = 1; INSERT INTO procedure_version_references VALUES ('"'"'$PARENT'"'"', 1, 9, '"'"'late'"'"', '"'"'$ID'"'"', '"'"'contextual'"'"', NULL, '"'"'{}'"'"');"'
check "raw SQL cannot change references or add one to an existing version" test "$RC" -ne 0

########################################################################
section "Composition graph and cycle rejection" \
	"\`GET /v1/procedures/{id}/versions/{n}/graph\` (CLI \`graph ID N\`) returns the nested tree of a version's references with the exact version each one selects: pinned references their pin, contextual ones the target's latest version (ADR-0009). Every write of a version with references is checked in its transaction: a path back to a procedure already on it (any version) is \`409 reference_cycle\` with the cycle path; graphs deeper than 32 or larger than 2048 nodes are \`422 graph_too_large\`. Nothing is stored on rejection." \
	"\`bin/polaroid graph \$PARENT 1\`; then try a revision whose references lead back to the procedure itself."
show 'bin/polaroid graph "$PARENT" 1 | jq -c "{root: \"\(.canonical_key)@\(.version)\", children: [.references[] | {name, policy: .version_policy, selected: \"\(.node.canonical_key)@\(.node.version)\"}]}"'
check "pinned child selects version 2, contextual child selects the latest (3)" json_has '.children == [{"name":"pinned-child","policy":{"pin":2},"selected":"go.dependency.add@2"},{"name":"latest-child","policy":{"contextual":{}},"selected":"go.dependency.add@3"}]'
SELF="$(jq -cn --arg id "$PARENT" '{base_version: 1, version: {philosophy: "p", method: "m", contract: {}, instructions: {}, references: [{name: "me", procedure_id: $id, version_policy: {pin: 1}, inputs: {}}], revision_reason: "r"}}')"
show 'api POST "/v1/procedures/$PARENT/versions" "$SELF" | tail -n1 | jq -c .error'
check "A -> A (even to an older version) is 409 reference_cycle with the path" json_has --arg id "$PARENT" '.code == "reference_cycle" and .cycle == [{"procedure_id":$id,"version":2,"reference":"me"},{"procedure_id":$id,"version":1}]'
show 'bin/polaroid create <<<"$(jq -cn --arg id "$PARENT" "{canonical_key: \"compose.uses-parent\", version: {philosophy: \"p\", method: \"m\", contract: {}, instructions: {}, references: [{name: \"parent\", procedure_id: \$id, version_policy: {contextual: {}}, inputs: {}}], revision_reason: \"r\"}}")" | jq -c "{id, canonical_key}"'
USER_ID="$(head -n1 <<<"$LAST" | jq -r .id)"
LOOP="$(jq -cn --arg id "$USER_ID" '{base_version: 1, version: {philosophy: "p", method: "m", contract: {}, instructions: {}, references: [{name: "back", procedure_id: $id, version_policy: {pin: 1}, inputs: {}}], revision_reason: "r"}}')"
show 'api POST "/v1/procedures/$PARENT/versions" "$LOOP" | tail -n1 | jq -c "{code: .error.code, message: .error.message}"'
check "A -> B -> A (B follows A's latest) is 409 reference_cycle" json_has '.code == "reference_cycle"'
show 'bin/polaroid get "$PARENT" | jq -c "[.versions[].version]"'
check "rejected revisions stored nothing" json_has '. == [1]'
show 'api POST /v1/procedures/$PARENT/versions/1/graph "{}" | head -n1'
check "graph is read-only (405)" out_has "405"

########################################################################
section "Input validation" \
	"Requests are decoded strictly: unknown, duplicate or server-assigned members are rejected (including planned fields such as \`aliases\`). Field validation lists every invalid field in \`fields\`. Contract and instructions must be JSON objects; they are compacted but otherwise kept exactly as sent." \
	"POST deliberately broken bodies, e.g. \`echo '{}' | bin/polaroid create\`."
show "api POST /v1/procedures '{}'"
check "400 listing every missing field" body_has '[.error.fields[].field] == ["canonical_key","version.philosophy","version.method","version.contract","version.instructions","version.revision_reason"]'
DEF='"philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"'
show "api POST /v1/procedures '{\"canonical_key\":\"Go Dependency\",\"version\":{$DEF}}'"
check "invalid canonical key: 400 naming canonical_key" body_has '.error.fields[0].field == "canonical_key"'
show "api POST /v1/procedures '{\"canonical_key\":\"x\",\"version\":{\"philosophy\":\"p\",\"method\":\"m\",\"contract\":[],\"instructions\":{},\"revision_reason\":\"r\"}}'"
check "non-object contract: 400 naming version.contract" body_has '.error.fields[0].field == "version.contract"'
show "api POST /v1/procedures '{\"canonical_key\":\"x\",\"version\":{$DEF,\"aliases\":[]}}'"
check "unknown field aliases is rejected" body_has '.error.code == "invalid_request" and (.error.message | test("unknown field"))'
show "api POST /v1/procedures '{\"id\":\"mine\",\"canonical_key\":\"x\",\"version\":{$DEF}}'"
check "server-assigned field id is rejected" body_has '.error.code == "invalid_request"'
show "api POST /v1/procedures '{\"canonical_key\":\"x\",\"canonical_key\":\"y\",\"version\":{$DEF}}'"
check "duplicate member is rejected" body_has '.error.code == "invalid_request"'
show "bin/polaroid create <<<'{\"canonical_key\":\"demo.compact\",\"version\":{\"philosophy\":\"p\",\"method\":\"m\",\"contract\":{ \"z\" : 1,  \"a\" : [ 1, 2 ] },\"instructions\":{},\"revision_reason\":\"r\"}}' | jq -c .versions[0].contract"
check "contract compacted, member order and values preserved" json_has '. == {"z":1,"a":[1,2]}'

########################################################################
section "HTTP protocol errors" \
	"Every response, errors included, is JSON with a stable \`code\`. Wrong media type is 415, bodies over 1 MiB are 413, unknown endpoints and IDs are 404, wrong methods are 405 with an \`Allow\` header, and a non-numeric version is 400." \
	"Use \`curl -i\` with the requests below."
show "CT=text/plain api POST /v1/procedures @$EX/v1.create.json"
check "415 unsupported_media_type" body_has '.error.code == "unsupported_media_type"'
{
	printf '{"canonical_key":"'
	head -c 1100000 /dev/zero | tr '\0' a
	printf '"}'
} >"$WORK/big.json"
show 'api POST /v1/procedures @"$WORK/big.json"   # a 1.1 MB body'
check "413 request_too_large" body_has '.error.code == "request_too_large"'
show 'api DELETE "/v1/procedures/$ID"'
check "405 method_not_allowed with Allow: GET, HEAD" header_is "allow: GET, HEAD"
show 'api GET /v1/procedures/0192f7e4-0000-7000-8000-000000000000; api GET /v2/anything'
check "unknown procedure and unknown endpoint are 404 not_found (JSON)" equal "$(grep -c '"code":"not_found"' <<<"$LAST")" 2
show 'api GET "/v1/procedures/$ID/versions/latest"'
check "non-numeric version is 400 naming version" body_has '.error.fields[0].field == "version"'

########################################################################
section "Local-only security" \
	"There is no authentication yet (ADR-0006), so the daemon defends its loopback API: a \`Host\` that is not a loopback name gets 403 (blocks DNS rebinding), and unsafe cross-origin browser requests get 403." \
	"\`curl -i -H 'Host: evil.example' http://127.0.0.1:7417/healthz\`; \`curl -i -X POST -H 'Sec-Fetch-Site: cross-site' ...\`."
show "api GET /healthz '' -H 'Host: evil.example'"
check "foreign Host: 403 forbidden" body_has '.error.code == "forbidden"'
show "api POST /v1/procedures '{\"canonical_key\":\"evil\",\"version\":{$DEF}}' -H 'Sec-Fetch-Site: cross-site'"
check "cross-site browser write: 403 forbidden" body_has '.error.code == "forbidden"'
show "api POST /v1/procedures '{\"canonical_key\":\"evil\",\"version\":{$DEF}}' -H 'Origin: https://attacker.example'"
check "foreign Origin write: 403 forbidden" body_has '.error.code == "forbidden"'
show 'bin/polaroid get-by-key evil 2>/dev/null'
check "nothing was created by the rejected requests" json_has '.error.code == "not_found"'

########################################################################
section "Bind one procedure in two repositories" \
	"A binding says \"this repository uses that procedure under this local name\". It stores the procedure ID, never a copy of the instructions, so every repository sees the same procedure history. Repositories are identified by a canonical path (ADR-0007): the remote's host and path in lowercase, or a chosen name like \`scratch\`." \
	"Write a request with \`repository\`, \`name\`, \`procedure_id\` and \`revision: {inputs, version_policy, revision_reason}\` and run \`bin/polaroid bind FILE\` (or pipe it on stdin)."
PROC_BEFORE="$(digest "/v1/procedures/$ID")"
show 'bind_req github.com/example/service-a "{\"pin\": 2}" | tee "$WORK/bind-a.json" | jq .'
show 'bin/polaroid bind "$WORK/bind-a.json"'
check "bind exits 0" rc_is 0
BA="$(head -n1 <<<"$LAST" | jq -r .id)"
check "binding A references procedure \$ID and pins version 2" json_has --arg id "$ID" '.procedure_id == $id and .revisions[0].version_policy == {"pin":2}'
check "binding does not copy instructions or philosophy" json_has '(tostring | test("instructions|philosophy")) | not'
show 'api POST /v1/bindings "$(bind_req scratch "{\"contextual\": {}}")"'
check "201 Created" status_is 201
BB="$(tail -n1 <<<"$LAST" | jq -r .id)"
check "Location is /v1/bindings/<id>" header_is "location: /v1/bindings/$BB"
check "binding B (other repository, same name) references the same procedure" body_has --arg id "$ID" '.procedure_id == $id and .repository == "scratch" and .name == "add-dependency"'
check "the procedure's history is unchanged by binding it (digest $PROC_BEFORE)" equal "$(digest "/v1/procedures/$ID")" "$PROC_BEFORE"
note "For the rest of this report: \`BA=$BA\` (github.com/example/service-a) and \`BB=$BB\` (scratch)."

########################################################################
section "Read bindings: list per repository, get, get one revision" \
	"Bindings are listed per repository (\`GET /v1/bindings?repository=...\`), ordered by name. A binding is returned with its full revision history. The list query is strict: \`repository\` is required, once, valid, and no other parameter is accepted." \
	"\`bin/polaroid bindings github.com/example/service-a\`, \`bin/polaroid get-binding \$BA\`, \`bin/polaroid get-binding-revision \$BA 1\`."
show 'bin/polaroid bindings github.com/example/service-a'
check "repository A lists exactly binding A" json_has --arg id "$BA" '[.bindings[].id] == [$id]'
show 'bin/polaroid bindings github.com/nobody/nothing'
check "unknown repository: empty list" json_has '. == {"bindings":[]}'
show 'bin/polaroid get-binding "$BA"'
check "get-binding returns the binding with latest_revision 1 and its revisions" json_has '.latest_revision == 1 and (.revisions | length) == 1'
show 'bin/polaroid get-binding-revision "$BA" 1'
check "get-binding-revision returns revision 1" json_has '.revision == 1 and .version_policy == {"pin":2}'
show 'for q in "" "?repository=scratch&repository=x" "?repository=scratch&limit=1" "?repository=GitHub.com/X/Y"; do api GET "/v1/bindings$q" | tail -n1; done'
check "missing, repeated, unknown and invalid query parameters all get 400" equal "$(grep -c '"code":"invalid_request"' <<<"$LAST")" 4

########################################################################
section "Revise a binding; stale and concurrent revisions" \
	"Binding revisions follow the same rule as procedure versions: each names \`base_revision\`, a stale base gets \`409 revision_conflict\` with \`latest_revision\`, and of concurrent revisions from one base exactly one wins." \
	"Write \`{\"base_revision\": 1, \"revision\": {...}}\` and run \`bin/polaroid revise-binding \$BA FILE\`."
show 'api POST "/v1/bindings/$BA/revisions" "$(rev_req 1 "{\"pin\": 1}")"'
check "201 Created, revision 2" body_has '.revision == 2 and .version_policy == {"pin":1}'
check "Location is /v1/bindings/\$BA/revisions/2" header_is "location: /v1/bindings/$BA/revisions/2"
BA_BEFORE="$(digest "/v1/bindings/$BA")"
show 'bin/polaroid revise-binding "$BA" <<<"$(rev_req 1 "{\"contextual\": {}}")"'
check "stale base_revision: CLI exits 1" rc_is 1
check "409 revision_conflict with latest_revision 2" json_has '.error.code == "revision_conflict" and .error.latest_revision == 2'
check "binding history unchanged by the stale revision" equal "$(digest "/v1/bindings/$BA")" "$BA_BEFORE"
show 'race 8 "/v1/bindings/$BA/revisions" "$(rev_req 2 "{\"pin\": 2}")" | sort | uniq -c'
check "exactly one of 8 concurrent revisions succeeded" count_is 1 201
check "the other seven got 409" count_is 7 409
show 'bin/polaroid get-binding "$BA" | jq -c "{latest_revision, revisions: [.revisions[] | {revision, version_policy}]}"'
check "revisions are contiguous [1,2,3] and revision 1 still pins 2" json_has '[.revisions[].revision] == [1,2,3] and .revisions[0].version_policy == {"pin":2}'

########################################################################
section "Binding rejections" \
	"Binding to an unknown procedure is 404. Pinning a version the procedure does not have is 400 naming \`revision.version_policy.pin\` (on create and on revise). A second binding with the same \`(repository, name)\` is \`409 binding_exists\`; the same name in another repository is allowed (binding B above)." \
	"Submit the requests below with \`bin/polaroid bind\` / \`revise-binding\`."
show 'api POST /v1/bindings "$(bind_req scratch "{\"pin\": 1}" other 0192f7e4-0000-7000-8000-000000000000)"'
check "unknown procedure: 404 not_found" body_has '.error.code == "not_found"'
show 'api POST /v1/bindings "$(bind_req scratch "{\"pin\": 99}" other)"'
check "missing pinned version: 400 naming revision.version_policy.pin" body_has '.error.fields == [{"field":"revision.version_policy.pin","message":(.error.fields[0].message)}] and (.error.fields[0].message | test("no version 99"))'
show 'api POST "/v1/bindings/$BA/revisions" "$(rev_req 3 "{\"pin\": 42}")"'
check "missing pinned version on revise: 400 naming revision.version_policy.pin" body_has '.error.fields[0].field == "revision.version_policy.pin"'
show 'api POST /v1/bindings "$(bind_req github.com/example/service-a "{\"contextual\": {}}")"'
check "duplicate (repository, name): 409 binding_exists" body_has '.error.code == "binding_exists"'
show 'bin/polaroid bindings scratch | jq -c "[.bindings[].name]"'
check "rejected requests created nothing in scratch" json_has '. == ["add-dependency"]'

########################################################################
section "Repository identifier and version policy validation" \
	"The repository must already be in canonical form; Polaroid never rewrites it, so uppercase, URLs, \`.git\` suffixes and empty or dot segments are refused. The version policy is exactly one of \`{\"pin\": N}\` (N >= 1) or \`{\"contextual\": {}}\` (no members yet)." \
	"Derive the identifier from your remote: \`git@github.com:Org/Repo.git\` becomes \`github.com/org/repo\`."
show 'for r in GitHub.com/org/repo https://github.com/org/repo github.com/org/repo.git github.com//repo github.com/../repo; do printf "%-32s " "$r"; api POST /v1/bindings "$(bind_req "$r" "{\"pin\": 1}")" | tail -n1 | jq -c "[.error.code, .error.fields[0].field]"; done'
check "all five non-canonical identifiers rejected with field repository" equal "$(grep -c '\["invalid_request","repository"\]' <<<"$LAST")" 5
show 'for p in "{\"pin\":1,\"contextual\":{}}" "{}" "{\"pin\":0}" "{\"pin\":\"2\"}" "{\"contextual\":{\"prefer\":\"latest\"}}" "{\"contextual\":true}" "{\"latest\":{}}"; do printf "%-36s " "$p"; api POST /v1/bindings "$(bind_req scratch "$p" bad-policy)" | tail -n1 | jq -c "[.error.code, .error.fields[0].field // .error.message]"; done'
check "all seven invalid policies rejected with 400" equal "$(grep -c '"invalid_request"' <<<"$LAST")" 7

########################################################################
section "Contextual policies are stored, not resolved" \
	"\`{\"contextual\": {}}\` is stored and returned exactly as given. Contextual resolution is increment 3: no endpoint resolves it and no response names a selected version." \
	"\`bin/polaroid get-binding \$BB\`."
show 'bin/polaroid get-binding "$BB" | jq -c ".revisions[0].version_policy"'
check "returned verbatim as {\"contextual\":{}}" json_has '. == {"contextual":{}}'
show 'bin/polaroid get-binding "$BB" | jq -c "[paths | map(tostring) | join(\".\")] | map(select(test(\"resolv|selected|version$\")))"'
check "no resolved/selected version field anywhere in the binding" json_has '. == []'
show 'api GET "/v1/bindings/$BB/resolve"'
check "no resolve endpoint (404)" status_is 404

########################################################################
section "Record executions" \
	"An execution is an immutable record of one finished run (ADR-0010): the exact procedure version, an optional binding revision (same procedure and repository; a pinned revision must pin the version that ran), the repository, a full commit hash, a named environment with free-form attributes, the effective inputs, \`succeeded\`/\`failed\`, and non-empty inline evidence. Lists are per procedure, oldest first, without inputs and evidence." \
	"Write the JSON below and run \`bin/polaroid record FILE\`; read it back with \`get-execution ID\` and \`executions PROCEDURE_ID [REPOSITORY]\`."
COMMIT=0123456789abcdef0123456789abcdef01234567
exec_req() { # exec_req VERSION BINDING_REVISION [OUTCOME]
	jq -cn --arg id "$ID" --arg b "$BA" --argjson v "$1" --argjson r "$2" --arg c "$COMMIT" --arg o "${3:-succeeded}" \
		'{procedure_id: $id, version: $v, binding_id: $b, binding_revision: $r, repository: "github.com/example/service-a", commit: $c,
		  environment: {name: "manual.laptop", attributes: {os: "darwin"}}, inputs: {module: "modernc.org/sqlite"},
		  outcome: $o, evidence: {commands: [{run: "go test ./...", exit: 0}]}}'
}
PROC_BEFORE="$(digest "/v1/procedures/$ID")$(digest "/v1/bindings/$BA")"
show 'exec_req 1 2 | jq .'
show 'exec_req 1 2 | bin/polaroid record'
check "recorded with the binding revision, exit 0" json_has --arg b "$BA" '.binding_id == $b and .binding_revision == 2 and .version == 1 and .outcome == "succeeded"'
EXEC="$(head -n1 <<<"$LAST" | jq -r .id)"
EXEC_BODY="$(head -n1 <<<"$LAST")"
show 'bin/polaroid get-execution "$EXEC"'
check "GET returns the recorded execution byte-for-byte" equal "$(head -n1 <<<"$LAST")" "$EXEC_BODY"
show 'exec_req 2 2 | bin/polaroid record 2>/dev/null | jq -c .error.fields'
check "version 2 under a revision pinning 1: 400 naming version" json_has '.[0].field == "version"'
show 'exec_req 1 9 | bin/polaroid record 2>/dev/null | jq -c .error.fields'
check "missing binding revision: 400 naming binding_revision" json_has '.[0].field == "binding_revision"'
show 'exec_req 1 2 | jq -c ".commit = \"0123456\" | .evidence = {}" | bin/polaroid record 2>/dev/null | jq -c "[.error.fields[].field]"'
check "abbreviated commit and empty evidence are rejected" json_has '. == ["commit", "evidence"]'
show 'exec_req 1 2 | jq -c ".procedure_id = \"0192f7e4-0000-7000-8000-000000000000\"" | bin/polaroid record 2>/dev/null | jq -c .error.code'
check "unknown procedure: 404 not_found" json_has '. == "not_found"'
show 'exec_req 2 3 failed | bin/polaroid record | jq -c "{version, binding_revision, outcome}"'
show 'bin/polaroid executions "$ID" github.com/example/service-a | jq -c "[.executions[] | {version, outcome, has_evidence: has(\"evidence\")}]"'
check "listed oldest first, without evidence" json_has '. == [{"version":1,"outcome":"succeeded","has_evidence":false},{"version":2,"outcome":"failed","has_evidence":false}]'
check "recording executions changed neither the procedure nor the binding" equal "$(digest "/v1/procedures/$ID")$(digest "/v1/bindings/$BA")" "$PROC_BEFORE"
show 'sqlite3 "$DB" "UPDATE executions SET outcome = '"'"'failed'"'"' WHERE id = '"'"'$EXEC'"'"'"'
check "raw SQL cannot change an execution" test "$RC" -ne 0

########################################################################
section "Link child executions to a parent execution" \
	"A child execution is an ordinary execution that fulfilled one of the parent version's references (ADR-0011). Children are recorded first; the parent lists them in \`children: [{reference, execution_id}]\`. A child must have run the reference's target (exactly the pin, if pinned; any version if contextual) in the parent's repository and commit. Each reference has at most one child, each execution at most one parent, none is required, and links never change. List summaries omit \`children\`." \
	"Record the children with \`bin/polaroid record\`, then record the parent with their IDs in \`children\` (example below)."
run_req() { # run_req PROCEDURE_ID VERSION [COMMIT] [CHILDREN_JSON]
	jq -cn --arg id "$1" --argjson v "$2" --arg c "${3:-$COMMIT}" --argjson ch "${4:-[]}" \
		'{procedure_id: $id, version: $v, repository: "github.com/example/service-a", commit: $c,
		  environment: {name: "manual.laptop", attributes: {}}, inputs: {}, outcome: "succeeded", evidence: {log: "ok"}}
		 | if ($ch | length) > 0 then .children = $ch else . end'
}
show 'run_req "$ID" 2 | bin/polaroid record | jq -c "{id, procedure: .procedure_id, version}"'
C2="$(head -n1 <<<"$LAST" | jq -r .id)"
show 'run_req "$ID" 3 | bin/polaroid record | jq -c "{id, procedure: .procedure_id, version}"'
C3="$(head -n1 <<<"$LAST" | jq -r .id)"
CHILDREN="$(jq -cn --arg a "$C2" --arg b "$C3" '[{reference: "pinned-child", execution_id: $a}, {reference: "latest-child", execution_id: $b}]')"
show 'run_req "$PARENT" 1 "$COMMIT" "$CHILDREN" | jq .'
show 'run_req "$PARENT" 1 "$COMMIT" "$CHILDREN" | bin/polaroid record'
check "parent recorded with both children, in order" json_has --argjson ch "$CHILDREN" '.children == $ch'
PEXEC="$(head -n1 <<<"$LAST" | jq -r .id)"
PEXEC_BODY="$(head -n1 <<<"$LAST")"
show 'bin/polaroid get-execution "$PEXEC"'
check "GET returns the parent and its links byte-for-byte" equal "$(head -n1 <<<"$LAST")" "$PEXEC_BODY"
show 'bin/polaroid get-execution "$C2" | jq -c "has(\"children\")"'
check "an execution without children has no children field" equal "$LAST" false
show 'bin/polaroid executions "$PARENT" | jq -c "[.executions[] | has(\"children\")]"'
check "list summaries omit children" json_has '. == [false]'
show 'run_req "$PARENT" 1 "$COMMIT" "$(jq -cn --arg b "$C3" "[{reference: \"pinned-child\", execution_id: \$b}]")" | bin/polaroid record 2>/dev/null | jq -c .error.fields'
check "a child of version 3 for a reference pinning 2: 400 naming children[0].execution_id" json_has '.[0].field == "children[0].execution_id" and (.[0].message | test("pins version 2"))'
show 'run_req "$PARENT" 1 "$COMMIT" "$(jq -cn --arg b "$C3" "[{reference: \"ghost\", execution_id: \$b}]")" | bin/polaroid record 2>/dev/null | jq -c .error.fields'
check "unknown reference: 400 naming children[0].reference" json_has '.[0].field == "children[0].reference"'
show 'run_req "$ID" 2 fedcba9876543210fedcba9876543210fedcba98 | bin/polaroid record | jq -c "{id, commit}"'
COTHER="$(head -n1 <<<"$LAST" | jq -r .id)"
show 'run_req "$PARENT" 1 "$COMMIT" "$(jq -cn --arg a "$COTHER" "[{reference: \"pinned-child\", execution_id: \$a}]")" | bin/polaroid record 2>/dev/null | jq -c .error.fields'
check "a child at another commit: 400 naming children[0].execution_id" json_has '.[0].field == "children[0].execution_id" and (.[0].message | test("not the parent'"'"'s"))'
show 'run_req "$PARENT" 1 "$COMMIT" "$(jq -cn --arg a "$C2" "[{reference: \"pinned-child\", execution_id: \$a}]")" | bin/polaroid record 2>/dev/null | jq -c .error.fields'
check "a child that already has a parent: 400 naming its parent" json_has --arg p "$PEXEC" '.[0].field == "children[0].execution_id" and (.[0].message | contains($p))'
show 'run_req "$PARENT" 1 "$COMMIT" "$(jq -cn --arg a "$C2" "[{reference: \"pinned-child\", execution_id: \$a}, {reference: \"pinned-child\", execution_id: \$a}]")" | bin/polaroid record 2>/dev/null | jq -c "[.error.fields[].field]"'
check "a repeated reference and a repeated child are both named" json_has '. == ["children[1].reference", "children[1].execution_id"]'
show 'bin/polaroid executions "$PARENT" | jq -c "[.executions[].id]"'
check "rejected parents stored nothing" json_has --arg p "$PEXEC" '. == [$p]'
show 'sqlite3 "$DB" "UPDATE execution_children SET reference = '"'"'latest-child'"'"' WHERE child_execution_id = '"'"'$C2'"'"'"'
check "raw SQL cannot change a link" test "$RC" -ne 0
show 'sqlite3 "$DB" "INSERT INTO execution_children VALUES ('"'"'$PEXEC'"'"', '"'"'$PARENT'"'"', 1, '"'"'github.com/example/service-a'"'"', '"'"'$COMMIT'"'"', 9, '"'"'late'"'"', '"'"'$COTHER'"'"')"'
check "raw SQL cannot add a link to a recorded parent" test "$RC" -ne 0

########################################################################
section "Verify executions in context" \
	"Verification is derived from executions on every read; nothing is stored (ADR-0012). An execution is verified when it succeeded and every reference of its version has a linked child that is itself verified, recursively; otherwise it lists its direct problems (\`outcome_failed\`, \`missing_child\`, \`child_not_verified\`). Each execution belongs to a combination: repository, commit, environment name, canonical inputs and the child-version tree. A combination's status is that of its latest execution." \
	"\`bin/polaroid verification EXECUTION_ID\`; \`bin/polaroid verifications PROCEDURE_ID N [REPO [COMMIT [ENV]]]\`."
show 'bin/polaroid verification "$PEXEC"'
check "the parent with verified children for both references is verified" json_has '.verified == true and (has("problems") | not)'
check "its combination has the child-version tree in reference order" json_has '.combination.children == [{"reference":"pinned-child","version":2},{"reference":"latest-child","version":3}] and .combination.environment == {"name":"manual.laptop"}'
show 'bin/polaroid verifications "$PARENT" 1 | jq -c "[.verifications[] | {verified, executions: (.execution_ids | length)}]"'
check "one combination, verified, with one execution" json_has '. == [{"verified":true,"executions":1}]'
child() { run_req "$ID" "$1" | jq -c ".outcome = \"${2:-succeeded}\"" | bin/polaroid record | jq -r .id; }
pair() { jq -cn --arg a "$1" --arg b "$2" '[{reference: "pinned-child", execution_id: $a}, {reference: "latest-child", execution_id: $b}]'; }
BADKID="$(child 3 failed)"
show 'run_req "$PARENT" 1 "$COMMIT" "$(pair "$(child 2)" "$BADKID")" | bin/polaroid record | jq -r .id'
PBAD="$LAST"
show 'bin/polaroid verification "$PBAD" | jq -c "{verified, problems}"'
check "a parent whose child failed: child_not_verified naming the child" json_has --arg c "$BADKID" '.verified == false and .problems == [{"code":"child_not_verified","reference":"latest-child","execution_id":$c}]'
show 'bin/polaroid verifications "$PARENT" 1 | jq -c "[.verifications[] | {verified, executions: (.execution_ids | length)}]"'
check "same combination, now judged by its latest execution: unverified" json_has '. == [{"verified":false,"executions":2}]'
show 'run_req "$PARENT" 1 "$COMMIT" "$(pair "$(child 2)" "$(child 3)")" | bin/polaroid record | jq -r .id; bin/polaroid verifications "$PARENT" 1 | jq -c "[.verifications[] | {verified, executions: (.execution_ids | length)}]"'
check "a newer success verifies it again; all three executions stay in its history" out_has '[{"verified":true,"executions":3}]'
show 'run_req "$PARENT" 1 "$COMMIT" "$(jq -cn --arg a "$(child 2)" "[{reference: \"pinned-child\", execution_id: \$a}]")" | bin/polaroid record | jq -r .id'
show 'bin/polaroid verification "$LAST" | jq -c "{verified, problems}"'
check "a parent without a child for every reference: missing_child" json_has '.verified == false and .problems == [{"code":"missing_child","reference":"latest-child"}]'
show 'run_req "$PARENT" 1 "$COMMIT" "$(pair "$(child 2)" "$(child 2)")" | bin/polaroid record >/dev/null; bin/polaroid verifications "$PARENT" 1 | jq -c "[.verifications[] | {tree: [.combination.children[]? | \"\(.reference)@\(.version)\"], verified, executions: (.execution_ids | length)}]"'
check "a different child version, or a missing child, is a separate combination" json_has '. == [{"tree":["pinned-child@2","latest-child@3"],"verified":true,"executions":3},{"tree":["pinned-child@2"],"verified":false,"executions":1},{"tree":["pinned-child@2","latest-child@2"],"verified":true,"executions":1}]'
canon() { run_req "$ID" 1 | jq -c --argjson i "$1" --argjson a "$2" '.inputs = $i | .environment = {name: "manual.canon", attributes: $a}' | bin/polaroid record | jq -r .id; }
show 'canon "{\"b\": 1.0, \"a\": [1e2]}" "{\"os\": \"darwin\"}"; canon "{\"a\": [100], \"b\": 1}" "{\"os\": \"darwin\", \"arch\": \"arm64\"}"; bin/polaroid verifications "$ID" 1 github.com/example/service-a "$COMMIT" manual.canon | jq -c "[.verifications[] | {inputs: .combination.inputs, executions: (.execution_ids | length)}]"'
check "member order, number spelling and environment attributes do not split a combination" out_has '[{"inputs":{"a":[100],"b":1},"executions":2}]'
show 'api GET "/v1/procedures/$ID/versions/1/verifications?commit=0123456" | tail -n1 | jq -c "[.error.code, .error.fields[0].field]"; api GET "/v1/procedures/$ID/versions/9/verifications" | head -n1; api POST "/v1/executions/$PEXEC/verification" "{}" | head -n1'
check "abbreviated commit filter: 400 naming commit" out_has '["invalid_request","commit"]'
check "unknown version: 404" out_has "404"
check "read-only: POST gets 405" out_has "405"

########################################################################
section "Resolve contextual references from evidence" \
	"With a repository and environment, contextual references resolve from verification evidence (ADR-0013). A version is verified there when its latest execution there is verified. Under a node with evidence, references follow the child executions it linked (a verified combination is used as a whole); otherwise a contextual reference takes the highest verified version, else the latest. Edges say \`selected_by\` (\`pin\`, \`evidence\`, \`latest\`); nodes with evidence name it in \`verified_by\`." \
	"\`bin/polaroid graph ID N REPO ENV\` and \`bin/polaroid resolve BINDING_ID ENV\`."
edges() { jq -c '{root_verified: has("verified_by"), edges: [.references[] | {name, version: .node.version, selected_by, verified: (.node | has("verified_by"))}]}'; }
show 'bin/polaroid graph "$PARENT" 1 | edges'
check "without context: pin, and the latest version (3) unverified" json_has '.root_verified == false and .edges == [{"name":"pinned-child","version":2,"selected_by":"pin","verified":false},{"name":"latest-child","version":3,"selected_by":"latest","verified":false}]'
show 'bin/polaroid graph "$PARENT" 1 github.com/example/service-a manual.laptop | edges'
check "in context: the parent's latest run is verified, so latest-child follows the version it linked (2), not the newer verified 3" json_has '.root_verified == true and .edges == [{"name":"pinned-child","version":2,"selected_by":"pin","verified":true},{"name":"latest-child","version":2,"selected_by":"evidence","verified":true}]'
SOLO="$(bin/polaroid create <<<"$(jq -cn --arg id "$ID" '{canonical_key: "compose.solo", version: {philosophy: "p", method: "m", contract: {}, instructions: {}, references: [{name: "dep", procedure_id: $id, version_policy: {contextual: {}}, inputs: {}}], revision_reason: "r"}}')" | jq -r .id)"
show 'bin/polaroid graph "$SOLO" 1 github.com/example/service-a manual.laptop | edges'
check "a parent without evidence: the highest version verified in the context (3)" json_has '.edges == [{"name":"dep","version":3,"selected_by":"evidence","verified":true}]'
show 'child 3 failed >/dev/null; bin/polaroid graph "$SOLO" 1 github.com/example/service-a manual.laptop | edges'
check "a newer failed run of 3 withdraws it: 2 is selected" json_has '.edges == [{"name":"dep","version":2,"selected_by":"evidence","verified":true}]'
show 'bin/polaroid graph "$SOLO" 1 github.com/example/service-a ci.elsewhere | edges'
check "another environment has no evidence: the latest version, unverified" json_has '.edges == [{"name":"dep","version":3,"selected_by":"latest","verified":false}]'
show 'bin/polaroid bind <<<"$(bind_req github.com/example/service-a "{\"contextual\": {}}" follow)" | jq -r .id'
FOLLOW="$LAST"
show 'for b in "$FOLLOW" "$BA" "$BB"; do bin/polaroid resolve "$b" manual.laptop | jq -c "{repository, binding_revision, selected_by, version: .graph.version, verified: (.graph | has(\"verified_by\"))}"; done'
check "contextual binding in service-a: highest verified version (2), by evidence" out_has '{"repository":"github.com/example/service-a","binding_revision":1,"selected_by":"evidence","version":2,"verified":true}'
check "binding A's latest revision pins 2: by pin, with its evidence" out_has '{"repository":"github.com/example/service-a","binding_revision":3,"selected_by":"pin","version":2,"verified":true}'
check "binding B is in scratch, where nothing ran: the latest version, unverified" out_has '{"repository":"scratch","binding_revision":1,"selected_by":"latest","version":3,"verified":false}'
show 'api GET "/v1/procedures/$SOLO/versions/1/graph?repository=github.com/example/service-a" | tail -n1 | jq -c "[.error.code, .error.fields[0].field]"; api GET "/v1/bindings/$FOLLOW/resolution" | tail -n1 | jq -c "[.error.code, .error.fields[0].field]"'
check "graph context needs both parameters; resolution needs an environment" equal "$LAST" '["invalid_request","environment"]
["invalid_request","environment"]'

########################################################################
section "Use Polaroid from an agent over MCP" \
	"\`polaroidd\` serves MCP at \`/mcp\` (ADR-0014): stateless streamable HTTP, protocols 2026-07-28 and 2025-11-25 (ADR-0016), behind the same loopback and cross-origin checks. 20 tools mirror the HTTP API with flat arguments named after record fields; results are the API's record shapes, errors are tool errors with the API's codes, and three read-only resource templates serve procedures, versions and bindings. Below, raw JSON-RPC over curl shows exactly what an MCP client sends." \
	"Point an MCP client at \`http://127.0.0.1:7417/mcp\`, e.g. VS Code \`.vscode/mcp.json\`: \`{\"servers\": {\"polaroid\": {\"type\": \"http\", \"url\": \"http://127.0.0.1:7417/mcp\"}}}\`."
MCP_META='{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"manual-test","version":"1"}}'
rpc() { # rpc METHOD NAME PARAMS [PROTOCOL]: one JSON-RPC request; NAME may be empty
	local meta params
	meta="$(jq -c --arg v "${4:-2026-07-28}" '."io.modelcontextprotocol/protocolVersion" = $v' <<<"$MCP_META")"
	params="$(jq -c --argjson m "$meta" '. + {_meta: $m}' <<<"$3")"
	curl -sS -X POST "$URL/mcp" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
		-H "Mcp-Protocol-Version: ${4:-2026-07-28}" -H "Mcp-Method: $1" ${2:+-H "Mcp-Name: $2"} \
		--data-binary "$(jq -cn --arg m "$1" --argjson p "$params" '{jsonrpc: "2.0", id: 1, method: $m, params: $p}')"
}
tool() { rpc tools/call "$1" "$(jq -cn --arg n "$1" --argjson a "$2" '{name: $n, arguments: $a}')"; }
show 'rpc server/discover "" "{}" | jq -c "{versions: .result.supportedVersions, capabilities: (.result.capabilities | keys), server: .result._meta[\"io.modelcontextprotocol/serverInfo\"].name}"'
check "server/discover: protocols 2026-07-28 and 2025-11-25, tools and resources" json_has '.versions == ["2026-07-28","2025-11-25"] and .capabilities == ["resources","tools"] and .server == "polaroid"'
show 'rpc tools/list "" "{}" | jq -c "{tools: (.result.tools | length), read_only: [.result.tools[] | select(.annotations.readOnlyHint) | .name] | length, names: [.result.tools[].name] | sort}"'
check "20 tools, 14 of them read-only" json_has '.tools == 20 and .read_only == 14'
show 'tool get_procedure "{\"canonical_key\": \"go.dependency.add\"}" | jq -S .result.structuredContent | shasum -a 256 | cut -c1-16; get /v1/procedures/by-key/go.dependency.add | jq -S . | shasum -a 256 | cut -c1-16'
check "get_procedure over MCP returns the same document as GET /v1/procedures/by-key" equal "$(sed -n 1p <<<"$LAST")" "$(sed -n 2p <<<"$LAST")"
show 'tool create_procedure "{\"canonical_key\": \"mcp.created\", \"philosophy\": \"p\", \"method\": \"m\", \"contract\": {\"z\": 1, \"a\": 2}, \"instructions\": {}, \"revision_reason\": \"Created by an agent over MCP.\"}" | jq -r ".result.content[0].text" | jq -c "{id, canonical_key, contract: .versions[0].contract}"'
check "create_procedure over MCP stores the record, keeping contract member order" json_has '.canonical_key == "mcp.created" and (.contract | keys_unsorted) == ["z","a"]'
MCPID="$(head -n1 <<<"$LAST" | jq -r .id)"
show 'bin/polaroid get "$MCPID" | jq -c "{canonical_key, latest_version}"'
check "the HTTP API sees the procedure the agent created" json_has '.canonical_key == "mcp.created" and .latest_version == 1'
show 'tool revise_procedure "$(jq -cn --arg id "$ID" "{procedure_id: \$id, base_version: 1, philosophy: \"p\", method: \"m\", contract: {}, instructions: {}, revision_reason: \"stale\"}")" | jq -c "{isError: .result.isError, error: (.result.content[0].text | fromjson | .error | {code, latest_version})}"'
check "a stale revise_procedure is a tool error: version_conflict with latest_version" json_has '.isError == true and .error.code == "version_conflict" and .error.latest_version == 3'
show 'tool create_procedure "{\"canonical_key\": \"mcp.bad\", \"method\": \"m\", \"contract\": {}, \"instructions\": {}, \"revision_reason\": \"r\", \"aliases\": []}" | jq -c ".result.content[0].text | fromjson | .error | {code, message}"; tool create_procedure "{\"canonical_key\": \"mcp.bad\", \"method\": \"m\", \"contract\": {}, \"instructions\": {}, \"revision_reason\": \"r\"}" | jq -c ".result.content[0].text | fromjson | .error | {code, fields: [.fields[].field]}"'
check "an unknown argument is rejected" out_has '"message":"unknown field \"/aliases\""'
check "a missing argument is named with its flat name" out_has '{"code":"invalid_request","fields":["philosophy"]}'
show 'rpc resources/read "polaroid://procedures/$ID" "{\"uri\": \"polaroid://procedures/$ID\"}" | jq -r ".result.contents[0].text" | jq -S . | shasum -a 256 | cut -c1-16; get "/v1/procedures/$ID" | jq -S . | shasum -a 256 | cut -c1-16'
check "resource polaroid://procedures/{id} matches GET /v1/procedures/{id}" equal "$(sed -n 1p <<<"$LAST")" "$(sed -n 2p <<<"$LAST")"
show 'rpc tools/call list_procedures "{\"name\": \"list_procedures\", \"arguments\": {}}" 2025-06-18'
check "a request at protocol 2025-06-18 is refused before any tool runs" out_has "Unsupported protocol version (supported versions: 2026-07-28,2025-11-25)"
show 'curl -sS -o /dev/null -w "%{http_code}\n" -X POST "$URL/mcp" -H "Host: evil.example" -H "Content-Type: application/json" --data-binary "{}"'
check "a foreign Host on /mcp gets 403" equal "$LAST" 403

########################################################################
section "Feedback box: report problems and suggestions about Polaroid" \
	"Agents (and people) record what got in their way, or what would help, as immutable feedback reports (ADR-0015): \`kind\` problem or suggestion, a one-line \`summary\`, \`details\`, a canonical-key \`reporter\` and an optional free-form \`context\`. Polaroid stores and lists them but tracks no triage state; triage happens elsewhere, for example as GitHub issues. Reporting changes no other record." \
	"\`bin/polaroid feedback report.json\` (or stdin), \`bin/polaroid feedbacks [problem|suggestion]\`, \`bin/polaroid get-feedback ID\`; agents use the MCP tool \`report_feedback\`."
FB_BEFORE="$(digest /v1/procedures)$(digest "/v1/procedures/$ID")$(digest "/v1/bindings/$BA")$(digest "/v1/executions?procedure_id=$SOLO")"
jq -n '{kind: "problem", summary: "record_execution rejected a short commit without saying why", details: "I passed a 7-character hash.\nThe error named the field but not the rule.", reporter: "copilot.vscode", context: {tool: "record_execution", z: 1, a: 2}}' >"$WORK/feedback.json"
show 'jq -c . "$WORK/feedback.json"'
show 'api POST /v1/feedback @"$WORK/feedback.json"'
check "201 Created" status_is 201
FB1="$(tail -n1 <<<"$LAST" | jq -r .id)"
check "Location header points at the new report" header_is "location: /v1/feedback/$FB1"
check "the body is the request plus id and created_at, context member order kept" body_has '.kind == "problem" and .reporter == "copilot.vscode" and (.details | contains("\n")) and (.context | keys_unsorted) == ["tool","z","a"] and has("created_at")'
FB1_BODY="$(tail -n1 <<<"$LAST")"
show 'bin/polaroid get-feedback "$FB1"'
check "get-feedback returns the same bytes" equal "$LAST" "$FB1_BODY"
show 'bin/polaroid feedback <<<"{\"kind\": \"suggestion\", \"summary\": \"List feedback by reporter\", \"details\": \"Would help triage.\", \"reporter\": \"manual.tester\"}"'
check "without context, the report has context {}" json_has '.kind == "suggestion" and .context == {}'
FB2="$(head -n1 <<<"$LAST" | jq -r .id)"
show 'bin/polaroid feedbacks | jq -c "[.feedback[] | {kind, summary}]"; bin/polaroid feedbacks problem | jq -c "[.feedback[].kind]"; bin/polaroid feedbacks suggestion | jq -c "[.feedback[].kind]"'
check "listed oldest first, every field" equal "$(bin/polaroid feedbacks | jq -r '[.feedback[].id] | join(",")')" "$FB1,$FB2"
check "filter by kind" equal "$(sed -n 2,3p <<<"$LAST")" '["problem"]
["suggestion"]'
show 'bin/polaroid feedback <<<"{\"kind\": \"bug\", \"summary\": \"one\\ntwo\", \"details\": \" \", \"reporter\": \"Manual Tester\", \"context\": [1]}" 2>/dev/null | jq -c "{code: .error.code, fields: [.error.fields[].field]}"'
check "invalid kind, multi-line summary, blank details, bad reporter and non-object context are each named" json_has '.code == "invalid_request" and .fields == ["kind","summary","details","reporter","context"]'
show 'for q in "?kind=bug" "?kind=" "?kind=problem&kind=suggestion" "?reporter=x"; do api GET "/v1/feedback$q" | head -n1; done'
check "all four list queries are 400" equal "$(grep -c '^HTTP/1.1 400 ' <<<"$LAST")" 4
show 'api GET /v1/feedback/0192f7e4-0000-7000-8000-000000000000 | tail -n1; api DELETE "/v1/feedback/$FB1" | head -n1'
check "unknown report: not_found" out_has '"code":"not_found"'
check "reports cannot be deleted over HTTP: 405" out_has "HTTP/1.1 405 "
show 'tool report_feedback "{\"kind\": \"problem\", \"summary\": \"Reported by an agent over MCP\", \"details\": \"The report_feedback tool.\", \"reporter\": \"manual.agent\", \"context\": {\"tool\": \"report_feedback\"}}" | jq -r ".result.content[0].text"'
FB3="$(head -n1 <<<"$LAST" | jq -r .id)"
check "an agent's report over MCP is stored and is byte-identical through the CLI" equal "$(bin/polaroid get-feedback "$FB3")" "$(head -n1 <<<"$LAST")"
check "reporting feedback changed no other record" equal "$(digest /v1/procedures)$(digest "/v1/procedures/$ID")$(digest "/v1/bindings/$BA")$(digest "/v1/executions?procedure_id=$SOLO")" "$FB_BEFORE"

########################################################################
section "The database enforces immutability itself" \
	"Even a client that bypasses polaroidd cannot rewrite history: schema triggers reject UPDATE/DELETE of procedures, versions, bindings, binding revisions, references, executions and execution links, feedback reports, gaps in numbering, and pins to missing versions. \`PRAGMA user_version\` records the schema version (6 migrations)." \
	"Open the database with \`sqlite3 polaroid.db\` and try the statements below."
SNAP_BEFORE="$(digest "/v1/procedures/$ID")$(digest "/v1/bindings/$BA")$(digest "/v1/bindings/$BB")"
show 'sqlite3 "$DB" "PRAGMA user_version; SELECT name FROM sqlite_master WHERE type = '"'"'table'"'"' ORDER BY name;"'
check "schema version is 6 with procedure, binding, reference, execution, execution-link and feedback tables" equal "$(head -n1 <<<"$LAST")" 6
check "the execution_children table exists" out_has "execution_children"
check "the feedback table exists" out_has "feedback"
for stmt in \
	"UPDATE procedure_versions SET method = 'tampered' WHERE version = 1" \
	"DELETE FROM procedure_versions WHERE version = 3" \
	"UPDATE procedures SET canonical_key = 'stolen' WHERE canonical_key = 'go.dependency.add'" \
	"UPDATE bindings SET repository = 'elsewhere' WHERE id = '$BA'" \
	"DELETE FROM binding_revisions WHERE binding_id = '$BA'" \
	"INSERT INTO binding_revisions VALUES ('$BA', 9, '{}', 'contextual', NULL, 'r', 'now')" \
	"INSERT INTO binding_revisions VALUES ('$BA', 4, '{}', 'pin', 77, 'r', 'now')" \
	"UPDATE feedback SET summary = 'tampered' WHERE id = '$FB1'" \
	"DELETE FROM feedback WHERE id = '$FB2'"; do
	show "sqlite3 \"\$DB\" \"$stmt\""
	check "rejected: ${stmt:0:60}…" test "$RC" -ne 0
done
check "all histories are unchanged after the tampering attempts" equal "$(digest "/v1/procedures/$ID")$(digest "/v1/bindings/$BA")$(digest "/v1/bindings/$BB")" "$SNAP_BEFORE"
check "the feedback reports are unchanged too" equal "$(bin/polaroid get-feedback "$FB1")" "$FB1_BODY"

########################################################################
section "Graceful shutdown and restart persistence" \
	"On SIGTERM the daemon stops accepting connections, finishes in-flight requests and exits 0. Data is durable (WAL, synchronous=FULL): after a restart every history is byte-identical." \
	"\`kill %1\` (or Ctrl-C), then start \`bin/polaroidd -db polaroid.db\` again and re-read."
declare -A SNAP
for p in "/v1/procedures/$ID" "/v1/bindings/$BA" "/v1/bindings/$BB" /v1/procedures "/v1/bindings?repository=scratch" /v1/feedback; do SNAP[$p]="$(digest "$p")"; done
stop_daemon
check "SIGTERM: daemon exits 0" rc_is 0
check "log shows shutting down and stopped" grep -q 'polaroidd stopped' "$LOG"
start_daemon "$DB" flags
check "restarted from flags (-addr, -db)" grep -q 'polaroidd listening' "$LOG"
show 'for p in "/v1/procedures/$ID" "/v1/bindings/$BA" "/v1/bindings/$BB" /v1/procedures "/v1/bindings?repository=scratch" /v1/feedback; do printf "%-62s %s\n" "$p" "$(digest "$p")"; done'
for p in "${!SNAP[@]}"; do
	check "after restart, $p is byte-identical (${SNAP[$p]})" equal "$(digest "$p")" "${SNAP[$p]}"
done

########################################################################
section "CLI contract" \
	"The CLI prints every response body (JSON) on stdout, also on failure, plus a one-line diagnostic on stderr. Exit status: 0 success, 1 request failed, 2 usage error. The server is \`-server URL\`, else \`\$POLAROID_URL\`, else http://127.0.0.1:7417." \
	"\`bin/polaroid help\`; try the failures below and check \`echo \$?\`."
show 'bin/polaroid help'
check "help exits 0 and lists binding commands" out_has "get-binding-revision ID N"
show 'bin/polaroid get no-such-id 2>/dev/null'
check "failed request: exit 1, JSON error on stdout" json_has '.error.code == "not_found"'
check "exit status 1" rc_is 1
show 'bin/polaroid get no-such-id >/dev/null'
check "failed request: diagnostic on stderr" out_has "polaroid: GET /v1/procedures/no-such-id: 404 Not Found: not_found"
show 'bin/polaroid frobnicate; bin/polaroid get-binding-revision only-one-arg; bin/polaroid -server ftp://x list'
check "usage errors exit 2" rc_is 2
show 'bin/polaroid -server http://127.0.0.1:1 list'
check "unreachable server exits 1" rc_is 1
show 'POLAROID_URL= bin/polaroid -server "$URL" get-binding "$BB" | jq -c "{id, repository}"'
check "-server flag selects the daemon" json_has --arg id "$BB" '.id == $id'
stop_daemon

########################################################################
section "Schema safety: upgrade in place, refuse newer schemas" \
	"Migrations are counted by \`PRAGMA user_version\` and applied at start-up in one transaction. A database from before bindings (schema 1) is upgraded in place with its procedures kept. A database with a newer schema than the binary understands is refused, so an old binary can never corrupt it." \
	"Point \`bin/polaroidd -db\` at an older or newer database file."
show 'sqlite3 "$WORK/v1.db" < internal/storage/sqlite/migrations/0001_procedures.sql && sqlite3 "$WORK/v1.db" "PRAGMA user_version = 1; INSERT INTO procedures VALUES ('"'"'old-1'"'"', '"'"'legacy.task'"'"', '"'"'2026-10-01T00:00:00.000000000Z'"'"'); INSERT INTO procedure_versions VALUES ('"'"'old-1'"'"', 1, '"'"'p'"'"', '"'"'m'"'"', '"'"'{}'"'"', '"'"'{}'"'"', '"'"'r'"'"', '"'"'2026-10-01T00:00:00.000000000Z'"'"'); PRAGMA user_version;"'
check "a schema-1 database with one procedure exists" equal "$LAST" 1
start_daemon "$WORK/v1.db" flags
show 'bin/polaroid get-by-key legacy.task | jq -c "{id, canonical_key, latest_version}"'
check "the old procedure is served after the upgrade" json_has '.id == "old-1"'
show 'bin/polaroid bind <<<"$(bind_req scratch "{\"pin\": 1}" legacy old-1)" | jq -c "{repository, name, procedure_id}"'
check "bindings work on the upgraded database" json_has '.procedure_id == "old-1"'
stop_daemon
show 'sqlite3 "$WORK/v1.db" "PRAGMA user_version"'
check "schema version is now 6" equal "$LAST" 6
show 'cp "$DB" "$WORK/newer.db" && sqlite3 "$WORK/newer.db" "PRAGMA user_version = 99" && bin/polaroidd -addr 127.0.0.1:0 -db "$WORK/newer.db"'
check "newer schema refused with exit 1" rc_is 1
check "error says the schema is newer than this build supports" out_has "newer than this build supports"
close_section

########################################################################
{
	echo "# Polaroid end-to-end test report"
	echo
	echo "Generated by \`make e2e\` (\`scripts/e2e.sh\`) on $(date -u +%Y-%m-%dT%H:%M:%SZ)."
	echo "Commit \`$(git rev-parse --short HEAD)\` ($(git log -1 --format=%s)), $(go version | cut -d' ' -f3-4), $(uname -sm)."
	echo
	echo "Every block below is the real command and its real output, captured while the script ran against real \`polaroidd\` processes and a fresh SQLite file in a temporary directory. Each **PASS**/**FAIL** line is an automatic check of that output. Ports are random (\`-addr 127.0.0.1:0\`) so a daemon you already run is not disturbed; normally you use the default \`127.0.0.1:7417\` and can drop \`-server\`."
	echo
	echo "**Result: $PASS checks passed, $FAIL failed.**"
	echo
	echo "| # | Feature | Checks | Result |"
	echo "| --- | --- | --- | --- |"
	printf '%s\n' "${SECTIONS[@]}"
	echo
	echo "Helpers used in the commands: \`api METHOD PATH [BODY|@FILE]\` is \`curl -i\` showing the status line, Location/Allow/Content-Type headers and the body; \`race N PATH BODY\` sends N POSTs in parallel and prints each status code; \`bind_req REPO POLICY [NAME] [PROCEDURE_ID]\` and \`rev_req BASE POLICY\` print binding request bodies with \`jq\`; \`digest PATH\` is the first 16 hex digits of the SHA-256 of a GET response. All are defined at the top of the script."
	echo
	echo "Not covered here: the warning when listening on a non-loopback address (exposing an unauthenticated port was avoided; \`cmd/polaroidd\` tests cover it), and the automated gates (fmt, vet, lint, tests, race, dependencies), which \`make check\` runs and \`make ci\` runs alongside this script."
	cat "$BODY"
} >"$REPORT"
rm -f "$BODY"
echo "REPORT: $REPORT  passed=$PASS failed=$FAIL" >&2
((FAIL == 0))
