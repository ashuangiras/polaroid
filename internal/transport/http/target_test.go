package http_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// ADR-0018: resolution reports selection evidence from any commit, and
// target verification only for the exact commit and inputs requested.

const otherCommit = "89abcdef0123456789abcdef0123456789abcdef"

// The parent passes its module input to the leaf, so the leaf's effective
// inputs are executionRequest's default {"module": "modernc.org/sqlite"}.
const parentInputs = `{"module": "modernc.org/sqlite", "working_tree": "clean"}`

type targetWorld struct {
	leaf, parent, binding string
}

func newTargetWorld(t *testing.T, s *testServer) targetWorld {
	t.Helper()
	leaf := s.create(t).ID
	ref := fmt.Sprintf(`{"name": "leaf", "procedure_id": %q, "version_policy": {"contextual": {}}, "inputs": {"module": {"input": "module"}}}`, leaf)
	parent := s.createWith(t, "compose.verify", "["+ref+"]")
	return targetWorld{leaf: leaf, parent: parent, binding: s.bind(t, repoA, "verify", parent, `{"contextual": {}}`).ID}
}

// run records the leaf and then the parent at commit, and returns the
// parent's execution ID.
func (w targetWorld) run(t *testing.T, s *testServer, commit, outcome string) string {
	t.Helper()
	at := `"` + commit + `"`
	leaf := s.record(t, w.leaf, 1, map[string]string{"commit": at})
	return s.record(t, w.parent, 1, map[string]string{"commit": at, "inputs": parentInputs, "outcome": `"` + outcome + `"`, "children": childrenJSON("leaf", leaf)})
}

func targetQuery(environment, commit, inputs string) string {
	return "?" + url.Values{"environment": {environment}, "commit": {commit}, "inputs": {inputs}}.Encode()
}

func (s *testServer) resolveAt(t *testing.T, binding, query string) (bindingResolutionJSON, string) {
	t.Helper()
	resp := s.call(t, http.MethodGet, "/v1/bindings/"+binding+"/resolution"+query, "")
	expect(t, resp, http.StatusOK, "")
	return decodeStrict[bindingResolutionJSON](t, resp.body), string(resp.body)
}

func TestTargetVerificationAcrossCommits(t *testing.T) {
	s := newTestServer(t)
	w := newTargetWorld(t, s)
	parentA := w.run(t, s, fullCommit, "succeeded")

	// Exact match at commit A.
	r, _ := s.resolveAt(t, w.binding, targetQuery(ubuntu, fullCommit, parentInputs))
	leaf := r.Graph.References[0].Node
	if tv := r.Graph.TargetVerification; tv == nil || !tv.Verified || tv.LatestExecutionID != parentA || tv.Combination.Commit != fullCommit {
		t.Fatalf("at A: %+v", tv)
	}
	if tv := leaf.TargetVerification; !tv.Verified || string(tv.Combination.Inputs) != `{"module":"modernc.org/sqlite"}` {
		t.Fatalf("leaf at A, with inputs mapped from the parent: %+v", tv)
	}

	// Unseen commit B: the same candidates, selected by A's evidence, which
	// says it ran at A; nothing is verified at B.
	r, raw := s.resolveAt(t, w.binding, targetQuery(ubuntu, otherCommit, parentInputs))
	ev := r.Graph.SelectionEvidence
	if r.SelectedBy != "evidence" || r.Graph.VerifiedBy != parentA || ev == nil || ev.ExecutionID != parentA || ev.Commit != fullCommit ||
		ev.Repository != repoA || ev.Environment.Name != ubuntu {
		t.Fatalf("selection at B: %s", raw)
	}
	tv := r.Graph.TargetVerification
	if tv.Verified || len(tv.ExecutionIDs) != 0 || tv.Combination.Commit != otherCommit || tv.Combination.Repository != repoA ||
		len(tv.Combination.Children) != 1 || tv.Combination.Children[0].Reference != "leaf" || tv.Combination.Children[0].Version != 1 {
		t.Fatalf("target at B: %s", raw)
	}
	if strings.Contains(raw, `"latest_execution_id"`) || r.Graph.References[0].Node.TargetVerification.Verified {
		t.Fatalf("nothing has run at B: %s", raw)
	}

	// No target: selection evidence only, as before plus provenance.
	r, raw = s.resolveAt(t, w.binding, "?environment="+ubuntu)
	if strings.Contains(raw, "target_verification") || r.Graph.VerifiedBy != parentA || r.Graph.SelectionEvidence.Commit != fullCommit {
		t.Fatalf("without a target: %s", raw)
	}

	// Recording at B verifies B; A's records are unchanged.
	beforeA := s.call(t, http.MethodGet, "/v1/executions/"+parentA, "").body
	parentB := w.run(t, s, otherCommit, "succeeded")
	r, _ = s.resolveAt(t, w.binding, targetQuery(ubuntu, otherCommit, parentInputs))
	if tv := r.Graph.TargetVerification; !tv.Verified || tv.LatestExecutionID != parentB {
		t.Fatalf("after recording at B: %+v", tv)
	}
	if string(s.call(t, http.MethodGet, "/v1/executions/"+parentA, "").body) != string(beforeA) {
		t.Fatal("A's execution changed")
	}
	if v, _ := s.verification(t, parentA); !v.Verified || v.Combination.Commit != fullCommit {
		t.Fatalf("A's execution is no longer verified at A: %+v", v)
	}

	// Scope: reordered members match; any other input value, environment or
	// repository does not.
	if r, _ := s.resolveAt(t, w.binding, targetQuery(ubuntu, otherCommit, `{"working_tree":"clean","module":"modernc.org/sqlite"}`)); !r.Graph.TargetVerification.Verified {
		t.Fatal("reordered input members must match")
	}
	for name, query := range map[string]string{
		"modified working tree": targetQuery(ubuntu, otherCommit, `{"module": "modernc.org/sqlite", "working_tree": "modified:3f2a"}`),
		"other environment":     targetQuery("ci.macos", otherCommit, parentInputs),
	} {
		if r, raw := s.resolveAt(t, w.binding, query); r.Graph.TargetVerification.Verified {
			t.Errorf("%s: %s", name, raw)
		}
	}
	elsewhere := s.bind(t, repoB, "verify", w.parent, `{"contextual": {}}`).ID
	if r, raw := s.resolveAt(t, elsewhere, targetQuery(ubuntu, otherCommit, parentInputs)); r.Graph.TargetVerification.Verified || r.Graph.SelectionEvidence != nil || r.SelectedBy != "latest" {
		t.Errorf("another repository: %s", raw)
	}

	// A later failure at B withdraws B's verification; history stays.
	failedB := w.run(t, s, otherCommit, "failed")
	r, _ = s.resolveAt(t, w.binding, targetQuery(ubuntu, otherCommit, parentInputs))
	if tv := r.Graph.TargetVerification; tv.Verified || tv.LatestExecutionID != failedB || !slices.Equal(tv.ExecutionIDs, []string{parentB, failedB}) {
		t.Fatalf("after a failure at B: %+v", tv)
	}
	if v, _ := s.verification(t, parentB); !v.Verified {
		t.Fatal("the earlier success at B must stay verified as an execution")
	}
}

func TestTargetOnTheGraphEndpoint(t *testing.T) {
	s := newTestServer(t)
	w := newTargetWorld(t, s)
	parentA := w.run(t, s, fullCommit, "succeeded")
	query := contextQuery(repoA, ubuntu) + "&" + url.Values{"commit": {fullCommit}, "inputs": {parentInputs}}.Encode()
	if g := s.graph(t, w.parent, 1, query); !g.TargetVerification.Verified || g.TargetVerification.LatestExecutionID != parentA {
		t.Fatalf("graph at A: %+v", g.TargetVerification)
	}
	if g := s.graph(t, w.parent, 1, contextQuery(repoA, ubuntu)); g.TargetVerification != nil || g.SelectionEvidence == nil {
		t.Fatalf("graph without a target: %+v", g)
	}
}

func TestTargetRequestsAreChecked(t *testing.T) {
	s := newTestServer(t)
	w := newTargetWorld(t, s)
	resolution := "/v1/bindings/" + w.binding + "/resolution?environment=" + ubuntu + "&"
	graph := "/v1/procedures/" + w.parent + "/versions/1/graph?"
	for path, fields := range map[string][]string{
		resolution + "commit=" + fullCommit:                                          {"inputs"},
		resolution + "inputs=%7B%7D":                                                 {"commit"},
		resolution + "commit=0123456&inputs=%7B%7D":                                  {"commit"},
		resolution + "commit=" + fullCommit + "&inputs=%5B1%5D":                      {"inputs"},
		resolution + "commit=" + fullCommit + "&inputs=%7Bnot+json":                  {"inputs"},
		graph + "commit=" + fullCommit + "&inputs=%7B%7D":                            {"repository", "environment"},
		graph + contextQuery(repoA, ubuntu)[1:] + "&commit=" + fullCommit:            {"inputs"},
		resolution + "commit=" + fullCommit + "&inputs=%7B%7D&commit=" + otherCommit: {"commit"},
	} {
		resp := s.call(t, http.MethodGet, path, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		var got []string
		for _, f := range errorOf(t, resp).Fields {
			got = append(got, f.Field)
		}
		if !slices.Equal(got, fields) {
			t.Errorf("%s: fields %v, want %v", path, got, fields)
		}
	}
}
