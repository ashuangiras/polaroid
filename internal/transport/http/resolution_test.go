package http_test

import (
	"encoding/json/jsontext"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// graphNodeJSON and bindingResolutionJSON restate the documented shapes.
type graphNodeJSON struct {
	ProcedureID       string `json:"procedure_id"`
	CanonicalKey      string `json:"canonical_key"`
	Version           int    `json:"version"`
	VerifiedBy        string `json:"verified_by"`
	SelectionEvidence *struct {
		ExecutionID string `json:"execution_id"`
		Repository  string `json:"repository"`
		Commit      string `json:"commit"`
		Environment struct {
			Name string `json:"name"`
		} `json:"environment"`
	} `json:"selection_evidence"`
	TargetVerification *struct {
		Combination       combinationJSON `json:"combination"`
		Verified          bool            `json:"verified"`
		LatestExecutionID string          `json:"latest_execution_id"`
		ExecutionIDs      []string        `json:"execution_ids"`
	} `json:"target_verification"`
	References []struct {
		Name          string         `json:"name"`
		VersionPolicy jsontext.Value `json:"version_policy"`
		SelectedBy    string         `json:"selected_by"`
		Inputs        jsontext.Value `json:"inputs"`
		Node          graphNodeJSON  `json:"node"`
	} `json:"references"`
}

type bindingResolutionJSON struct {
	BindingID       string `json:"binding_id"`
	BindingRevision int    `json:"binding_revision"`
	Repository      string `json:"repository"`
	Environment     struct {
		Name string `json:"name"`
	} `json:"environment"`
	VersionPolicy jsontext.Value `json:"version_policy"`
	SelectedBy    string         `json:"selected_by"`
	Graph         graphNodeJSON  `json:"graph"`
}

const ubuntu = "ci.ubuntu-latest" // the environment executionRequest records

// leafWorld is a leaf procedure at versions 1-3 with runs in repoA/ubuntu:
// v1 and v2 succeeded, v3 failed. mid follows the leaf contextually.
type leafWorld struct {
	leaf, mid, v1Run, v2Run string
}

func newLeafWorld(t *testing.T, s *testServer) leafWorld {
	t.Helper()
	leaf := s.create(t).ID
	for base := 1; base <= 2; base++ {
		expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+leaf+"/versions", reviseBody(base, "next")), http.StatusCreated, "")
	}
	w := leafWorld{leaf: leaf, mid: s.createWith(t, "compose.mid", "["+latestRef("leaf", leaf)+"]")}
	w.v1Run = s.record(t, leaf, 1, nil)
	w.v2Run = s.record(t, leaf, 2, nil)
	s.record(t, leaf, 3, map[string]string{"outcome": `"failed"`})
	return w
}

func contextQuery(repository, environment string) string {
	return "?" + url.Values{"repository": {repository}, "environment": {environment}}.Encode()
}

func (s *testServer) graph(t *testing.T, id string, version int, query string) graphNodeJSON {
	t.Helper()
	resp := s.call(t, http.MethodGet, "/v1/procedures/"+id+"/versions/"+strconv.Itoa(version)+"/graph"+query, "")
	expect(t, resp, http.StatusOK, "")
	return decodeStrict[graphNodeJSON](t, resp.body)
}

func TestGraphResolvedInContext(t *testing.T) {
	s := newTestServer(t)
	w := newLeafWorld(t, s)
	leafEdge := func(query string) (int, string, string) {
		t.Helper()
		e := s.graph(t, w.mid, 1, query).References[0]
		return e.Node.Version, e.SelectedBy, e.Node.VerifiedBy
	}

	if v, by, ev := leafEdge(contextQuery(repoA, ubuntu)); v != 2 || by != "evidence" || ev != w.v2Run {
		t.Fatalf("in context: version %d, %s, %s; want 2 by evidence %s", v, by, ev, w.v2Run)
	}
	for _, query := range []string{"", contextQuery(repoA, "ci.macos"), contextQuery(repoB, ubuntu)} {
		if v, by, ev := leafEdge(query); v != 3 || by != "latest" || ev != "" {
			t.Errorf("query %q: version %d, %s, %q; want the latest version, unverified", query, v, by, ev)
		}
	}
	if raw := s.call(t, http.MethodGet, "/v1/procedures/"+w.mid+"/versions/1/graph", "").body; strings.Contains(string(raw), "verified_by") {
		t.Fatalf("a graph without context names evidence: %s", raw)
	}

	// A newer failure of version 2 withdraws it.
	s.record(t, w.leaf, 2, map[string]string{"outcome": `"failed"`})
	if v, by, ev := leafEdge(contextQuery(repoA, ubuntu)); v != 1 || by != "evidence" || ev != w.v1Run {
		t.Fatalf("after version 2 regressed: version %d, %s, %s; want 1 by evidence %s", v, by, ev, w.v1Run)
	}

	// A verified run of mid fixes its child to leaf@1, although leaf@2 is
	// verified again on its own.
	s.record(t, w.leaf, 2, nil)
	midRun := s.record(t, w.mid, 1, map[string]string{"children": childrenJSON("leaf", w.v1Run)})
	g := s.graph(t, w.mid, 1, contextQuery(repoA, ubuntu))
	if e := g.References[0]; g.VerifiedBy != midRun || e.Node.Version != 1 || e.SelectedBy != "evidence" || e.Node.VerifiedBy != w.v1Run {
		t.Fatalf("under mid's evidence: %+v", g)
	}
}

func TestBindingResolution(t *testing.T) {
	s := newTestServer(t)
	w := newLeafWorld(t, s)
	follow := s.bind(t, repoA, "follow", w.leaf, `{"contextual": {}}`).ID
	pinned := s.bind(t, repoA, "pinned", w.leaf, `{"pin": 1}`).ID
	resolve := func(binding, environment string) bindingResolutionJSON {
		t.Helper()
		resp := s.call(t, http.MethodGet, "/v1/bindings/"+binding+"/resolution?environment="+environment, "")
		expect(t, resp, http.StatusOK, "")
		return decodeStrict[bindingResolutionJSON](t, resp.body)
	}

	r := resolve(follow, ubuntu)
	if r.BindingID != follow || r.BindingRevision != 1 || r.Repository != repoA || r.Environment.Name != ubuntu ||
		string(r.VersionPolicy) != `{"contextual":{}}` || r.SelectedBy != "evidence" || r.Graph.Version != 2 || r.Graph.VerifiedBy != w.v2Run {
		t.Fatalf("contextual binding: %+v", r)
	}
	if r := resolve(follow, "ci.macos"); r.SelectedBy != "latest" || r.Graph.Version != 3 || r.Graph.VerifiedBy != "" {
		t.Fatalf("contextual binding without evidence: %+v", r)
	}
	if r := resolve(pinned, ubuntu); r.SelectedBy != "pin" || r.Graph.Version != 1 || r.Graph.VerifiedBy != w.v1Run {
		t.Fatalf("pinned binding: %+v", r)
	}

	// The latest revision is resolved.
	expect(t, s.call(t, http.MethodPost, "/v1/bindings/"+follow+"/revisions", reviseBindingBody(1, `{"pin": 3}`)), http.StatusCreated, "")
	if r := resolve(follow, ubuntu); r.BindingRevision != 2 || r.SelectedBy != "pin" || r.Graph.Version != 3 || r.Graph.VerifiedBy != "" {
		t.Fatalf("after revising to pin 3: %+v", r)
	}

	base := "/v1/bindings/" + follow + "/resolution"
	for query, field := range map[string]string{"": "environment", "?environment=CI%20Linux": "environment", "?environment=a&environment=b": "environment"} {
		resp := s.call(t, http.MethodGet, base+query, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if e := errorOf(t, resp); len(e.Fields) != 1 || e.Fields[0].Field != field {
			t.Errorf("query %q: fields %+v", query, e.Fields)
		}
	}
	expect(t, s.call(t, http.MethodGet, base+"?environment=ci.linux&repository="+repoA, ""), http.StatusBadRequest, "invalid_request")
	expect(t, s.call(t, http.MethodGet, "/v1/bindings/0192f7e4-0000-7000-8000-000000000000/resolution?environment=ci.linux", ""), http.StatusNotFound, "not_found")
	expect(t, s.call(t, http.MethodPost, base, "{}"), http.StatusMethodNotAllowed, "method_not_allowed")
}

func TestGraphContextRequestsAreChecked(t *testing.T) {
	s := newTestServer(t)
	w := newLeafWorld(t, s)
	base := "/v1/procedures/" + w.mid + "/versions/1/graph"
	for query, field := range map[string]string{
		"?repository=" + repoA:                         "environment",
		"?environment=" + ubuntu:                       "repository",
		contextQuery("Not/Canonical", ubuntu):          "repository",
		contextQuery(repoA, "CI Linux"):                "environment",
		contextQuery(repoA, ubuntu) + "&environment=x": "environment",
	} {
		resp := s.call(t, http.MethodGet, base+query, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if e := errorOf(t, resp); len(e.Fields) != 1 || e.Fields[0].Field != field {
			t.Errorf("query %q: fields %+v, want %s", query, e.Fields, field)
		}
	}
	expect(t, s.call(t, http.MethodGet, base+"?depth=2", ""), http.StatusBadRequest, "invalid_request")
}

// Evidence can select an older version whose references close a cycle the
// write-time check (which follows latest versions) never saw.
func TestEvidenceSelectedCycleIsReported(t *testing.T) {
	s := newTestServer(t)
	a := s.create(t).ID
	b := s.createWith(t, "compose.b", "["+latestRef("a", a)+"]")
	b1Run := s.record(t, b, 1, map[string]string{"children": childrenJSON("a", s.record(t, a, 1, nil))})
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+b+"/versions", reviseBody(1, "no references")), http.StatusCreated, "")
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+a+"/versions", reviseWithReferences(1, "["+latestRef("b", b)+"]")), http.StatusCreated, "")

	if g := s.graph(t, a, 2, ""); g.References[0].Node.Version != 2 {
		t.Fatalf("without context b@2 is selected: %+v", g)
	}
	resp := s.call(t, http.MethodGet, "/v1/procedures/"+a+"/versions/2/graph"+contextQuery(repoA, ubuntu), "")
	expect(t, resp, http.StatusConflict, "reference_cycle")
	if e := errorOf(t, resp); len(e.Cycle) != 3 || e.Cycle[1].ProcedureID != b || e.Cycle[1].Version != 1 {
		t.Fatalf("cycle = %+v (b@1 is verified by %s)", e.Cycle, b1Run)
	}
}
