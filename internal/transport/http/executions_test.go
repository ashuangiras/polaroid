package http_test

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

const fullCommit = "0123456789abcdef0123456789abcdef01234567"

// executionJSON restates the documented execution shape.
type executionJSON struct {
	ID              string `json:"id"`
	ProcedureID     string `json:"procedure_id"`
	Version         int    `json:"version"`
	BindingID       string `json:"binding_id"`
	BindingRevision int    `json:"binding_revision"`
	Repository      string `json:"repository"`
	Commit          string `json:"commit"`
	Environment     struct {
		Name       string         `json:"name"`
		Attributes jsontext.Value `json:"attributes"`
	} `json:"environment"`
	Inputs   jsontext.Value `json:"inputs"`
	Outcome  string         `json:"outcome"`
	Evidence jsontext.Value `json:"evidence"`
	Children []struct {
		Reference   string `json:"reference"`
		ExecutionID string `json:"execution_id"`
	} `json:"children"`
	CreatedAt time.Time `json:"created_at"`
}

// executionRequest returns a record-execution body. fields overrides or adds
// raw JSON members, and a "-" value removes one.
func executionRequest(procedureID string, version int, fields map[string]string) string {
	members := map[string]string{
		"procedure_id": fmt.Sprintf("%q", procedureID),
		"version":      fmt.Sprint(version),
		"repository":   `"github.com/ashuangiras/polaroid"`,
		"commit":       `"` + fullCommit + `"`,
		"environment":  `{"name": "ci.ubuntu-latest", "attributes": {"os": "linux", "go": "1.27.2"}}`,
		"inputs":       `{"module": "modernc.org/sqlite"}`,
		"outcome":      `"succeeded"`,
		"evidence":     `{"commands": [{"run": "make ci", "exit": 0}], "log": "https://example.com/log#sha256=ab"}`,
	}
	for k, v := range fields {
		members[k] = v
	}
	var parts []string
	for _, k := range []string{"procedure_id", "version", "binding_id", "binding_revision", "repository", "commit", "environment", "inputs", "outcome", "evidence", "children"} {
		if v, ok := members[k]; ok && v != "-" {
			parts = append(parts, fmt.Sprintf("%q: %s", k, v))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// executionFixture is a procedure at version 2 bound in repoA (pinned to 2)
// and repoB (contextual).
type executionFixture struct {
	procedure, pinned, contextual string
}

func newExecutionFixture(t *testing.T, s *testServer) executionFixture {
	t.Helper()
	p := s.create(t).ID
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+p+"/versions", reviseBody(1, "v2")), http.StatusCreated, "")
	return executionFixture{
		procedure:  p,
		pinned:     s.bind(t, repoA, "add-dependency", p, `{"pin": 2}`).ID,
		contextual: s.bind(t, repoB, "add-dependency", p, `{"contextual": {}}`).ID,
	}
}

func TestRecordAndReadAnExecution(t *testing.T) {
	s := newTestServer(t)
	f := newExecutionFixture(t, s)
	procedureBefore := s.call(t, http.MethodGet, "/v1/procedures/"+f.procedure, "").body
	bindingBefore := s.call(t, http.MethodGet, "/v1/bindings/"+f.pinned, "").body

	created := s.call(t, http.MethodPost, "/v1/executions", executionRequest(f.procedure, 2,
		map[string]string{"binding_id": fmt.Sprintf("%q", f.pinned), "binding_revision": "1"}))
	expect(t, created, http.StatusCreated, "")
	e := decodeStrict[executionJSON](t, created.body)
	if loc := created.header.Get("Location"); loc != "/v1/executions/"+e.ID {
		t.Fatalf("Location = %q", loc)
	}
	want := fmt.Sprintf(`{"id":%q,"procedure_id":%q,"version":2,"binding_id":%q,"binding_revision":1,"repository":"github.com/ashuangiras/polaroid",`+
		`"commit":%q,"environment":{"name":"ci.ubuntu-latest","attributes":{"os":"linux","go":"1.27.2"}},"inputs":{"module":"modernc.org/sqlite"},`+
		`"outcome":"succeeded","evidence":{"commands":[{"run":"make ci","exit":0}],"log":"https://example.com/log#sha256=ab"},"created_at":%q}`,
		e.ID, f.procedure, f.pinned, fullCommit, e.CreatedAt.Format(time.RFC3339Nano))
	if got := strings.TrimSpace(string(created.body)); got != want {
		t.Fatalf("execution =\n %s\nwant\n %s", got, want)
	}

	got := s.call(t, http.MethodGet, "/v1/executions/"+e.ID, "")
	expect(t, got, http.StatusOK, "")
	if !bytes.Equal(got.body, created.body) {
		t.Fatalf("GET differs from the recorded execution:\n%s\n%s", got.body, created.body)
	}

	// Without a binding, the binding fields are absent.
	plain := s.call(t, http.MethodPost, "/v1/executions", executionRequest(f.procedure, 1, map[string]string{"outcome": `"failed"`}))
	expect(t, plain, http.StatusCreated, "")
	if strings.Contains(string(plain.body), "binding") {
		t.Fatalf("an execution without a binding names one: %s", plain.body)
	}

	list := s.call(t, http.MethodGet, "/v1/executions?procedure_id="+url.QueryEscape(f.procedure), "")
	expect(t, list, http.StatusOK, "")
	items := decodeStrict[struct {
		Executions []executionJSON `json:"executions"`
	}](t, list.body).Executions
	if len(items) != 2 || items[0].ID != e.ID || items[0].Inputs != nil || items[0].Evidence != nil || items[1].Outcome != "failed" {
		t.Fatalf("list = %s", list.body)
	}

	if after := s.call(t, http.MethodGet, "/v1/procedures/"+f.procedure, "").body; !bytes.Equal(after, procedureBefore) {
		t.Fatal("recording an execution changed the procedure")
	}
	if after := s.call(t, http.MethodGet, "/v1/bindings/"+f.pinned, "").body; !bytes.Equal(after, bindingBefore) {
		t.Fatal("recording an execution changed the binding")
	}
}

func TestExecutionTargetsAreChecked(t *testing.T) {
	s := newTestServer(t)
	f := newExecutionFixture(t, s)
	other := s.createWith(t, "other.procedure", "[]")
	otherBinding := s.bind(t, repoA, "other", other, `{"pin": 1}`).ID
	binding := func(id string, revision int) map[string]string {
		return map[string]string{"binding_id": fmt.Sprintf("%q", id), "binding_revision": fmt.Sprint(revision)}
	}
	with := func(m map[string]string, k, v string) map[string]string {
		m[k] = v
		return m
	}

	cases := []struct {
		name       string
		procedure  string
		version    int
		fields     map[string]string
		status     int
		code       string
		fieldNames []string
	}{
		{"unknown procedure", "0192f7e4-0000-7000-8000-000000000000", 1, nil, 404, "not_found", nil},
		{"unknown binding", f.procedure, 2, binding("0192f7e4-0000-7000-8000-000000000000", 1), 404, "not_found", nil},
		{"missing version", f.procedure, 3, nil, 400, "invalid_request", []string{"version"}},
		{"missing binding revision", f.procedure, 2, binding(f.pinned, 2), 400, "invalid_request", []string{"binding_revision"}},
		{"binding in another repository", f.procedure, 2, binding(f.contextual, 1), 400, "invalid_request", []string{"binding_id"}},
		{"binding of another procedure", f.procedure, 2, binding(otherBinding, 1), 400, "invalid_request", []string{"binding_id"}},
		{"version other than the pin", f.procedure, 1, binding(f.pinned, 1), 400, "invalid_request", []string{"version"}},
		{"binding without revision", f.procedure, 2, map[string]string{"binding_id": fmt.Sprintf("%q", f.pinned)}, 400, "invalid_request", []string{"binding_revision"}},
		{"contextual binding, any version", f.procedure, 1, with(binding(f.contextual, 1), "repository", `"`+repoB+`"`), 201, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.call(t, http.MethodPost, "/v1/executions", executionRequest(tc.procedure, tc.version, tc.fields))
			expect(t, resp, tc.status, tc.code)
			if tc.fieldNames == nil {
				return
			}
			var got []string
			for _, p := range errorOf(t, resp).Fields {
				got = append(got, p.Field)
			}
			if !slices.Equal(got, tc.fieldNames) {
				t.Fatalf("fields = %v, want %v; body: %s", got, tc.fieldNames, resp.body)
			}
		})
	}
}

func TestInvalidExecutionsAreRejected(t *testing.T) {
	s := newTestServer(t)
	f := newExecutionFixture(t, s)
	cases := map[string]struct {
		fields map[string]string
		field  string
	}{
		"abbreviated commit":       {map[string]string{"commit": `"0123456"`}, "commit"},
		"uppercase commit":         {map[string]string{"commit": `"` + strings.ToUpper(fullCommit) + `"`}, "commit"},
		"non-canonical repository": {map[string]string{"repository": `"https://github.com/Org/Repo.git"`}, "repository"},
		"invalid environment name": {map[string]string{"environment": `{"name": "CI Box", "attributes": {}}`}, "environment.name"},
		"attributes not an object": {map[string]string{"environment": `{"name": "ci", "attributes": "linux"}`}, "environment.attributes"},
		"unknown outcome":          {map[string]string{"outcome": `"passed"`}, "outcome"},
		"inputs not an object":     {map[string]string{"inputs": `[]`}, "inputs"},
		"evidence not an object":   {map[string]string{"evidence": `"it worked"`}, "evidence"},
		"empty evidence":           {map[string]string{"evidence": `{}`}, "evidence"},
		"missing evidence":         {map[string]string{"evidence": "-"}, "evidence"},
		"server-assigned id":       {map[string]string{"id": `"mine"`}, ""},
		"unknown member":           {map[string]string{"status": `"ok"`}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := executionRequest(f.procedure, 2, tc.fields)
			if tc.field == "" {
				for k, v := range tc.fields {
					body = strings.TrimSuffix(body, "}") + fmt.Sprintf(", %q: %s}", k, v)
				}
			}
			resp := s.call(t, http.MethodPost, "/v1/executions", body)
			expect(t, resp, http.StatusBadRequest, "invalid_request")
			if tc.field == "" {
				return
			}
			if e := errorOf(t, resp); len(e.Fields) != 1 || e.Fields[0].Field != tc.field {
				t.Fatalf("fields = %+v, want [%s]", e.Fields, tc.field)
			}
		})
	}
	list := s.call(t, http.MethodGet, "/v1/executions?procedure_id="+url.QueryEscape(f.procedure), "")
	if string(bytes.TrimSpace(list.body)) != `{"executions":[]}` {
		t.Fatalf("rejected requests recorded executions: %s", list.body)
	}
}

func TestListExecutionsQuery(t *testing.T) {
	s := newTestServer(t)
	f := newExecutionFixture(t, s)
	for _, req := range []string{
		executionRequest(f.procedure, 1, nil),
		executionRequest(f.procedure, 2, nil),
		executionRequest(f.procedure, 2, map[string]string{"repository": `"scratch"`}),
	} {
		expect(t, s.call(t, http.MethodPost, "/v1/executions", req), http.StatusCreated, "")
	}
	ids := func(query string) string {
		resp := s.call(t, http.MethodGet, "/v1/executions?procedure_id="+url.QueryEscape(f.procedure)+query, "")
		expect(t, resp, http.StatusOK, "")
		var got []string
		for _, e := range decodeStrict[struct {
			Executions []executionJSON `json:"executions"`
		}](t, resp.body).Executions {
			got = append(got, fmt.Sprintf("v%d@%s", e.Version, e.Repository))
		}
		return strings.Join(got, ",")
	}
	for query, want := range map[string]string{
		"":                                      "v1@github.com/ashuangiras/polaroid,v2@github.com/ashuangiras/polaroid,v2@scratch",
		"&version=2":                            "v2@github.com/ashuangiras/polaroid,v2@scratch",
		"&repository=scratch":                   "v2@scratch",
		"&version=1&repository=scratch":         "",
		"&repository=github.com/nobody/nowhere": "",
	} {
		if got := ids(query); got != want {
			t.Errorf("query %q: %s, want %s", query, got, want)
		}
	}

	for _, query := range []string{
		"?procedure_id=",
		"?version=1",
		"?commit=0123abc",
		"?procedure_id=" + f.procedure + "&procedure_id=x",
		"?procedure_id=" + f.procedure + "&outcome=failed",
		"?procedure_id=" + f.procedure + "&version=latest",
		"?procedure_id=" + f.procedure + "&version=0",
		"?procedure_id=" + f.procedure + "&repository=Not/Canonical",
	} {
		expect(t, s.call(t, http.MethodGet, "/v1/executions"+query, ""), http.StatusBadRequest, "invalid_request")
	}
	expect(t, s.call(t, http.MethodGet, "/v1/executions/0192f7e4-0000-7000-8000-000000000000", ""), http.StatusNotFound, "not_found")
	expect(t, s.call(t, http.MethodDelete, "/v1/executions/x", ""), http.StatusMethodNotAllowed, "method_not_allowed")
}
