package mcp_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"slices"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// TestLifecycleThroughTools: the invalidation and retirement tools
// (ADR-0035) return the HTTP API's documents and errors, and the list tools
// take the lifecycle filters.
func TestLifecycleThroughTools(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	svc := memory.NewService(h.store)
	same := func(text string, doc any) {
		t.Helper()
		want, err := jsonv2.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if text != string(want) {
			t.Fatalf("tool text %s\nHTTP document %s", text, want)
		}
	}
	leaf := field(t, h.ok(t, "create_procedure", `{"canonical_key":"go.build","philosophy":"p","method":"Build the module.","contract":{},"instructions":{},"revision_reason":"r"}`), "id")
	parent := field(t, h.ok(t, "create_procedure", `{"canonical_key":"go.release","philosophy":"p","method":"m","contract":{},"instructions":{},`+
		`"references":[{"name":"build","procedure_id":"`+leaf+`","version_policy":{"contextual":{}},"inputs":{}}],"revision_reason":"r"}`), "id")
	recordRun := func(procedure, children string) string {
		return field(t, h.ok(t, "record_execution", `{"procedure_id":"`+procedure+`","version":1,"repository":"github.com/o/a",`+
			`"commit":"0123456789abcdef0123456789abcdef01234567","environment":{"name":"ci","attributes":{}},"inputs":{},"outcome":"succeeded","evidence":{"exit":0}`+children+`}`), "id")
	}
	run := recordRun(leaf, "")
	parentRun := recordRun(parent, `,"children":[{"reference":"build","execution_id":"`+run+`"}]`)

	if got := h.ok(t, "get_execution_invalidation", `{"execution_id":"`+run+`"}`); got != `{"execution_id":"`+run+`","evidence_status":"valid"}` {
		t.Fatalf("get_execution_invalidation = %s", got)
	}
	if e := h.fail(t, "invalidate_execution", `{"execution_id":"`+run+`","reason":"","actor":"A"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"reason", "actor"}) {
		t.Fatalf("invalid invalidation: %+v", e)
	}
	invalidated := h.ok(t, "invalidate_execution", `{"execution_id":"`+run+`","reason":"wrong version","actor":"copilot.vscode"}`)
	state, err := svc.ExecutionInvalidation(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	same(invalidated, wire.NewInvalidationState(state))
	h.fail(t, "invalidate_execution", `{"execution_id":"`+run+`","reason":"again","actor":"copilot.vscode"}`, "execution_invalidated")
	h.fail(t, "invalidate_execution", `{"execution_id":"nope","reason":"r","actor":"copilot.vscode"}`, "not_found")
	if v := h.ok(t, "get_verification", `{"execution_id":"`+parentRun+`"}`); !strings.Contains(v,
		`"verified":false,"evidence_status":"valid","problems":[{"code":"child_invalidated","reference":"build","execution_id":"`+run+`"}]`) {
		t.Fatalf("parent verification = %s", v)
	}
	if got := h.ok(t, "list_executions", `{"evidence_status":"invalidated"}`); !strings.Contains(got, run) || strings.Contains(got, parentRun) {
		t.Fatalf("list_executions invalidated = %s", got)
	}
	if got := h.ok(t, "get_execution", `{"id":"`+run+`"}`); !strings.Contains(got, `"evidence_status":"invalidated","invalidation":{"reason":"wrong version","actor":"copilot.vscode"`) {
		t.Fatalf("get_execution = %s", got)
	}

	if got := h.ok(t, "get_procedure_lifecycle", `{"procedure_id":"`+leaf+`"}`); got != `{"procedure_id":"`+leaf+`","status":"active","latest_event":0,"events":[]}` {
		t.Fatalf("get_procedure_lifecycle = %s", got)
	}
	retired := h.ok(t, "retire_procedure", `{"procedure_id":"`+leaf+`","base_event":0,"reason":"superseded","actor":"copilot.vscode"}`)
	l, err := svc.ProcedureLifecycle(ctx, leaf)
	if err != nil {
		t.Fatal(err)
	}
	same(retired, wire.NewLifecycle(l))
	if got := h.ok(t, "get_procedure_lifecycle", `{"procedure_id":"`+leaf+`"}`); got != retired {
		t.Fatalf("get_procedure_lifecycle = %s, want %s", got, retired)
	}
	if e := h.fail(t, "retire_procedure", `{"procedure_id":"`+leaf+`","base_event":0,"reason":"again","actor":"copilot.vscode"}`, "lifecycle_conflict"); e.LatestEvent != 1 {
		t.Fatalf("lifecycle_conflict = %+v", e)
	}
	h.fail(t, "retire_procedure", `{"procedure_id":"`+leaf+`","base_event":1,"reason":"again","actor":"copilot.vscode"}`, "lifecycle_unchanged")
	if e := h.fail(t, "revise_procedure", `{"procedure_id":"`+parent+`","base_version":1,"philosophy":"p","method":"m","contract":{},"instructions":{},`+
		`"references":[{"name":"build","procedure_id":"`+leaf+`","version_policy":{"pin":1},"inputs":{}}],"revision_reason":"r"}`, "procedure_retired"); !slices.Equal(e.fields(), []string{"references[0].procedure_id"}) {
		t.Fatalf("procedure_retired = %+v", e)
	}
	if got := h.ok(t, "list_procedures", `{}`); strings.Contains(got, leaf) {
		t.Fatalf("list_procedures lists a retired procedure: %s", got)
	}
	if got := h.ok(t, "list_procedures", `{"lifecycle":"retired"}`); !strings.Contains(got, `"id":"`+leaf+`"`) || !strings.Contains(got, `"lifecycle_status":"retired"`) {
		t.Fatalf("list_procedures retired = %s", got)
	}
	if got := h.ok(t, "discover_procedures", `{"task":"build the module"}`); strings.Contains(got, leaf) {
		t.Fatalf("discover_procedures found a retired procedure: %s", got)
	}
	if got := h.ok(t, "discover_procedures", `{"task":"build the module","lifecycle":"all"}`); !strings.Contains(got, `"procedure_id":"`+leaf+`","canonical_key":"go.build","version":1,"scope":"unspecified","lifecycle_status":"retired"`) {
		t.Fatalf("discover_procedures all = %s", got)
	}
	if got := h.ok(t, "get_graph", `{"procedure_id":"`+parent+`","version":1}`); !strings.Contains(got, `"retirement":{"event":1,"status":"retired","reason":"superseded","actor":"copilot.vscode"`) {
		t.Fatalf("get_graph = %s", got)
	}
	reactivated := h.ok(t, "reactivate_procedure", `{"procedure_id":"`+leaf+`","base_event":1,"reason":"needed again","actor":"copilot.vscode"}`)
	if !strings.Contains(reactivated, `"status":"active","latest_event":2`) {
		t.Fatalf("reactivate_procedure = %s", reactivated)
	}
}
