package mcp_test

import (
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// TestWriteLimitsThroughTools: tools refuse oversized values with the HTTP
// API's value_too_large error, under the flat argument names, and the
// statistics tool reports the enforced policy (ADR-0037).
func TestWriteLimitsThroughTools(t *testing.T) {
	h := newHarness(t)
	big := `{"a":"` + strings.Repeat("Q", memory.MaxDocumentBytes-7) + `"}`
	e := h.fail(t, "create_procedure", `{"canonical_key":"go.big","philosophy":"p","method":"m","contract":`+big+`,"instructions":{},"revision_reason":"r"}`, "value_too_large")
	if e.Message != "value too large: contract is 65537 bytes, at most 65536" {
		t.Fatalf("create_procedure: %+v", e)
	}
	res := h.call(t, "create_procedure", `{"canonical_key":"go.big","philosophy":"p","method":"m","contract":`+big+`,"instructions":{},"revision_reason":"r"}`)
	text := textOf(t, res)
	if !strings.Contains(text, `"limits":[{"field":"contract","bytes":65537,"max_bytes":65536}]`) || strings.Contains(text, "QQQQ") {
		t.Fatalf("tool error %.300s", text)
	}
	if got := h.ok(t, "list_procedures", `{}`); got != `{"procedures":[]}` {
		t.Fatalf("a refused create stored %s", got)
	}

	stats := h.ok(t, "get_catalog_statistics", `{}`)
	var doc struct {
		WriteLimits struct {
			Fields []struct {
				Record, Field string
				MaxBytes      int `json:"max_bytes"`
			} `json:"fields"`
		} `json:"write_limits"`
	}
	if err := jsonv2.Unmarshal([]byte(stats), &doc); err != nil || len(doc.WriteLimits.Fields) != len(memory.WriteLimits()) {
		t.Fatalf("statistics write_limits: %v %.300s", err, stats)
	}
}
