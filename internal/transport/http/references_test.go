package http_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// procedureWithReferences returns a create-procedure body whose version
// carries the given raw "references" JSON.
func procedureWithReferences(key, references string) string {
	return fmt.Sprintf(`{"canonical_key": %q, "version": {"philosophy": "Compose, don't copy.", "method": "Delegate to children.",
  "contract": {"inputs": {"driver": "module path"}}, "instructions": {"steps": ["run children"]},
  "references": %s, "revision_reason": "Initial version."}}`, key, references)
}

func reviseWithReferences(base int, references string) string {
	return fmt.Sprintf(`{"base_version": %d, "version": {"philosophy": "p", "method": "m", "contract": {}, "instructions": {},
  "references": %s, "revision_reason": "Changed the children."}}`, base, references)
}

func TestProcedureVersionsCarryReferences(t *testing.T) {
	s := newTestServer(t)
	child := s.create(t)
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+child.ID+"/versions", reviseBody(1, "v2")), http.StatusCreated, "")
	childBefore := s.call(t, http.MethodGet, "/v1/procedures/"+child.ID, "").body

	refs := fmt.Sprintf(`[
  {"name": "pinned-child", "procedure_id": %q, "version_policy": {"pin": 2},
   "inputs": {"module": {"input": "driver"}, "strict": {"value": true}}},
  {"name": "latest-child", "procedure_id": %q, "version_policy": {"contextual": {}}, "inputs": {}}]`, child.ID, child.ID)
	created := s.call(t, http.MethodPost, "/v1/procedures", procedureWithReferences("compose.parent", refs))
	expect(t, created, http.StatusCreated, "")
	parent := decodeStrict[historyJSON](t, created.body)
	v1 := decodeStrict[versionJSON](t, parent.Versions[0])
	wantRefs := fmt.Sprintf(`[{"name":"pinned-child","procedure_id":%q,"version_policy":{"pin":2},"inputs":{"module":{"input":"driver"},"strict":{"value":true}}},`+
		`{"name":"latest-child","procedure_id":%q,"version_policy":{"contextual":{}},"inputs":{}}]`, child.ID, child.ID)
	if string(v1.References) != wantRefs {
		t.Fatalf("references =\n %s\nwant\n %s", v1.References, wantRefs)
	}

	revised := s.call(t, http.MethodPost, "/v1/procedures/"+parent.ID+"/versions",
		reviseWithReferences(1, fmt.Sprintf(`[{"name": "only-child", "procedure_id": %q, "version_policy": {"pin": 1}, "inputs": {"x": {"value": [1, "two", null]}}}]`, child.ID)))
	expect(t, revised, http.StatusCreated, "")
	v2 := decodeStrict[versionJSON](t, revised.body)
	if want := fmt.Sprintf(`[{"name":"only-child","procedure_id":%q,"version_policy":{"pin":1},"inputs":{"x":{"value":[1,"two",null]}}}]`, child.ID); string(v2.References) != want {
		t.Fatalf("revision references = %s, want %s", v2.References, want)
	}

	// History returns every version, references included, byte-for-byte.
	history := decodeStrict[historyJSON](t, s.call(t, http.MethodGet, "/v1/procedures/"+parent.ID, "").body)
	if len(history.Versions) != 2 || !bytes.Equal(history.Versions[0], parent.Versions[0]) || !bytes.Equal(history.Versions[1], bytes.TrimSpace(revised.body)) {
		t.Fatalf("history differs from the write results:\n%s\n%s", history.Versions, created.body)
	}
	for n, want := range map[int][]byte{1: parent.Versions[0], 2: bytes.TrimSpace(revised.body)} {
		got := s.call(t, http.MethodGet, fmt.Sprintf("/v1/procedures/%s/versions/%d", parent.ID, n), "")
		if !bytes.Equal(bytes.TrimSpace(got.body), want) {
			t.Fatalf("version %d = %s, want %s", n, got.body, want)
		}
	}
	if after := s.call(t, http.MethodGet, "/v1/procedures/"+child.ID, "").body; !bytes.Equal(after, childBefore) {
		t.Fatalf("referencing a procedure changed it:\n%s\n%s", after, childBefore)
	}
}

func TestInvalidReferencesAreRejected(t *testing.T) {
	s := newTestServer(t)
	child := s.create(t)
	ref := func(name, policy, inputs string) string {
		return fmt.Sprintf(`{"name": %q, "procedure_id": %q, "version_policy": %s, "inputs": %s}`, name, child.ID, policy, inputs)
	}
	unknown := `{"name": "ghost", "procedure_id": "0192f7e4-0000-7000-8000-000000000000", "version_policy": {"pin": 1}, "inputs": {}}`

	cases := []struct {
		name, refs string
		fields     []string
	}{
		{"unknown target", "[" + unknown + "]", []string{"version.references[0].procedure_id"}},
		{"unknown pinned version", "[" + ref("a", `{"pin": 2}`, `{}`) + "]", []string{"version.references[0].version_policy.pin"}},
		{"every missing target is reported", "[" + ref("a", `{"pin": 1}`, `{}`) + "," + unknown + "," + ref("b", `{"pin": 7}`, `{}`) + "]",
			[]string{"version.references[1].procedure_id", "version.references[2].version_policy.pin"}},
		{"duplicate name", "[" + ref("a", `{"pin": 1}`, `{}`) + "," + ref("a", `{"contextual": {}}`, `{}`) + "]", []string{"version.references[1].name"}},
		{"invalid name", "[" + ref("Not A Name", `{"pin": 1}`, `{}`) + "]", []string{"version.references[0].name"}},
		{"mapping to a bare string", "[" + ref("a", `{"pin": 1}`, `{"module": "driver"}`) + "]", []string{"version.references[0].inputs.module"}},
		{"mapping with both sources", "[" + ref("a", `{"pin": 1}`, `{"module": {"input": "driver", "value": "x"}}`) + "]", []string{"version.references[0].inputs.module"}},
		{"mapping with an unknown source", "[" + ref("a", `{"pin": 1}`, `{"module": {"from": "driver"}}`) + "]", []string{"version.references[0].inputs.module"}},
		{"mapping not an object", "[" + ref("a", `{"pin": 1}`, `["driver"]`) + "]", []string{"version.references[0].inputs"}},
		{"invalid policy", "[" + ref("a", `{"pin": 1, "contextual": {}}`, `{}`) + "]", []string{"version.references[0].version_policy"}},
		{"missing fields", `[{}]`, []string{"version.references[0].name", "version.references[0].procedure_id", "version.references[0].version_policy", "version.references[0].inputs"}},
		{"unknown member in a reference", `[{"name": "a", "procedure_id": "x", "version_policy": {"pin": 1}, "inputs": {}, "alias": "b"}]`, nil},
		{"references not a list", `{"a": {}}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.call(t, http.MethodPost, "/v1/procedures", procedureWithReferences("compose.parent", tc.refs))
			expect(t, resp, http.StatusBadRequest, "invalid_request")
			if tc.fields == nil {
				return
			}
			var got []string
			for _, f := range errorOf(t, resp).Fields {
				got = append(got, f.Field)
			}
			if !slices.Equal(got, tc.fields) {
				t.Fatalf("fields = %v, want %v; body: %s", got, tc.fields, resp.body)
			}
		})
	}

	// The same rules apply to revisions, and nothing is stored.
	before := s.call(t, http.MethodGet, "/v1/procedures/"+child.ID, "").body
	resp := s.call(t, http.MethodPost, "/v1/procedures/"+child.ID+"/versions", reviseWithReferences(1, "["+unknown+"]"))
	expect(t, resp, http.StatusBadRequest, "invalid_request")
	if after := s.call(t, http.MethodGet, "/v1/procedures/"+child.ID, "").body; !bytes.Equal(after, before) {
		t.Fatal("a rejected revision changed the history")
	}
	if list := s.call(t, http.MethodGet, "/v1/procedures", ""); strings.Count(string(list.body), `"id"`) != 1 {
		t.Fatalf("rejected requests created procedures: %s", list.body)
	}
}

func TestVersionsWithoutReferencesOmitTheField(t *testing.T) {
	s := newTestServer(t)
	for key, refs := range map[string]string{"empty.list": `[]`, "null.list": `null`} {
		resp := s.call(t, http.MethodPost, "/v1/procedures", procedureWithReferences(key, refs))
		expect(t, resp, http.StatusCreated, "")
		if strings.Contains(string(resp.body), "references") {
			t.Fatalf("%s: a version without references returned the field: %s", refs, resp.body)
		}
	}
}

// A version stored before references existed is served exactly as the
// increment 1 API served it.
func TestVersionsFromBeforeReferencesReadBackUnchanged(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "polaroid.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"0001_procedures.sql", "0002_bindings.sql"} {
		script, err := os.ReadFile(filepath.Join("..", "..", "storage", "sqlite", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, string(script)); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		`PRAGMA user_version = 2`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('p1', 'go.dependency.add', '2026-10-09T12:00:00.123456789Z')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		 VALUES ('p1', 1, 'p', 'm', '{"inputs":{}}', '{"steps":["one"]}', 'r', '2026-10-09T12:00:00.123456789Z')`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()

	s := newTestServerAt(t, path)
	const v1 = `{"procedure_id":"p1","version":1,"philosophy":"p","method":"m","contract":{"inputs":{}},"instructions":{"steps":["one"]},"revision_reason":"r","created_at":"2026-10-09T12:00:00.123456789Z"}`
	if got := s.call(t, http.MethodGet, "/v1/procedures/p1/versions/1", ""); string(got.body) != v1+"\n" {
		t.Fatalf("version 1 =\n %s\nwant\n %s", got.body, v1)
	}
	const history = `{"id":"p1","canonical_key":"go.dependency.add","created_at":"2026-10-09T12:00:00.123456789Z","latest_version":1,"versions":[` + v1 + `]}`
	if got := s.call(t, http.MethodGet, "/v1/procedures/p1", ""); string(got.body) != history+"\n" {
		t.Fatalf("history =\n %s\nwant\n %s", got.body, history)
	}
}
