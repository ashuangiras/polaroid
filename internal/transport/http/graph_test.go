package http_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// createWith creates a procedure whose version 1 has the given references and
// returns its ID.
func (s *testServer) createWith(t *testing.T, key, refs string) string {
	t.Helper()
	resp := s.call(t, http.MethodPost, "/v1/procedures", procedureWithReferences(key, refs))
	expect(t, resp, http.StatusCreated, "")
	return decodeStrict[historyJSON](t, resp.body).ID
}

func pinRef(name, id string, n int) string {
	return fmt.Sprintf(`{"name": %q, "procedure_id": %q, "version_policy": {"pin": %d}, "inputs": {}}`, name, id, n)
}

func latestRef(name, id string) string {
	return fmt.Sprintf(`{"name": %q, "procedure_id": %q, "version_policy": {"contextual": {}}, "inputs": {}}`, name, id)
}

func TestCompositionGraphEndpoint(t *testing.T) {
	s := newTestServer(t)
	leaf := s.create(t).ID
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+leaf+"/versions", reviseBody(1, "v2")), http.StatusCreated, "")
	mid := s.createWith(t, "compose.mid", "["+latestRef("leaf", leaf)+"]")
	root := s.createWith(t, "compose.root", `[`+pinRef("old-leaf", leaf, 1)+`,
		{"name": "mid", "procedure_id": "`+mid+`", "version_policy": {"contextual": {}}, "inputs": {"module": {"input": "driver"}}}]`)

	resp := s.call(t, http.MethodGet, "/v1/procedures/"+root+"/versions/1/graph", "")
	expect(t, resp, http.StatusOK, "")
	leafNode := func(n int) string {
		return fmt.Sprintf(`{"procedure_id":%q,"canonical_key":"go.dependency.add","version":%d,"scope":"unspecified","references":[]}`, leaf, n)
	}
	want := fmt.Sprintf(`{"procedure_id":%q,"canonical_key":"compose.root","version":1,"scope":"unspecified","references":[`+
		`{"name":"old-leaf","version_policy":{"pin":1},"selected_by":"pin","inputs":{},"node":%s},`+
		`{"name":"mid","version_policy":{"contextual":{}},"selected_by":"latest","inputs":{"module":{"input":"driver"}},"node":`+
		`{"procedure_id":%q,"canonical_key":"compose.mid","version":1,"scope":"unspecified","references":[`+
		`{"name":"leaf","version_policy":{"contextual":{}},"selected_by":"latest","inputs":{},"node":%s}]}}]}`,
		root, leafNode(1), mid, leafNode(2))
	if got := strings.TrimSpace(string(resp.body)); got != want {
		t.Fatalf("graph =\n %s\nwant\n %s", got, want)
	}

	// A leaf's graph is just the node.
	got := s.call(t, http.MethodGet, "/v1/procedures/"+leaf+"/versions/1/graph", "")
	if strings.TrimSpace(string(got.body)) != leafNode(1) {
		t.Fatalf("leaf graph = %s", got.body)
	}

	for path, code := range map[string]string{
		"/v1/procedures/" + root + "/versions/2/graph":                         "not_found",
		"/v1/procedures/0192f7e4-0000-7000-8000-000000000000/versions/1/graph": "not_found",
		"/v1/procedures/" + root + "/versions/0/graph":                         "invalid_request",
		"/v1/procedures/" + root + "/versions/latest/graph":                    "invalid_request",
	} {
		resp := s.call(t, http.MethodGet, path, "")
		if e := errorOf(t, resp); e.Code != code {
			t.Errorf("GET %s: code %s, want %s", path, e.Code, code)
		}
	}
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+root+"/versions/1/graph", "{}"), http.StatusMethodNotAllowed, "method_not_allowed")
}

func TestReferenceCyclesAreRejected(t *testing.T) {
	s := newTestServer(t)
	a := s.create(t).ID
	b := s.createWith(t, "compose.b", "["+latestRef("a", a)+"]")
	before := s.call(t, http.MethodGet, "/v1/procedures/"+a, "").body

	for name, refs := range map[string]string{
		"A -> A":      "[" + pinRef("me", a, 1) + "]",
		"A -> B -> A": "[" + latestRef("b", b) + "]",
	} {
		t.Run(name, func(t *testing.T) {
			resp := s.call(t, http.MethodPost, "/v1/procedures/"+a+"/versions", reviseWithReferences(1, refs))
			expect(t, resp, http.StatusConflict, "reference_cycle")
			e := errorOf(t, resp)
			if len(e.Cycle) < 2 || e.Cycle[0].ProcedureID != a || e.Cycle[0].Version != 2 || e.Cycle[len(e.Cycle)-1].ProcedureID != a || e.Cycle[len(e.Cycle)-1].Reference != "" {
				t.Fatalf("cycle = %+v", e.Cycle)
			}
		})
	}
	if after := s.call(t, http.MethodGet, "/v1/procedures/"+a, "").body; string(after) != string(before) {
		t.Fatalf("a rejected cycle changed the history:\n%s\n%s", after, before)
	}
}

func TestGraphsOverTheLimitAreRefused(t *testing.T) {
	s := newTestServer(t)
	// p(i) follows p(i-1)'s latest version, so p(32) is 32 references deep.
	ids := []string{s.createWith(t, "chain.p0", "[]")}
	for i := 1; i <= 32; i++ {
		ids = append(ids, s.createWith(t, fmt.Sprint("chain.p", i), "["+latestRef("prev", ids[i-1])+"]"))
	}
	top := ids[32]
	expect(t, s.call(t, http.MethodGet, "/v1/procedures/"+top+"/versions/1/graph", ""), http.StatusOK, "")

	// One more level on top is rejected at write time, and nothing is stored.
	resp := s.call(t, http.MethodPost, "/v1/procedures", procedureWithReferences("chain.too-deep", "["+latestRef("prev", top)+"]"))
	expect(t, resp, http.StatusUnprocessableEntity, "graph_too_large")
	expect(t, s.call(t, http.MethodGet, "/v1/procedures/by-key/chain.too-deep", ""), http.StatusNotFound, "not_found")

	// Growing the bottom of the chain is checked only from the new version,
	// so it is stored, and the top's graph then exceeds the limit on read.
	leaf := s.create(t).ID
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+ids[0]+"/versions", reviseWithReferences(1, "["+pinRef("leaf", leaf, 1)+"]")), http.StatusCreated, "")
	resp = s.call(t, http.MethodGet, "/v1/procedures/"+top+"/versions/1/graph", "")
	expect(t, resp, http.StatusUnprocessableEntity, "graph_too_large")
	if strings.Contains(string(resp.body), `"references"`) {
		t.Fatalf("a refused graph returned partial data: %s", resp.body)
	}
}
