package http_test

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"testing"
)

// ADR-0029 over HTTP: conditional references, execution decisions and
// target decisions.

const integrationCondition = "The change can affect behaviour the integration suite observes."

type conditionalWorld struct {
	leaf, verify string
}

func newConditionalWorld(t *testing.T, s *testServer) conditionalWorld {
	t.Helper()
	leaf := s.create(t).ID
	verify := s.createWith(t, "change.verify", `[`+pinRef("checks", leaf, 1)+`,
		{"name": "integration", "procedure_id": "`+leaf+`", "version_policy": {"contextual": {}}, "inputs": `+passModule+`, "condition": "`+integrationCondition+`"}]`)
	return conditionalWorld{leaf: leaf, verify: verify}
}

func decisionsJSON(reference string, applicable bool, rationale string) string {
	b, _ := json.Marshal([]map[string]any{{"reference": reference, "applicable": applicable, "rationale": rationale}})
	return string(b)
}

func (s *testServer) graphAt(t *testing.T, id, decisions string) graphNodeJSON {
	t.Helper()
	q := url.Values{"repository": {"github.com/ashuangiras/polaroid"}, "environment": {"ci.ubuntu-latest"}, "commit": {fullCommit}, "inputs": {`{"module":"modernc.org/sqlite"}`}}
	if decisions != "" {
		q.Set("decisions", decisions)
	}
	return s.graph(t, id, 1, "?"+q.Encode())
}

func TestConditionalReferencesOverHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polaroid.db")
	s := newTestServerAt(t, path)
	w := newConditionalWorld(t, s)

	// The declaration reads back; a required reference has no condition.
	resp := s.call(t, http.MethodGet, "/v1/procedures/"+w.verify+"/versions/1", "")
	expect(t, resp, http.StatusOK, "")
	var refs []map[string]jsontext.Value
	if err := json.Unmarshal(decodeStrict[versionJSON](t, resp.body).References, &refs); err != nil || len(refs) != 2 {
		t.Fatalf("references = %v, %v", refs, err)
	}
	if _, ok := refs[0]["condition"]; ok || string(refs[1]["condition"]) != `"`+integrationCondition+`"` {
		t.Fatalf("conditions: checks %s, integration %s", refs[0]["condition"], refs[1]["condition"])
	}
	for _, bad := range []string{`""`, `"  "`, `7`, `null`} {
		body := procedureWithReferences("bad.condition", `[{"name": "x", "procedure_id": "`+w.leaf+`", "version_policy": {"pin": 1}, "inputs": {}, "condition": `+bad+`}]`)
		resp := s.call(t, http.MethodPost, "/v1/procedures", body)
		if bad == "null" {
			expect(t, resp, http.StatusCreated, "") // null is absent: a required reference
			continue
		}
		expect(t, resp, http.StatusBadRequest, "invalid_request")
	}

	// A documentation task skips integration with a rationale.
	checksDocs := s.record(t, w.leaf, 1, nil)
	docs := s.record(t, w.verify, 1, map[string]string{
		"children":  childrenJSON("checks", checksDocs),
		"decisions": `[{"reference": "integration", "applicable": false, "rationale": "Only docs/guide.md changed.", "evidence": {"changed": ["docs/guide.md"]}}]`,
	})
	resp = s.call(t, http.MethodGet, "/v1/executions/"+docs, "")
	expect(t, resp, http.StatusOK, "")
	if e := decodeStrict[executionJSON](t, resp.body); len(e.Decisions) != 1 || *e.Decisions[0].Applicable || string(e.Decisions[0].Evidence) != `{"changed":["docs/guide.md"]}` {
		t.Fatalf("decisions read back as %+v", e.Decisions)
	}
	v, _ := s.verification(t, docs)
	if !v.Verified || len(v.Combination.Children) != 2 || !v.Combination.Children[1].Skipped || v.Combination.Children[1].Version != 0 {
		t.Fatalf("docs verification = %+v", v)
	}

	// Contradictory, unknown and incomplete decisions.
	checks := s.record(t, w.leaf, 1, nil)
	integration := s.record(t, w.leaf, 1, nil)
	for name, tc := range map[string]struct {
		fields map[string]string
		field  string
	}{
		"skip a required reference": {map[string]string{"decisions": decisionsJSON("checks", false, "fast")}, "decisions[0].reference"},
		"unknown reference":         {map[string]string{"decisions": decisionsJSON("lint", false, "r")}, "decisions[0].reference"},
		"not applicable with a child": {map[string]string{"children": childrenJSON("checks", checks, "integration", integration),
			"decisions": decisionsJSON("integration", false, "r")}, "decisions[0].applicable"},
		"duplicate":       {map[string]string{"decisions": `[{"reference":"integration","applicable":true,"rationale":"a"},{"reference":"integration","applicable":false,"rationale":"b"}]`}, "decisions[1].reference"},
		"no applicable":   {map[string]string{"decisions": `[{"reference":"integration","rationale":"a"}]`}, "decisions[0].applicable"},
		"blank rationale": {map[string]string{"decisions": decisionsJSON("integration", false, " ")}, "decisions[0].rationale"},
	} {
		resp := s.call(t, http.MethodPost, "/v1/executions", executionRequest(w.verify, 1, tc.fields))
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if e := errorOf(t, resp); !slices.ContainsFunc(e.Fields, func(f struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		}) bool {
			return f.Field == tc.field
		}) {
			t.Errorf("%s: fields %+v, want %s", name, e.Fields, tc.field)
		}
	}
	unknownMember := executionRequest(w.verify, 1, map[string]string{"decisions": `[{"reference":"integration","applicable":false,"rationale":"a","why":"x"}]`})
	expect(t, s.call(t, http.MethodPost, "/v1/executions", unknownMember), http.StatusBadRequest, "invalid_request")

	// A missing decision is recordable, and never verified.
	undecided := s.record(t, w.verify, 1, map[string]string{"children": childrenJSON("checks", checks)})
	if v, _ := s.verification(t, undecided); v.Verified || len(v.Problems) != 1 || v.Problems[0].Code != "missing_decision" || v.Problems[0].Reference != "integration" {
		t.Fatalf("undecided verification = %+v", v)
	}

	// Target decisions: the documentation run verifies only a target that
	// also decides integration does not apply.
	g := s.graphAt(t, w.verify, `{"integration":false}`)
	if !g.TargetVerification.Verified || g.TargetVerification.LatestExecutionID != docs || g.References[1].Decision != "not_applicable" || g.References[1].Node.TargetVerification != nil {
		t.Fatalf("docs target = %+v", g)
	}
	g = s.graphAt(t, w.verify, `{"integration":true}`)
	if g.TargetVerification.Verified || g.References[1].Decision != "applicable" || g.References[1].Condition == nil || *g.References[1].Condition != integrationCondition {
		t.Fatalf("a target needing integration = %+v", g)
	}
	g = s.graphAt(t, w.verify, "")
	if g.TargetVerification.Verified || !slices.Equal(g.TargetVerification.Undecided, []string{"integration"}) || g.References[1].Decision != "undecided" {
		t.Fatalf("an undecided target = %+v", g.TargetVerification)
	}
	if g.References[0].Decision != "" || g.References[0].Condition != nil {
		t.Fatalf("a required edge has no decision or condition: %+v", g.References[0])
	}
	q := url.Values{"repository": {"github.com/ashuangiras/polaroid"}, "environment": {"ci.ubuntu-latest"}}
	for name, extra := range map[string]url.Values{
		"decisions without commit and inputs": {"decisions": {`{"integration":true}`}},
		"a required path":                     {"commit": {fullCommit}, "inputs": {`{}`}, "decisions": {`{"checks":true}`}},
		"a non-boolean":                       {"commit": {fullCommit}, "inputs": {`{}`}, "decisions": {`{"integration":"yes"}`}},
	} {
		query := url.Values{}
		for k, v := range q {
			query[k] = v
		}
		for k, v := range extra {
			query[k] = v
		}
		resp := s.call(t, http.MethodGet, "/v1/procedures/"+w.verify+"/versions/1/graph?"+query.Encode(), "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if e := errorOf(t, resp); len(e.Fields) == 0 || e.Fields[0].Field != "decisions" {
			t.Errorf("%s: %+v", name, e)
		}
	}

	// The same over a binding's resolution. The latest run in the context is
	// the undecided one, so the root has no selection evidence to skip with.
	b := s.bind(t, "github.com/ashuangiras/polaroid", "verify", w.verify, `{"contextual": {}}`)
	res, _ := s.resolveAt(t, b.ID, targetQuery("ci.ubuntu-latest", fullCommit, `{"module":"modernc.org/sqlite"}`)+"&decisions="+url.QueryEscape(`{"integration":false}`))
	if !res.Graph.TargetVerification.Verified || res.Graph.VerifiedBy != "" || res.Graph.References[1].SkippedBy != "" || res.Graph.References[1].Decision != "not_applicable" {
		t.Fatalf("resolution with decisions = %+v", res.Graph)
	}

	// Everything persists across a restart.
	before := s.call(t, http.MethodGet, "/v1/executions/"+docs, "")
	s.Close()
	_ = s.store.Close()
	s = newTestServerAt(t, path)
	after := s.call(t, http.MethodGet, "/v1/executions/"+docs, "")
	if string(after.body) != string(before.body) {
		t.Fatalf("after restart:\n%s\nwant\n%s", after.body, before.body)
	}
	if g := s.graphAt(t, w.verify, `{"integration":false}`); !g.TargetVerification.Verified {
		t.Fatal("the documentation target is no longer verified after a restart")
	}
}
