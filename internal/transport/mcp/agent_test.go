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

	text := agent.ok(t, "read", `{"repository":"github.com/example/service","binding":"change-module"}`)
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
