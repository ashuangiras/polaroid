package http_test

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
	httptransport "github.com/ashuangiras/polaroid/internal/transport/http"
)

// The write-limit shapes of ADR-0037, restated.

type limitViolationJSON struct {
	Field    string `json:"field"`
	Bytes    int    `json:"bytes"`
	MaxBytes int    `json:"max_bytes"`
}

type fieldLimitJSON struct {
	Record   string `json:"record"`
	Field    string `json:"field"`
	Kind     string `json:"kind"`
	MaxBytes int    `json:"max_bytes"`
}

type writeLimitsJSON struct {
	PolicyVersion   int              `json:"policy_version"`
	WriteMaxBytes   int              `json:"write_max_bytes"`
	RequestMaxBytes int              `json:"request_max_bytes"`
	Fields          []fieldLimitJSON `json:"fields"`
}

// objectOfSize is a compact JSON object of exactly n bytes; filler marks its
// content so a test can check that errors never echo it.
func objectOfSize(n int) string {
	return `{"a":"` + strings.Repeat("Q", n-8) + `"}`
}

const filler = "QQQQQQQQ"

func (s *testServer) tooLarge(t *testing.T, method, path, body string, want ...limitViolationJSON) {
	t.Helper()
	resp := s.call(t, method, path, body)
	expect(t, resp, http.StatusRequestEntityTooLarge, "value_too_large")
	if got := errorOf(t, resp).Limits; !slices.Equal(got, want) {
		t.Fatalf("%s %s: limits %+v, want %+v", method, path, got, want)
	}
	if strings.Contains(string(resp.body), filler) {
		t.Fatalf("the error echoes the rejected content: %.300s", resp.body)
	}
}

// TestWriteLimitsAreReportedAsEnforced: the statistics report the policy
// that validation enforces, and the reported limit is the boundary.
func TestWriteLimitsAreReportedAsEnforced(t *testing.T) {
	s := newTestServer(t)
	p := s.create(t).ID
	got := s.statistics(t).WriteLimits
	want := writeLimitsJSON{PolicyVersion: 1, WriteMaxBytes: memory.MaxWriteBytes, RequestMaxBytes: httptransport.MaxRequestBytes}
	for _, l := range memory.WriteLimits() {
		want.Fields = append(want.Fields, fieldLimitJSON{Record: l.Record, Field: l.Field, Kind: string(l.Kind), MaxBytes: l.MaxBytes})
	}
	if encode(t, got) != encode(t, want) || len(got.Fields) != 37 {
		t.Fatalf("write_limits = %s\nwant %s", encode(t, got), encode(t, want))
	}
	i := slices.IndexFunc(got.Fields, func(f fieldLimitJSON) bool { return f.Record == "execution" && f.Field == "evidence" })
	limit := got.Fields[i].MaxBytes
	// Whitespace is not counted: this body is larger than the limit, its
	// compacted evidence exactly at it.
	spaced := "{\n    \"a\"  :  \"" + strings.Repeat("Q", limit-8) + "\"\n}"
	s.mustPost(t, "/v1/executions", executionRequest(p, 1, map[string]string{"evidence": spaced}))
	s.tooLarge(t, http.MethodPost, "/v1/executions", executionRequest(p, 1, map[string]string{"evidence": objectOfSize(limit + 1)}),
		limitViolationJSON{Field: "evidence", Bytes: limit + 1, MaxBytes: limit})
	if n := s.statistics(t).Records["executions"]; n != 1 {
		t.Fatalf("%d executions stored, want 1", n)
	}
}

// TestOversizedWritesStoreNothing: across record families, a refused write
// names every field over its limit, or the whole write, and stores no
// identity, version, revision, event or link.
func TestOversizedWritesStoreNothing(t *testing.T) {
	s := newTestServer(t)
	w := newToilWorld(t, s)
	binding := s.bind(t, "github.com/o/a", "verify", w.steps, `{"pin": 1}`).ID
	before := s.statistics(t)

	text := strings.Repeat("Q", memory.MaxTextBytes+1)
	s.tooLarge(t, http.MethodPost, "/v1/procedures", `{"canonical_key":"big.one","origin":{"repository_id":"`+w.repoA+`","reason":"`+text+`"},`+
		`"version":{"philosophy":"p","method":"`+text+`","contract":`+objectOfSize(memory.MaxDocumentBytes+2)+`,"instructions":{},"revision_reason":"r"}}`,
		limitViolationJSON{"version.method", memory.MaxTextBytes + 1, memory.MaxTextBytes},
		limitViolationJSON{"version.contract", memory.MaxDocumentBytes + 2, memory.MaxDocumentBytes},
		limitViolationJSON{"origin.reason", memory.MaxTextBytes + 1, memory.MaxTextBytes})
	s.tooLarge(t, http.MethodPost, "/v1/bindings/"+binding+"/revisions",
		`{"base_revision":1,"revision":{"inputs":`+objectOfSize(memory.MaxObjectBytes+1)+`,"version_policy":{"pin":1},"revision_reason":"r"}}`,
		limitViolationJSON{"revision.inputs", memory.MaxObjectBytes + 1, memory.MaxObjectBytes})
	s.tooLarge(t, http.MethodPost, "/v1/feedback", feedbackRequest(map[string]string{"details": `"` + text + `"`}),
		limitViolationJSON{"details", memory.MaxTextBytes + 1, memory.MaxTextBytes})
	s.tooLarge(t, http.MethodPost, "/v1/procedures/"+w.phases+"/retirement", lifecycleBody(0, text),
		limitViolationJSON{"reason", memory.MaxTextBytes + 1, memory.MaxTextBytes})
	s.tooLarge(t, http.MethodPost, "/v1/executions/"+w.runA+"/invalidation", `{"reason":"`+text+`","actor":"a.b"}`,
		limitViolationJSON{"reason", memory.MaxTextBytes + 1, memory.MaxTextBytes})
	s.tooLarge(t, http.MethodPost, "/v1/procedures/"+w.steps+"/origin", `{"repository_id":"`+w.repoA+`","reason":"`+text+`"}`,
		limitViolationJSON{"origin.reason", memory.MaxTextBytes + 1, memory.MaxTextBytes})
	line := strings.Repeat("Q", memory.MaxLineBytes+1)
	s.tooLarge(t, http.MethodPost, "/v1/repositories", `{"identifier":"github.com/o/c","name":"`+line+`"}`,
		limitViolationJSON{"name", memory.MaxLineBytes + 1, memory.MaxLineBytes})
	s.tooLarge(t, http.MethodPost, "/v1/repositories/"+w.repoA+"/aliases", `{"identifier":"mirror.example/o/c","reason":"`+text+`"}`,
		limitViolationJSON{"reason", memory.MaxTextBytes + 1, memory.MaxTextBytes})

	// A nested entry of a collection is named by its indices.
	notes := w.content("observations", []any{map[string]any{"execution_id": w.runA, "note": "short"},
		map[string]any{"execution_id": w.failedA, "note": text}})
	s.tooLarge(t, http.MethodPost, "/v1/toil-items", encode(t, createToil("toil.big", notes)),
		limitViolationJSON{"revision.observations[1].note", memory.MaxTextBytes + 1, memory.MaxTextBytes})

	// 130 steps of 2,048 bytes each are within their limit, not together.
	var steps []string
	for i := range 130 {
		steps = append(steps, fmt.Sprintf("%04d", i)+strings.Repeat("Q", memory.MaxLineBytes-4))
	}
	many := w.content("sources", w.source(w.steps, steps...))
	content := w.content()
	var total int
	for _, f := range []string{content["activity"].(string), "Seen in recorded runs.", "First assessment.", "classified 4 files by hand"} {
		total += len(f)
	}
	total += len(encode(t, toilClaims)) + 130*memory.MaxLineBytes
	s.tooLarge(t, http.MethodPost, "/v1/toil-items", encode(t, createToil("toil.many", many)),
		limitViolationJSON{"*", total, memory.MaxWriteBytes})

	after := s.statistics(t)
	if encode(t, after.Records) != encode(t, before.Records) || encode(t, after.Payloads) != encode(t, before.Payloads) {
		t.Fatalf("a refused write stored something:\n%s\n%s", encode(t, after.Records), encode(t, before.Records))
	}
	if l := decodeStrict[lifecycleJSON](t, s.call(t, http.MethodGet, "/v1/procedures/"+w.phases+"/lifecycle/events", "").body); l.Status != "active" {
		t.Fatalf("lifecycle = %+v", l)
	}

	// The transport's body limit still applies first, to the raw bytes.
	resp := s.call(t, http.MethodPost, "/v1/executions", executionRequest(w.steps, 1, map[string]string{"evidence": objectOfSize(httptransport.MaxRequestBytes)}))
	expect(t, resp, http.StatusRequestEntityTooLarge, "request_too_large")
}

// TestOversizedHistoryStaysUsable: records an earlier build stored above the
// limits read and verify as before; any new write is checked, including one
// that repeats a stored value, and a retry of the original create is
// value_too_large (ADR-0037).
func TestOversizedHistoryStaysUsable(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	big := jsontext.Value(objectOfSize(memory.MaxDocumentBytes + 100))
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	def := memory.Definition{Philosophy: "p", Method: "m", Contract: big, Instructions: jsontext.Value(`{}`), RevisionReason: "Before the limits."}
	if err := s.store.CreateProcedure(ctx, memory.Procedure{ID: "old-p", CanonicalKey: "old.big", CreatedAt: at, LatestVersion: 1},
		memory.Version{ProcedureID: "old-p", Number: 1, CreatedAt: at, Definition: def}); err != nil {
		t.Fatal(err)
	}
	run := memory.Execution{ID: "old-e", CreatedAt: at, ExecutionRecord: memory.ExecutionRecord{ProcedureID: "old-p", Version: 1,
		Repository: "github.com/o/a", Commit: fullCommit, Environment: memory.Environment{Name: "ci", Attributes: jsontext.Value(`{}`)},
		Inputs: jsontext.Value(`{}`), Outcome: memory.OutcomeSucceeded, Evidence: big}}
	if err := s.store.CreateExecution(ctx, &run); err != nil {
		t.Fatal(err)
	}

	h := decodeStrict[historyJSON](t, s.call(t, http.MethodGet, "/v1/procedures/old-p", "").body)
	if v := decodeStrict[versionJSON](t, h.Versions[0]); len(v.Contract) != len(big) {
		t.Fatalf("stored contract reads back as %d bytes", len(v.Contract))
	}
	if v, _ := s.verification(t, "old-e"); !v.Verified {
		t.Fatalf("the oversized execution no longer verifies: %+v", v)
	}
	if st := s.statistics(t); st.Payloads["execution_evidence"].MaxBytes != int64(len(big)) {
		t.Fatalf("statistics measure %+v", st.Payloads["execution_evidence"])
	}

	over := limitViolationJSON{"version.contract", len(big), memory.MaxDocumentBytes}
	create := `{"canonical_key":"old.big","version":{"philosophy":"p","method":"m","contract":` + string(big) + `,"instructions":{},"revision_reason":"Before the limits."}}`
	s.tooLarge(t, http.MethodPost, "/v1/procedures", create, over)
	s.tooLarge(t, http.MethodPost, "/v1/procedures/old-p/versions",
		`{"base_version":1,"version":{"philosophy":"p","method":"m","contract":`+string(big)+`,"instructions":{},"revision_reason":"Unchanged contract."}}`, over)
	s.mustPost(t, "/v1/procedures/old-p/versions", `{"base_version":1,"version":{"philosophy":"p","method":"m","contract":{"log":"see the run"},"instructions":{},"revision_reason":"Shortened."}}`)
	s.mustPost(t, "/v1/executions/old-e/invalidation", invalidateBody)
	h = decodeStrict[historyJSON](t, s.call(t, http.MethodGet, "/v1/procedures/old-p", "").body)
	if v := decodeStrict[versionJSON](t, h.Versions[0]); len(h.Versions) != 2 || len(v.Contract) != len(big) {
		t.Fatalf("history after the revision: %d versions", len(h.Versions))
	}
}
