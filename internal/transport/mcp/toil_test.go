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

// TestToilItemsThroughTools: the five toil tools (ADR-0034) return the HTTP
// API's documents and errors, with flat argument names in field errors.
func TestToilItemsThroughTools(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	proc := field(t, h.ok(t, "create_procedure", `{"canonical_key":"go.build","philosophy":"p","method":"m","contract":{},"instructions":{"phases":["build"]},"revision_reason":"r"}`), "id")
	run := field(t, h.ok(t, "record_execution", `{"procedure_id":"`+proc+`","version":1,"repository":"github.com/o/a",`+
		`"commit":"0123456789abcdef0123456789abcdef01234567","environment":{"name":"ci","attributes":{}},"inputs":{},"outcome":"failed","evidence":{"exit":1}}`), "id")
	content := `"activity":"Build by hand","applicability":{"shared":{}},"sources":[{"procedure_id":"` + proc + `","version":1,"steps":["build"]}],` +
		`"observations":[{"execution_id":"` + run + `","note":"a failed build"}],"assessment":{"repetition":{"status":"observed"},"burden":{"status":"unknown"}},` +
		`"status_rationale":"Seen once.","revision_reason":"r","author":"copilot.vscode"`
	proposal := `"proposal":{"inputs":1,"outputs":2,"remaining_judgment":3,"failure_and_recovery":4,"acceptance_criteria":["c"]},`

	created := h.ok(t, "create_toil_item", `{"canonical_key":"toil.build","status":"observed",`+content+`}`)
	id := field(t, created, "id")
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
	hist, err := svc.ToilItem(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	same(created, wire.NewToilHistory(hist))
	if got := h.ok(t, "get_toil_item", `{"canonical_key":"toil.build"}`); got != created {
		t.Fatalf("get_toil_item by key = %s, want %s", got, created)
	}
	h.fail(t, "create_toil_item", `{"canonical_key":"toil.build","status":"observed",`+content+`}`, "canonical_key_exists")
	if e := h.fail(t, "revise_toil_item", `{"toil_item_id":"`+id+`","base_revision":1,"status":"candidate",`+content+`}`, "invalid_request"); !slices.Equal(e.fields(), []string{"proposal"}) {
		t.Fatalf("candidate without a proposal: %+v", e)
	}
	if e := h.fail(t, "create_toil_item", `{"canonical_key":"toil.x","status":"observed",`+strings.Replace(content, `"version":1`, `"version":9`, 1)+`}`, "invalid_request"); !slices.Equal(e.fields(), []string{"sources[0].version"}) {
		t.Fatalf("a missing version: %+v", e)
	}
	revised := h.ok(t, "revise_toil_item", `{"toil_item_id":"`+id+`","base_revision":1,"status":"candidate",`+proposal+content+`}`)
	rev, err := svc.ToilRevision(ctx, id, 2)
	if err != nil {
		t.Fatal(err)
	}
	same(revised, wire.NewToilRevision(rev))
	if got := h.ok(t, "get_toil_revision", `{"toil_item_id":"`+id+`","revision":2}`); got != revised {
		t.Fatalf("get_toil_revision = %s, want %s", got, revised)
	}
	conflict := h.fail(t, "revise_toil_item", `{"toil_item_id":"`+id+`","base_revision":1,"status":"candidate",`+proposal+content+`}`, "revision_conflict")
	if !strings.Contains(conflict.Message, "latest is 2") {
		t.Fatalf("conflict: %+v", conflict)
	}
	list := h.ok(t, "list_toil_items", `{"procedure_id":"`+proc+`","status":"candidate","repository":"github.com/o/a","limit":5,"snapshot":true}`)
	items, _, err := svc.ListToilItems(ctx, memory.ToilFilter{ProcedureID: proc, Status: memory.ToilCandidate, Repository: "github.com/o/a", Page: memory.Page{Limit: 5, Snapshot: true}})
	if err != nil || len(items) != 1 {
		t.Fatalf("ListToilItems = %v, %v", items, err)
	}
	same(list, wire.NewToilItemList(items, ""))
	if e := h.fail(t, "list_toil_items", `{"status":"verified"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"status"}) {
		t.Fatalf("an unknown status: %+v", e)
	}
	h.fail(t, "get_toil_item", `{"id":"missing"}`, "not_found")
	h.fail(t, "get_toil_item", `{}`, "invalid_request")
	h.fail(t, "create_toil_item", `{"canonical_key":"toil.y","status":"observed","verified":true,`+content+`}`, "invalid_request")
}
