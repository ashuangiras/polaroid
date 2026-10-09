package http_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// record stores an execution and returns its ID.
func (s *testServer) record(t *testing.T, procedureID string, version int, fields map[string]string) string {
	t.Helper()
	resp := s.call(t, http.MethodPost, "/v1/executions", executionRequest(procedureID, version, fields))
	expect(t, resp, http.StatusCreated, "")
	return decodeStrict[executionJSON](t, resp.body).ID
}

func childrenJSON(pairs ...string) string {
	var items []string
	for i := 0; i+1 < len(pairs); i += 2 {
		items = append(items, fmt.Sprintf(`{"reference": %q, "execution_id": %q}`, pairs[i], pairs[i+1]))
	}
	return "[" + strings.Join(items, ", ") + "]"
}

// composedFixture is a child procedure at versions 1 and 2, and a parent
// procedure referencing it pinned at 2 ("pinned") and contextually
// ("latest"), with child executions at each version.
type composedFixture struct {
	child, parent, childV2Run, childV1Run string
}

func newComposedFixture(t *testing.T, s *testServer) composedFixture {
	t.Helper()
	child := s.create(t).ID
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+child+"/versions", reviseBody(1, "v2")), http.StatusCreated, "")
	parent := s.createWith(t, "compose.parent", "["+pinRef("pinned", child, 2)+", "+latestRef("latest", child)+"]")
	return composedFixture{
		child:      child,
		parent:     parent,
		childV2Run: s.record(t, child, 2, nil),
		childV1Run: s.record(t, child, 1, nil),
	}
}

func TestParentExecutionLinksItsChildren(t *testing.T) {
	s := newTestServer(t)
	f := newComposedFixture(t, s)

	created := s.call(t, http.MethodPost, "/v1/executions", executionRequest(f.parent, 1,
		map[string]string{"children": childrenJSON("pinned", f.childV2Run, "latest", f.childV1Run)}))
	expect(t, created, http.StatusCreated, "")
	want := fmt.Sprintf(`"children":[{"reference":"pinned","execution_id":%q},{"reference":"latest","execution_id":%q}],"created_at"`, f.childV2Run, f.childV1Run)
	if !strings.Contains(string(created.body), want) {
		t.Fatalf("parent = %s\nwant it to contain %s", created.body, want)
	}
	parent := decodeStrict[executionJSON](t, created.body)
	if got := s.call(t, http.MethodGet, "/v1/executions/"+parent.ID, ""); !bytes.Equal(got.body, created.body) {
		t.Fatalf("GET differs from the recorded parent:\n%s\n%s", got.body, created.body)
	}

	// Executions without children, and list summaries, carry no children field.
	child := s.call(t, http.MethodGet, "/v1/executions/"+f.childV2Run, "")
	list := s.call(t, http.MethodGet, "/v1/executions?procedure_id="+url.QueryEscape(f.parent), "")
	for _, body := range [][]byte{child.body, list.body} {
		if strings.Contains(string(body), "children") {
			t.Fatalf("unexpected children field: %s", body)
		}
	}
}

func TestChildLinksAreChecked(t *testing.T) {
	s := newTestServer(t)
	f := newComposedFixture(t, s)
	other := s.createWith(t, "other.procedure", "[]")
	otherRun := s.record(t, other, 1, nil)
	elsewhere := s.record(t, f.child, 2, map[string]string{"repository": `"scratch"`})
	laterCommit := s.record(t, f.child, 2, map[string]string{"commit": `"` + strings.Repeat("9", 40) + `"`})
	claimed := s.record(t, f.child, 2, nil)
	s.record(t, f.parent, 1, map[string]string{"children": childrenJSON("pinned", claimed)})

	cases := []struct {
		name, children, field, message string
	}{
		{"unknown reference", childrenJSON("missing", f.childV2Run), "children[0].reference", `no reference named "missing"`},
		{"unknown child", childrenJSON("pinned", "0192f7e4-0000-7000-8000-000000000000"), "children[0].execution_id", "does not exist"},
		{"child of another procedure", childrenJSON("latest", otherRun), "children[0].execution_id", "ran procedure"},
		{"version other than the pin", childrenJSON("pinned", f.childV1Run), "children[0].execution_id", "pins version 2"},
		{"another repository", childrenJSON("latest", elsewhere), "children[0].execution_id", "not the parent's"},
		{"another commit", childrenJSON("latest", laterCommit), "children[0].execution_id", "not the parent's"},
		{"reference fulfilled twice", childrenJSON("latest", f.childV1Run, "latest", f.childV2Run), "children[1].reference", "already fulfilled"},
		{"child listed twice", childrenJSON("pinned", f.childV2Run, "latest", f.childV2Run), "children[1].execution_id", "already listed"},
		{"child of another parent", childrenJSON("latest", f.childV1Run, "pinned", claimed), "children[1].execution_id", "already the child of execution"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.call(t, http.MethodPost, "/v1/executions", executionRequest(f.parent, 1, map[string]string{"children": tc.children}))
			expect(t, resp, http.StatusBadRequest, "invalid_request")
			e := errorOf(t, resp)
			if len(e.Fields) != 1 || e.Fields[0].Field != tc.field || !strings.Contains(e.Fields[0].Message, tc.message) {
				t.Fatalf("fields = %+v, want one %s containing %q", e.Fields, tc.field, tc.message)
			}
		})
	}

	// Only the one valid parent was stored, and the rejected ones claimed nothing.
	list := decodeStrict[struct {
		Executions []executionJSON `json:"executions"`
	}](t, s.call(t, http.MethodGet, "/v1/executions?procedure_id="+url.QueryEscape(f.parent), "").body).Executions
	if len(list) != 1 {
		t.Fatalf("%d parent executions stored, want 1", len(list))
	}
	s.record(t, f.parent, 1, map[string]string{"children": childrenJSON("pinned", f.childV2Run, "latest", f.childV1Run)})
}
