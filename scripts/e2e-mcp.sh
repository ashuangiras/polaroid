#!/usr/bin/env bash
# Real-daemon MCP v8 end-to-end checks. E2E_INTEROP=0 skips independent clients.
# The daemon's HOME and database are isolated under bin/e2e and removed on exit.
# show() evaluates the single-quoted commands; SEED is read there.
# shellcheck disable=SC2016,SC2034
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2

((BASH_VERSINFO[0] >= 4)) || { echo "e2e-mcp: bash 4+ required" >&2; exit 2; }
for command in curl jq; do
	command -v "$command" >/dev/null || { echo "e2e-mcp: $command required" >&2; exit 2; }
done
OUT=bin/e2e
WORK="$OUT/.mcp-work-$$"
REPORT="$OUT/MCP-REPORT.md"
BODY="$WORK/body.md"
DB="$WORK/polaroid.db"
TSDIR="$OUT/polaroid-mcp-ts"
PROTO=2026-07-28
MCP_PATH=/mcp/agent/v8
PID="" URL="" LOG="" starts=0
PASS=0 FAIL=0 SEC_PASS=0 SEC_FAIL=0 STEP=0 ISERR=""
SECTIONS=()
mkdir -p "$WORK/home"
unset POLAROID_DB
: >"$BODY"

cleanup() {
	[[ -n "$PID" ]] && { kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null; }
	rm -rf "$WORK"
}
trap cleanup EXIT
md() { printf '%s\n' "$@" >>"$BODY"; }
anchor() { printf '#%s' "$(printf '%s-%s' "$STEP" "$TITLE" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9 -' | tr ' ' '-')"; }
close_section() {
	((STEP == 0)) && return
	local result=PASS
	((SEC_FAIL > 0)) && result="**FAIL ($SEC_FAIL)**"
	SECTIONS+=("| [$STEP]($(anchor)) | $TITLE | $SEC_PASS | $result |")
}
section() {
	close_section
	STEP=$((STEP + 1)) TITLE="$1" SEC_PASS=0 SEC_FAIL=0
	md "" "## $STEP $TITLE" "" "**What it is:** $2" "" "**How to do it yourself:** $3" "" "**Proof:**" ""
	echo "[$STEP] $TITLE" >&2
}
show() {
	LAST="$(eval "$1" 2>&1)"
	RC=$?
	md '```console' "\$ $1" "$LAST" "[exit status $RC]" '```'
}
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
json_has() { head -n1 <<<"$LAST" | jq -e "$@"; }
out_has() { grep -qF -- "$1" <<<"$LAST"; }
equal() { [[ "$1" == "$2" ]]; }
is_error() { [[ "$ISERR" == "$1" ]]; }
inspector_result() {
	[[ "$RC" == 0 && "$LAST" == '["advance","attach_child","discover","feedback","propose_advance","propose_publish_capture","propose_start","read","resolve","start"]' ]]
}

start_daemon() {
	local addr=""
	starts=$((starts + 1))
	LOG="$WORK/polaroidd.$starts.log"
	HOME="$WORK/home" bin/polaroidd -addr 127.0.0.1:0 -db "$DB" -knowledge-dir "$WORK/knowledge" 2>"$LOG" &
	PID=$!
	for _ in {1..100}; do
		addr="$(sed -n 's/.*msg="polaroidd listening" addr=\([^ ]*\).*/\1/p' "$LOG")"
		[[ -n "$addr" ]] && break
		kill -0 "$PID" 2>/dev/null || break
		sleep 0.1
	done
	URL="http://$addr"
	md '```console' "\$ HOME=$WORK/home bin/polaroidd -addr 127.0.0.1:0 -db $DB -knowledge-dir $WORK/knowledge &" "$(cat "$LOG")" '```'
	check "daemon started and healthz responds" equal "$(curl -sS "$URL/healthz" 2>/dev/null)" '{"status":"ok"}'
}
stop_daemon() {
	kill -TERM "$PID"
	wait "$PID"
	RC=$?
	PID=""
	md '```console' '$ kill -TERM <pid>; wait <pid>' "$(cat "$LOG")" "[exit status $RC]" '```'
}

# 2026-07-28 is sessionless and carries matching protocol/method/name headers.
meta() { printf '"_meta":{"io.modelcontextprotocol/protocolVersion":"%s","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"manual-test","version":"1"}}' "${1:-$PROTO}"; }
post() {
	local v="${4:-$PROTO}" sep=""
	[[ -n "$3" ]] && sep=","
	curl -sS -X POST "$URL$MCP_PATH" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
		-H "Mcp-Protocol-Version: $v" -H "Mcp-Method: $1" ${2:+-H "Mcp-Name: $2"} \
		--data-binary "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":{$3$sep$(meta "$v")}}"
}
tool() { post tools/call "$1" "\"name\":\"$1\",\"arguments\":$2"; }
step() {
	local raw
	raw="$(tool "$1" "$2")"
	ISERR="$(jq -r '.result.isError // false' <<<"$raw" 2>/dev/null)"
	LAST="$(jq -c ".result.content[0].text | fromjson | ${3:-.}" <<<"$raw" 2>/dev/null)" || LAST="$raw"
	md '```text' "tools/call $1" "arguments: $2" "isError: $ISERR" "result${3:+ | $3}:" "$LAST" '```'
	[[ "$ISERR" == true ]] && md "tool error: $(jq -cr '.result.content[0].text | fromjson | .error' <<<"$raw")"
}
active() { curl -sS "$URL/v1/knowledge/status" -H "Polaroid-Operator-Token: $TOKEN" | jq -r .snapshot; }

section "Start and discover the sole MCP route" \
	"The daemon exposes only /mcp/agent/v8. It accepts protocol 2026-07-28 and 2025-11-25." \
	"Run make binaries, start a daemon with an isolated database, then call server/discover at /mcp/agent/v8."
make -s binaries >/dev/null
start_daemon
show 'post server/discover "" "" | jq -c "{versions: .result.supportedVersions, capabilities: (.result.capabilities | keys), server: .result._meta[\"io.modelcontextprotocol/serverInfo\"]}"'
check "both supported protocol revisions; tools only" json_has '.versions == ["2026-07-28","2025-11-25"] and .capabilities == ["tools"]'
check "the server identifies as Polaroid" json_has '.server.name == "polaroid" and .server.version == "v1"'
show 'post tools/list "" "" | jq -c "[.result.tools[].name] | sort"'
check "exactly ten agent tools and no operator/evidence tools" equal "$LAST" '["advance","attach_child","discover","feedback","propose_advance","propose_publish_capture","propose_start","read","resolve","start"]'
for path in /mcp /mcp/agent /mcp/agent/v2 /mcp/agent/v3 /mcp/agent/v4 /mcp/agent/v5 /mcp/agent/v6 /mcp/agent/v7; do
	show "curl -sS -o /dev/null -w '%{http_code}' -X POST '$URL$path' -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' --data-binary '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"old\",\"version\":\"1\"}}}'"
	check "$path does not route or fall back to MCP" equal "$LAST" 404
done

section "Discover, read and resolve knowledge" \
	"An empty knowledge path returns a well-formed discovery. Operator HTTP seeds three procedures into the isolated Git catalog; v8 then reads and resolves at its snapshot." \
	"Call discover, publish a fixture through the authenticated HTTP operator API, and call read and resolve."
step discover '{"task":"verify task"}' '{empty}'
check "fresh discovery is explicitly empty" json_has '. == {"empty":true}'
TOKEN="$(cat "$WORK/knowledge-operator-token")"
SEED="$(jq -nc '
  def entry($key;$goal;$scope):
    {key:$key, goal:$goal, philosophy:"Follow verified steps.", method:"Follow the procedure.",
     contract:{inputs:{}}, instructions:{steps:["Follow the procedure."]}}
    + (if $scope then {scope:"local",repository:"github.com/ashuangiras/polaroid"} else {} end);
  def receipt($key;$goal;$scope):
    {kind:"procedure", key:$key, fields:[
      {field:"goal",operation:"replace",citations:[{source:"e2e-mcp",quote:$goal}]},
      {field:"philosophy",operation:"replace",citations:[{source:"e2e-mcp",quote:"Follow verified steps."}]},
      {field:"method",operation:"replace",citations:[{source:"e2e-mcp",quote:"Follow the procedure."}]},
      (if $scope then {field:"applicability",operation:"replace"} else {field:"applicability",operation:"clear"} end),
      {field:"contract",operation:"replace"},
      {field:"instructions",operation:"replace",citations:[{source:"e2e-mcp",quote:"Follow the procedure."}]},
      {field:"references",operation:"clear"}]};
  def typed($key;$goal):
    {key:$key,document:{"@context":"urn:polaroid:context:v1",id:("k:"+$key),type:"Procedure",label:$goal,definition:"Verified task procedure.",requires:["k:test.obligation"]}};
  def typed_receipt($key;$goal):
    {kind:"record",key:$key,type:"Procedure",fields:(
      [{field:"label",operation:"replace",citations:[{source:"e2e-mcp",quote:$goal}]},
       {field:"definition",operation:"replace",citations:[{source:"e2e-mcp",quote:"Verified task procedure."}]}]
      + ("classified_as specializes uses composes precedes"
         | split(" ") | map({field:.,operation:"clear"}))
      + [{field:"requires",operation:"replace"}]
      + ("supersedes inputs outputs outputs_complete preconditions postconditions invariants informal_conditions preconditions_complete postconditions_complete invariants_complete informal_conditions_complete"
         | split(" ") | map({field:.,operation:"clear"})))};
  {base:"",changes:[
    entry("polaroid.using";"Guide repository tasks";false),
    entry("polaroid.session-close-capture";"Capture reusable learning";true),
    entry("task.verified";"Verify a task";false)],
   records:[
     {key:"test.obligation",document:{"@context":"urn:polaroid:context:v1",id:"k:test.obligation",type:"Concept",label:"Verified obligation",definition:"A task verification obligation."}},
     typed("polaroid.using";"Guide repository tasks"),
     typed("polaroid.session-close-capture";"Capture reusable learning"),
     typed("task.verified";"Verify a task")],
   author:"operator.e2e",summary:"seed",rationale:"isolated e2e fixture",
   review:{version:1,entities:[
     receipt("polaroid.using";"Guide repository tasks";false),
     receipt("polaroid.session-close-capture";"Capture reusable learning";true),
     receipt("task.verified";"Verify a task";false),
     {kind:"record",key:"test.obligation",type:"Concept",fields:[
       {field:"label",operation:"replace",citations:[{source:"e2e-mcp",quote:"Verified obligation"}]},
       {field:"definition",operation:"replace",citations:[{source:"e2e-mcp",quote:"A task verification obligation."}]},
       {field:"classified_as",operation:"clear"},{field:"broader",operation:"clear"},
       {field:"specializes",operation:"clear"},{field:"uses",operation:"clear"},
       {field:"contradicts",operation:"clear"},{field:"supersedes",operation:"clear"}]},
     typed_receipt("polaroid.using";"Guide repository tasks"),
     typed_receipt("polaroid.session-close-capture";"Capture reusable learning"),
     typed_receipt("task.verified";"Verify a task")]}}')"
show 'curl -sS -X POST "$URL/v2/knowledge/publications" -H "Content-Type: application/json" -H "Polaroid-Operator-Token: $TOKEN" --data-binary "$SEED" | jq -c "{snapshot, error}"'
SNAP="$(jq -r '.snapshot // empty' <<<"$LAST")"
check "authenticated fixture publication returned a snapshot" test -n "$SNAP"
step discover '{"task":"verify a task","repository":"github.com/ashuangiras/polaroid"}' '{snapshot, candidates: [.candidates[].key]}'
check "discovery uses the new Git snapshot" json_has --arg snap "$SNAP" '.snapshot == $snap and (.candidates | index("task.verified") != null)'
step read "{\"key\":\"task.verified\",\"snapshot\":\"$SNAP\"}" '{key, method, snapshot}'
check "read returns the published method at the pinned snapshot" json_has '.key == "task.verified" and .method == "Follow the procedure."'
step resolve "{\"goal\":\"task.verified\",\"snapshot\":\"$SNAP\"}" '{snapshot, goal, complete}'
check "resolve reports an applicable complete procedure" json_has '.goal == "task.verified" and .complete == true'
step read '{"key":"task.verified","unexpected":true}' '.error.code'
check "v8 arguments are strictly decoded" json_has '. == "invalid_request"'
check "malformed arguments are tool errors, not success" is_error true

section "Dynamic walkthrough and field-complete capture" \
	"A v8 parent pins the knowledge snapshot. Capture publication needs a bound draft, a completed task child and an active capture child; validation alone never publishes." \
	"Start polaroid.using dynamically, attach capture and task, review each field, attempt premature and invalid publishes, then complete the task and publish the exact candidate."
step start "{\"goal\":\"polaroid.using\",\"repository\":\"github.com/ashuangiras/polaroid\",\"allow_dynamic_children\":true,\"snapshot\":\"$SNAP\"}" '{token, status, snapshot}'
WALK="$(jq -r .token <<<"$LAST")"
check "walkthrough starts at the pinned snapshot" json_has --arg snap "$SNAP" '.status == "in_progress" and .snapshot == $snap and (.token | length > 0)'
step attach_child "{\"token\":\"$WALK\",\"key\":\"polaroid.session-close-capture\"}" '{status, plan: [.plan[].key]}'
check "capture is attached first" json_has '.plan == ["polaroid.session-close-capture"]'
step attach_child "{\"token\":\"$WALK\",\"key\":\"task.verified\"}" '{status, plan: [.plan[].key]}'
check "task is attached as another child" json_has '.plan | index("task.verified") != null'
step propose_start "{\"key\":\"task.verified\",\"record_type\":\"Procedure\",\"proposer\":\"manual.agent\",\"rationale\":\"verified e2e change\",\"walkthrough_token\":\"$WALK\"}" '{token, status, fields: [.fields[] | {entity, field, answered}]}'
DRAFT="$(jq -r .token <<<"$LAST")"
check "the complete field inventory is returned" json_has '(.fields | length) >= 7 and (.token | length > 0)'
FIELDS="$(jq -c '.fields' <<<"$LAST")"
review_draft() {
	local token="$1" method="$2" field entity name outcome args
	while IFS= read -r field; do
		entity="$(jq -r .entity <<<"$field")"
		name="$(jq -r .field <<<"$field")"
		if [[ "$entity" == procedure && "$name" == method ]]; then
			outcome="$(jq -nc --arg method "$method" '{entity:"procedure",field:"method",operation:"replace",value:$method,citations:[{source:"e2e-mcp",quote:$method}]}')"
		else
			outcome="$(jq -nc --arg entity "$entity" --arg field "$name" '{entity:$entity,field:$field,operation:"keep"}')"
		fi
		args="$(jq -nc --arg token "$token" --argjson outcome "$outcome" '{token:$token,outcome:$outcome}')"
		step propose_advance "$args" '{status, candidate_hash, next_entity, next_field}'
		check "$entity.$name reviewed without error" is_error false
	done < <(jq -c '.[]' <<<"$FIELDS")
}
review_draft "$DRAFT" "Follow the verified procedure."
HASH="$(jq -r '.candidate_hash // empty' <<<"$LAST")"
check "final review is ready, not published" json_has '.status == "ready" and (.candidate_hash | length == 64)'
check "review did not move the active snapshot" equal "$(active)" "$SNAP"
step propose_start "{\"key\":\"task.verified\",\"record_type\":\"Procedure\",\"proposer\":\"manual.agent\",\"rationale\":\"concurrent review\",\"walkthrough_token\":\"$WALK\"}" '{token, status}'
STALE_DRAFT="$(jq -r .token <<<"$LAST")"
review_draft "$STALE_DRAFT" "Follow the verified procedure twice."
STALE_HASH="$(jq -r '.candidate_hash // empty' <<<"$LAST")"
check "a second reviewed candidate is ready against the same base" json_has '.status == "ready" and (.candidate_hash | length == 64)'
show 'curl -sS -o /dev/null -w "%{http_code}" -X POST "$URL/v2/knowledge/drafts/$STALE_DRAFT/publication" -H "Content-Type: application/json" --data-binary "{\"candidate_hash\":\"$STALE_HASH\"}"'
check "unauthenticated operator approval cannot publish" equal "$LAST" 401
REQUEST="$(jq -nc --arg token "$DRAFT" --arg candidate_hash "$HASH" '{token:$token,candidate_hash:$candidate_hash}')"
step propose_publish_capture "$REQUEST" '.error.code'
check "premature capture publication is refused" json_has '. == "invalid_request"'
check "premature refusal leaves the active snapshot unchanged" equal "$(active)" "$SNAP"
step advance "{\"token\":\"$WALK\",\"outcome\":{\"key\":\"task.verified\",\"status\":\"done\"}}" '{status, plan: [.plan[] | {key, status}]}'
check "the task child is done and capture is next" json_has '.status == "in_progress" and (.plan | any(.[]; .key == "polaroid.session-close-capture" and .status == "next"))'
step propose_publish_capture "{\"token\":\"$DRAFT\",\"candidate_hash\":\"$(printf '0%.0s' {1..64})\"}" '.error.code'
check "a wrong candidate hash is refused" json_has '. == "invalid_request"'
check "wrong hash leaves the active snapshot unchanged" equal "$(active)" "$SNAP"
step propose_publish_capture "$REQUEST" '{status, snapshot, key}'
NEW_SNAP="$(jq -r '.snapshot // empty' <<<"$LAST")"
check "exact ready candidate publishes once" json_has --arg old "$SNAP" '.status == "complete" and .key == "task.verified" and .snapshot != $old'
step propose_publish_capture "{\"token\":\"$STALE_DRAFT\",\"candidate_hash\":\"$STALE_HASH\"}" '.error.code'
check "concurrent ready candidate cannot publish after its base became stale" json_has '. == "stale_snapshot"'
step propose_publish_capture "$REQUEST" '.error.code'
check "replaying the consumed draft is refused" json_has '. == "not_found"'
step read "{\"key\":\"task.verified\",\"snapshot\":\"$NEW_SNAP\"}" '{method}'
check "the published method can be read back" json_has '.method == "Follow the verified procedure."'
step advance "{\"token\":\"$WALK\",\"outcome\":{\"key\":\"polaroid.session-close-capture\",\"status\":\"done\"}}" '{status}'
check "parent completes after capture" json_has '.status == "complete"'

section "Protocol, security and persistence" \
	"Both supported transport revisions work at v8; unsupported revisions, missing headers, oversized bodies, foreign hosts and cross-origin requests remain rejected. A restart preserves knowledge." \
	"Send a legacy initialize, self-contained v8 calls and malformed requests; restart the daemon and read its Git snapshot."
show 'curl -sS -X POST "$URL$MCP_PATH" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" --data-binary "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"legacy\",\"version\":\"1\"}}}" | jq -c "{version: .result.protocolVersion}"'
check "2025-11-25 initializes on v8" json_has '.version == "2025-11-25"'
show 'post tools/list "" "" 2025-11-25 | jq -c "[.result.tools[].name] | length"'
check "2025-11-25 lists all ten v8 tools" equal "$LAST" 10
show 'post tools/list "" "" 2025-06-18'
check "unsupported self-contained protocol is refused" out_has "Unsupported protocol version (supported versions: 2026-07-28,2025-11-25)"
show 'curl -sS -D - -o /dev/null -X POST "$URL$MCP_PATH" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" --data-binary "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"legacy\",\"version\":\"1\"}}}" | tr -d "\r" | grep -iE "^(HTTP/|mcp-session-id)"'
check "2025-11-25 handshake is 200 without a session" equal "$(grep -ci mcp-session-id <<<"$LAST")$(grep -c '^HTTP/1.1 200' <<<"$LAST")" 01
show 'curl -sS -X POST "$URL$MCP_PATH" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" -H "Mcp-Protocol-Version: 2026-07-28" --data-binary "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{$(meta)}}" | jq -c .error'
check "2026-07-28 requires matching Mcp-Method" json_has '.code == -32020'
for header in 'Host: evil.example' 'Sec-Fetch-Site: cross-site'; do
	show "curl -sS -o /dev/null -w '%{http_code}' -X POST '$URL$MCP_PATH' -H '$header' -H 'Content-Type: application/json' --data-binary '{}'"
	check "$header is forbidden at v8" equal "$LAST" 403
done
head -c 1100000 /dev/zero | tr '\0' a >"$WORK/big.txt"
show 'curl -sS -o /dev/null -w "%{http_code}" -X POST "$URL$MCP_PATH" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" -H "Mcp-Protocol-Version: 2026-07-28" -H "Mcp-Method: tools/list" --data-binary @"$WORK/big.txt"'
check "oversized v8 body is rejected" equal "$LAST" 413
show 'curl -sS -o /dev/null -w "%{http_code}" "$URL$MCP_PATH" -H "Accept: text/event-stream"'
check "GET stream is not offered" equal "$LAST" 405
stop_daemon
check "daemon stopped gracefully" equal "$RC" 0
start_daemon
step read "{\"key\":\"task.verified\",\"snapshot\":\"$NEW_SNAP\"}" '{method}'
check "published knowledge survives restart" json_has '.method == "Follow the verified procedure."'

section "Independent MCP clients" \
	"The official TypeScript SDK and MCP Inspector, when requested, connect to the same v8 daemon at 2025-11-25. E2E_INTEROP=0 skips this section." \
	"Use E2E_INTEROP=1 with npm available; connect clients to /mcp/agent/v8."
if [[ "${E2E_INTEROP:-1}" == 0 ]]; then
	note "> **Skipped:** E2E_INTEROP=0; independent-client interoperability is not counted as passed."
elif ! command -v npm >/dev/null || ! command -v node >/dev/null; then
	note "> **Blocker:** npm and node are required for the independent-client checks."
	check "independent-client prerequisites available" false
else
	if [[ ! -d "$TSDIR/node_modules/@modelcontextprotocol/sdk" || ! -d "$TSDIR/node_modules/@modelcontextprotocol/inspector" ]]; then
		mkdir -p "$TSDIR"
		if ! (cd "$TSDIR" && npm init -y >/dev/null && npm i --silent @modelcontextprotocol/sdk@1.32.1 @modelcontextprotocol/inspector@2.10.1); then
			note "> **Blocker:** independent-client packages could not be installed."
			check "independent-client dependencies installed" false
		fi
	fi
	if [[ -d "$TSDIR/node_modules/@modelcontextprotocol/sdk" && -d "$TSDIR/node_modules/@modelcontextprotocol/inspector" ]]; then
		cat >"$TSDIR/probe.mjs" <<'EOF'
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
const client = new Client({ name: 'ts-sdk-interop', version: '1.0.0' });
try {
  await client.connect(new StreamableHTTPClientTransport(new URL(process.argv[2])));
  console.log('negotiated:', client.transport.protocolVersion, 'session:', client.transport.sessionId ?? 'none');
  const { tools } = await client.listTools();
  console.log('tools:', tools.map(t => t.name).sort().join(','));
  const res = await client.callTool({ name: 'read', arguments: { key: 'task.verified' } });
  console.log('read:', JSON.parse(res.content[0].text).key);
} catch (e) { console.error('FAILED:', e.message); process.exitCode = 1; }
await client.close().catch(() => {});
EOF
		show '(cd "$TSDIR" && node probe.mjs "$URL$MCP_PATH")'
		check "TypeScript SDK probe exits successfully" equal "$RC" 0
		check "TypeScript SDK connected and read the v8 procedure" out_has "read: task.verified"
		check "TypeScript SDK negotiated 2025-11-25 without sessions" out_has "negotiated: 2025-11-25 session: none"
		check "TypeScript SDK sees exactly the agent tool set" out_has "tools: advance,attach_child,discover,feedback,propose_advance,propose_publish_capture,propose_start,read,resolve,start"
		show '(cd "$TSDIR" && npx --no-install mcp-inspector --cli "$URL$MCP_PATH" --transport http --method tools/list 2>"$OLDPWD/$WORK/inspector.err" | jq -c "[.tools[].name] | sort")'
		check "MCP Inspector lists v8's ten tools" inspector_result
		[[ "$RC" == 0 ]] || note "**Inspector stderr:**" '```text' "$(cat "$WORK/inspector.err")" '```'
	fi
fi

close_section
{
	echo "# Polaroid MCP v8 end-to-end test report"
	echo
	echo "Generated by \`make e2e-mcp\` on $(date -u +%Y-%m-%dT%H:%M:%SZ), commit \`$(git rev-parse --short HEAD)\`."
	echo "Real polaroidd, isolated SQLite and Git catalog. Every tool call shows arguments, tool error state and result."
	echo
	echo "**Result: $PASS checks passed, $FAIL failed.**"
	echo
	echo "| # | Feature | Checks | Result |"
	echo "| --- | --- | --- | --- |"
	printf '%s\n' "${SECTIONS[@]}"
	cat "$BODY"
} >"$REPORT"
echo "REPORT: $REPORT  passed=$PASS failed=$FAIL" >&2
((FAIL == 0))
