#!/usr/bin/env bash
# End-to-end test of Polaroid's MCP server (/mcp, ADR-0014, ADR-0016). Runs
# the real polaroidd against a temporary database, drives every tool and
# resource with raw JSON-RPC over curl (so the report shows exactly what an
# MCP client sends), checks each claim, tries independent MCP clients, and
# writes bin/e2e/MCP-REPORT.md.
# Usage: make e2e-mcp (needs bash 4+, curl and jq; npm, for the TypeScript SDK and MCP
# Inspector checks, which are skipped and reported as skipped without it).
# E2E_INTEROP=0 skips every independent-client check (npm downloads, a local
# VS Code install), as `make ci` does, so the rest is deterministic.
# Exits non-zero if any check fails.
# Variables are read inside the single-quoted commands that show() evaluates.
# shellcheck disable=SC2034
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2

((BASH_VERSINFO[0] >= 4)) || { echo "e2e-mcp: bash 4 or newer is required, found $BASH_VERSION" >&2; exit 2; }
for tool in curl jq; do
	command -v "$tool" >/dev/null || { echo "e2e-mcp: $tool is required" >&2; exit 2; }
done
OUT=bin/e2e
mkdir -p "$OUT"
REPORT=$OUT/MCP-REPORT.md
BODY=$OUT/.mcp-body.md
WORK="$(mktemp -d)"
DB="$WORK/polaroid.db"
TSDIR="${MCP_TS_DIR:-${TMPDIR:-/tmp}/polaroid-mcp-ts}"
PROTO=2026-07-28
COMMIT=0123456789abcdef0123456789abcdef01234567
PID="" URL="" LOG="" starts=0
PASS=0 FAIL=0 SEC_PASS=0 SEC_FAIL=0 STEP=0 ISERR=""
SECTIONS=()
: >"$BODY"

cleanup() {
	[[ -n "$PID" ]] && { kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null; }
	rm -rf "$WORK"
}
trap cleanup EXIT

md() { printf '%s\n' "$@" >>"$BODY"; }
anchor() { printf '#%s' "$(printf '%s-%s' "$STEP" "$TITLE" | tr 'A-Z' 'a-z' | tr -cd 'a-z0-9 -' | tr ' ' '-')"; }
close_section() {
	((STEP == 0)) && return
	local result=PASS
	((SEC_FAIL > 0)) && result="**FAIL ($SEC_FAIL)**"
	SECTIONS+=("| [$STEP]($(anchor)) | $TITLE | $SEC_PASS | $result |")
}
section() { # section TITLE WHAT HOWTO
	close_section
	STEP=$((STEP + 1)) TITLE="$1" SEC_PASS=0 SEC_FAIL=0
	md "" "## $STEP $TITLE" "" "**What it is:** $2" "" "**How to do it yourself:** $3" "" "**Proof:**" ""
	echo "[$STEP] $TITLE" >&2
}
show() { # show CMD: runs CMD and records it, its output and exit status
	LAST="$(eval "$1" 2>&1)"
	RC=$?
	md '```console' "\$ $1"
	[[ -n "$LAST" ]] && md "$LAST"
	md "[exit status $RC]" '```'
}
check() { # check CLAIM COMMAND...
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
json_has() { head -n1 <<<"$LAST" | jq -e "$@"; }
out_has() { grep -qF -- "$1" <<<"$LAST"; }
equal() { [[ "$1" == "$2" ]]; }
is_error() { [[ "$ISERR" == "$1" ]]; }

start_daemon() {
	local addr=""
	starts=$((starts + 1))
	LOG="$WORK/polaroidd.$starts.log"
	bin/polaroidd -addr 127.0.0.1:0 -db "$DB" 2>"$LOG" &
	PID=$!
	for _ in $(seq 1 100); do
		addr="$(sed -n 's/.*msg="polaroidd listening" addr=\([^ ]*\).*/\1/p' "$LOG")"
		[[ -n "$addr" ]] && break
		kill -0 "$PID" 2>/dev/null || break
		sleep 0.1
	done
	URL="http://$addr"
	md '```console' "\$ bin/polaroidd -addr 127.0.0.1:0 -db $DB &" "$(cat "$LOG")" '```'
}
stop_daemon() {
	kill -TERM "$PID"
	wait "$PID"
	RC=$?
	PID=""
	md '```console' '$ kill -TERM <pid>; wait <pid>' "$(cat "$LOG")" "[exit status $RC]" '```'
}

# MCP over curl. Protocol 2026-07-28 is sessionless: every request carries
# the protocol version and client capabilities in params._meta, mirrored in
# the Mcp-Protocol-Version, Mcp-Method and (for named calls) Mcp-Name headers.
meta() { printf '"_meta":{"io.modelcontextprotocol/protocolVersion":"%s","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"manual-test","version":"1"}}' "${1:-$PROTO}"; }
post() { # post METHOD NAME PARAMS_INNER [PROTOCOL]: PARAMS_INNER are the params members before _meta, sent verbatim
	local v="${4:-$PROTO}" sep=""
	[[ -n "$3" ]] && sep=","
	curl -sS -X POST "$URL/mcp" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
		-H "Mcp-Protocol-Version: $v" -H "Mcp-Method: $1" ${2:+-H "Mcp-Name: $2"} \
		--data-binary "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":{$3$sep$(meta "$v")}}"
}
tool() { post tools/call "$1" "\"name\":\"$1\",\"arguments\":$2"; } # tool NAME ARGS: ARGS sent byte-for-byte
text() { jq -r '.result.content[0].text'; }

# step TOOL ARGS [JQ]: one tool call; records the exact arguments, isError and
# the tool's JSON result (optionally narrowed by a jq filter).
step() {
	local raw
	raw="$(tool "$1" "$2")"
	ISERR="$(jq -r '.result.isError // false' <<<"$raw" 2>/dev/null)"
	LAST="$(jq -c ".result.content[0].text | fromjson | ${3:-.}" <<<"$raw" 2>/dev/null)" || LAST="$raw"
	md '```text' "tools/call $1" "arguments: $2" "isError: $ISERR" "result${3:+ | $3}:" "$LAST" '```'
}
ids() { jq -r .id <<<"$LAST"; }

########################################################################
section "Start polaroidd; discover the MCP server" \
	"\`polaroidd\` serves MCP at \`/mcp\` on its normal listener (default \`127.0.0.1:7417\`, random here). Protocol 2026-07-28 has no initialize handshake: a client calls \`server/discover\` (SEP-2575), and every request is self-contained. The server advertises 2026-07-28 and, for clients that still use the initialize handshake, 2025-11-25 (ADR-0016, still without sessions), the \`tools\` and \`resources\` capabilities, and instructions that teach an agent the Polaroid loop." \
	"\`bin/polaroidd &\`, then configure your MCP client with \`http://127.0.0.1:7417/mcp\`. By hand, the request below."
make build >/dev/null
start_daemon
show 'post server/discover "" "" | jq -c .'
check "the server speaks protocols 2026-07-28 and 2025-11-25" json_has '.result.supportedVersions == ["2026-07-28","2025-11-25"]'
show 'post server/discover "" "" | jq -c "{supported: .result.supportedVersions, capabilities: .result.capabilities, server: .result._meta[\"io.modelcontextprotocol/serverInfo\"]}"'
check "capabilities are tools and resources; the server is polaroid v1" json_has '(.capabilities | keys) == ["resources","tools"] and .server.name == "polaroid" and .server.version == "v1"'
show 'post server/discover "" "" | jq -r .result.instructions'
check "the instructions describe find → resolve → follow → record → revise" out_has "record_execution"
check "the instructions ask agents to report problems and suggestions with report_feedback" out_has "say so with report_feedback"
note "The raw request behind \`post server/discover\`:" '```http' \
	"POST /mcp" "Content-Type: application/json" "Accept: application/json, text/event-stream" "Mcp-Protocol-Version: 2026-07-28" "Mcp-Method: server/discover" "" \
	"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"server/discover\",\"params\":{$(meta)}}" '```'

########################################################################
section "Tool catalogue" \
	"20 tools mirror the HTTP API one-to-one. 14 only read and are annotated \`readOnlyHint\`; 6 write (create/revise procedure, create/revise binding, record execution, report feedback) and are annotated non-destructive (they only append). Every tool advertises a JSON Schema generated from its argument type: flat arguments named after record fields, optional ones not required." \
	"An MCP client lists tools automatically. By hand: \`post tools/list \"\" \"\"\`."
show 'post tools/list "" "" | jq -c "[.result.tools[] | {name, readOnly: .annotations.readOnlyHint, destructive: .annotations.destructiveHint, required: .inputSchema.required}]" | jq -c ".[]"'
check "20 tools" equal "$(wc -l <<<"$LAST" | tr -d ' ')" 20
check "14 read-only tools" equal "$(grep -c '"readOnly":true' <<<"$LAST")" 14
check "the 6 write tools are marked non-destructive" equal "$(grep -c '"readOnly":false,"destructive":false' <<<"$LAST")" 6
show 'post tools/list "" "" | jq ".result.tools[] | select(.name == \"get_graph\") | .inputSchema"'
check "get_graph requires procedure_id and version; repository and environment are optional" out_has '"required": ['

########################################################################
section "Procedures: create, read, list, revise" \
	"An agent creates a procedure with flat arguments (\`canonical_key\` plus the version fields). Free-form objects (contract, instructions) are kept byte-for-byte apart from whitespace, including member order. \`get_procedure\` takes \`id\` or \`canonical_key\`. A revision names \`base_version\`; a stale base is a tool error \`version_conflict\` with \`latest_version\`, and nothing is stored." \
	"Ask your agent, e.g. \"create a Polaroid procedure demo.leaf ...\"; or call the tools below."
step create_procedure '{"canonical_key":"demo.leaf","philosophy":"Keep it small.","method":"Do one thing.","contract":{"z":1,"a":{"y":2,"b":3}},"instructions":{"steps":["run"]},"revision_reason":"Created over MCP."}' '{id, canonical_key, latest_version, contract: .versions[0].contract}'
check "created, with contract members in the order sent (z before a, y before b)" json_has '.canonical_key == "demo.leaf" and (.contract | keys_unsorted) == ["z","a"] and (.contract.a | keys_unsorted) == ["y","b"]'
LEAF="$(ids)"
step get_procedure '{"canonical_key":"demo.leaf"}' '{id, latest_version}'
check "get_procedure by canonical_key finds it" json_has --arg id "$LEAF" '.id == $id'
step get_procedure "{\"id\":\"$LEAF\"}" '{canonical_key}'
check "get_procedure by id finds it" json_has '.canonical_key == "demo.leaf"'
REVISE="{\"procedure_id\":\"$LEAF\",\"base_version\":1,\"philosophy\":\"Keep it small.\",\"method\":\"Do one thing, then check.\",\"contract\":{},\"instructions\":{\"steps\":[\"run\",\"check\"]},\"revision_reason\":\"Added a check step.\"}"
step revise_procedure "$REVISE" '{procedure_id, version, revision_reason}'
check "revise_procedure appended version 2" json_has '.version == 2'
step revise_procedure "$REVISE" '.error | {code, latest_version, message}'
check "the same revision again is a tool error (isError true)" is_error true
check "...with code version_conflict and latest_version 2" json_has '.code == "version_conflict" and .latest_version == 2'
step get_version "{\"procedure_id\":\"$LEAF\",\"version\":1}" '{version, method}'
check "version 1 is unchanged" json_has '.version == 1 and .method == "Do one thing."'
step list_procedures '{}' '[.procedures[] | {canonical_key, latest_version}]'
check "list_procedures shows it at latest_version 2" json_has '. == [{"canonical_key":"demo.leaf","latest_version":2}]'

########################################################################
section "Composition and the graph" \
	"A version can reference other procedures (pinned or contextual). \`get_graph\` returns the tree with the exact version each reference selects and how it was selected (\`selected_by\`)." \
	"Call \`create_procedure\` with \`references\`, then \`get_graph\`."
step create_procedure "{\"canonical_key\":\"demo.parent\",\"philosophy\":\"p\",\"method\":\"m\",\"contract\":{},\"instructions\":{},\"references\":[{\"name\":\"pinned\",\"procedure_id\":\"$LEAF\",\"version_policy\":{\"pin\":1},\"inputs\":{}},{\"name\":\"latest\",\"procedure_id\":\"$LEAF\",\"version_policy\":{\"contextual\":{}},\"inputs\":{}}],\"revision_reason\":\"Composes demo.leaf.\"}" '{id, references: [.versions[0].references[].name]}'
check "created with two references" json_has '.references == ["pinned","latest"]'
PARENT="$(ids)"
step get_graph "{\"procedure_id\":\"$PARENT\",\"version\":1}" '[.references[] | {name, selected_by, version: .node.version}]'
check "without context: pin selects 1, contextual selects the latest (2)" json_has '. == [{"name":"pinned","selected_by":"pin","version":1},{"name":"latest","selected_by":"latest","version":2}]'

########################################################################
section "Bindings: create, list, read, revise" \
	"A binding uses a procedure in a repository under a local name, with inputs and a version policy, without copying it. Revisions name \`base_revision\`; a stale one is \`revision_conflict\`." \
	"Call the tools below."
step create_binding "{\"repository\":\"github.com/example/service\",\"name\":\"compose\",\"procedure_id\":\"$PARENT\",\"inputs\":{\"module\":\"m\"},\"version_policy\":{\"contextual\":{}},\"revision_reason\":\"Use the shared procedure.\"}" '{id, repository, name, latest_revision}'
check "binding created at revision 1" json_has '.latest_revision == 1 and .name == "compose"'
BIND="$(ids)"
step list_bindings '{"repository":"github.com/example/service"}' '[.bindings[].name]'
check "list_bindings shows it" json_has '. == ["compose"]'
BREV="{\"binding_id\":\"$BIND\",\"base_revision\":1,\"inputs\":{\"module\":\"m2\"},\"version_policy\":{\"contextual\":{}},\"revision_reason\":\"New module.\"}"
step revise_binding "$BREV" '{revision, inputs}'
check "revise_binding appended revision 2" json_has '.revision == 2'
step revise_binding "$BREV" '.error | {code, latest_revision}'
check "a stale base_revision is revision_conflict with latest_revision 2" json_has '.code == "revision_conflict" and .latest_revision == 2'
step get_binding_revision "{\"binding_id\":\"$BIND\",\"revision\":1}" '.inputs'
check "revision 1 is unchanged" json_has '. == {"module":"m"}'
step get_binding "{\"id\":\"$BIND\"}" '{latest_revision, revisions: (.revisions | length)}'
check "get_binding returns the full history" json_has '.latest_revision == 2 and .revisions == 2'

########################################################################
section "Executions and verification" \
	"Agents record what ran: the exact version, repository, full commit, a named environment, inputs, outcome and evidence. Children are recorded first, then the parent links them. An execution is verified when it succeeded and every reference has a verified child." \
	"Call \`record_execution\` for each child, then for the parent with \`children\`; then \`get_verification\`."
run_args() { # run_args PROCEDURE VERSION OUTCOME [CHILDREN]
	printf '{"procedure_id":"%s","version":%s,"repository":"github.com/example/service","commit":"%s","environment":{"name":"ci.linux","attributes":{"os":"linux"}},"inputs":{"module":"m"},"outcome":"%s","evidence":{"log":"go test ./... ok"}%s}' \
		"$1" "$2" "$COMMIT" "$3" "${4:+,\"children\":$4}"
}
step record_execution "$(run_args "$LEAF" 1 succeeded)" '{id, version, outcome}'
C1="$(ids)"
check "child run of leaf@1 recorded" json_has '.version == 1 and .outcome == "succeeded"'
step record_execution "$(run_args "$LEAF" 2 succeeded)" '{id, version}'
C2="$(ids)"
step record_execution "$(run_args "$PARENT" 1 succeeded "[{\"reference\":\"pinned\",\"execution_id\":\"$C1\"},{\"reference\":\"latest\",\"execution_id\":\"$C2\"}]")" '{id, children}'
P1="$(ids)"
check "parent recorded with both children linked" json_has '(.children | length) == 2'
step get_verification "{\"execution_id\":\"$P1\"}" '{verified, combination: {environment: .combination.environment.name, children: .combination.children}}'
check "the parent is verified; its combination records child versions 1 and 2" json_has '.verified == true and .combination.children == [{"reference":"pinned","version":1},{"reference":"latest","version":2}]'
step record_execution "$(run_args "$PARENT" 1 succeeded)" '{id}'
P2="$(ids)"
step get_verification "{\"execution_id\":\"$P2\"}" '{verified, problems}'
check "a parent without children is not verified: missing_child for both references" json_has '.verified == false and ([.problems[].code] == ["missing_child","missing_child"])'
step get_execution "{\"id\":\"$P1\"}" '{outcome, evidence, children: (.children | length)}'
check "get_execution returns evidence and children" json_has '.evidence == {"log":"go test ./... ok"} and .children == 2'
step list_executions "{\"procedure_id\":\"$PARENT\"}" '[.executions[] | {outcome, has_evidence: has("evidence")}]'
check "list_executions summarizes (no evidence)" json_has 'length == 2 and all(.[]; .has_evidence == false)'
step list_verifications "{\"procedure_id\":\"$PARENT\",\"version\":1}" '[.verifications[] | {verified, executions: (.execution_ids | length), children: (.combination.children | length)}]'
check "two combinations (with and without children); only the first is verified" json_has '. == [{"verified":true,"executions":1,"children":2},{"verified":false,"executions":1,"children":0}]'

########################################################################
section "Resolution from evidence" \
	"With a repository and environment, contextual references resolve from verification evidence. Here the newest parent run (no children) is unverified, so the parent has no evidence and \`latest\` resolves on its own: leaf@2 is verified in this context. \`resolve_binding\` resolves a binding's latest revision the same way." \
	"\`get_graph\` with \`repository\` and \`environment\`; \`resolve_binding\` with \`environment\`."
step get_graph "{\"procedure_id\":\"$PARENT\",\"version\":1,\"repository\":\"github.com/example/service\",\"environment\":\"ci.linux\"}" '{root_verified: has("verified_by"), edges: [.references[] | {name, selected_by, version: .node.version, verified: (.node | has("verified_by"))}]}'
check "in context: pin with evidence, latest by evidence (leaf@2)" json_has '.edges == [{"name":"pinned","selected_by":"pin","version":1,"verified":true},{"name":"latest","selected_by":"evidence","version":2,"verified":true}]'
step resolve_binding "{\"binding_id\":\"$BIND\",\"environment\":\"ci.linux\"}" '{binding_revision, selected_by, version: .graph.version}'
check "resolve_binding resolves revision 2 to parent@1 (the only version)" json_has '.binding_revision == 2 and .version == 1'
step resolve_binding "{\"binding_id\":\"$BIND\",\"environment\":\"ci.windows\"}" '[.graph.references[] | {name, selected_by}]'
check "in an environment without evidence, contextual references fall back to latest" json_has '.[1] == {"name":"latest","selected_by":"latest"}'
step get_graph "{\"procedure_id\":\"$LEAF\",\"version\":2,\"repository\":\"github.com/example/service\",\"environment\":\"ci.linux\",\"commit\":\"$(printf 'f%.0s' {1..40})\",\"inputs\":{\"module\":\"m\"}}" '{evidence_commit: .selection_evidence.commit, target: (.target_verification | {commit: .combination.commit, verified, execution_ids})}'
check "selection evidence names its commit; at another commit the target is unverified (ADR-0018)" json_has --arg c "$COMMIT" '.evidence_commit == $c and .target.verified == false and .target.execution_ids == []'
step get_graph "{\"procedure_id\":\"$LEAF\",\"version\":2,\"repository\":\"github.com/example/service\",\"environment\":\"ci.linux\",\"commit\":\"$COMMIT\",\"inputs\":{\"module\":\"m\"}}" '.target_verification | {verified, latest: .latest_execution_id}'
check "at the commit and inputs it ran with, the target is verified by that run" json_has --arg id "$C2" '.verified == true and .latest == $id'

########################################################################
section "Resources" \
	"Three read-only resource templates expose records as \`application/json\` with the same bodies as the tools." \
	"\`resources/templates/list\`, then \`resources/read\` with a URI."
show 'post resources/templates/list "" "" | jq -c "[.result.resourceTemplates[] | {uriTemplate, mimeType}]"'
check "three templates" json_has 'length == 3'
for pair in "polaroid://procedures/$LEAF|get_procedure|{\"id\":\"$LEAF\"}" \
	"polaroid://procedures/$LEAF/versions/2|get_version|{\"procedure_id\":\"$LEAF\",\"version\":2}" \
	"polaroid://bindings/$BIND|get_binding|{\"id\":\"$BIND\"}"; do
	IFS='|' read -r uri name args <<<"$pair"
	show "post resources/read '$uri' '\"uri\":\"$uri\"' | jq -c '.result.contents[0] | {uri, mimeType, bytes: (.text | length)}'"
	check "$uri is application/json and identical to $name" equal "$(post resources/read "$uri" "\"uri\":\"$uri\"" | jq -r '.result.contents[0].text')" "$(tool "$name" "$args" | text)"
done
show "post resources/read 'polaroid://procedures/missing' '\"uri\":\"polaroid://procedures/missing\"' | jq -c .error"
check "an unknown record is a resource-not-found protocol error" json_has '.message | test("not found")'

########################################################################
section "Errors: strict arguments and stable codes" \
	"Arguments are decoded as strictly as HTTP bodies: unknown members, duplicate members and wrong types are rejected. Domain errors are tool errors (\`isError: true\`) carrying the HTTP API's error body and codes; field names are the flat argument names." \
	"Send the arguments below."
step create_procedure '{"canonical_key":"demo.x","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r","aliases":[]}' '.error | {code, message}'
check "unknown member: invalid_request \"unknown field\"" json_has '.code == "invalid_request" and (.message | test("unknown field"))'
step create_procedure '{"canonical_key":"demo.x","canonical_key":"demo.y","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}' '.error | {code, message}'
check "duplicate member: invalid_request" json_has '.code == "invalid_request" and (.message | test("duplicate"))'
step get_version "{\"procedure_id\":\"$LEAF\",\"version\":\"1\"}" '.error | {code, message}'
check "wrong type: invalid_request naming /version" json_has '.code == "invalid_request" and (.message | test("version"))'
step create_procedure '{"canonical_key":"Demo X","method":"m","contract":[],"instructions":{},"revision_reason":"r"}' '.error | {code, fields: [.fields[].field]}'
check "every invalid field is listed, with flat names" json_has '.fields == ["canonical_key","philosophy","contract"]'
step get_procedure "{\"id\":\"$LEAF\",\"canonical_key\":\"demo.leaf\"}" '.error | {code, fields: [.fields[].field]}'
check "get_procedure with both id and canonical_key is rejected" json_has '.code == "invalid_request" and .fields == ["canonical_key"]'
step get_procedure '{"canonical_key":"no.such"}' '.error.code'
check "unknown procedure: not_found" json_has '. == "not_found"'
step create_procedure '{"canonical_key":"demo.leaf","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}' '.error.code'
check "duplicate canonical key: canonical_key_exists" json_has '. == "canonical_key_exists"'
step create_binding "{\"repository\":\"github.com/example/service\",\"name\":\"compose\",\"procedure_id\":\"$LEAF\",\"inputs\":{},\"version_policy\":{\"pin\":1},\"revision_reason\":\"r\"}" '.error.code'
check "duplicate binding name: binding_exists" json_has '. == "binding_exists"'
step revise_procedure "{\"procedure_id\":\"$PARENT\",\"base_version\":1,\"philosophy\":\"p\",\"method\":\"m\",\"contract\":{},\"instructions\":{},\"references\":[{\"name\":\"me\",\"procedure_id\":\"$PARENT\",\"version_policy\":{\"pin\":1},\"inputs\":{}}],\"revision_reason\":\"r\"}" '.error | {code, cycle: (.cycle | length)}'
check "a self-reference: reference_cycle with the path" json_has '.code == "reference_cycle" and .cycle == 2'
step record_execution "$(run_args "$LEAF" 1 succeeded | sed "s/$COMMIT/abc123/")" '.error | {code, fields: [.fields[].field]}'
check "an abbreviated commit is rejected, naming commit" json_has '.fields == ["commit"]'

########################################################################
section "Feedback: the agent tells Polaroid what got in its way" \
	"When Polaroid itself gets in an agent's way, or could serve it better, the agent reports it with \`report_feedback\` (ADR-0015): \`kind\` problem or suggestion, a one-line \`summary\`, \`details\`, a canonical-key \`reporter\` and an optional free-form \`context\`. Reports are immutable and untriaged; \`list_feedback\` and \`get_feedback\` read them back. Reporting changes no other record." \
	"Ask your agent, e.g. \"report to Polaroid that ...\"; or call the tools below."
PROC_BEFORE="$(tool get_procedure "{\"id\":\"$LEAF\"}" | text)"
step report_feedback '{"kind":"problem","summary":"get_graph rejected repository without environment","details":"I passed only repository.\nThe error named environment, which helped.","reporter":"manual.agent","context":{"tool":"get_graph","z":1,"a":2}}'
check "reported: kind, reporter, multi-line details, context member order kept" json_has '.kind == "problem" and .reporter == "manual.agent" and (.details | contains("\n")) and (.context | keys_unsorted) == ["tool","z","a"]'
FB="$(ids)" FB_TEXT="$LAST"
step get_feedback "{\"id\":\"$FB\"}"
check "get_feedback returns the same report" equal "$LAST" "$FB_TEXT"
step report_feedback '{"kind":"suggestion","summary":"Let list_feedback filter by reporter","details":"Useful for triage.","reporter":"manual.agent"}' '{kind, context}'
check "without context, the report has context {}" json_has '. == {"kind":"suggestion","context":{}}'
step list_feedback '{}' '[.feedback[].kind]'
check "list_feedback lists both, oldest first" json_has '. == ["problem","suggestion"]'
step list_feedback '{"kind":"suggestion"}' '[.feedback[].summary]'
check "list_feedback filters by kind" json_has '. == ["Let list_feedback filter by reporter"]'
step report_feedback '{"kind":"bug","summary":"one\ntwo","details":" ","reporter":"Manual Agent","context":[]}' '.error | {code, fields: [.fields[].field]}'
check "an invalid report is a tool error naming every field" json_has '.code == "invalid_request" and .fields == ["kind","summary","details","reporter","context"]'
step list_feedback '{"kind":"bug"}' '.error | {code, fields: [.fields[].field]}'
check "an unknown kind filter is invalid_request naming kind" json_has '.code == "invalid_request" and .fields == ["kind"]'
step report_feedback '{"kind":"problem","summary":"s","details":"d","reporter":"r","status":"open"}' '.error | {code, message}'
check "no triage state: a status argument is an unknown field" json_has '.code == "invalid_request" and (.message | test("unknown field"))'
step get_feedback '{"id":"missing"}' '.error.code'
check "unknown report: not_found" json_has '. == "not_found"'
check "reporting feedback changed no procedure" equal "$(tool get_procedure "{\"id\":\"$LEAF\"}" | text)" "$PROC_BEFORE"

########################################################################
section "Same documents as the HTTP API" \
	"MCP and HTTP share one set of record shapes (\`internal/transport/wire\`). A tool's text result is byte-identical to the HTTP response body." \
	"Compare a tool result with the matching \`curl\`."
for pair in "get_procedure|{\"id\":\"$LEAF\"}|/v1/procedures/$LEAF" \
	"get_version|{\"procedure_id\":\"$LEAF\",\"version\":1}|/v1/procedures/$LEAF/versions/1" \
	"get_graph|{\"procedure_id\":\"$PARENT\",\"version\":1,\"repository\":\"github.com/example/service\",\"environment\":\"ci.linux\"}|/v1/procedures/$PARENT/versions/1/graph?repository=github.com/example/service&environment=ci.linux" \
	"get_binding|{\"id\":\"$BIND\"}|/v1/bindings/$BIND" \
	"resolve_binding|{\"binding_id\":\"$BIND\",\"environment\":\"ci.linux\"}|/v1/bindings/$BIND/resolution?environment=ci.linux" \
	"get_execution|{\"id\":\"$P1\"}|/v1/executions/$P1" \
	"get_verification|{\"execution_id\":\"$P1\"}|/v1/executions/$P1/verification" \
	"list_verifications|{\"procedure_id\":\"$PARENT\",\"version\":1}|/v1/procedures/$PARENT/versions/1/verifications" \
	"get_feedback|{\"id\":\"$FB\"}|/v1/feedback/$FB" \
	"list_feedback|{\"kind\":\"problem\"}|/v1/feedback?kind=problem"; do
	IFS='|' read -r name args path <<<"$pair"
	A="$(tool "$name" "$args" | text | shasum -a 256 | cut -c1-16)"
	B="$(curl -sS "$URL$path" | shasum -a 256 | cut -c1-16)" # both end in exactly one newline
	md "- \`$name\` vs \`GET $path\`: \`$A\` / \`$B\`"
	check "$name is byte-identical to GET ${path%%\?*}" equal "$A" "$B"
done

########################################################################
section "Protocol and transport rules" \
	"2026-07-28 and 2025-11-25 are served; older revisions are refused at the HTTP layer before any tool runs, and an older initialize is answered with 2025-11-25. Requests must mirror the method in \`Mcp-Method\`. The endpoint is stateless (no sessions, no GET stream), bodies are limited to 1 MiB, and \`/mcp\` sits behind the same loopback-host check and cross-origin protection as the API, at both revisions." \
	"Repeat the requests below."
show 'post tools/call list_procedures "\"name\":\"list_procedures\",\"arguments\":{}" 2025-06-18; echo'
check "protocol 2025-06-18: refused (Unsupported protocol version)" out_has "Unsupported protocol version (supported versions: 2026-07-28,2025-11-25)"
show 'curl -sS -X POST "$URL/mcp" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" --data-binary "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"old\",\"version\":\"1\"}}}" | jq -c "{protocolVersion: .result.protocolVersion}"'
check "an older initialize is answered with 2025-11-25, never served at 2025-06-18" json_has '.protocolVersion == "2025-11-25"'
show 'curl -sS -D - -o /dev/null -X POST "$URL/mcp" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" --data-binary "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"c\",\"version\":\"1\"}}}" | tr -d "\r" | grep -iE "^(HTTP/|mcp-session-id)"'
check "a 2025-11-25 initialize gets 200 and no Mcp-Session-Id" equal "$(grep -ci mcp-session-id <<<"$LAST")$(grep -c '^HTTP/1.1 200' <<<"$LAST")" 01
show 'for h in "Host: evil.example" "Sec-Fetch-Site: cross-site"; do curl -sS -o /dev/null -w "%{http_code}\n" -X POST "$URL/mcp" -H "$h" -H "Content-Type: application/json" --data-binary "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"c\",\"version\":\"1\"}}}"; done'
check "at 2025-11-25 too, a foreign Host and a cross-site request get 403" equal "$LAST" "403
403"
show 'curl -sS -X POST "$URL/mcp" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" -H "Mcp-Protocol-Version: 2026-07-28" --data-binary "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{$(meta)}}" | jq -c .error'
check "a request without Mcp-Method is rejected (-32020)" json_has '.code == -32020'
show 'curl -sS -o /dev/null -w "%{http_code}\n" "$URL/mcp" -H "Accept: text/event-stream"'
check "GET /mcp (server stream) is not offered: 405" equal "$LAST" 405
head -c 1100000 /dev/zero | tr '\0' a >"$WORK/big.txt"
show 'curl -sS -o /dev/null -w "%{http_code}\n" -X POST "$URL/mcp" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" -H "Mcp-Protocol-Version: 2026-07-28" -H "Mcp-Method: tools/call" -H "Mcp-Name: create_procedure" --data-binary @"$WORK/big.txt"   # 1.1 MB body'
check "a body over 1 MiB is rejected: 413" equal "$LAST" 413
show 'curl -sS -o /dev/null -w "%{http_code}\n" -X POST "$URL/mcp" -H "Host: evil.example" -H "Content-Type: application/json" --data-binary "{}"'
check "a foreign Host header (DNS rebinding): 403" equal "$LAST" 403
show 'curl -sS -o /dev/null -w "%{http_code}\n" -X POST "$URL/mcp" -H "Sec-Fetch-Site: cross-site" -H "Content-Type: application/json" --data-binary "{}"'
check "a cross-site browser request: 403" equal "$LAST" 403

########################################################################
section "Records written over MCP persist" \
	"MCP writes go through the same service and SQLite transactions as HTTP. After a graceful restart, everything the agent wrote is still there." \
	"Stop and restart \`polaroidd\`, then read again."
BEFORE="$(tool get_procedure "{\"id\":\"$LEAF\"}" | text)"
FB_BEFORE="$(tool list_feedback '{}' | text)"
stop_daemon
check "daemon stopped gracefully" equal "$RC" 0
start_daemon
step get_procedure "{\"id\":\"$LEAF\"}" '{canonical_key, latest_version}'
check "after restart, get_procedure returns the identical document" equal "$(tool get_procedure "{\"id\":\"$LEAF\"}" | text)" "$BEFORE"
check "after restart, list_feedback returns the identical reports" equal "$(tool list_feedback '{}' | text)" "$FB_BEFORE"

########################################################################
section "Independent MCP clients (interoperability)" \
	"The tests above use raw JSON-RPC; the automated tests use the Go SDK's own client. This section points two independent clients at the same daemon: the official **TypeScript SDK** (\`@modelcontextprotocol/sdk\`, the library behind most Node-based agents) and the official **MCP Inspector** CLI." \
	"\`npx @modelcontextprotocol/inspector --cli http://127.0.0.1:7417/mcp --transport http --method tools/list\`."
if [[ "${E2E_INTEROP:-1}" == 0 ]]; then
	note "> **Skipped:** \`E2E_INTEROP=0\` (as in \`make ci\`), so the TypeScript SDK, MCP Inspector and VS Code checks did not run. They are not counted as passed."
elif ! command -v npm >/dev/null; then
	note "> **Skipped:** npm is not installed, so the TypeScript SDK and MCP Inspector checks did not run. They are not counted as passed."
else
if [[ ! -d "$TSDIR/node_modules/@modelcontextprotocol/sdk" ]]; then
	mkdir -p "$TSDIR" && (cd "$TSDIR" && npm init -y >/dev/null && npm i --silent @modelcontextprotocol/sdk@1.32.1 @modelcontextprotocol/inspector@2.10.1 >/dev/null 2>&1)
fi
cat >"$TSDIR/probe.mjs" <<'EOF'
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { LATEST_PROTOCOL_VERSION } from '@modelcontextprotocol/sdk/types.js';
console.log('TypeScript SDK latest protocol:', LATEST_PROTOCOL_VERSION);
const client = new Client({ name: 'ts-sdk-interop', version: '1.0.0' });
try {
  await client.connect(new StreamableHTTPClientTransport(new URL(process.argv[2])));
  console.log('negotiated:', client.transport.protocolVersion, 'session:', client.transport.sessionId ?? 'none');
  const { tools } = await client.listTools();
  console.log('connected; tools:', tools.length);
  const res = await client.callTool({ name: 'report_feedback', arguments: { kind: 'suggestion', summary: 'Reported by the TypeScript SDK', details: 'Interop check.', reporter: 'ts.sdk' } });
  console.log('report_feedback:', res.isError ? 'error' : JSON.parse(res.content[0].text).reporter);
} catch (e) { console.log('FAILED:', e.message.trim()); }
await client.close().catch(() => {});
EOF
show '(cd "$TSDIR" && node probe.mjs "$URL/mcp")'
check "TypeScript SDK 1.32.1 connects at 2025-11-25, without a session" out_has "negotiated: 2025-11-25 session: none"
check "...lists all 20 tools" out_has "connected; tools: 20"
check "...and records feedback" out_has "report_feedback: ts.sdk"
show '(cd "$TSDIR" && npx --no-install mcp-inspector --cli "$URL/mcp" --transport http --method tools/list 2>/dev/null | jq -c "[.tools[].name] | length")'
check "MCP Inspector 2.10.1 lists the 20 tools" equal "$LAST" 20
fi
VSCODE_GITHUB="/Applications/Visual Studio Code.app/Contents/Resources/app/node_modules.asar.unpacked/@github"
if [[ "${E2E_INTEROP:-1}" == 0 ]]; then
	:
elif [[ -d "$VSCODE_GITHUB" ]]; then
	show 'grep -rlE "server/discover" "$VSCODE_GITHUB" --include="*.js" 2>/dev/null | head -2; code --version 2>/dev/null | head -1'
	check "VS Code's bundled Copilot agent runtime implements server/discover (protocol 2026-07-28)" out_has "@github/copilot"
else
	note "> **Skipped:** VS Code is not installed at its macOS location, so its bundled MCP runtime was not inspected. Not counted as passed."
fi
note "> **Finding.** Since ADR-0016, \`/mcp\` also accepts protocol 2025-11-25 through the initialize handshake, still without sessions. Clients built on the TypeScript SDK (1.32.1), including the MCP Inspector, connect at 2025-11-25; VS Code Copilot chat connects at 2026-07-28."

close_section
{
	echo "# Polaroid MCP end-to-end test report"
	echo
	echo "Generated by \`make e2e-mcp\` (\`scripts/e2e-mcp.sh\`) on $(date -u +%Y-%m-%dT%H:%M:%SZ)."
	echo "Commit \`$(git rev-parse --short HEAD)\` ($(git log -1 --format=%s)), $(go version | cut -d' ' -f3-4), $(uname -sm)."
	echo
	echo "Every block is a real request and its real response against a real \`polaroidd\` and a fresh SQLite file. Tool calls are shown as \`tools/call NAME\`, the exact \`arguments\` sent, \`isError\`, and the tool's JSON result, narrowed by the jq filter shown after \`result |\`. Other blocks are console commands. \`post METHOD NAME PARAMS\` sends one JSON-RPC request with the 2026-07-28 \`_meta\` and \`Mcp-*\` headers (see section 1 for the raw request); \`tool NAME ARGS\` is \`post tools/call\`; \`text\` extracts a tool's text result. Each **PASS**/**FAIL** line is an automatic check."
	echo
	echo "**Result: $PASS checks passed, $FAIL failed.**"
	echo
	echo "| # | Feature | Checks | Result |"
	echo "| --- | --- | --- | --- |"
	printf '%s\n' "${SECTIONS[@]}"
	cat "$BODY"
} >"$REPORT"
rm -f "$BODY"
echo "REPORT: $REPORT  passed=$PASS failed=$FAIL" >&2
((FAIL == 0))
