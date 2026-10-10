package http_test

import (
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// The structs below restate the documented verification shapes.

type childVersionJSON struct {
	Reference string             `json:"reference"`
	Version   int                `json:"version"`
	Skipped   bool               `json:"skipped"`
	Children  []childVersionJSON `json:"children"`
}

type combinationJSON struct {
	Repository   string `json:"repository"`
	RepositoryID string `json:"repository_id"`
	Commit       string `json:"commit"`
	Environment  struct {
		Name string `json:"name"`
	} `json:"environment"`
	Inputs   jsontext.Value     `json:"inputs"`
	Children []childVersionJSON `json:"children"`
}

type verificationJSON struct {
	ExecutionID string `json:"execution_id"`
	ProcedureID string `json:"procedure_id"`
	Version     int    `json:"version"`
	Verified    bool   `json:"verified"`
	Problems    []struct {
		Code        string `json:"code"`
		Reference   string `json:"reference"`
		ExecutionID string `json:"execution_id"`
	} `json:"problems"`
	Combination combinationJSON `json:"combination"`
}

type verificationListJSON struct {
	Verifications []struct {
		Combination       combinationJSON `json:"combination"`
		Verified          bool            `json:"verified"`
		LatestExecutionID string          `json:"latest_execution_id"`
		ExecutionIDs      []string        `json:"execution_ids"`
	} `json:"verifications"`
}

func (s *testServer) verification(t *testing.T, executionID string) (verificationJSON, string) {
	t.Helper()
	resp := s.call(t, http.MethodGet, "/v1/executions/"+executionID+"/verification", "")
	expect(t, resp, http.StatusOK, "")
	return decodeStrict[verificationJSON](t, resp.body), string(resp.body)
}

func (s *testServer) verifications(t *testing.T, procedureID string, version int, query string) verificationListJSON {
	t.Helper()
	resp := s.call(t, http.MethodGet, fmt.Sprintf("/v1/procedures/%s/versions/%d/verifications%s", procedureID, version, query), "")
	expect(t, resp, http.StatusOK, "")
	return decodeStrict[verificationListJSON](t, resp.body)
}

func problemsOf(v verificationJSON) []string {
	var out []string
	for _, p := range v.Problems {
		out = append(out, strings.TrimSpace(strings.Join([]string{p.Code, p.Reference, p.ExecutionID}, " ")))
	}
	return out
}

func TestExecutionVerification(t *testing.T) {
	s := newTestServer(t)
	f := newComposedFixture(t, s)
	failedRun := s.record(t, f.child, 1, map[string]string{"outcome": `"failed"`})
	nested := s.createWith(t, "compose.nested", "["+latestRef("inner", f.parent)+"]")

	complete := s.record(t, f.parent, 1, map[string]string{"children": childrenJSON("latest", f.childV1Run, "pinned", f.childV2Run)})
	v, raw := s.verification(t, complete)
	if !v.Verified || strings.Contains(raw, "problems") {
		t.Fatalf("a parent with verified children for every reference must be verified: %s", raw)
	}
	want := fmt.Sprintf(`"combination":{"repository":"github.com/ashuangiras/polaroid","commit":%q,"environment":{"name":"ci.ubuntu-latest"},`+
		`"inputs":{"module":"modernc.org/sqlite"},"children":[{"reference":"pinned","version":2},{"reference":"latest","version":1}]}`, fullCommit)
	if !strings.Contains(raw, want) || v.ExecutionID != complete || v.ProcedureID != f.parent || v.Version != 1 {
		t.Fatalf("verification = %s\nwant it to contain %s", raw, want)
	}

	leaf, raw := s.verification(t, f.childV1Run)
	if !leaf.Verified || strings.Contains(raw, `"children"`) {
		t.Fatalf("a succeeded leaf must be verified, without children: %s", raw)
	}

	failed, _ := s.verification(t, failedRun)
	if failed.Verified || !slices.Equal(problemsOf(failed), []string{"outcome_failed"}) {
		t.Fatalf("failed leaf: %+v", failed)
	}

	partial := s.record(t, f.parent, 1, map[string]string{
		"outcome":  `"failed"`,
		"children": childrenJSON("latest", failedRun),
	})
	got, _ := s.verification(t, partial)
	if want := []string{"outcome_failed", "missing_child pinned", "child_not_verified latest " + failedRun}; got.Verified || !slices.Equal(problemsOf(got), want) {
		t.Fatalf("problems = %q, want %q", problemsOf(got), want)
	}

	// Two levels: the nested parent's child is verified only if its own children are.
	okParent := s.record(t, f.parent, 1, map[string]string{"children": childrenJSON("pinned", s.record(t, f.child, 2, nil), "latest", s.record(t, f.child, 1, nil))})
	top, raw := s.verification(t, s.record(t, nested, 1, map[string]string{"children": childrenJSON("inner", okParent)}))
	if !top.Verified || !strings.Contains(raw, `"children":[{"reference":"inner","version":1,"children":[{"reference":"pinned","version":2},{"reference":"latest","version":1}]}]`) {
		t.Fatalf("nested verified tree: %s", raw)
	}
	badParent := s.record(t, f.parent, 1, map[string]string{"children": childrenJSON("pinned", s.record(t, f.child, 2, nil), "latest", s.record(t, f.child, 1, map[string]string{"outcome": `"failed"`}))})
	bad, _ := s.verification(t, s.record(t, nested, 1, map[string]string{"children": childrenJSON("inner", badParent)}))
	if want := []string{"child_not_verified inner " + badParent}; bad.Verified || !slices.Equal(problemsOf(bad), want) {
		t.Fatalf("nested failure: problems = %q, want %q", problemsOf(bad), want)
	}

	expect(t, s.call(t, http.MethodGet, "/v1/executions/0192f7e4-0000-7000-8000-000000000000/verification", ""), http.StatusNotFound, "not_found")
}

func TestVerificationCombinations(t *testing.T) {
	s := newTestServer(t)
	f := newComposedFixture(t, s)
	statuses := func(procedure string, version int, query string) []string {
		t.Helper()
		var out []string
		for _, v := range s.verifications(t, procedure, version, query).Verifications {
			if v.LatestExecutionID != v.ExecutionIDs[len(v.ExecutionIDs)-1] {
				t.Fatalf("latest_execution_id %s is not the last of %v", v.LatestExecutionID, v.ExecutionIDs)
			}
			out = append(out, fmt.Sprintf("%s %s %d %v", v.Combination.Environment.Name, v.Combination.Inputs, len(v.ExecutionIDs), v.Verified))
		}
		return out
	}

	// Member order, whitespace, number spelling and environment attributes do not split a combination.
	s.record(t, f.child, 1, map[string]string{
		"inputs":      `{ "b" : 1.0, "a" : [ 1e2 ] }`,
		"environment": `{"name": "ci.ubuntu-latest", "attributes": {"os": "linux"}}`,
	})
	s.record(t, f.child, 1, map[string]string{
		"inputs":      `{"a":[100],"b":1}`,
		"environment": `{"name": "ci.ubuntu-latest", "attributes": {"os": "linux", "kernel": "6.8"}}`,
	})
	if got, want := statuses(f.child, 1, ""), []string{
		`ci.ubuntu-latest {"module":"modernc.org/sqlite"} 1 true`,
		`ci.ubuntu-latest {"a":[100],"b":1} 2 true`,
	}; !slices.Equal(got, want) {
		t.Fatalf("child@1 combinations:\n%q\nwant\n%q", got, want)
	}

	// The latest execution decides; every execution stays in the history.
	s.record(t, f.child, 2, map[string]string{"outcome": `"failed"`})
	if got := statuses(f.child, 2, ""); !slices.Equal(got, []string{`ci.ubuntu-latest {"module":"modernc.org/sqlite"} 2 false`}) {
		t.Fatalf("after a failure: %q", got)
	}
	s.record(t, f.child, 2, nil)
	if got := statuses(f.child, 2, ""); !slices.Equal(got, []string{`ci.ubuntu-latest {"module":"modernc.org/sqlite"} 3 true`}) {
		t.Fatalf("after a new success: %q", got)
	}

	// Parents that differ only in a child's version are separate combinations.
	first := s.record(t, f.parent, 1, map[string]string{"children": childrenJSON("pinned", f.childV2Run, "latest", f.childV1Run)})
	second := s.record(t, f.parent, 1, map[string]string{"children": childrenJSON("pinned", s.record(t, f.child, 2, nil), "latest", s.record(t, f.child, 2, nil))})
	list := s.verifications(t, f.parent, 1, "").Verifications
	if len(list) != 2 || !slices.Equal(list[0].ExecutionIDs, []string{first}) || !slices.Equal(list[1].ExecutionIDs, []string{second}) {
		t.Fatalf("parent combinations: %+v", list)
	}
	if list[0].Combination.Children[1].Version != 1 || list[1].Combination.Children[1].Version != 2 || !list[0].Verified || !list[1].Verified {
		t.Fatalf("child-version trees: %+v", list)
	}

	// Filters.
	s.record(t, f.child, 1, map[string]string{"environment": `{"name": "ci.macos", "attributes": {}}`, "commit": `"` + strings.Repeat("b", 64) + `"`, "repository": `"scratch"`})
	for query, want := range map[string]int{
		"?environment=ci.macos":                            1,
		"?commit=" + strings.Repeat("b", 64):               1,
		"?commit=" + fullCommit:                            2,
		"?repository=scratch":                              1,
		"?repository=scratch&environment=ci.ubuntu-latest": 0,
		"?repository=github.com%2Fnobody%2Fnowhere":        0,
	} {
		if got := len(statuses(f.child, 1, query)); got != want {
			t.Errorf("query %s: %d combinations, want %d", query, got, want)
		}
	}
}

func TestVerificationRequestsAreChecked(t *testing.T) {
	s := newTestServer(t)
	f := newComposedFixture(t, s)
	base := "/v1/procedures/" + f.child + "/versions/1/verifications"
	for query, field := range map[string]string{
		"?repository=Not/Canonical":    "repository",
		"?commit=0123456":              "commit",
		"?environment=CI%20Linux":      "environment",
		"?environment=a&environment=b": "environment",
	} {
		resp := s.call(t, http.MethodGet, base+query, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if e := errorOf(t, resp); len(e.Fields) != 1 || e.Fields[0].Field != field {
			t.Errorf("query %s: fields = %+v, want %s", query, e.Fields, field)
		}
	}
	expect(t, s.call(t, http.MethodGet, base+"?outcome=failed", ""), http.StatusBadRequest, "invalid_request")
	expect(t, s.call(t, http.MethodGet, "/v1/procedures/"+f.child+"/versions/latest/verifications", ""), http.StatusBadRequest, "invalid_request")
	expect(t, s.call(t, http.MethodGet, "/v1/procedures/"+f.child+"/versions/0/verifications", ""), http.StatusBadRequest, "invalid_request")
	expect(t, s.call(t, http.MethodGet, "/v1/procedures/"+f.child+"/versions/3/verifications", ""), http.StatusNotFound, "not_found")
	expect(t, s.call(t, http.MethodGet, "/v1/procedures/0192f7e4-0000-7000-8000-000000000000/versions/1/verifications", ""), http.StatusNotFound, "not_found")

	// Both endpoints are read-only and change nothing.
	executions := func() string {
		return string(s.call(t, http.MethodGet, "/v1/executions?procedure_id="+url.QueryEscape(f.child), "").body)
	}
	before := executions()
	s.verifications(t, f.child, 2, "")
	s.verification(t, f.childV2Run)
	for _, path := range []string{base, "/v1/executions/" + f.childV2Run + "/verification"} {
		resp := s.call(t, http.MethodPost, path, "{}")
		expect(t, resp, http.StatusMethodNotAllowed, "method_not_allowed")
		if allow := resp.header.Get("Allow"); allow != "GET, HEAD" {
			t.Errorf("%s: Allow = %q", path, allow)
		}
	}
	if after := executions(); after != before {
		t.Fatalf("executions changed:\n%s\n%s", before, after)
	}
}
