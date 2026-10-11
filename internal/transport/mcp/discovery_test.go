package mcp_test

import (
	"slices"
	"strings"
	"testing"
)

// TestDiscoveryAndDuplicatesThroughTools: the ADR-0032 operations as MCP
// tools, with flat arguments, read-only, and the HTTP API's bodies and
// field errors.
func TestDiscoveryAndDuplicatesThroughTools(t *testing.T) {
	h := newHarness(t)
	a := field(t, h.ok(t, "register_repository", `{"identifier":"github.com/o/a","name":"A"}`), "id")
	build := field(t, h.ok(t, "create_procedure", `{"canonical_key":"go.module.build","goal":"Build every package of a Go module.",`+
		`"philosophy":"A build is evidence only with the declared toolchain.","method":"Run the repository's build command from the module root.",`+
		`"applicability":{"shared":{}},"contract":{},"instructions":{"steps":["Run the build command."]},"revision_reason":"r"}`), "id")
	h.ok(t, "create_procedure", `{"canonical_key":"a.release.publish","goal":"Publish a prerelease build from a tag.",`+
		`"philosophy":"Test the published bytes.","method":"Tag, package, test every archive.","applicability":{"repository":"`+a+`"},`+
		`"contract":{},"instructions":{},"references":[{"name":"build","procedure_id":"`+build+`","version_policy":{"contextual":{}},"inputs":{}}],"revision_reason":"r"}`)

	d := h.ok(t, "discover_procedures", `{"task":"building a Go module","limit":1}`)
	for _, want := range []string{`{"terms":["building","go","module"],"considered":2,"rarity":[{"term":"building","procedures":2,`,
		`"matched":2,"candidates":[{"procedure_id":"` + build + `","canonical_key":"go.module.build","version":1,` +
			`"scope":"shared","applicability":{"shared":{}},"goal":"Build every package of a Go module.","method":"Run the repository's build command from the module root.",` +
			`"matched_terms":3,"score":`,
		`"contributions":[{"term":"building","field":"canonical_key",`,
		`"matches":[{"field":"canonical_key","terms":["building","go","module"]},{"field":"goal","terms":["building","go","module"]},` +
			`{"field":"method","terms":["building","module"]},{"field":"philosophy","terms":["building"]},{"field":"instructions","terms":["building"]}]}]}`,
	} {
		if !strings.Contains(d, want) {
			t.Fatalf("discover_procedures =\n%s\nlacks\n%s", d, want)
		}
	}
	if d := h.ok(t, "discover_procedures", `{"task":"publish a prerelease","repository":"github.com/o/b"}`); !strings.Contains(d, `"candidates":[]`) {
		t.Fatalf("a local procedure of A discovered in B: %s", d)
	}
	if e := h.fail(t, "discover_procedures", `{"task":"the and","limit":99}`, "invalid_request"); !slices.Equal(e.fields(), []string{"task", "limit"}) {
		t.Fatalf("discover_procedures errors: %+v", e)
	}

	dup := h.ok(t, "suggest_duplicates", `{"canonical_key":"go.module.build","goal":"Build every package of a Go module.",`+
		`"method":"Run the build command from the module root.","philosophy":"Evidence only with the declared toolchain."}`)
	if !strings.HasPrefix(dup, `{"proposal_terms":`) || !strings.Contains(dup, `"key_collision":{"procedure_id":"`+build+`","canonical_key":"go.module.build","latest_version":1}`) ||
		!strings.Contains(dup, `"suggestions":[{"procedure_id":"`+build+`"`) {
		t.Fatalf("suggest_duplicates = %s", dup)
	}
	excluded := h.ok(t, "suggest_duplicates", `{"canonical_key":"go.module.build","method":"Run the build command from the module root.",`+
		`"philosophy":"Evidence only with the declared toolchain.","exclude_procedure_id":"`+build+`"}`)
	if strings.Contains(excluded, build) {
		t.Fatalf("the excluded procedure was reported: %s", excluded)
	}
	if e := h.fail(t, "suggest_duplicates", `{"goal":"a\nb"}`, "invalid_request"); !slices.Equal(e.fields(), []string{"goal", "method", "philosophy"}) {
		t.Fatalf("suggest_duplicates errors: %+v", e)
	}
	h.fail(t, "suggest_duplicates", `{"method":"m","philosophy":"p","instructions":{}}`, "invalid_request")
	if got := h.ok(t, "list_procedures", `{}`); strings.Count(got, `"canonical_key"`) != 2 {
		t.Fatalf("a read-only tool wrote: %s", got)
	}
}
