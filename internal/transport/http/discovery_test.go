package http_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestScopeLabelsNameTheirVersion covers ADR-0023: shared, local and
// unspecified are distinguishable on every version and graph node, list
// metadata describes latest_version only, and resolution can select a
// version whose declaration differs from it.
func TestScopeLabelsNameTheirVersion(t *testing.T) {
	s := newTestServer(t)
	a := s.register(t, "github.com/o/a")
	legacy := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("d.legacy", "")))
	s.mustPost(t, "/v1/procedures", procedureBody("d.shared", `"goal":"Shared.","applicability":{"shared":{}},`))
	s.mustPost(t, "/v1/procedures", procedureBody("d.local", `"applicability":{"repository":"`+a+`"},`))

	list := s.call(t, http.MethodGet, "/v1/procedures", "").body
	for _, want := range []string{
		`"canonical_key":"d.legacy","created_at"`, `"latest_version":1,"scope":"unspecified"`,
		`"latest_version":1,"scope":"shared","goal":"Shared.","applicability":{"shared":{}}`,
		`"latest_version":1,"scope":"local","applicability":{"repository":"` + a + `"}`,
	} {
		if !strings.Contains(string(list), want) {
			t.Fatalf("list lacks %s:\n%s", want, list)
		}
	}

	// Version 1 of d.legacy is verified in A; version 2 declares it shared
	// but has no evidence, so resolution keeps selecting version 1.
	s.mustPost(t, "/v1/executions", runBody(legacy, 1, "github.com/o/a", ""))
	s.mustPost(t, "/v1/procedures/"+legacy+"/versions", reviseWith(1, `"applicability":{"shared":{}},`))
	history := decodeStrict[historyJSON](t, s.call(t, http.MethodGet, "/v1/procedures/"+legacy, "").body)
	v1, v2 := decodeStrict[versionJSON](t, history.Versions[0]), decodeStrict[versionJSON](t, history.Versions[1])
	if history.Scope != "shared" || history.LatestVersion != 2 || v1.Scope != "unspecified" || len(v1.Applicability) != 0 || v2.Scope != "shared" {
		t.Fatalf("history labels: %+v %+v %+v", history, v1, v2)
	}
	binding := idOf(t, s.mustPost(t, "/v1/bindings", bindRequest("github.com/o/a", "legacy", legacy, `{"contextual":{}}`)))
	res := decodeStrict[bindingResolutionJSON](t, s.call(t, http.MethodGet, "/v1/bindings/"+binding+"/resolution?environment=ci", "").body)
	if res.Graph.Version != 1 || res.Graph.Scope != "unspecified" || len(res.Graph.Applicability) != 0 {
		t.Fatalf("resolution selected %d (%s), want version 1, unspecified", res.Graph.Version, res.Graph.Scope)
	}
	for v, want := range map[int]string{1: "unspecified", 2: "shared"} {
		version := decodeStrict[versionJSON](t, s.call(t, http.MethodGet, fmt.Sprintf("/v1/procedures/%s/versions/%d", legacy, v), "").body)
		node := decodeStrict[graphNodeJSON](t, s.call(t, http.MethodGet, fmt.Sprintf("/v1/procedures/%s/versions/%d/graph", legacy, v), "").body)
		if version.Scope != want || node.Scope != want {
			t.Fatalf("version %d: scope %q, graph node %q, want %q", v, version.Scope, node.Scope, want)
		}
	}
}
