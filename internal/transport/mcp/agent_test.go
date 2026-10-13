package mcp_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/memory"
	mcptransport "github.com/ashuangiras/polaroid/internal/transport/mcp"
)

// ADR-0043: the agent-mode endpoint, over the same store as the full one.

func newAgentHarness(t *testing.T) (full, agent *harness) {
	t.Helper()
	full = newHarness(t)
	srv := httptest.NewServer(mcptransport.NewAgentHandler(memory.NewService(full.store), slog.New(slog.NewTextHandler(full.logs, nil))))
	t.Cleanup(srv.Close)
	agent = &harness{url: srv.URL, store: full.store, logs: full.logs}
	agent.session = agent.connect(t, "")
	return full, agent
}

func decodeJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	return m
}

func TestAgentModeAdvertisesOnlyItsTools(t *testing.T) {
	_, agent := newAgentHarness(t)
	var names []string
	for tool, err := range agent.session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"discover", "feedback", "read"}) {
		t.Fatalf("agent tools = %v", names)
	}
	if instr := agent.session.InitializeResult().Instructions; !strings.Contains(instr, "Nothing is recorded here") || strings.Contains(instr, "record_execution") {
		t.Fatalf("agent instructions = %q", instr)
	}
	for _, name := range []string{"record_execution", "get_procedure", "resolve_binding"} {
		if _, err := agent.session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: json.RawMessage(`{}`)}); err == nil {
			t.Errorf("%s is callable in agent mode", name)
		}
	}
}

func TestAgentDiscoverAndRead(t *testing.T) {
	full, agent := newAgentHarness(t)
	leaf := field(t, full.ok(t, "create_procedure", `{"canonical_key":"go.module.tidy","philosophy":"p","method":"Tidy the module.","goal":"Tidy a Go module's dependencies.","contract":{"inputs":{"module":"path"}},"instructions":{"steps":["go mod tidy"]},"revision_reason":"r"}`), "id")
	full.ok(t, "revise_procedure", `{"procedure_id":"`+leaf+`","base_version":1,"philosophy":"p","method":"Tidy the module.","goal":"Tidy a Go module's dependencies.","contract":{"inputs":{"module":"path"}},"instructions":{"steps":["go mod tidy","git diff"]},"revision_reason":"r2"}`)
	root := field(t, full.ok(t, "create_procedure", `{"canonical_key":"go.module.change","philosophy":"Why it works.","method":"Change, tidy, check.","goal":"Safely modify a Go module.","contract":{"inputs":{"module":"path"}},"instructions":{"steps":["change","tidy"]},"revision_reason":"r",
		"references":[{"name":"tidy","procedure_id":"`+leaf+`","version_policy":{"pin":1},"inputs":{"module":{"input":"module"}}},
		{"name":"tidy-latest","procedure_id":"`+leaf+`","version_policy":{"contextual":{}},"inputs":{"module":{"input":"module"},"strict":{"value":true}},"condition":"Only when dependencies changed."}]}`), "id")
	full.ok(t, "create_binding", `{"repository":"github.com/example/service","name":"change-module","procedure_id":"`+root+`","inputs":{"module":"example.com/m"},"version_policy":{"contextual":{}},"revision_reason":"r"}`)

	d := decodeJSON(t, agent.ok(t, "discover", `{"task":"safely modify a Go module","repository":"github.com/example/service"}`))
	cands := d["candidates"].([]any)
	first := cands[0].(map[string]any)
	if first["canonical_key"] != "go.module.change" || first["goal"] != "Safely modify a Go module." || first["scope"] != "unspecified" {
		t.Fatalf("first candidate = %v", first)
	}
	for _, c := range cands {
		for k := range c.(map[string]any) {
			if !slices.Contains([]string{"procedure_id", "canonical_key", "version", "scope", "goal"}, k) {
				t.Fatalf("candidate member %q is not compact: %v", k, c)
			}
		}
	}
	if b := d["bindings"].([]any); len(b) != 1 || b[0].(map[string]any)["name"] != "change-module" {
		t.Fatalf("bindings = %v", d["bindings"])
	}

	text := agent.ok(t, "read", `{"repository":"github.com/example/service","binding":"change-module","environment":"ci.test"}`)
	g := decodeJSON(t, text)
	if g["procedure_id"] != root || g["version"] != 1.0 || g["method"] != "Change, tidy, check." || g["binding"].(map[string]any)["revision"] != 1.0 {
		t.Fatalf("read binding = %s", text)
	}
	for _, absent := range []string{"philosophy", "revision_reason", "created_at", "versions"} {
		if _, ok := g[absent]; ok {
			t.Fatalf("read returned %q by default: %s", absent, text)
		}
	}
	refs := g["references"].([]any)
	tidy, latest := refs[0].(map[string]any), refs[1].(map[string]any)
	if tidy["version"] != 1.0 || latest["version"] != 2.0 || latest["condition"] != "Only when dependencies changed." || tidy["condition"] != nil {
		t.Fatalf("references = %v", refs)
	}
	if !strings.Contains(text, `"inputs":{"module":"example.com/m","strict":true}`) {
		t.Fatalf("mapped child inputs missing: %s", text)
	}

	// The mapped inputs equal the full API's own derivation for the child.
	plan := decodeJSON(t, full.ok(t, "prepare_execution", `{"binding_id":"`+g["binding"].(map[string]any)["id"].(string)+`","environment":"ci.test","commit":"`+commit40+`","inputs":{"module":"example.com/m"},"decisions":{"tidy-latest":true}}`))
	for _, s := range plan["steps"].([]any) {
		step := s.(map[string]any)
		for _, r := range refs {
			ref := r.(map[string]any)
			if step["path"] == ref["name"] {
				want, _ := json.Marshal(step["inputs"])
				got, _ := json.Marshal(ref["inputs"])
				if string(got) != string(want) {
					t.Fatalf("%s: read inputs %s, prepare_execution inputs %s", ref["name"], got, want)
				}
			}
		}
	}

	byKey := agent.ok(t, "read", `{"canonical_key":"go.module.change","full":true}`)
	if m := decodeJSON(t, byKey); m["philosophy"] != "Why it works." || m["revision_reason"] != "r" || !strings.Contains(byKey, `"input_mapping":{"module":{"input":"module"}}`) {
		t.Fatalf("full read without inputs = %s", byKey)
	}
	if v := decodeJSON(t, agent.ok(t, "read", `{"procedure_id":"`+leaf+`","version":1,"inputs":{"module":"a"}}`)); v["version"] != 1.0 || v["latest_version"] != 2.0 {
		t.Fatalf("read version 1 = %v", v)
	}

	agent.fail(t, "read", `{}`, "invalid_request")
	agent.fail(t, "read", `{"binding":"change-module"}`, "invalid_request")
	agent.fail(t, "read", `{"procedure_id":"`+leaf+`","inputs":[1]}`, "invalid_request")
	agent.fail(t, "read", `{"repository":"github.com/example/service","binding":"change-module","version":1}`, "invalid_request")
	agent.fail(t, "read", `{"repository":"github.com/example/service","binding":"missing"}`, "not_found")
	agent.fail(t, "read", `{"procedure_id":"`+leaf+`","version":9}`, "not_found")

	if got := full.ok(t, "list_executions", `{}`); got != `{"executions":[]}` {
		t.Fatalf("agent mode wrote executions: %s", got)
	}
}

// read never guesses an evidence-based selection: a binding whose version or
// references are selected from evidence needs environment, and with it the
// selection is resolve_binding's own.
func TestAgentReadSelection(t *testing.T) {
	full, agent := newAgentHarness(t)
	const repo = "github.com/example/service"
	def := `"philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"`
	p := field(t, full.ok(t, "create_procedure", `{"canonical_key":"demo.sel",`+def+`}`), "id")
	full.ok(t, "revise_procedure", `{"procedure_id":"`+p+`","base_version":1,`+def+`}`)
	full.ok(t, "create_binding", `{"repository":"`+repo+`","name":"sel","procedure_id":"`+p+`","inputs":{},"version_policy":{"contextual":{}},"revision_reason":"r"}`)

	e := agent.fail(t, "read", `{"repository":"`+repo+`","binding":"sel"}`, "invalid_request")
	if got := e.fields(); len(got) != 1 || got[0] != "environment" || !strings.Contains(e.Message, "context required") {
		t.Fatalf("contextual binding without environment = %+v", e)
	}
	if g := decodeJSON(t, agent.ok(t, "read", `{"repository":"`+repo+`","binding":"sel","environment":"ci.test"}`)); g["version"] != 2.0 || g["selected_by"] != "latest" {
		t.Fatalf("without evidence: %v", g)
	}

	full.ok(t, "record_execution", `{"procedure_id":"`+p+`","version":1,"repository":"`+repo+`","commit":"`+commit40+`","environment":{"name":"ci.test","attributes":{}},"inputs":{},"outcome":"succeeded","evidence":{"ran":"x"}}`)
	g := decodeJSON(t, agent.ok(t, "read", `{"repository":"`+repo+`","binding":"sel","environment":"ci.test"}`))
	res := decodeJSON(t, full.ok(t, "resolve_binding", `{"binding_id":"`+g["binding"].(map[string]any)["id"].(string)+`","environment":"ci.test"}`))
	if g["version"] != 1.0 || g["selected_by"] != "evidence" || g["latest_version"] != 2.0 || res["graph"].(map[string]any)["version"] != 1.0 {
		t.Fatalf("evidence must select version 1, as resolve_binding does: read %v, resolve %v", g, res["graph"])
	}

	ref := func(name, policy string) string {
		return `{"name":"` + name + `","procedure_id":"` + p + `","version_policy":` + policy + `,"inputs":{}}`
	}
	q := field(t, full.ok(t, "create_procedure", `{"canonical_key":"demo.sel-parent",`+def+`,"references":[`+ref("child", `{"contextual":{}}`)+`]}`), "id")
	full.ok(t, "create_binding", `{"repository":"`+repo+`","name":"pinned-parent","procedure_id":"`+q+`","inputs":{},"version_policy":{"pin":1},"revision_reason":"r"}`)
	e = agent.fail(t, "read", `{"repository":"`+repo+`","binding":"pinned-parent"}`, "invalid_request")
	if !strings.Contains(e.Message, "composes a reference selected from evidence") {
		t.Fatalf("pinned binding over a contextual reference = %+v", e)
	}
	child := decodeJSON(t, agent.ok(t, "read", `{"repository":"`+repo+`","binding":"pinned-parent","environment":"ci.test"}`))["references"].([]any)[0].(map[string]any)
	if child["version"] != 1.0 || child["selected_by"] != "evidence" {
		t.Fatalf("child with environment = %v", child)
	}
	if c := decodeJSON(t, agent.ok(t, "read", `{"procedure_id":"`+q+`"}`))["references"].([]any)[0].(map[string]any); c["version"] != 2.0 || c["selected_by"] != "latest" {
		t.Fatalf("without a context a procedure read selects as get_graph does, and says so: %v", c)
	}
	if c := decodeJSON(t, agent.ok(t, "read", `{"procedure_id":"`+q+`","repository":"`+repo+`","environment":"ci.test"}`))["references"].([]any)[0].(map[string]any); c["version"] != 1.0 || c["selected_by"] != "evidence" {
		t.Fatalf("procedure read in a context = %v", c)
	}
	agent.fail(t, "read", `{"procedure_id":"`+q+`","environment":"ci.test"}`, "invalid_request")

	r := field(t, full.ok(t, "create_procedure", `{"canonical_key":"demo.sel-pinned",`+def+`,"references":[`+ref("child", `{"pin":2}`)+`]}`), "id")
	full.ok(t, "create_binding", `{"repository":"`+repo+`","name":"all-pinned","procedure_id":"`+r+`","inputs":{},"version_policy":{"pin":1},"revision_reason":"r"}`)
	if g := decodeJSON(t, agent.ok(t, "read", `{"repository":"`+repo+`","binding":"all-pinned"}`)); g["selected_by"] != "pin" || g["references"].([]any)[0].(map[string]any)["version"] != 2.0 {
		t.Fatalf("a fully pinned binding needs no environment: %v", g)
	}
}

// Shared guidance is a required reference pinned to an exact version: a
// missing version is refused when written, and read reports the pin even
// after the guidance moves on, so it cannot be silently omitted or drift.
func TestAgentSharedGuidanceIsAPinnedReference(t *testing.T) {
	full, agent := newAgentHarness(t)
	def := `"philosophy":"p","method":"m","contract":{"rules":[{"id":"target"}]},"instructions":{},"revision_reason":"r"`
	gid := field(t, full.ok(t, "create_procedure", `{"canonical_key":"agent.guidance.shared",`+def+`}`), "id")
	guidance := func(pin string) string {
		return `{"canonical_key":"demo.uses-guidance","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r","references":[{"name":"guidance","procedure_id":"` + gid + `","version_policy":{"pin":` + pin + `},"inputs":{}}]}`
	}
	if e := full.fail(t, "create_procedure", guidance("2"), "invalid_request"); !slices.Contains(e.fields(), "references[0].version_policy.pin") {
		t.Fatalf("a pin to a missing guidance version = %+v", e)
	}
	p := field(t, full.ok(t, "create_procedure", guidance("1")), "id")
	full.ok(t, "revise_procedure", `{"procedure_id":"`+gid+`","base_version":1,`+def+`}`)
	ref := decodeJSON(t, agent.ok(t, "read", `{"procedure_id":"`+p+`"}`))["references"].([]any)[0].(map[string]any)
	if ref["name"] != "guidance" || ref["version"] != 1.0 || ref["selected_by"] != "pin" || ref["condition"] != nil {
		t.Fatalf("guidance reference = %v", ref)
	}
	full.ok(t, "retire_procedure", `{"procedure_id":"`+gid+`","base_event":0,"reason":"superseded","actor":"test.agent"}`)
	full.fail(t, "create_procedure", strings.Replace(guidance("1"), "demo.uses-guidance", "demo.uses-retired", 1), "procedure_retired")
}

// Nested references: a contextual reference two levels below a binding needs
// a context, and a child read in the binding's repository and environment
// selects what resolve_binding selects at that depth; the pinned guidance of
// the middle procedure is validated and reported the same way.
func TestAgentReadNestedSelection(t *testing.T) {
	full, agent := newAgentHarness(t)
	const repo = "github.com/example/service"
	def := `"philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"`
	ref := func(name, id, policy string) string {
		return `{"name":"` + name + `","procedure_id":"` + id + `","version_policy":` + policy + `,"inputs":{}}`
	}
	leaf := field(t, full.ok(t, "create_procedure", `{"canonical_key":"demo.leaf",`+def+`}`), "id")
	full.ok(t, "revise_procedure", `{"procedure_id":"`+leaf+`","base_version":1,`+def+`}`)
	g := field(t, full.ok(t, "create_procedure", `{"canonical_key":"demo.guidance",`+def+`}`), "id")
	mid := field(t, full.ok(t, "create_procedure", `{"canonical_key":"demo.mid",`+def+`,"references":[`+ref("guidance", g, `{"pin":1}`)+`,`+ref("leaf", leaf, `{"contextual":{}}`)+`]}`), "id")
	root := field(t, full.ok(t, "create_procedure", `{"canonical_key":"demo.root",`+def+`,"references":[`+ref("mid", mid, `{"pin":1}`)+`]}`), "id")
	bid := field(t, full.ok(t, "create_binding", `{"repository":"`+repo+`","name":"nested","procedure_id":"`+root+`","inputs":{},"version_policy":{"pin":1},"revision_reason":"r"}`), "id")
	full.ok(t, "record_execution", `{"procedure_id":"`+leaf+`","version":1,"repository":"`+repo+`","commit":"`+commit40+`","environment":{"name":"ci.test","attributes":{}},"inputs":{},"outcome":"succeeded","evidence":{"ran":"x"}}`)

	if e := agent.fail(t, "read", `{"repository":"`+repo+`","binding":"nested"}`, "invalid_request"); !strings.Contains(e.Message, "context required") {
		t.Fatalf("contextual reference two levels down, no environment = %+v", e)
	}
	b := decodeJSON(t, agent.ok(t, "read", `{"repository":"`+repo+`","binding":"nested","environment":"ci.test"}`))
	if r := b["references"].([]any)[0].(map[string]any); r["procedure_id"] != mid || r["version"] != 1.0 || r["selected_by"] != "pin" {
		t.Fatalf("binding read reference = %v", r)
	}
	res := decodeJSON(t, full.ok(t, "resolve_binding", `{"binding_id":"`+bid+`","environment":"ci.test"}`))
	var resolved map[string]any
	for _, e := range res["graph"].(map[string]any)["references"].([]any)[0].(map[string]any)["node"].(map[string]any)["references"].([]any) {
		if e := e.(map[string]any); e["name"] == "leaf" {
			resolved = e
		}
	}
	if resolved["node"].(map[string]any)["version"] != 1.0 || resolved["selected_by"] != "evidence" {
		t.Fatalf("resolve_binding leaf = %v", resolved)
	}

	refs := func(args string) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, r := range decodeJSON(t, agent.ok(t, "read", args))["references"].([]any) {
			out[r.(map[string]any)["name"].(string)] = r.(map[string]any)
		}
		return out
	}
	if instr := agent.session.InitializeResult().Instructions; !strings.Contains(instr, "same repository and environment as the binding") {
		t.Fatalf("agent instructions do not say how to read a binding's children: %q", instr)
	}
	in := refs(`{"procedure_id":"` + mid + `","version":1,"repository":"` + repo + `","environment":"ci.test"}`)
	if in["leaf"]["version"] != 1.0 || in["leaf"]["selected_by"] != "evidence" || in["guidance"]["version"] != 1.0 || in["guidance"]["selected_by"] != "pin" {
		t.Fatalf("child read in the binding's context must match resolve_binding: %v", in)
	}
	if out := refs(`{"procedure_id":"` + mid + `","version":1}`); out["leaf"]["version"] != 2.0 || out["leaf"]["selected_by"] != "latest" {
		t.Fatalf("child read without a context selects as get_graph does, and says so: %v", out)
	}

	if e := full.fail(t, "revise_procedure", `{"procedure_id":"`+mid+`","base_version":1,`+def+`,"references":[`+ref("guidance", g, `{"pin":2}`)+`]}`, "invalid_request"); !slices.Contains(e.fields(), "references[0].version_policy.pin") {
		t.Fatalf("nested guidance pinned to a missing version = %+v", e)
	}
	full.ok(t, "retire_procedure", `{"procedure_id":"`+g+`","base_event":0,"reason":"superseded","actor":"test.agent"}`)
	if r := decodeJSON(t, agent.ok(t, "read", `{"procedure_id":"`+g+`","version":1}`)); r["retired"] != true {
		t.Fatalf("retired guidance read = %v", r)
	}
	full.fail(t, "revise_procedure", `{"procedure_id":"`+mid+`","base_version":1,`+def+`,"references":[`+ref("guidance", g, `{"pin":1}`)+`]}`, "procedure_retired")
}

func TestAgentFeedback(t *testing.T) {
	full, agent := newAgentHarness(t)
	p := field(t, full.ok(t, "create_procedure", `{"canonical_key":"demo.fb","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`), "id")
	args := func(classification string) string {
		return `{"procedure_id":"` + p + `","version":1,"classification":"` + classification + `","summary":"Step 2 needs go 1.27","details":"go vet failed with go1.26; the contract names no toolchain.","reporter":"copilot.agent","repository":"github.com/example/service","evidence":{"step":"checks","exit":1}}`
	}
	res := decodeJSON(t, agent.ok(t, "feedback", args("missing_precondition")))
	if res["classification"] != "missing_precondition" || len(res) != 3 {
		t.Fatalf("feedback result = %v", res)
	}
	stored := full.ok(t, "get_feedback", `{"id":"`+res["id"].(string)+`"}`)
	for _, want := range []string{`"kind":"problem"`, `"context":{"classification":"missing_precondition","evidence":{"step":"checks","exit":1}}`, p, `"version":1`} {
		if !strings.Contains(stored, want) {
			t.Fatalf("stored feedback %s lacks %s", stored, want)
		}
	}
	if s := full.ok(t, "get_feedback", `{"id":"`+decodeJSON(t, agent.ok(t, "feedback", args("improved_method")))["id"].(string)+`"}`); !strings.Contains(s, `"kind":"suggestion"`) {
		t.Fatalf("improved_method is a suggestion: %s", s)
	}
	for _, c := range []string{"product_defect", "environment_problem", "transient_failure"} {
		e := agent.fail(t, "feedback", args(c), "invalid_request")
		if !strings.Contains(e.Message, "not procedural knowledge") {
			t.Fatalf("%s: %s", c, e.Message)
		}
	}
	agent.fail(t, "feedback", args("bogus"), "invalid_request")
	agent.fail(t, "feedback", `{"procedure_id":"`+p+`","classification":"missing_precondition","summary":"s","details":"d","reporter":"r"}`, "invalid_request")
	if got := full.ok(t, "list_feedback", `{}`); strings.Count(got, `"id"`) != 2 {
		t.Fatalf("refused feedback was stored: %s", got)
	}
}
