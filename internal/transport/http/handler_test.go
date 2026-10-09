package http_test

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	httptransport "github.com/ashuangiras/polaroid/internal/transport/http"
)

func TestCreateRetrieveAndRevise(t *testing.T) {
	s := newTestServer(t)

	created := s.call(t, http.MethodPost, "/v1/procedures", createBody)
	expect(t, created, http.StatusCreated, "")
	h := decodeStrict[historyJSON](t, created.body)
	if h.ID == "" || h.CanonicalKey != "go.dependency.add" || h.LatestVersion != 1 || len(h.Versions) != 1 {
		t.Fatalf("unexpected creation result: %s", created.body)
	}
	if loc := created.header.Get("Location"); loc != "/v1/procedures/"+h.ID {
		t.Fatalf("Location = %q", loc)
	}
	v1 := decodeStrict[versionJSON](t, h.Versions[0])
	if v1.ProcedureID != h.ID || v1.Version != 1 || v1.RevisionReason != "Initial version." || !v1.CreatedAt.Equal(h.CreatedAt) {
		t.Fatalf("unexpected version 1: %s", h.Versions[0])
	}
	if got, want := string(v1.Contract), `{"inputs":{"module":"string"}}`; got != want {
		t.Fatalf("contract = %s, want %s", got, want)
	}

	// The procedure reads back identically by ID and by canonical key.
	for _, path := range []string{"/v1/procedures/" + h.ID, "/v1/procedures/by-key/go.dependency.add"} {
		got := s.call(t, http.MethodGet, path, "")
		expect(t, got, http.StatusOK, "")
		if !bytes.Equal(got.body, created.body) {
			t.Fatalf("GET %s differs from creation result:\n%s\n%s", path, got.body, created.body)
		}
	}
	got := s.call(t, http.MethodGet, "/v1/procedures/"+h.ID+"/versions/1", "")
	expect(t, got, http.StatusOK, "")
	if !bytes.Equal(bytes.TrimSpace(got.body), h.Versions[0]) {
		t.Fatalf("version 1 = %s, want %s", got.body, h.Versions[0])
	}

	revised := s.call(t, http.MethodPost, "/v1/procedures/"+h.ID+"/versions", reviseBody(1, "corrected"))
	expect(t, revised, http.StatusCreated, "")
	v2 := decodeStrict[versionJSON](t, revised.body)
	if v2.Version != 2 || string(v2.Instructions) != `{"steps":["corrected"]}` {
		t.Fatalf("unexpected version 2: %s", revised.body)
	}
	if loc := revised.header.Get("Location"); loc != "/v1/procedures/"+h.ID+"/versions/2" {
		t.Fatalf("Location = %q", loc)
	}

	after := decodeStrict[historyJSON](t, s.call(t, http.MethodGet, "/v1/procedures/"+h.ID, "").body)
	if after.LatestVersion != 2 || len(after.Versions) != 2 {
		t.Fatalf("history after revision: %+v", after)
	}
	if !bytes.Equal(after.Versions[0], h.Versions[0]) {
		t.Fatalf("version 1 changed after revision:\n%s\n%s", after.Versions[0], h.Versions[0])
	}
	if !bytes.Equal(after.Versions[1], bytes.TrimSpace(revised.body)) {
		t.Fatalf("version 2 in history differs from revision result")
	}

	list := s.call(t, http.MethodGet, "/v1/procedures", "")
	expect(t, list, http.StatusOK, "")
	procedures := decodeStrict[struct {
		Procedures []procedureJSON `json:"procedures"`
	}](t, list.body).Procedures
	if len(procedures) != 1 || procedures[0].ID != h.ID || procedures[0].LatestVersion != 2 {
		t.Fatalf("list = %s", list.body)
	}
}

func TestListWithoutProcedures(t *testing.T) {
	s := newTestServer(t)
	resp := s.call(t, http.MethodGet, "/v1/procedures", "")
	expect(t, resp, http.StatusOK, "")
	if got := string(bytes.TrimSpace(resp.body)); got != `{"procedures":[]}` {
		t.Fatalf("empty list = %s", got)
	}
}

func TestDuplicateCanonicalKeyIsAConflict(t *testing.T) {
	s := newTestServer(t)
	original := s.create(t)

	resp := s.call(t, http.MethodPost, "/v1/procedures", createBody)
	expect(t, resp, http.StatusConflict, "canonical_key_exists")

	list := s.call(t, http.MethodGet, "/v1/procedures", "")
	if !strings.Contains(string(list.body), original.ID) || strings.Count(string(list.body), `"id"`) != 1 {
		t.Fatalf("list after duplicate = %s", list.body)
	}
}

func TestStaleRevisionIsRejected(t *testing.T) {
	s := newTestServer(t)
	h := s.create(t)
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+h.ID+"/versions", reviseBody(1, "first")), http.StatusCreated, "")
	before := s.call(t, http.MethodGet, "/v1/procedures/"+h.ID, "")

	stale := s.call(t, http.MethodPost, "/v1/procedures/"+h.ID+"/versions", reviseBody(1, "stale"))
	expect(t, stale, http.StatusConflict, "version_conflict")
	if e := errorOf(t, stale); e.LatestVersion != 2 {
		t.Fatalf("latest_version = %d, want 2", e.LatestVersion)
	}
	if after := s.call(t, http.MethodGet, "/v1/procedures/"+h.ID, ""); !bytes.Equal(after.body, before.body) {
		t.Fatalf("history changed by a stale revision:\n%s\n%s", after.body, before.body)
	}
}

func TestConcurrentRevisionsFromSameBase(t *testing.T) {
	s := newTestServer(t)
	h := s.create(t)

	const writers = 8
	start := make(chan struct{})
	results := make([]response, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			<-start
			results[i], errs[i] = s.send(http.MethodPost, "/v1/procedures/"+h.ID+"/versions", reviseBody(1, fmt.Sprintf("writer %d", i)), nil)
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
			if e := errorOf(t, resp); e.Code != "version_conflict" || e.LatestVersion != 2 {
				t.Fatalf("writer %d: %s", i, resp.body)
			}
		default:
			t.Fatalf("writer %d: status %d: %s", i, resp.status, resp.body)
		}
	}
	if created != 1 {
		t.Fatalf("%d revisions from the same base succeeded, want exactly 1", created)
	}
	after := decodeStrict[historyJSON](t, s.call(t, http.MethodGet, "/v1/procedures/"+h.ID, "").body)
	if len(after.Versions) != 2 {
		t.Fatalf("history has %d versions, want 2", len(after.Versions))
	}
}

func TestInvalidRequestsAreRejected(t *testing.T) {
	s := newTestServer(t)
	h := s.create(t)
	revisePath := "/v1/procedures/" + h.ID + "/versions"
	definition := `"philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"`

	cases := []struct {
		name        string
		path        string
		contentType string
		body        string
		status      int
		code        string
		fields      []string
	}{
		{name: "malformed JSON", body: `{"canonical_key":`, status: 400, code: "invalid_request"},
		{name: "trailing data", body: createBody + `{}`, status: 400, code: "invalid_request"},
		{name: "not an object", body: `[]`, status: 400, code: "invalid_request"},
		{name: "unknown field", body: `{"canonical_key":"a","extra":1,"version":{` + definition + `}}`, status: 400, code: "invalid_request"},
		{name: "server-assigned field", body: `{"canonical_key":"a","version":{"version":1,` + definition + `}}`, status: 400, code: "invalid_request"},
		{name: "field name case", body: `{"Canonical_Key":"a","version":{` + definition + `}}`, status: 400, code: "invalid_request"},
		{name: "wrong type", body: `{"canonical_key":5,"version":{` + definition + `}}`, status: 400, code: "invalid_request"},
		{name: "duplicate member", body: `{"canonical_key":"a","version":{` + definition + `,"method":"again"}}`, status: 400, code: "invalid_request"},
		{name: "duplicate member in instructions", body: `{"canonical_key":"a","version":{"philosophy":"p","method":"m","contract":{},"instructions":{"s":1,"s":2},"revision_reason":"r"}}`, status: 400, code: "invalid_request"},
		{name: "invalid UTF-8", body: "{\"canonical_key\":\"\xff\",\"version\":{" + definition + "}}", status: 400, code: "invalid_request"},
		{name: "missing everything", body: `{}`, status: 400, code: "invalid_request",
			fields: []string{"canonical_key", "version.philosophy", "version.method", "version.contract", "version.instructions", "version.revision_reason"}},
		{name: "non-object contract", body: `{"canonical_key":"a","version":{"philosophy":"p","method":"m","contract":[],"instructions":{},"revision_reason":"r"}}`, status: 400, code: "invalid_request",
			fields: []string{"version.contract"}},
		{name: "null instructions", body: `{"canonical_key":"a","version":{"philosophy":"p","method":"m","contract":{},"instructions":null,"revision_reason":"r"}}`, status: 400, code: "invalid_request",
			fields: []string{"version.instructions"}},
		{name: "invalid canonical key", body: `{"canonical_key":"Go Dependency","version":{` + definition + `}}`, status: 400, code: "invalid_request",
			fields: []string{"canonical_key"}},
		{name: "missing base version", path: revisePath, body: `{"version":{` + definition + `}}`, status: 400, code: "invalid_request",
			fields: []string{"base_version"}},
		{name: "wrong content type", contentType: "text/plain", body: createBody, status: 415, code: "unsupported_media_type"},
		{name: "missing content type", contentType: "-", body: createBody, status: 415, code: "unsupported_media_type"},
		{name: "empty body", body: "", status: 400, code: "invalid_request"},
		{name: "too large", body: `{"canonical_key":"` + strings.Repeat("a", httptransport.MaxRequestBytes) + `"}`, status: 413, code: "request_too_large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			if path == "" {
				path = "/v1/procedures"
			}
			header := http.Header{}
			switch tc.contentType {
			case "":
				header.Set("Content-Type", "application/json")
			case "-":
			default:
				header.Set("Content-Type", tc.contentType)
			}
			resp, err := s.send(http.MethodPost, path, tc.body, header)
			if err != nil {
				t.Fatal(err)
			}
			expect(t, resp, tc.status, tc.code)
			if e := errorOf(t, resp); e.Message == "" {
				t.Fatalf("error without message: %s", resp.body)
			} else if tc.fields != nil {
				var got []string
				for _, f := range e.Fields {
					got = append(got, f.Field)
				}
				if !slices.Equal(got, tc.fields) {
					t.Fatalf("fields = %v, want %v", got, tc.fields)
				}
			}
		})
	}

	list := s.call(t, http.MethodGet, "/v1/procedures", "")
	if n := strings.Count(string(list.body), `"id"`); n != 1 {
		t.Fatalf("invalid requests created records: %s", list.body)
	}
	if after := decodeStrict[historyJSON](t, s.call(t, http.MethodGet, "/v1/procedures/"+h.ID, "").body); len(after.Versions) != 1 {
		t.Fatalf("invalid revision created a version: %d versions", len(after.Versions))
	}
}

func TestMissingRecordsAndUnknownEndpoints(t *testing.T) {
	s := newTestServer(t)
	h := s.create(t)

	cases := []struct {
		method, path, body string
		status             int
		code               string
	}{
		{http.MethodGet, "/v1/procedures/0192f7e4-0000-7000-8000-000000000000", "", 404, "not_found"},
		{http.MethodGet, "/v1/procedures/by-key/no.such.key", "", 404, "not_found"},
		{http.MethodGet, "/v1/procedures/" + h.ID + "/versions/2", "", 404, "not_found"},
		{http.MethodGet, "/v1/procedures/" + h.ID + "/versions/0", "", 400, "invalid_request"},
		{http.MethodGet, "/v1/procedures/" + h.ID + "/versions/latest", "", 400, "invalid_request"},
		{http.MethodPost, "/v1/procedures/0192f7e4-0000-7000-8000-000000000000/versions", reviseBody(1, "x"), 404, "not_found"},
		{http.MethodGet, "/v2/procedures", "", 404, "not_found"},
		{http.MethodGet, "/", "", 404, "not_found"},
		{http.MethodDelete, "/v1/procedures/" + h.ID, "", 405, "method_not_allowed"},
		{http.MethodPut, "/v1/procedures", "", 405, "method_not_allowed"},
	}
	for _, tc := range cases {
		resp := s.call(t, tc.method, tc.path, tc.body)
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			expect(t, resp, tc.status, tc.code)
		})
	}

	resp := s.call(t, http.MethodDelete, "/v1/procedures/"+h.ID, "")
	if allow := resp.header.Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow = %q, want %q", allow, "GET, HEAD")
	}
}

func TestInternalErrorsAreNotExposed(t *testing.T) {
	s := newTestServer(t)
	s.create(t)
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}

	resp := s.call(t, http.MethodGet, "/v1/procedures", "")
	expect(t, resp, http.StatusInternalServerError, "internal")
	if e := errorOf(t, resp); e.Message != "internal error" {
		t.Fatalf("internal error message = %q", e.Message)
	}
	for _, leak := range []string{"sql", "database", "closed"} {
		if strings.Contains(strings.ToLower(string(resp.body)), leak) {
			t.Fatalf("response exposes internal detail %q: %s", leak, resp.body)
		}
	}
	if !strings.Contains(s.logs.String(), "database is closed") {
		t.Fatalf("internal error was not logged; logs:\n%s", s.logs)
	}
	expect(t, s.call(t, http.MethodGet, "/healthz", ""), http.StatusServiceUnavailable, "unavailable")
}

func TestHealth(t *testing.T) {
	s := newTestServer(t)
	resp := s.call(t, http.MethodGet, "/healthz", "")
	expect(t, resp, http.StatusOK, "")
	if got := string(bytes.TrimSpace(resp.body)); got != `{"status":"ok"}` {
		t.Fatalf("health = %s", got)
	}
}

func TestRequestsMustNameALoopbackHost(t *testing.T) {
	s := newTestServer(t)
	for host, status := range map[string]int{
		"polaroid.example":      http.StatusForbidden,
		"polaroid.example:7417": http.StatusForbidden,
		"192.0.2.1:7417":        http.StatusForbidden,
		"localhost:7417":        http.StatusOK,
		"127.0.0.1:7417":        http.StatusOK,
		"[::1]:7417":            http.StatusOK,
	} {
		resp, err := s.send(http.MethodGet, "/healthz", "", http.Header{"Host": {host}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.status != status {
			t.Errorf("Host %s: status = %d, want %d", host, resp.status, status)
		}
		if status == http.StatusForbidden {
			expect(t, resp, status, "forbidden")
		}
	}
}

func TestCrossOriginBrowserWritesAreRejected(t *testing.T) {
	s := newTestServer(t)
	for _, header := range []http.Header{
		{"Content-Type": {"application/json"}, "Sec-Fetch-Site": {"cross-site"}},
		{"Content-Type": {"application/json"}, "Origin": {"https://attacker.example"}},
	} {
		resp, err := s.send(http.MethodPost, "/v1/procedures", createBody, header)
		if err != nil {
			t.Fatal(err)
		}
		expect(t, resp, http.StatusForbidden, "forbidden")
	}
	if list := s.call(t, http.MethodGet, "/v1/procedures", ""); strings.Contains(string(list.body), `"id"`) {
		t.Fatalf("cross-origin request created a record: %s", list.body)
	}
}
