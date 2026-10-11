package mcp_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"regexp"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

var generatedAt = regexp.MustCompile(`"generated_at":"[^"]+"`)

// TestStatisticsThroughTools: get_catalog_statistics returns the HTTP API's
// statistics document (ADR-0036), takes no arguments and writes nothing.
func TestStatisticsThroughTools(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	svc := memory.NewService(h.store)
	leaf := field(t, h.ok(t, "create_procedure", `{"canonical_key":"go.build","philosophy":"p","method":"m",`+
		`"contract":{"note":"é"},"instructions":{},"applicability":{"shared":{}},"revision_reason":"r"}`), "id")
	record := func(outcome string) string {
		return field(t, h.ok(t, "record_execution", `{"procedure_id":"`+leaf+`","version":1,"repository":"github.com/o/a",`+
			`"commit":"0123456789abcdef0123456789abcdef01234567","environment":{"name":"ci","attributes":{}},"inputs":{},`+
			`"outcome":"`+outcome+`","evidence":{"exit":0}}`), "id")
	}
	record("succeeded")
	h.ok(t, "invalidate_execution", `{"execution_id":"`+record("failed")+`","reason":"wrong version","actor":"copilot.vscode"}`)

	text := h.ok(t, "get_catalog_statistics", `{}`)
	st, err := svc.Statistics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonv2.Marshal(wire.NewStatistics(st))
	if err != nil {
		t.Fatal(err)
	}
	strip := func(s string) string { return generatedAt.ReplaceAllString(s, `"generated_at":""`) }
	if strip(text) != strip(string(doc)) {
		t.Fatalf("tool text %s\nHTTP document %s", text, doc)
	}
	for _, want := range []string{`"procedures":1,`, `"by_scope":{"shared":1,"local":0,"unspecified":0}`,
		`"by_outcome":{"succeeded":1,"failed":1}`, `"by_evidence_status":{"valid":1,"invalidated":1}`,
		`"procedure_contracts":{"count":1,"total_bytes":13,"mean_bytes":13,"max_bytes":13}`} {
		if !strings.Contains(text, want) {
			t.Errorf("statistics lack %s: %s", want, text)
		}
	}
	if again := h.ok(t, "get_catalog_statistics", `{}`); strip(again) != strip(text) {
		t.Fatalf("a second read differs:\n%s\n%s", again, text)
	}
	h.fail(t, "get_catalog_statistics", `{"procedure_id":"`+leaf+`"}`, "invalid_request")
}
