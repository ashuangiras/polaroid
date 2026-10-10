package mcp_test

import (
	"slices"
	"strings"
	"testing"
)

// TestConditionalReferencesThroughTools: the MCP tools take and return the
// same condition, decisions and target decisions as the HTTP API (ADR-0029).
func TestConditionalReferencesThroughTools(t *testing.T) {
	h := newHarness(t)
	leaf := field(t, h.ok(t, "create_procedure", `{"canonical_key":"demo.leaf","philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}`), "id")
	created := h.ok(t, "create_procedure", `{"canonical_key":"demo.verify","philosophy":"p","method":"m","contract":{},"instructions":{},`+
		`"references":[{"name":"checks","procedure_id":"`+leaf+`","version_policy":{"pin":1},"inputs":{}},`+
		`{"name":"integration","procedure_id":"`+leaf+`","version_policy":{"contextual":{}},"inputs":{},"condition":"Only when code changes."}],"revision_reason":"r"}`)
	if !strings.Contains(created, `"inputs":{},"condition":"Only when code changes."}`) || strings.Count(created, `"condition"`) != 1 {
		t.Fatalf("condition not returned once: %s", created)
	}
	verify := field(t, created, "id")

	checks := field(t, h.ok(t, "record_execution", run(leaf, 1, "succeeded", "")), "id")
	docsArgs := strings.TrimSuffix(run(verify, 1, "succeeded", `[{"reference":"checks","execution_id":"`+checks+`"}]`), "}") +
		`,"decisions":[{"reference":"integration","applicable":false,"rationale":"Only README.md changed."}]}`
	docs := h.ok(t, "record_execution", docsArgs)
	if !strings.Contains(docs, `"decisions":[{"reference":"integration","applicable":false,"rationale":"Only README.md changed."}]`) {
		t.Fatalf("record_execution: %s", docs)
	}
	docsID := field(t, docs, "id")
	if v := h.ok(t, "get_verification", `{"execution_id":"`+docsID+`"}`); !strings.Contains(v, `"verified":true`) ||
		!strings.Contains(v, `{"reference":"integration","skipped":true}`) {
		t.Fatalf("get_verification: %s", v)
	}

	skipRequired := strings.TrimSuffix(run(verify, 1, "succeeded", ""), "}") + `,"decisions":[{"reference":"checks","applicable":false,"rationale":"fast"}]}`
	if e := h.fail(t, "record_execution", skipRequired, "invalid_request"); !slices.Equal(e.fields(), []string{"decisions[0].reference"}) {
		t.Fatalf("skipping a required reference: %+v", e)
	}

	target := `{"procedure_id":"` + verify + `","version":1,"repository":"github.com/o/r","environment":"ci.linux","commit":"` + commit + `","inputs":{}`
	if g := h.ok(t, "get_graph", target+`,"decisions":{"integration":false}}`); !strings.Contains(g, `"verified":true,"latest_execution_id":"`+docsID+`"`) ||
		!strings.Contains(g, `"decision":"not_applicable"`) {
		t.Fatalf("get_graph with a documentation decision: %s", g)
	}
	if g := h.ok(t, "get_graph", target+`,"decisions":{"integration":true}}`); strings.Contains(g, `"verified":true,"latest_execution_id":"`+docsID+`"`) ||
		!strings.Contains(g, `"skipped_by":"`+docsID+`"`) || !strings.Contains(g, `"decision":"applicable"`) {
		t.Fatalf("get_graph needing integration: %s", g)
	}
	if g := h.ok(t, "get_graph", target+`}`); !strings.Contains(g, `"undecided":["integration"]`) {
		t.Fatalf("get_graph without decisions: %s", g)
	}
	if e := h.fail(t, "get_graph", target+`,"decisions":{"checks":true}}`, "invalid_request"); !slices.Equal(e.fields(), []string{"decisions"}) {
		t.Fatalf("a decision for a required reference: %+v", e)
	}
}
