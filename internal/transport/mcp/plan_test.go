package mcp_test

import (
	"strings"
	"testing"
)

// #131: the same fields/format/prepare_execution capability as the HTTP API,
// over MCP tools.

const commit40 = "0123456789abcdef0123456789abcdef01234567"

func TestGetVersionFieldsOverMCP(t *testing.T) {
	h := newHarness(t)
	created := h.ok(t, "create_procedure", `{"canonical_key":"demo.fields","philosophy":"p","method":"m","goal":"g","contract":{"a":1},"instructions":{"steps":["x"]},"revision_reason":"r"}`)
	id := field(t, created, "id")

	full := h.ok(t, "get_version", `{"procedure_id":"`+id+`","version":1}`)
	projected := h.ok(t, "get_version", `{"procedure_id":"`+id+`","version":1,"fields":"goal,instructions"}`)
	if projected == full {
		t.Fatalf("a fields projection must differ from the complete version")
	}
	for _, want := range []string{`"goal":"g"`, `"instructions":{"steps":["x"]}`} {
		if !strings.Contains(projected, want) {
			t.Fatalf("projected = %s, want it to contain %s", projected, want)
		}
	}
	for _, mustNotContain := range []string{`"philosophy"`, `"contract"`, `"method"`} {
		if strings.Contains(projected, mustNotContain) {
			t.Fatalf("projected = %s, must not contain %s (not requested)", projected, mustNotContain)
		}
	}
	e := h.fail(t, "get_version", `{"procedure_id":"`+id+`","version":1,"fields":"bogus"}`, "invalid_request")
	if got := e.fields(); len(got) != 1 || got[0] != "fields" {
		t.Fatalf("fields = %v", got)
	}

	// Unaffected: without fields, get_version is unchanged.
	if again := h.ok(t, "get_version", `{"procedure_id":"`+id+`","version":1}`); again != full {
		t.Fatalf("get_version without fields changed:\n%s\nwant\n%s", again, full)
	}
}

func TestGraphAndResolveBindingPlanFormatOverMCP(t *testing.T) {
	h := newHarness(t)
	leaf := field(t, h.ok(t, "create_procedure", `{"canonical_key":"demo.leaf","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`), "id")
	rootBody := h.ok(t, "create_procedure", `{"canonical_key":"demo.root","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r",
		"references":[{"name":"leaf","procedure_id":"`+leaf+`","version_policy":{"contextual":{}},"inputs":{}}]}`)
	root := field(t, rootBody, "id")

	full := h.ok(t, "get_graph", `{"procedure_id":"`+root+`","version":1}`)
	plan := h.ok(t, "get_graph", `{"procedure_id":"`+root+`","version":1,"format":"plan"}`)
	if !strings.Contains(plan, `"path":"leaf"`) || !strings.Contains(plan, `"status":"selected"`) || strings.Contains(plan, "canonical_key") {
		t.Fatalf("plan = %s", plan)
	}
	if again := h.ok(t, "get_graph", `{"procedure_id":"`+root+`","version":1,"format":"full"}`); again != full {
		t.Fatalf("format=full must equal the default:\n%s\nwant\n%s", again, full)
	}
	h.fail(t, "get_graph", `{"procedure_id":"`+root+`","version":1,"format":"bogus"}`, "invalid_request")

	binding := field(t, h.ok(t, "create_binding", `{"repository":"scratch","name":"n","procedure_id":"`+root+`","inputs":{},"version_policy":{"contextual":{}},"revision_reason":"r"}`), "id")
	resPlan := h.ok(t, "resolve_binding", `{"binding_id":"`+binding+`","environment":"ci.test","format":"plan"}`)
	if !strings.Contains(resPlan, `"binding_id":"`+binding+`"`) || !strings.Contains(resPlan, `"path":"leaf"`) {
		t.Fatalf("resolve_binding plan = %s", resPlan)
	}
	h.fail(t, "resolve_binding", `{"binding_id":"`+binding+`","environment":"ci.test","format":"bogus"}`, "invalid_request")

	prep := h.ok(t, "prepare_execution", `{"binding_id":"`+binding+`","environment":"ci.test","commit":"`+commit40+`","inputs":{}}`)
	want := h.ok(t, "resolve_binding", `{"binding_id":"`+binding+`","environment":"ci.test","commit":"`+commit40+`","inputs":{},"format":"plan"}`)
	if prep != want {
		t.Fatalf("prepare_execution must equal resolve_binding format=plan at the same target:\n%s\nwant\n%s", prep, want)
	}
	e := h.fail(t, "prepare_execution", `{"binding_id":"`+binding+`","environment":"ci.test"}`, "invalid_request")
	if got := e.fields(); len(got) == 0 {
		t.Fatalf("prepare_execution without a target must name the missing field(s): %+v", e)
	}
}
