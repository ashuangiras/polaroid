package http_test

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
)

// run is an execution request at commit in environment env with inputs.
func run(procedureID string, version int, repository, commit, env, inputs, outcome, children string) string {
	return fmt.Sprintf(`{"procedure_id":%q,"version":%d,"repository":%q,"commit":%q,"environment":{"name":%q,"attributes":{}},`+
		`"inputs":%s,"outcome":%q,"evidence":{"exit":0}%s}`, procedureID, version, repository, commit, env, inputs, outcome, children)
}

// target returns the root node of a version's graph resolved in repository
// and env at commit with inputs.
func (s *testServer) target(t *testing.T, procedureID string, version int, repository, env, commit, inputs string) graphNodeJSON {
	t.Helper()
	q := url.Values{"repository": {repository}, "environment": {env}, "commit": {commit}, "inputs": {inputs}}
	resp := s.call(t, http.MethodGet, fmt.Sprintf("/v1/procedures/%s/versions/%d/graph?%s", procedureID, version, q.Encode()), "")
	expect(t, resp, http.StatusOK, "")
	return decodeStrict[graphNodeJSON](t, resp.body)
}

func (s *testServer) combinations(t *testing.T, procedureID string, version int, repository string) verificationListJSON {
	t.Helper()
	resp := s.call(t, http.MethodGet, fmt.Sprintf("/v1/procedures/%s/versions/%d/verifications?repository=%s", procedureID, version, url.QueryEscape(repository)), "")
	expect(t, resp, http.StatusOK, "")
	return decodeStrict[verificationListJSON](t, resp.body)
}

// TestEvidenceMatchesByRepositoryIdentity covers ADR-0022 end to end: an
// alias registered after its executions joins the canonical identifier's
// combinations, failures decide across identifiers, and nothing else
// (another repository, a similar name, another commit, environment, inputs
// or child version) matches.
func TestEvidenceMatchesByRepositoryIdentity(t *testing.T) {
	s := newTestServer(t)
	const canonical, alias, other, similar = "github.com/o/a", "old.example/o/a", "github.com/o/b", "github.com/o/a-fork"
	commit, later := scopeCommit, "fedcba9876543210fedcba9876543210fedcba98"
	a := s.register(t, canonical)
	s.register(t, other)
	leaf := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("id.leaf", "")))
	parent := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("id.parent",
		`"references":[{"name":"leaf","procedure_id":"`+leaf+`","version_policy":{"contextual":{}},"inputs":{}}],`)))

	// Runs recorded under an identifier nobody has registered yet.
	aliasRun := idOf(t, s.mustPost(t, "/v1/executions", run(leaf, 1, alias, commit, "ci", `{}`, "succeeded", "")))
	if n := s.target(t, leaf, 1, canonical, "ci", commit, `{}`); n.TargetVerification.Verified || len(n.TargetVerification.ExecutionIDs) != 0 {
		t.Fatalf("before the alias, the canonical identifier saw the alias's run: %+v", n.TargetVerification)
	}
	before := s.call(t, http.MethodGet, "/v1/executions/"+aliasRun, "")
	if e := decodeStrict[executionJSON](t, before.body); e.Repository != alias || e.RepositoryID != "" {
		t.Fatalf("unregistered execution: repository %q, repository_id %q", e.Repository, e.RepositoryID)
	}

	// Ownership conflicts are still refused.
	expect(t, s.call(t, http.MethodPost, "/v1/repositories/"+a+"/aliases", `{"identifier":"`+other+`","reason":"r"}`), http.StatusConflict, "repository_identifier_exists")
	s.mustPost(t, "/v1/repositories/"+a+"/aliases", `{"identifier":"`+alias+`","reason":"The repository's old host."}`)

	// The earlier run now verifies the combination under every identifier of
	// A, and reports the identity apart from its own identifier.
	after := decodeStrict[executionJSON](t, s.call(t, http.MethodGet, "/v1/executions/"+aliasRun, "").body)
	if after.Repository != alias || after.RepositoryID != a {
		t.Fatalf("aliased execution: repository %q, repository_id %q", after.Repository, after.RepositoryID)
	}
	for _, identifier := range []string{canonical, alias} {
		tv := s.target(t, leaf, 1, identifier, "ci", commit, `{}`).TargetVerification
		if !tv.Verified || tv.LatestExecutionID != aliasRun || tv.Combination.Repository != canonical || tv.Combination.RepositoryID != a {
			t.Fatalf("target under %s: %+v", identifier, tv)
		}
	}
	list := s.combinations(t, leaf, 1, canonical)
	if len(list.Verifications) != 1 || list.Verifications[0].Combination.Repository != canonical || !slices.Equal(list.Verifications[0].ExecutionIDs, []string{aliasRun}) {
		t.Fatalf("combinations in %s: %+v", canonical, list)
	}

	// Nothing else matches: another repository, a similar unregistered name,
	// another commit, environment or inputs.
	for name, tv := range map[string]targetVerificationJSON{
		"other repository": *s.target(t, leaf, 1, other, "ci", commit, `{}`).TargetVerification,
		"similar name":     *s.target(t, leaf, 1, similar, "ci", commit, `{}`).TargetVerification,
		"other commit":     *s.target(t, leaf, 1, canonical, "ci", later, `{}`).TargetVerification,
		"other env":        *s.target(t, leaf, 1, canonical, "ci.other", commit, `{}`).TargetVerification,
		"other inputs":     *s.target(t, leaf, 1, canonical, "ci", commit, `{"x":1}`).TargetVerification,
	} {
		if tv.Verified || len(tv.ExecutionIDs) != 0 {
			t.Errorf("%s: %+v", name, tv)
		}
	}
	if n := len(s.combinations(t, leaf, 1, other).Verifications); n != 0 {
		t.Fatalf("%s lists %d combinations of A's runs", other, n)
	}

	// Selection evidence names the run's own identifier and the identity.
	g := s.target(t, parent, 1, canonical, "ci", commit, `{}`)
	if e := g.References[0].Node; e.SelectionEvidence == nil || e.SelectionEvidence.Repository != alias || e.SelectionEvidence.RepositoryID != a {
		t.Fatalf("selection evidence of the leaf: %+v", e.SelectionEvidence)
	}

	// Child versions still split combinations: a parent under the alias with
	// leaf 1, and one under the canonical identifier with leaf 2.
	s.mustPost(t, "/v1/executions", run(parent, 1, alias, commit, "ci", `{}`, "succeeded", `,"children":[{"reference":"leaf","execution_id":"`+aliasRun+`"}]`))
	s.mustPost(t, "/v1/procedures/"+leaf+"/versions", reviseWith(1, ""))
	leaf2 := idOf(t, s.mustPost(t, "/v1/executions", run(leaf, 2, canonical, commit, "ci", `{}`, "succeeded", "")))
	s.mustPost(t, "/v1/executions", run(parent, 1, canonical, commit, "ci", `{}`, "succeeded", `,"children":[{"reference":"leaf","execution_id":"`+leaf2+`"}]`))
	parents := s.combinations(t, parent, 1, alias)
	if len(parents.Verifications) != 2 || !parents.Verifications[0].Verified || !parents.Verifications[1].Verified ||
		parents.Verifications[0].Combination.Children[0].Version != 1 || parents.Verifications[1].Combination.Children[0].Version != 2 {
		t.Fatalf("parent combinations by child version: %+v", parents)
	}

	// A later failure under the canonical identifier decides the combination
	// the alias's run verified; the alias's run itself is unchanged.
	failed := idOf(t, s.mustPost(t, "/v1/executions", run(leaf, 1, canonical, commit, "ci", `{}`, "failed", "")))
	tv := s.target(t, leaf, 1, alias, "ci", commit, `{}`).TargetVerification
	if tv.Verified || tv.LatestExecutionID != failed || !slices.Equal(tv.ExecutionIDs, []string{aliasRun, failed}) {
		t.Fatalf("after a failure under the canonical identifier, under the alias: %+v", tv)
	}
	if e := decodeStrict[executionJSON](t, s.call(t, http.MethodGet, "/v1/executions/"+aliasRun, "").body); e.Outcome != "succeeded" || e.Repository != alias {
		t.Fatalf("the alias's run changed: %+v", e)
	}
	var v verificationJSON
	if err := json.Unmarshal(s.call(t, http.MethodGet, "/v1/executions/"+aliasRun+"/verification", "").body, &v); err != nil || !v.Verified {
		t.Fatalf("the alias's run on its own is still verified: %+v %v", v, err)
	}
}
