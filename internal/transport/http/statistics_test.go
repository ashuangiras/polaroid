package http_test

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// The statistics shape of ADR-0036, restated. Maps are compared with their
// complete expected key sets, so a missing or extra key fails.

type payloadJSON struct {
	Count      int64 `json:"count"`
	TotalBytes int64 `json:"total_bytes"`
	MeanBytes  int64 `json:"mean_bytes"`
	MaxBytes   int64 `json:"max_bytes"`
}

type fileJSON struct {
	Present bool   `json:"present"`
	Bytes   *int64 `json:"bytes"`
}

type statisticsJSON struct {
	StatisticsVersion int              `json:"statistics_version"`
	GeneratedAt       time.Time        `json:"generated_at"`
	Records           map[string]int64 `json:"records"`
	Procedures        struct {
		ByLifecycle map[string]int64 `json:"by_lifecycle"`
		ByScope     map[string]int64 `json:"by_scope"`
	} `json:"procedures"`
	Executions struct {
		ByOutcome                 map[string]int64 `json:"by_outcome"`
		ByEvidenceStatus          map[string]int64 `json:"by_evidence_status"`
		InvalidatedByOutcome      map[string]int64 `json:"invalidated_by_outcome"`
		ValidWithInvalidatedChild int64            `json:"valid_with_invalidated_child"`
	} `json:"executions"`
	ToilItems struct {
		ByStatus map[string]int64 `json:"by_status"`
	} `json:"toil_items"`
	Payloads map[string]payloadJSON `json:"payloads"`
	Storage  struct {
		SchemaVersion int64               `json:"schema_version"`
		PageSize      int64               `json:"page_size"`
		PageCount     int64               `json:"page_count"`
		FreelistCount int64               `json:"freelist_count"`
		DatabaseBytes int64               `json:"database_bytes"`
		Files         map[string]fileJSON `json:"files"`
	} `json:"storage"`
}

var payloadCategories = []string{"procedure_contracts", "procedure_instructions", "reference_inputs", "binding_revision_inputs",
	"execution_inputs", "execution_evidence", "execution_environment_attributes", "decision_evidence", "feedback_contexts",
	"toil_assessments", "toil_proposals"}

func (s *testServer) statistics(t *testing.T) statisticsJSON {
	t.Helper()
	resp := s.call(t, http.MethodGet, "/v1/statistics", "")
	expect(t, resp, http.StatusOK, "")
	return decodeStrict[statisticsJSON](t, resp.body)
}

func zeroCounts(keys ...string) map[string]int64 {
	m := map[string]int64{}
	for _, k := range keys {
		m[k] = 0
	}
	return m
}

func counts(base map[string]int64, kv map[string]int64) map[string]int64 {
	out := maps.Clone(base)
	maps.Copy(out, kv)
	return out
}

var (
	recordKinds = zeroCounts("procedures", "procedure_versions", "procedure_references", "procedure_origins", "procedure_lifecycle_events",
		"repositories", "repository_aliases", "bindings", "binding_revisions", "executions", "execution_children", "execution_decisions",
		"execution_invalidations", "feedback", "toil_items", "toil_revisions", "toil_revision_sources", "toil_revision_executions")
	lifecycleKeys = zeroCounts("active", "retired")
	scopeKeys     = zeroCounts("shared", "local", "unspecified")
	outcomeKeys   = zeroCounts("succeeded", "failed")
	evidenceKeys  = zeroCounts("valid", "invalidated")
	toilKeys      = zeroCounts("observed", "candidate", "implemented", "validated", "retired")
)

func sizeOf(values []jsontext.Value) payloadJSON {
	var p payloadJSON
	for _, v := range values {
		if v == nil {
			continue
		}
		p.Count++
		p.TotalBytes += int64(len(v))
		p.MaxBytes = max(p.MaxBytes, int64(len(v)))
	}
	if p.Count > 0 {
		p.MeanBytes = p.TotalBytes / p.Count
	}
	return p
}

// TestStatisticsOfAnEmptyCatalog: every count and payload is zero, every
// key is present, and the storage is described.
func TestStatisticsOfAnEmptyCatalog(t *testing.T) {
	s := newTestServer(t)
	before := time.Now().UTC().Add(-time.Second)
	st := s.statistics(t)
	if st.StatisticsVersion != 1 || st.GeneratedAt.Before(before) || st.GeneratedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("version %d, generated_at %v", st.StatisticsVersion, st.GeneratedAt)
	}
	switch {
	case !maps.Equal(st.Records, recordKinds):
		t.Fatalf("records = %v", st.Records)
	case !maps.Equal(st.Procedures.ByLifecycle, lifecycleKeys) || !maps.Equal(st.Procedures.ByScope, scopeKeys):
		t.Fatalf("procedures = %+v", st.Procedures)
	case !maps.Equal(st.Executions.ByOutcome, outcomeKeys) || !maps.Equal(st.Executions.ByEvidenceStatus, evidenceKeys) ||
		!maps.Equal(st.Executions.InvalidatedByOutcome, outcomeKeys) || st.Executions.ValidWithInvalidatedChild != 0:
		t.Fatalf("executions = %+v", st.Executions)
	case !maps.Equal(st.ToilItems.ByStatus, toilKeys):
		t.Fatalf("toil items = %+v", st.ToilItems)
	}
	want := map[string]payloadJSON{}
	for _, c := range payloadCategories {
		want[c] = payloadJSON{}
	}
	if !maps.Equal(st.Payloads, want) {
		t.Fatalf("payloads = %+v", st.Payloads)
	}
	g := st.Storage
	if g.SchemaVersion != 10 || g.PageSize <= 0 || g.PageCount <= 0 || g.DatabaseBytes != g.PageSize*g.PageCount || len(g.Files) != 3 {
		t.Fatalf("storage = %+v", g)
	}
	for _, name := range []string{"database", "wal", "shm"} {
		if f := g.Files[name]; !f.Present || f.Bytes == nil || *f.Bytes < 0 {
			t.Fatalf("file %s = %+v", name, f)
		}
	}
	if !strings.Contains(string(s.call(t, http.MethodGet, "/v1/statistics", "").body), `"shm":{"present":true,"bytes":`) {
		t.Fatal("file sizes are not {present, bytes}")
	}
}

// Unicode in a stored JSON text: é literally (2 bytes), escaped (6 bytes)
// and an emoji (4 bytes).
const unicodeContract = `{"name":"é","escaped":"\u00e9","emoji":"😀"}`

// statisticsWorld adds to the toil world: a procedure whose scope changes
// in version 2, a parent with two pinned references and a conditional one,
// two runs of it (one valid with two invalidated children, one itself
// invalidated), a retired and a reactivated procedure, an origin, a binding
// with two revisions, feedback without context and a toil item revised from
// observed to candidate.
type statisticsWorld struct {
	toilWorld
	shared, parent, parentRun, binding, feedback, toil string
	invalidated                                        []string
}

func newStatisticsWorld(t *testing.T, s *testServer) statisticsWorld {
	t.Helper()
	w := statisticsWorld{toilWorld: newToilWorld(t, s)}
	w.shared = idOf(t, s.mustPost(t, "/v1/procedures", `{"canonical_key":"p.shared","version":{"philosophy":"p","method":"m",`+
		`"contract":`+unicodeContract+`,"instructions":{"steps":["s"]},"revision_reason":"r"}}`))
	s.mustPost(t, "/v1/procedures/"+w.shared+"/versions", reviseWith(1, `"applicability":{"shared":{}},`))
	ref := func(name, extra string) string {
		return `{"name":"` + name + `","procedure_id":"` + w.shared + `","version_policy":{"pin":1},"inputs":{}` + extra + `}`
	}
	w.parent = idOf(t, s.mustPost(t, "/v1/procedures", `{"canonical_key":"p.parent","version":{"philosophy":"p","method":"m",`+
		`"contract":{},"instructions":{"steps":["s"]},"references":[`+ref("a", "")+`,`+ref("b", "")+`,`+
		ref("c", `,"condition":"Only when asked."`)+`],"revision_reason":"r"}}`))
	s.mustPost(t, "/v1/procedures/"+w.parent+"/origin", `{"repository_id":"`+w.repoA+`","reason":"Written for A."}`)

	run := func(procedure, evidence, links string) string {
		body := runBody(procedure, 1, "github.com/o/a", links)
		if evidence != "" {
			body = strings.Replace(body, `{"exit":0}`, evidence, 1)
		}
		return idOf(t, s.mustPost(t, "/v1/executions", body))
	}
	childA := run(w.shared, `{"log":"ünïcödé ✓"}`, "")
	childB, childC, childD := run(w.shared, "", ""), run(w.shared, "", ""), run(w.shared, "", "")
	w.parentRun = run(w.parent, "", `,"children":`+childrenJSON("a", childA, "b", childB)+
		`,"decisions":[{"reference":"c","applicable":false,"rationale":"Not asked.","evidence":{"note":"ça"}}]`)
	parentRun2 := run(w.parent, "", `,"children":`+childrenJSON("a", childC, "b", childD)+
		`,"decisions":[{"reference":"c","applicable":false,"rationale":"Not asked."}]`)
	w.invalidated = []string{childA, childB, childC, parentRun2, w.failedA}

	w.binding = s.bind(t, "github.com/o/a", "verify", w.shared, `{"pin": 1}`).ID
	s.mustPost(t, "/v1/bindings/"+w.binding+"/revisions", reviseBindingBody(1, `{"pin": 2}`))
	w.feedback = idOf(t, s.mustPost(t, "/v1/feedback", feedbackRequest(map[string]string{"context": "-"})))
	w.toil = s.toilItem(t, "toil.classify", w.content()).ID
	s.reviseToil(t, w.toil, 1, w.content("status", "candidate", "proposal", toilProposal, "revision_reason", "Proposed a tool."))

	s.mustPost(t, "/v1/procedures/"+w.phases+"/retirement", lifecycleBody(0, "Superseded."))
	s.mustPost(t, "/v1/procedures/"+w.steps+"/retirement", lifecycleBody(0, "Paused."))
	s.mustPost(t, "/v1/procedures/"+w.steps+"/reactivation", lifecycleBody(1, "Resumed."))
	return w
}

func (w statisticsWorld) invalidate(t *testing.T, s *testServer) {
	t.Helper()
	for _, id := range w.invalidated {
		s.mustPost(t, "/v1/executions/"+id+"/invalidation", invalidateBody)
	}
}

// payloads measures every stored JSON value as reads return it.
func (w statisticsWorld) payloads(t *testing.T, s *testServer) map[string]payloadJSON {
	t.Helper()
	get := func(path string) []byte {
		t.Helper()
		resp := s.call(t, http.MethodGet, path, "")
		expect(t, resp, http.StatusOK, "")
		return resp.body
	}
	values := map[string][]jsontext.Value{}
	for _, id := range []string{w.steps, w.phases, w.localB, w.shared, w.parent} {
		for _, raw := range decodeStrict[historyJSON](t, get("/v1/procedures/"+id)).Versions {
			v := decodeStrict[versionJSON](t, raw)
			values["procedure_contracts"] = append(values["procedure_contracts"], v.Contract)
			values["procedure_instructions"] = append(values["procedure_instructions"], v.Instructions)
			var refs []struct {
				Inputs jsontext.Value `json:"inputs"`
			}
			if v.References != nil {
				if err := json.Unmarshal(v.References, &refs); err != nil {
					t.Fatal(err)
				}
			}
			for _, r := range refs {
				values["reference_inputs"] = append(values["reference_inputs"], r.Inputs)
			}
		}
	}
	page := decodeStrict[struct {
		Executions []executionJSON `json:"executions"`
	}](t, get("/v1/executions"))
	for _, summary := range page.Executions {
		e := decodeStrict[executionJSON](t, get("/v1/executions/"+summary.ID))
		values["execution_inputs"] = append(values["execution_inputs"], e.Inputs)
		values["execution_evidence"] = append(values["execution_evidence"], e.Evidence)
		values["execution_environment_attributes"] = append(values["execution_environment_attributes"], e.Environment.Attributes)
		for _, d := range e.Decisions {
			values["decision_evidence"] = append(values["decision_evidence"], d.Evidence)
		}
	}
	for _, raw := range decodeStrict[bindingHistoryJSON](t, get("/v1/bindings/"+w.binding)).Revisions {
		values["binding_revision_inputs"] = append(values["binding_revision_inputs"], decodeStrict[bindingRevisionJSON](t, raw).Inputs)
	}
	values["feedback_contexts"] = []jsontext.Value{decodeStrict[feedbackJSON](t, get("/v1/feedback/"+w.feedback)).Context}
	for _, r := range decodeStrict[toilHistoryJSON](t, get("/v1/toil-items/"+w.toil)).Revisions {
		values["toil_assessments"] = append(values["toil_assessments"], r.Assessment)
		values["toil_proposals"] = append(values["toil_proposals"], r.Proposal)
	}
	out := map[string]payloadJSON{}
	for _, c := range payloadCategories {
		out[c] = sizeOf(values[c])
	}
	return out
}

// TestStatisticsCountIdentitiesHistoryAndLatestState: identities are
// counted apart from their revisions; lifecycle, scope and toil status come
// from the latest event, version and revision; recorded outcomes stay
// visible after invalidation, apart from evidence status (ADR-0036).
func TestStatisticsCountIdentitiesHistoryAndLatestState(t *testing.T) {
	s := newTestServer(t)
	w := newStatisticsWorld(t, s)
	before := s.statistics(t)
	w.invalidate(t, s)
	st := s.statistics(t)

	wantRecords := counts(recordKinds, map[string]int64{
		"procedures": 5, "procedure_versions": 6, "procedure_references": 3, "procedure_origins": 1, "procedure_lifecycle_events": 3,
		"repositories": 2, "repository_aliases": 1, "bindings": 1, "binding_revisions": 2,
		"executions": 9, "execution_children": 4, "execution_decisions": 2, "execution_invalidations": 5,
		"feedback": 1, "toil_items": 1, "toil_revisions": 2, "toil_revision_sources": 2, "toil_revision_executions": 2,
	})
	if !maps.Equal(st.Records, wantRecords) {
		t.Errorf("records = %v\nwant %v", st.Records, wantRecords)
	}
	if want := counts(lifecycleKeys, map[string]int64{"active": 4, "retired": 1}); !maps.Equal(st.Procedures.ByLifecycle, want) {
		t.Errorf("by_lifecycle = %v, want %v", st.Procedures.ByLifecycle, want)
	}
	if want := counts(scopeKeys, map[string]int64{"shared": 1, "local": 1, "unspecified": 3}); !maps.Equal(st.Procedures.ByScope, want) {
		t.Errorf("by_scope = %v, want %v", st.Procedures.ByScope, want)
	}
	if want := counts(toilKeys, map[string]int64{"candidate": 1}); !maps.Equal(st.ToilItems.ByStatus, want) {
		t.Errorf("toil by_status = %v, want %v", st.ToilItems.ByStatus, want)
	}

	outcomes := map[string]int64{"succeeded": 8, "failed": 1}
	if !maps.Equal(before.Executions.ByOutcome, outcomes) || !maps.Equal(st.Executions.ByOutcome, outcomes) {
		t.Errorf("by_outcome before %v, after %v, want %v both times", before.Executions.ByOutcome, st.Executions.ByOutcome, outcomes)
	}
	if want := map[string]int64{"valid": 9, "invalidated": 0}; !maps.Equal(before.Executions.ByEvidenceStatus, want) || before.Executions.ValidWithInvalidatedChild != 0 {
		t.Errorf("before invalidation: %+v", before.Executions)
	}
	if want := map[string]int64{"valid": 4, "invalidated": 5}; !maps.Equal(st.Executions.ByEvidenceStatus, want) {
		t.Errorf("by_evidence_status = %v, want %v", st.Executions.ByEvidenceStatus, want)
	}
	if want := map[string]int64{"succeeded": 4, "failed": 1}; !maps.Equal(st.Executions.InvalidatedByOutcome, want) {
		t.Errorf("invalidated_by_outcome = %v, want %v", st.Executions.InvalidatedByOutcome, want)
	}
	// Only the valid parent counts, once for its two invalidated children;
	// the invalidated parent does not.
	if st.Executions.ValidWithInvalidatedChild != 1 {
		t.Errorf("valid_with_invalidated_child = %d, want 1", st.Executions.ValidWithInvalidatedChild)
	}
	if v, _ := s.verification(t, w.parentRun); v.Verified || v.EvidenceStatus != "valid" || v.Problems[0].Code != "child_invalidated" {
		t.Errorf("the counted parent's verification = %+v", v)
	}

	// Payloads are the UTF-8 bytes of the stored JSON text, which reads
	// return, except that a response writes an escaped character as itself:
	// the contract's "\u00e9" is stored as 6 bytes and returned as 2. An
	// empty object counts, an absent value does not.
	read := w.payloads(t, s)
	readContracts := read["procedure_contracts"]
	read["procedure_contracts"] = payloadJSON{Count: readContracts.Count, TotalBytes: readContracts.TotalBytes + 4,
		MeanBytes: (readContracts.TotalBytes + 4) / readContracts.Count, MaxBytes: readContracts.MaxBytes + 4}
	if !maps.Equal(st.Payloads, read) {
		t.Errorf("payloads = %+v\nwant %+v", st.Payloads, read)
	}
	if utf8.RuneCountInString(unicodeContract) == len(unicodeContract) {
		t.Fatal("the Unicode contract has no multi-byte character")
	}
	if c := st.Payloads["procedure_contracts"]; c != (payloadJSON{Count: 6, TotalBytes: int64(len(unicodeContract)) + 5*2,
		MeanBytes: (int64(len(unicodeContract)) + 10) / 6, MaxBytes: int64(len(unicodeContract))}) {
		t.Errorf("procedure_contracts = %+v", c)
	}
	if f := st.Payloads["feedback_contexts"]; f != (payloadJSON{Count: 1, TotalBytes: 2, MeanBytes: 2, MaxBytes: 2}) {
		t.Errorf("feedback_contexts = %+v", f)
	}
	if p, d := st.Payloads["toil_proposals"], st.Payloads["decision_evidence"]; p.Count != 1 || d.Count != 1 {
		t.Errorf("absent values counted: toil_proposals %+v, decision_evidence %+v", p, d)
	}
	if e := st.Payloads["execution_evidence"]; e.MaxBytes != int64(len(`{"log":"ünïcödé ✓"}`)) {
		t.Errorf("execution_evidence = %+v", e)
	}
}

// TestStatisticsFilesEncoding: an absent file has no bytes member; a
// present empty file has bytes 0.
func TestStatisticsFilesEncoding(t *testing.T) {
	var st memory.Statistics
	st.Storage.Files = memory.StorageFiles{Database: memory.FileSize{Present: true, Bytes: 4096}, WAL: memory.FileSize{Present: true}}
	b, err := json.Marshal(wire.NewStatistics(st))
	if err != nil {
		t.Fatal(err)
	}
	if want := `"files":{"database":{"present":true,"bytes":4096},"wal":{"present":true,"bytes":0},"shm":{"present":false}}`; !strings.Contains(string(b), want) {
		t.Fatalf("%s lacks %s", b, want)
	}
}

// TestStatisticsChangeNothing: statistics requests store nothing and change
// no lifecycle state; they take no parameters and only GET.
func TestStatisticsChangeNothing(t *testing.T) {
	s := newTestServer(t)
	w := newStatisticsWorld(t, s)
	w.invalidate(t, s)
	paths := []string{"/v1/procedures?lifecycle=all", "/v1/executions", "/v1/toil-items", "/v1/feedback",
		"/v1/procedures/" + w.phases + "/lifecycle/events", "/v1/procedures/" + w.steps + "/lifecycle/events",
		"/v1/executions/" + w.parentRun + "/verification"}
	read := func() []string {
		var out []string
		for _, p := range paths {
			resp := s.call(t, http.MethodGet, p, "")
			expect(t, resp, http.StatusOK, "")
			out = append(out, string(resp.body))
		}
		return out
	}
	stripped := func(st statisticsJSON) statisticsJSON {
		st.GeneratedAt, st.Storage.Files = time.Time{}, nil
		return st
	}
	listed := read()
	first := s.statistics(t)
	for range 3 {
		if again := s.statistics(t); encode(t, stripped(again)) != encode(t, stripped(first)) {
			t.Fatalf("statistics changed without a write:\n%s\n%s", encode(t, again), encode(t, first))
		}
	}
	if after := read(); strings.Join(after, "\n") != strings.Join(listed, "\n") {
		t.Fatalf("records changed:\n%v\nbefore:\n%v", after, listed)
	}

	expect(t, s.call(t, http.MethodGet, "/v1/statistics?procedure_id="+w.parent, ""), http.StatusBadRequest, "invalid_request")
	resp := s.call(t, http.MethodPost, "/v1/statistics", "{}")
	expect(t, resp, http.StatusMethodNotAllowed, "method_not_allowed")
	if allow := resp.header.Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow = %q", allow)
	}
}
