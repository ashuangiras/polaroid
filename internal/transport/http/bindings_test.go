package http_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

const repoA, repoB = "github.com/ashuangiras/polaroid", "scratch"

func bindBody(repository, name, procedureID, policy string) string {
	return fmt.Sprintf(`{
  "repository": %q,
  "name": %q,
  "procedure_id": %q,
  "revision": {
    "inputs": { "module": "modernc.org/sqlite" },
    "version_policy": %s,
    "revision_reason": "Use the shared procedure."
  }
}`, repository, name, procedureID, policy)
}

func reviseBindingBody(base int, policy string) string {
	return fmt.Sprintf(`{"base_revision": %d, "revision": {"inputs": {"module": "example.com/m"}, "version_policy": %s, "revision_reason": "Changed the module."}}`, base, policy)
}

// bind creates a binding and returns its history.
func (s *testServer) bind(t *testing.T, repository, name, procedureID, policy string) bindingHistoryJSON {
	t.Helper()
	resp := s.call(t, http.MethodPost, "/v1/bindings", bindBody(repository, name, procedureID, policy))
	expect(t, resp, http.StatusCreated, "")
	return decodeStrict[bindingHistoryJSON](t, resp.body)
}

func listPath(repository string) string {
	return "/v1/bindings?" + url.Values{"repository": {repository}}.Encode()
}

func TestTwoRepositoriesBindOneProcedure(t *testing.T) {
	s := newTestServer(t)
	p := s.create(t)
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+p.ID+"/versions", reviseBody(1, "corrected")), http.StatusCreated, "")
	procedureBefore := s.call(t, http.MethodGet, "/v1/procedures/"+p.ID, "")

	for _, tc := range []struct{ repository, policy string }{
		{repoA, `{"pin": 2}`},
		{repoB, `{"contextual": {}}`},
	} {
		created := s.call(t, http.MethodPost, "/v1/bindings", bindBody(tc.repository, "add-dependency", p.ID, tc.policy))
		expect(t, created, http.StatusCreated, "")
		b := decodeStrict[bindingHistoryJSON](t, created.body)
		if b.ID == "" || b.Repository != tc.repository || b.Name != "add-dependency" || b.ProcedureID != p.ID || b.LatestRevision != 1 || len(b.Revisions) != 1 {
			t.Fatalf("unexpected binding: %s", created.body)
		}
		if loc := created.header.Get("Location"); loc != "/v1/bindings/"+b.ID {
			t.Fatalf("Location = %q", loc)
		}
		r1 := decodeStrict[bindingRevisionJSON](t, b.Revisions[0])
		if r1.BindingID != b.ID || r1.Revision != 1 || string(r1.Inputs) != `{"module":"modernc.org/sqlite"}` || !r1.CreatedAt.Equal(b.CreatedAt) {
			t.Fatalf("unexpected revision 1: %s", b.Revisions[0])
		}
		if got, want := string(r1.VersionPolicy), strings.ReplaceAll(tc.policy, " ", ""); got != want {
			t.Fatalf("version_policy = %s, want %s", got, want)
		}

		got := s.call(t, http.MethodGet, "/v1/bindings/"+b.ID, "")
		expect(t, got, http.StatusOK, "")
		if !bytes.Equal(got.body, created.body) {
			t.Fatalf("GET binding differs from creation result:\n%s\n%s", got.body, created.body)
		}
		rev := s.call(t, http.MethodGet, "/v1/bindings/"+b.ID+"/revisions/1", "")
		expect(t, rev, http.StatusOK, "")
		if !bytes.Equal(bytes.TrimSpace(rev.body), b.Revisions[0]) {
			t.Fatalf("revision 1 = %s, want %s", rev.body, b.Revisions[0])
		}
		list := s.call(t, http.MethodGet, listPath(tc.repository), "")
		expect(t, list, http.StatusOK, "")
		bindings := decodeStrict[struct {
			Bindings []bindingJSON `json:"bindings"`
		}](t, list.body).Bindings
		if len(bindings) != 1 || bindings[0].ID != b.ID || bindings[0].ProcedureID != p.ID {
			t.Fatalf("list for %s = %s", tc.repository, list.body)
		}
	}

	if after := s.call(t, http.MethodGet, "/v1/procedures/"+p.ID, ""); !bytes.Equal(after.body, procedureBefore.body) {
		t.Fatalf("binding changed the procedure:\n%s\n%s", after.body, procedureBefore.body)
	}
}

func TestBindingCreationRejections(t *testing.T) {
	s := newTestServer(t)
	p := s.create(t)
	s.bind(t, repoA, "add-dependency", p.ID, `{"pin": 1}`)

	unknown := s.call(t, http.MethodPost, "/v1/bindings", bindBody(repoB, "x", "0192f7e4-0000-7000-8000-000000000000", `{"pin": 1}`))
	expect(t, unknown, http.StatusNotFound, "not_found")

	missingPin := s.call(t, http.MethodPost, "/v1/bindings", bindBody(repoB, "x", p.ID, `{"pin": 2}`))
	expect(t, missingPin, http.StatusBadRequest, "invalid_request")
	if e := errorOf(t, missingPin); len(e.Fields) != 1 || e.Fields[0].Field != "revision.version_policy.pin" {
		t.Fatalf("missing pinned version: %s", missingPin.body)
	}

	duplicate := s.call(t, http.MethodPost, "/v1/bindings", bindBody(repoA, "add-dependency", p.ID, `{"contextual": {}}`))
	expect(t, duplicate, http.StatusConflict, "binding_exists")

	for repository, want := range map[string]int{repoA: 1, repoB: 0} {
		list := s.call(t, http.MethodGet, listPath(repository), "")
		if n := strings.Count(string(list.body), `"id"`); n != want {
			t.Fatalf("%s has %d bindings after rejections, want %d: %s", repository, n, want, list.body)
		}
	}
}

func TestStaleBindingRevisionIsRejected(t *testing.T) {
	s := newTestServer(t)
	p := s.create(t)
	b := s.bind(t, repoA, "add-dependency", p.ID, `{"pin": 1}`)
	path := "/v1/bindings/" + b.ID + "/revisions"

	revised := s.call(t, http.MethodPost, path, reviseBindingBody(1, `{"contextual": {}}`))
	expect(t, revised, http.StatusCreated, "")
	r2 := decodeStrict[bindingRevisionJSON](t, revised.body)
	if r2.Revision != 2 || string(r2.VersionPolicy) != `{"contextual":{}}` {
		t.Fatalf("unexpected revision 2: %s", revised.body)
	}
	if loc := revised.header.Get("Location"); loc != path+"/2" {
		t.Fatalf("Location = %q", loc)
	}
	before := s.call(t, http.MethodGet, "/v1/bindings/"+b.ID, "")

	stale := s.call(t, http.MethodPost, path, reviseBindingBody(1, `{"pin": 1}`))
	expect(t, stale, http.StatusConflict, "revision_conflict")
	if e := errorOf(t, stale); e.LatestRevision != 2 {
		t.Fatalf("latest_revision = %d, want 2", e.LatestRevision)
	}
	missingPin := s.call(t, http.MethodPost, path, reviseBindingBody(2, `{"pin": 5}`))
	expect(t, missingPin, http.StatusBadRequest, "invalid_request")
	if e := errorOf(t, missingPin); len(e.Fields) != 1 || e.Fields[0].Field != "revision.version_policy.pin" {
		t.Fatalf("missing pinned version: %s", missingPin.body)
	}
	if after := s.call(t, http.MethodGet, "/v1/bindings/"+b.ID, ""); !bytes.Equal(after.body, before.body) {
		t.Fatalf("history changed by rejected revisions:\n%s\n%s", after.body, before.body)
	}
	if h := decodeStrict[bindingHistoryJSON](t, before.body); h.LatestRevision != 2 || !bytes.Equal(h.Revisions[0], b.Revisions[0]) {
		t.Fatalf("revision 1 changed after revision: %s", before.body)
	}
}

func TestConcurrentBindingRevisionsFromSameBase(t *testing.T) {
	s := newTestServer(t)
	p := s.create(t)
	b := s.bind(t, repoA, "add-dependency", p.ID, `{"pin": 1}`)

	const writers = 8
	start := make(chan struct{})
	results := make([]response, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			<-start
			body := fmt.Sprintf(`{"base_revision":1,"revision":{"inputs":{"writer":%d},"version_policy":{"pin":1},"revision_reason":"r"}}`, i)
			results[i], errs[i] = s.send(http.MethodPost, "/v1/bindings/"+b.ID+"/revisions", body, nil)
		})
	}
	close(start)
	wg.Wait()

	created := 0
	for i, resp := range results {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
		switch resp.status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			if e := errorOf(t, resp); e.Code != "revision_conflict" || e.LatestRevision != 2 {
				t.Fatalf("writer %d: %s", i, resp.body)
			}
		default:
			t.Fatalf("writer %d: status %d: %s", i, resp.status, resp.body)
		}
	}
	if created != 1 {
		t.Fatalf("%d revisions from the same base succeeded, want exactly 1", created)
	}
	after := decodeStrict[bindingHistoryJSON](t, s.call(t, http.MethodGet, "/v1/bindings/"+b.ID, "").body)
	if len(after.Revisions) != 2 {
		t.Fatalf("history has %d revisions, want 2", len(after.Revisions))
	}
}

// A contextual policy is stored and returned exactly as accepted. Binding
// reads never carry a selected version; only /resolution resolves it.
func TestContextualPolicyIsStoredVerbatim(t *testing.T) {
	s := newTestServer(t)
	p := s.create(t)
	b := s.bind(t, repoA, "add-dependency", p.ID, `{"contextual": {}}`)

	for _, path := range []string{"/v1/bindings/" + b.ID, "/v1/bindings/" + b.ID + "/revisions/1"} {
		resp := s.call(t, http.MethodGet, path, "")
		expect(t, resp, http.StatusOK, "")
		if !strings.Contains(string(resp.body), `"version_policy":{"contextual":{}}`) {
			t.Fatalf("GET %s does not return the policy verbatim: %s", path, resp.body)
		}
		for _, claim := range []string{`"version":`, `"resolved`, `"selected`} {
			if strings.Contains(string(resp.body), claim) {
				t.Fatalf("GET %s claims a resolution (%s): %s", path, claim, resp.body)
			}
		}
	}
	expect(t, s.call(t, http.MethodGet, "/v1/bindings/"+b.ID+"/resolve", ""), http.StatusNotFound, "not_found")
}

func TestInvalidBindingRequestsAreRejected(t *testing.T) {
	s := newTestServer(t)
	p := s.create(t)
	b := s.bind(t, repoA, "add-dependency", p.ID, `{"pin": 1}`)
	revisePath := "/v1/bindings/" + b.ID + "/revisions"
	revision := func(policy string) string {
		return `"revision":{"inputs":{},"version_policy":` + policy + `,"revision_reason":"r"}`
	}
	create := func(fields string) string {
		return `{"repository":"scratch","name":"x","procedure_id":"` + p.ID + `",` + fields + `}`
	}

	cases := []struct {
		name   string
		path   string
		body   string
		code   string
		fields []string
	}{
		{name: "malformed JSON", body: `{"repository":`, code: "invalid_request"},
		{name: "unknown field", body: create(revision(`{"pin":1}`) + `,"extra":1`), code: "invalid_request"},
		{name: "server-assigned field", body: create(`"id":"x",` + revision(`{"pin":1}`)), code: "invalid_request"},
		{name: "unknown policy", body: create(revision(`{"latest":{}}`)), code: "invalid_request"},
		{name: "member in contextual", body: create(revision(`{"contextual":{"prefer":"latest"}}`)), code: "invalid_request"},
		{name: "contextual not an object", body: create(revision(`{"contextual":true}`)), code: "invalid_request"},
		{name: "pin not an integer", body: create(revision(`{"pin":"2"}`)), code: "invalid_request"},
		{name: "pin and contextual", body: create(revision(`{"pin":1,"contextual":{}}`)), code: "invalid_request",
			fields: []string{"revision.version_policy"}},
		{name: "empty policy", body: create(revision(`{}`)), code: "invalid_request",
			fields: []string{"revision.version_policy"}},
		{name: "null pin", body: create(revision(`{"pin":null}`)), code: "invalid_request",
			fields: []string{"revision.version_policy"}},
		{name: "pin zero", body: create(revision(`{"pin":0}`)), code: "invalid_request",
			fields: []string{"revision.version_policy.pin"}},
		{name: "missing everything", body: `{}`, code: "invalid_request",
			fields: []string{"repository", "name", "procedure_id", "revision.inputs", "revision.version_policy", "revision.revision_reason"}},
		{name: "non-canonical repository", body: bindBody("https://github.com/ashuangiras/polaroid.git", "x", p.ID, `{"pin":1}`), code: "invalid_request",
			fields: []string{"repository"}},
		{name: "non-object inputs", body: create(`"revision":{"inputs":[],"version_policy":{"pin":1},"revision_reason":"r"}`), code: "invalid_request",
			fields: []string{"revision.inputs"}},
		{name: "missing base revision", path: revisePath, body: `{` + revision(`{"pin":1}`) + `}`, code: "invalid_request",
			fields: []string{"base_revision"}},
		{name: "base_version instead of base_revision", path: revisePath, body: `{"base_version":1,` + revision(`{"pin":1}`) + `}`, code: "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			if path == "" {
				path = "/v1/bindings"
			}
			resp := s.call(t, http.MethodPost, path, tc.body)
			expect(t, resp, http.StatusBadRequest, tc.code)
			if tc.fields == nil {
				return
			}
			var got []string
			for _, f := range errorOf(t, resp).Fields {
				got = append(got, f.Field)
			}
			if !slices.Equal(got, tc.fields) {
				t.Fatalf("fields = %v, want %v", got, tc.fields)
			}
		})
	}

	if list := s.call(t, http.MethodGet, listPath("scratch"), ""); strings.Contains(string(list.body), `"id"`) {
		t.Fatalf("invalid requests created bindings: %s", list.body)
	}
	if after := decodeStrict[bindingHistoryJSON](t, s.call(t, http.MethodGet, "/v1/bindings/"+b.ID, "").body); len(after.Revisions) != 1 {
		t.Fatalf("invalid revision created a revision: %d revisions", len(after.Revisions))
	}
}

func TestListBindingsQuery(t *testing.T) {
	s := newTestServer(t)
	p := s.create(t)
	s.bind(t, repoA, "b-second", p.ID, `{"pin": 1}`)
	s.bind(t, repoA, "a-first", p.ID, `{"contextual": {}}`)
	s.bind(t, repoB, "elsewhere", p.ID, `{"pin": 1}`)

	list := s.call(t, http.MethodGet, listPath(repoA), "")
	expect(t, list, http.StatusOK, "")
	var names []string
	for _, b := range decodeStrict[struct {
		Bindings []bindingJSON `json:"bindings"`
	}](t, list.body).Bindings {
		names = append(names, b.Name)
	}
	if want := []string{"a-first", "b-second"}; !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if empty := s.call(t, http.MethodGet, listPath("github.com/no/such"), ""); string(bytes.TrimSpace(empty.body)) != `{"bindings":[]}` {
		t.Fatalf("empty list = %s", empty.body)
	}

	for query, field := range map[string]string{
		"":                                 "repository",
		"?repository=":                     "repository",
		"?repository=Scratch":              "repository",
		"?repository=scratch&repository=x": "repository",
		"?repo=scratch":                    "",
		"?repository=scratch&limit=1":      "",
		"?repository=%zz":                  "",
	} {
		t.Run(query, func(t *testing.T) {
			resp := s.call(t, http.MethodGet, "/v1/bindings"+query, "")
			expect(t, resp, http.StatusBadRequest, "invalid_request")
			e := errorOf(t, resp)
			if field != "" && (len(e.Fields) != 1 || e.Fields[0].Field != field) {
				t.Fatalf("fields = %+v, want [%s]", e.Fields, field)
			}
		})
	}
}

func TestMissingBindingsAndMethods(t *testing.T) {
	s := newTestServer(t)
	p := s.create(t)
	b := s.bind(t, repoA, "add-dependency", p.ID, `{"pin": 1}`)

	cases := []struct {
		method, path, body string
		status             int
		code               string
	}{
		{http.MethodGet, "/v1/bindings/0192f7e4-0000-7000-8000-000000000000", "", 404, "not_found"},
		{http.MethodGet, "/v1/bindings/" + b.ID + "/revisions/2", "", 404, "not_found"},
		{http.MethodGet, "/v1/bindings/" + b.ID + "/revisions/0", "", 400, "invalid_request"},
		{http.MethodGet, "/v1/bindings/" + b.ID + "/revisions/latest", "", 400, "invalid_request"},
		{http.MethodPost, "/v1/bindings/0192f7e4-0000-7000-8000-000000000000/revisions", reviseBindingBody(1, `{"pin":1}`), 404, "not_found"},
		{http.MethodDelete, "/v1/bindings/" + b.ID, "", 405, "method_not_allowed"},
		{http.MethodPut, "/v1/bindings/" + b.ID + "/revisions/1", "", 405, "method_not_allowed"},
	}
	for _, tc := range cases {
		resp := s.call(t, tc.method, tc.path, tc.body)
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			expect(t, resp, tc.status, tc.code)
		})
	}
}
