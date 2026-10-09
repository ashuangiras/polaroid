package http_test

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// feedbackJSON restates the documented feedback shape.
type feedbackJSON struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Summary   string         `json:"summary"`
	Details   string         `json:"details"`
	Reporter  string         `json:"reporter"`
	Context   jsontext.Value `json:"context"`
	CreatedAt time.Time      `json:"created_at"`
}

// feedbackRequest returns a report-feedback body. fields overrides or adds
// raw JSON members, and a "-" value removes one.
func feedbackRequest(fields map[string]string) string {
	members := map[string]string{
		"kind":     `"problem"`,
		"summary":  `"record_execution rejected a short commit without saying why"`,
		"details":  `"I passed a 7-character hash.\nThe error named the field but not the rule."`,
		"reporter": `"copilot.vscode"`,
		"context":  `{"tool": "record_execution", "procedure_id": "0192f7e4"}`,
	}
	for k, v := range fields {
		members[k] = v
	}
	var parts []string
	for _, k := range []string{"kind", "summary", "details", "reporter", "context", "id", "status"} {
		if v, ok := members[k]; ok && v != "-" {
			parts = append(parts, fmt.Sprintf("%q: %s", k, v))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func (s *testServer) report(t *testing.T, fields map[string]string) (response, feedbackJSON) {
	t.Helper()
	resp := s.call(t, http.MethodPost, "/v1/feedback", feedbackRequest(fields))
	expect(t, resp, http.StatusCreated, "")
	return resp, decodeStrict[feedbackJSON](t, resp.body)
}

func TestReportAndReadFeedback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polaroid.db")
	s := newTestServerAt(t, path)
	f := newExecutionFixture(t, s)
	expect(t, s.call(t, http.MethodPost, "/v1/executions", executionRequest(f.procedure, 2, nil)), http.StatusCreated, "")
	before := map[string][]byte{}
	others := []string{"/v1/procedures", "/v1/procedures/" + f.procedure, "/v1/bindings/" + f.pinned, "/v1/executions?procedure_id=" + f.procedure}
	for _, p := range others {
		before[p] = s.call(t, http.MethodGet, p, "").body
	}

	created, fb := s.report(t, nil)
	if loc := created.header.Get("Location"); loc != "/v1/feedback/"+fb.ID {
		t.Fatalf("Location = %q", loc)
	}
	want := fmt.Sprintf(`{"id":%q,"kind":"problem","summary":"record_execution rejected a short commit without saying why",`+
		`"details":"I passed a 7-character hash.\nThe error named the field but not the rule.","reporter":"copilot.vscode",`+
		`"context":{"tool":"record_execution","procedure_id":"0192f7e4"},"created_at":%q}`, fb.ID, fb.CreatedAt.Format(time.RFC3339Nano))
	if got := strings.TrimSpace(string(created.body)); got != want {
		t.Fatalf("feedback =\n %s\nwant\n %s", got, want)
	}
	got := s.call(t, http.MethodGet, "/v1/feedback/"+fb.ID, "")
	expect(t, got, http.StatusOK, "")
	if !bytes.Equal(got.body, created.body) {
		t.Fatalf("GET differs from the reported feedback:\n%s\n%s", got.body, created.body)
	}

	// Without context, the report has an empty one.
	_, plain := s.report(t, map[string]string{"kind": `"suggestion"`, "context": "-"})
	if string(plain.Context) != "{}" {
		t.Fatalf("absent context = %s, want {}", plain.Context)
	}

	for _, p := range others {
		if after := s.call(t, http.MethodGet, p, "").body; !bytes.Equal(after, before[p]) {
			t.Fatalf("reporting feedback changed %s:\n%s\n%s", p, before[p], after)
		}
	}

	// The reports survive a restart byte for byte.
	list := s.call(t, http.MethodGet, "/v1/feedback", "").body
	s.Close()
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := newTestServerAt(t, path)
	if after := restarted.call(t, http.MethodGet, "/v1/feedback", "").body; !bytes.Equal(after, list) {
		t.Fatalf("after restart:\n%s\nwant\n%s", after, list)
	}
	if after := restarted.call(t, http.MethodGet, "/v1/feedback/"+fb.ID, "").body; !bytes.Equal(after, created.body) {
		t.Fatalf("after restart, GET = %s", after)
	}
}

func TestInvalidFeedbackIsRejected(t *testing.T) {
	s := newTestServer(t)
	cases := map[string]struct {
		fields map[string]string
		want   []string
	}{
		"unknown kind":          {map[string]string{"kind": `"bug"`}, []string{"kind"}},
		"blank summary":         {map[string]string{"summary": `"  "`}, []string{"summary"}},
		"multi-line summary":    {map[string]string{"summary": `"one\ntwo"`}, []string{"summary"}},
		"blank details":         {map[string]string{"details": `"\n"`}, []string{"details"}},
		"invalid reporter":      {map[string]string{"reporter": `"Copilot Chat"`}, []string{"reporter"}},
		"context not an object": {map[string]string{"context": `"record_execution"`}, []string{"context"}},
		"null context":          {map[string]string{"context": `null`}, []string{"context"}},
		"every field":           {map[string]string{"kind": `""`, "summary": `""`, "details": `""`, "reporter": `""`, "context": `[]`}, []string{"kind", "summary", "details", "reporter", "context"}},
		"server-assigned id":    {map[string]string{"id": `"mine"`}, nil},
		"unknown member":        {map[string]string{"status": `"open"`}, nil},
		"wrong type":            {map[string]string{"summary": `42`}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resp := s.call(t, http.MethodPost, "/v1/feedback", feedbackRequest(tc.fields))
			expect(t, resp, http.StatusBadRequest, "invalid_request")
			var got []string
			for _, p := range errorOf(t, resp).Fields {
				got = append(got, p.Field)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("fields = %v, want %v; body: %s", got, tc.want, resp.body)
			}
		})
	}
	if list := s.call(t, http.MethodGet, "/v1/feedback", ""); string(bytes.TrimSpace(list.body)) != `{"feedback":[]}` {
		t.Fatalf("rejected requests recorded feedback: %s", list.body)
	}
}

func TestListFeedbackQuery(t *testing.T) {
	s := newTestServer(t)
	var ids []string
	for _, kind := range []string{"problem", "suggestion", "problem"} {
		_, fb := s.report(t, map[string]string{"kind": `"` + kind + `"`})
		ids = append(ids, fb.ID)
	}
	list := func(query string) []string {
		resp := s.call(t, http.MethodGet, "/v1/feedback"+query, "")
		expect(t, resp, http.StatusOK, "")
		var got []string
		for _, f := range decodeStrict[struct {
			Feedback []feedbackJSON `json:"feedback"`
		}](t, resp.body).Feedback {
			got = append(got, f.ID)
		}
		return got
	}
	for query, want := range map[string][]string{
		"":                 ids,
		"?kind=problem":    {ids[0], ids[2]},
		"?kind=suggestion": {ids[1]},
	} {
		if got := list(query); !slices.Equal(got, want) {
			t.Errorf("query %q: %v, want %v", query, got, want)
		}
	}

	for query, field := range map[string]string{
		"?kind=bug":                     "kind",
		"?kind=":                        "kind",
		"?kind=problem&kind=suggestion": "kind",
		"?reporter=copilot.vscode":      "",
		"?kind=problem&limit=1":         "",
		"?%zz":                          "",
	} {
		resp := s.call(t, http.MethodGet, "/v1/feedback"+query, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if e := errorOf(t, resp); field != "" && (len(e.Fields) != 1 || e.Fields[0].Field != field) {
			t.Errorf("query %q: fields %+v, want [%s]", query, e.Fields, field)
		}
	}
	expect(t, s.call(t, http.MethodGet, "/v1/feedback/0192f7e4-0000-7000-8000-000000000000", ""), http.StatusNotFound, "not_found")
	expect(t, s.call(t, http.MethodDelete, "/v1/feedback/"+ids[0], ""), http.StatusMethodNotAllowed, "method_not_allowed")
	expect(t, s.call(t, http.MethodPut, "/v1/feedback/"+ids[0], feedbackRequest(nil)), http.StatusMethodNotAllowed, "method_not_allowed")
}
