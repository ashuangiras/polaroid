package http_test

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// idOf returns the "id" member of a JSON response body.
func idOf(t *testing.T, resp response) string {
	t.Helper()
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.body, &v); err != nil || v.ID == "" {
		t.Fatalf("no id in %s (%v)", resp.body, err)
	}
	return v.ID
}

// fieldsOf returns the field names of an invalid_request error.
func fieldsOf(t *testing.T, resp response) []string {
	t.Helper()
	var out []string
	for _, f := range errorOf(t, resp).Fields {
		out = append(out, f.Field)
	}
	return out
}

func (s *testServer) mustPost(t *testing.T, path, body string) response {
	t.Helper()
	resp := s.call(t, http.MethodPost, path, body)
	expect(t, resp, http.StatusCreated, "")
	return resp
}

func (s *testServer) refuse(t *testing.T, path, body string, fields ...string) {
	t.Helper()
	resp := s.call(t, http.MethodPost, path, body)
	expect(t, resp, http.StatusBadRequest, "invalid_request")
	if got := fieldsOf(t, resp); !slices.Equal(got, fields) {
		t.Fatalf("POST %s %s: fields %v, want %v; body %s", path, body, got, fields, resp.body)
	}
}

func (s *testServer) register(t *testing.T, identifier string) string {
	t.Helper()
	return idOf(t, s.mustPost(t, "/v1/repositories", `{"identifier":"`+identifier+`","name":"Repository `+identifier+`"}`))
}

// procedureBody is a create request; extra is spliced into the version.
func procedureBody(key, extra string) string {
	return `{"canonical_key":"` + key + `","version":{"philosophy":"p","method":"m",` + extra +
		`"contract":{},"instructions":{"steps":["s"]},"revision_reason":"r"}}`
}

func reviseWith(base int, extra string) string {
	return fmt.Sprintf(`{"base_version":%d,"version":{"philosophy":"p","method":"m",%s"contract":{},"instructions":{"steps":["s"]},"revision_reason":"r"}}`, base, extra)
}

func bindRequest(repository, name, procedureID, policy string) string {
	return `{"repository":"` + repository + `","name":"` + name + `","procedure_id":"` + procedureID +
		`","revision":{"inputs":{"cmd":"make ` + name + `"},"version_policy":` + policy + `,"revision_reason":"r"}}`
}

const scopeCommit = "0123456789abcdef0123456789abcdef01234567"

func runBody(procedureID string, version int, repository, children string) string {
	return fmt.Sprintf(`{"procedure_id":%q,"version":%d,"repository":%q,"commit":%q,"environment":{"name":"ci","attributes":{}},`+
		`"inputs":{},"outcome":"succeeded","evidence":{"exit":0}%s}`, procedureID, version, repository, scopeCommit, children)
}

func TestRepositoryRegistry(t *testing.T) {
	s := newTestServer(t)
	created := s.mustPost(t, "/v1/repositories", `{"identifier":"github.com/o/a","name":"A"}`)
	id := idOf(t, created)
	if got := created.header.Get("Location"); got != "/v1/repositories/"+id {
		t.Fatalf("Location = %q", got)
	}
	expect(t, s.call(t, http.MethodPost, "/v1/repositories", `{"identifier":"github.com/o/a","name":"Again"}`), http.StatusConflict, "repository_identifier_exists")
	aliased := s.mustPost(t, "/v1/repositories/"+id+"/aliases", `{"identifier":"mirror.example/o/a","reason":"The mirror of A."}`)
	for _, path := range []string{"/v1/repositories/" + id, "/v1/repositories/by-identifier/github.com/o/a", "/v1/repositories/by-identifier/mirror.example/o/a"} {
		got := s.call(t, http.MethodGet, path, "")
		expect(t, got, http.StatusOK, "")
		if string(got.body) != string(aliased.body) {
			t.Fatalf("GET %s = %s, want %s", path, got.body, aliased.body)
		}
	}
	expect(t, s.call(t, http.MethodPost, "/v1/repositories/"+id+"/aliases", `{"identifier":"github.com/o/a","reason":"r"}`), http.StatusConflict, "repository_identifier_exists")
	other := s.register(t, "github.com/o/fork-of-a")
	if other == id {
		t.Fatal("a similar identifier was merged into the existing repository")
	}
	expect(t, s.call(t, http.MethodPost, "/v1/repositories", `{"identifier":"mirror.example/o/a","name":"M"}`), http.StatusConflict, "repository_identifier_exists")

	s.refuse(t, "/v1/repositories", `{"identifier":"https://GitHub.com/o/a.git","name":" "}`, "identifier", "name")
	s.refuse(t, "/v1/repositories", `{"identifier":"github.com/o/x","name":"two\nlines"}`, "name")
	s.refuse(t, "/v1/repositories/"+id+"/aliases", `{"identifier":"x/y","reason":""}`, "reason")
	expect(t, s.call(t, http.MethodPost, "/v1/repositories/missing/aliases", `{"identifier":"x/y","reason":"r"}`), http.StatusNotFound, "not_found")
	expect(t, s.call(t, http.MethodGet, "/v1/repositories/by-identifier/github.com/o/none", ""), http.StatusNotFound, "not_found")
	expect(t, s.call(t, http.MethodGet, "/v1/repositories/missing", ""), http.StatusNotFound, "not_found")

	first := s.call(t, http.MethodGet, "/v1/repositories?limit=1", "")
	expect(t, first, http.StatusOK, "")
	var page struct {
		Repositories []struct {
			ID string `json:"id"`
		} `json:"repositories"`
		Next string `json:"next"`
	}
	if err := json.Unmarshal(first.body, &page); err != nil || len(page.Repositories) != 1 || page.Repositories[0].ID != id || page.Next == "" {
		t.Fatalf("first page = %s", first.body)
	}
	rest := s.call(t, http.MethodGet, "/v1/repositories?limit=5&after="+url.QueryEscape(page.Next), "")
	if !strings.Contains(string(rest.body), other) || strings.Contains(string(rest.body), `"next"`) {
		t.Fatalf("second page = %s", rest.body)
	}
}

// TestApplicability follows ADR-0020 through the API: local and shared
// procedures, composition, bindings, executions, promotion and narrowing.
func TestApplicability(t *testing.T) {
	s := newTestServer(t)
	a, b := s.register(t, "github.com/o/a"), s.register(t, "github.com/o/b")
	s.mustPost(t, "/v1/repositories/"+a+"/aliases", `{"identifier":"mirror.example/o/a","reason":"A's mirror."}`)
	shared := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("go.build", `"goal":"Build a Go module.","applicability":{"shared":{}},`)))
	ref := func(target, policy string) string {
		return `"references":[{"name":"build","procedure_id":"` + target + `","version_policy":` + policy + `,"inputs":{}}],`
	}
	localAt := func(repo string) string { return `"applicability":{"repository":"` + repo + `"},` }
	local := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("a.release", `"goal":"Release A.",`+localAt(a)+ref(shared, `{"contextual":{}}`))))

	// Declarations are checked, and a non-local parent cannot compose a local child.
	s.refuse(t, "/v1/procedures", procedureBody("x.one", localAt("missing")), "version.applicability.repository")
	s.refuse(t, "/v1/procedures", procedureBody("x.two", `"applicability":{"shared":{},"repository":"`+a+`"},`), "version.applicability")
	s.refuse(t, "/v1/procedures", procedureBody("x.three", `"applicability":{"shared":{}},`+ref(local, `{"pin":1}`)), "version.references[0].procedure_id")
	s.refuse(t, "/v1/procedures", procedureBody("x.four", ref(local, `{"contextual":{}}`)), "version.references[0].procedure_id")
	s.refuse(t, "/v1/procedures", procedureBody("x.five", localAt(b)+ref(local, `{"contextual":{}}`)), "version.references[0].procedure_id")

	// Bindings follow applicability; the alias is the same repository.
	s.mustPost(t, "/v1/bindings", bindRequest("github.com/o/a", "build", shared, `{"contextual":{}}`))
	bBuild := idOf(t, s.mustPost(t, "/v1/bindings", bindRequest("github.com/o/b", "build", shared, `{"contextual":{}}`)))
	s.mustPost(t, "/v1/bindings", bindRequest("mirror.example/o/a", "release", local, `{"pin":1}`))
	s.refuse(t, "/v1/bindings", bindRequest("github.com/o/b", "release", local, `{"contextual":{}}`), "procedure_id")
	s.refuse(t, "/v1/bindings", bindRequest("github.com/o/b", "release", local, `{"pin":1}`), "revision.version_policy.pin")
	s.refuse(t, "/v1/bindings", bindRequest("github.com/o/unregistered", "release", local, `{"contextual":{}}`), "procedure_id")
	expect(t, s.call(t, http.MethodPost, "/v1/bindings", bindRequest("github.com/o/a", "release", shared, `{"contextual":{}}`)), http.StatusConflict, "binding_exists")

	// Executions: the local version does not apply in B, and A's run verifies nothing in B.
	s.refuse(t, "/v1/executions", runBody(local, 1, "github.com/o/b", ""), "version")
	child := idOf(t, s.mustPost(t, "/v1/executions", runBody(shared, 1, "github.com/o/a", "")))
	parent := idOf(t, s.mustPost(t, "/v1/executions", runBody(local, 1, "github.com/o/a", `,"children":[{"reference":"build","execution_id":"`+child+`"}]`)))
	if v := s.call(t, http.MethodGet, "/v1/executions/"+parent+"/verification", ""); !strings.Contains(string(v.body), `"verified":true`) {
		t.Fatalf("A's run: %s", v.body)
	}
	resolution := func(bindingID string) string {
		resp := s.call(t, http.MethodGet, "/v1/bindings/"+bindingID+"/resolution?environment=ci", "")
		expect(t, resp, http.StatusOK, "")
		return string(resp.body)
	}
	if got := resolution(bBuild); strings.Contains(got, `"verified_by"`) || !strings.Contains(got, `"selected_by":"latest"`) {
		t.Fatalf("B's resolution used A's evidence: %s", got)
	}
	graph := s.call(t, http.MethodGet, "/v1/procedures/"+local+"/versions/1/graph?repository=github.com/o/b&environment=ci", "")
	expect(t, graph, http.StatusBadRequest, "invalid_request")
	if got := fieldsOf(t, graph); !slices.Equal(got, []string{"repository"}) {
		t.Fatalf("graph of a local version in B: fields %v", got)
	}

	// Promotion is a new version; B may now bind the procedure, and only the shared version applies there.
	s.mustPost(t, "/v1/procedures/"+local+"/versions", reviseWith(1, `"goal":"Release a Go module.","applicability":{"shared":{}},`+ref(shared, `{"contextual":{}}`)))
	bRelease := idOf(t, s.mustPost(t, "/v1/bindings", bindRequest("github.com/o/b", "release", local, `{"contextual":{}}`)))
	if got := resolution(bRelease); !strings.Contains(got, `"graph":{"procedure_id":"`+local+`","canonical_key":"a.release","version":2,"scope":"shared","applicability":{"shared":{}}`) {
		t.Fatalf("B resolves the promoted procedure to: %s", got)
	}

	// Narrowing: version 2 of the shared procedure is local to A.
	s.mustPost(t, "/v1/procedures/"+shared+"/versions", reviseWith(1, localAt(a)))
	if got := resolution(bBuild); !strings.Contains(got, `"version":1,"scope":"shared","applicability":{"shared":{}}`) {
		t.Fatalf("B's existing binding after narrowing: %s", got)
	}
	if got := resolution(bRelease); !strings.Contains(got, `"node":{"procedure_id":"`+shared+`","canonical_key":"go.build","version":1,`) {
		t.Fatalf("a shared parent selected a local child: %s", got)
	}
	s.refuse(t, "/v1/bindings", bindRequest("github.com/o/b", "build-two", shared, `{"contextual":{}}`), "procedure_id")
	s.refuse(t, "/v1/bindings/"+bBuild+"/revisions", `{"base_revision":1,"revision":{"inputs":{},"version_policy":{"contextual":{}},"revision_reason":"r"}}`, "revision.version_policy")
	s.refuse(t, "/v1/bindings/"+bBuild+"/revisions", `{"base_revision":1,"revision":{"inputs":{},"version_policy":{"pin":2},"revision_reason":"r"}}`, "revision.version_policy.pin")
	s.mustPost(t, "/v1/bindings/"+bBuild+"/revisions", `{"base_revision":1,"revision":{"inputs":{},"version_policy":{"pin":1},"revision_reason":"Stay on the shared version."}}`)

	// Discovery: what applies in B, what is local, and text search.
	list := func(query string) []string {
		resp := s.call(t, http.MethodGet, "/v1/procedures"+query, "")
		expect(t, resp, http.StatusOK, "")
		var body struct {
			Procedures []struct {
				CanonicalKey string `json:"canonical_key"`
				Scope        string `json:"scope"`
			} `json:"procedures"`
		}
		if err := json.Unmarshal(resp.body, &body); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range body.Procedures {
			out = append(out, p.CanonicalKey+":"+p.Scope)
		}
		return out
	}
	for query, want := range map[string][]string{
		"":                                  {"a.release:shared", "go.build:local"},
		"?repository=github.com/o/b":        {"a.release:shared"},
		"?repository=mirror.example/o/a":    {"a.release:shared", "go.build:local"},
		"?repository=github.com/o/c":        {"a.release:shared"},
		"?scope=local":                      {"go.build:local"},
		"?scope=unspecified":                nil,
		"?q=GO%20MOD":                       {"a.release:shared"},
		"?q=go.b&repository=github.com/o/a": {"go.build:local"},
	} {
		if got := list(query); !slices.Equal(got, want) {
			t.Errorf("procedures%s = %v, want %v", query, got, want)
		}
	}
	for query, field := range map[string]string{"?scope=global": "scope", "?repository=Bad": "repository", "?q=": "q", "?q=%20": "q", "?limit=1&after=bad": "after"} {
		resp := s.call(t, http.MethodGet, "/v1/procedures"+query, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if got := fieldsOf(t, resp); !slices.Equal(got, []string{field}) {
			t.Errorf("procedures%s: fields %v, want [%s]", query, got, field)
		}
	}
}

func TestFeedbackSubjects(t *testing.T) {
	s := newTestServer(t)
	a := s.register(t, "github.com/o/a")
	s.mustPost(t, "/v1/repositories/"+a+"/aliases", `{"identifier":"mirror.example/o/a","reason":"A's mirror."}`)
	p := idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("go.build", "")))
	s.mustPost(t, "/v1/procedures/"+p+"/versions", reviseWith(1, ""))
	binding := idOf(t, s.mustPost(t, "/v1/bindings", bindRequest("github.com/o/a", "build", p, `{"pin":2}`)))
	run := idOf(t, s.mustPost(t, "/v1/executions", strings.Replace(runBody(p, 2, "github.com/o/a", ""), `"version":2,`, `"version":2,"binding_id":"`+binding+`","binding_revision":1,`, 1)))
	elsewhere := idOf(t, s.mustPost(t, "/v1/executions", runBody(p, 1, "scratch", "")))

	report := func(extra string) string {
		return `{"kind":"problem","summary":"s","details":"d","reporter":"copilot.vscode"` + extra + `}`
	}
	ids := map[string]string{}
	for name, extra := range map[string]string{
		"legacy":     "",
		"service":    `,"subject":{"type":"service"}`,
		"version":    `,"subject":{"type":"procedure","procedure_id":"` + p + `","version":2},"repository":"mirror.example/o/a","execution_id":"` + run + `"`,
		"procedure":  `,"subject":{"type":"procedure","procedure_id":"` + p + `"},"execution_id":"` + elsewhere + `"`,
		"binding":    `,"subject":{"type":"binding","binding_id":"` + binding + `","revision":1},"execution_id":"` + run + `"`,
		"execution":  `,"subject":{"type":"execution","execution_id":"` + run + `"},"repository":"github.com/o/a"`,
		"repository": `,"subject":{"type":"repository","repository_id":"` + a + `"}`,
	} {
		ids[name] = idOf(t, s.mustPost(t, "/v1/feedback", report(extra)))
	}
	legacy := s.call(t, http.MethodGet, "/v1/feedback/"+ids["legacy"], "")
	if strings.Contains(string(legacy.body), `"subject"`) || strings.Contains(string(legacy.body), `"repository"`) {
		t.Fatalf("a report without a subject gained one: %s", legacy.body)
	}

	for extra, fields := range map[string][]string{
		`,"subject":{"type":"procedure","procedure_id":"missing"}`:                                            {"subject.procedure_id"},
		`,"subject":{"type":"procedure","procedure_id":"` + p + `","version":3}`:                              {"subject.version"},
		`,"subject":{"type":"procedure","procedure_id":"` + p + `","binding_id":"` + binding + `"}`:           {"subject.binding_id"},
		`,"subject":{"type":"service","execution_id":"` + run + `"}`:                                          {"subject.execution_id"},
		`,"subject":{"type":"thing"}`:                                                                         {"subject.type"},
		`,"subject":{"type":"procedure","procedure_id":"` + p + `","version":1},"execution_id":"` + run + `"`: {"execution_id"},
		`,"subject":{"type":"binding","binding_id":"` + binding + `"},"execution_id":"` + elsewhere + `"`:     {"execution_id", "execution_id"},
		`,"subject":{"type":"repository","repository_id":"` + a + `"},"repository":"github.com/o/b"`:          {"repository"},
		`,"subject":{"type":"execution","execution_id":"` + run + `"},"execution_id":"` + elsewhere + `"`:     {"execution_id", "execution_id"},
		`,"execution_id":"missing"`:                                   {"execution_id"},
		`,"execution_id":"` + run + `","repository":"github.com/o/b"`: {"repository"},
		`,"repository":"Not/Canonical"`:                               {"repository"},
	} {
		s.refuse(t, "/v1/feedback", report(extra), fields...)
	}

	listed := func(query string) []string {
		resp := s.call(t, http.MethodGet, "/v1/feedback"+query, "")
		expect(t, resp, http.StatusOK, "")
		var body struct {
			Feedback []struct {
				ID string `json:"id"`
			} `json:"feedback"`
		}
		if err := json.Unmarshal(resp.body, &body); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, f := range body.Feedback {
			for name, id := range ids {
				if id == f.ID {
					names = append(names, name)
				}
			}
		}
		slices.Sort(names)
		return names
	}
	for query, want := range map[string][]string{
		"?subject_type=service": {"legacy", "service"},
		"?subject_type=procedure&subject_id=" + p + "&subject_version=2&repository=github.com/o/a": {"version"},
		"?subject_type=procedure&subject_id=" + p:                                                  {"procedure", "version"},
		"?repository=mirror.example/o/a":                                                           {"execution", "repository", "version"},
		"?subject_type=binding&subject_id=" + binding + "&subject_version=1":                       {"binding"},
	} {
		if got := listed(query); !slices.Equal(got, want) {
			t.Errorf("feedback%s = %v, want %v", query, got, want)
		}
	}
	for query, field := range map[string]string{
		"?subject_id=" + p:    "subject_id",
		"?subject_type=thing": "subject_type",
		"?subject_type=execution&subject_version=1&subject_id=x": "subject_version",
		"?subject_version=x": "subject_version",
		"?repository=":       "repository",
	} {
		resp := s.call(t, http.MethodGet, "/v1/feedback"+query, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if got := fieldsOf(t, resp); !slices.Equal(got, []string{field}) {
			t.Errorf("feedback%s: fields %v, want [%s]", query, got, field)
		}
	}
}

// Paging returns every report exactly once, also when reports are appended,
// concurrently, between pages (ADR-0021).
func TestPagesNeverSkipOrRepeat(t *testing.T) {
	s := newTestServer(t)
	report := `{"kind":"problem","summary":"s","details":"d","reporter":"copilot.vscode"}`
	for range 5 {
		s.mustPost(t, "/v1/feedback", report)
	}
	page := func(after string) ([]string, string) {
		path := "/v1/feedback?limit=2"
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		resp := s.call(t, http.MethodGet, path, "")
		expect(t, resp, http.StatusOK, "")
		var body struct {
			Feedback []struct {
				ID string `json:"id"`
			} `json:"feedback"`
			Next string `json:"next"`
		}
		if err := json.Unmarshal(resp.body, &body); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, f := range body.Feedback {
			ids = append(ids, f.ID)
		}
		return ids, body.Next
	}
	seen, next := page("")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			<-start
			if resp, err := s.send(http.MethodPost, "/v1/feedback", report, nil); err != nil || resp.status != http.StatusCreated {
				t.Errorf("append: %v %+v", err, resp)
			}
		})
	}
	close(start)
	wg.Wait()
	for next != "" {
		var ids []string
		ids, next = page(next)
		seen = append(seen, ids...)
	}
	complete := s.call(t, http.MethodGet, "/v1/feedback", "")
	var body struct {
		Feedback []struct {
			ID string `json:"id"`
		} `json:"feedback"`
		Next *string `json:"next"`
	}
	if err := json.Unmarshal(complete.body, &body); err != nil || body.Next != nil {
		t.Fatalf("an unpaged list has a next cursor: %s", complete.body)
	}
	var want []string
	for _, f := range body.Feedback {
		want = append(want, f.ID)
	}
	if len(want) != 11 || !slices.Equal(seen, want) {
		t.Fatalf("paged %d reports %v\nwant %d %v", len(seen), seen, len(want), want)
	}
}
