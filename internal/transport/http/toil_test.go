package http_test

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The toil item shapes of ADR-0034, restated; decoding rejects unknown
// members.

type toilItemJSON struct {
	ID             string         `json:"id"`
	CanonicalKey   string         `json:"canonical_key"`
	CreatedAt      time.Time      `json:"created_at"`
	LatestRevision int            `json:"latest_revision"`
	Activity       string         `json:"activity"`
	Status         string         `json:"status"`
	Scope          string         `json:"scope"`
	Applicability  jsontext.Value `json:"applicability"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type toilRevisionJSON struct {
	ToilItemID      string         `json:"toil_item_id"`
	Revision        int            `json:"revision"`
	Activity        string         `json:"activity"`
	Scope           string         `json:"scope"`
	Applicability   jsontext.Value `json:"applicability"`
	Sources         []toilSource   `json:"sources"`
	Observations    []toilCite     `json:"observations"`
	Assessment      jsontext.Value `json:"assessment"`
	Proposal        jsontext.Value `json:"proposal"`
	Status          string         `json:"status"`
	StatusRationale string         `json:"status_rationale"`
	Replacement     *struct {
		Summary    string    `json:"summary"`
		References []toilRef `json:"references"`
	} `json:"replacement"`
	Validation *struct {
		Rationale  string     `json:"rationale"`
		Executions []toilCite `json:"executions"`
		References []toilRef  `json:"references"`
	} `json:"validation"`
	RevisionReason string    `json:"revision_reason"`
	Author         string    `json:"author"`
	CreatedAt      time.Time `json:"created_at"`
}

type toilSource struct {
	ProcedureID string   `json:"procedure_id"`
	Version     int      `json:"version"`
	Steps       []string `json:"steps"`
}

type toilCite struct {
	ExecutionID string `json:"execution_id"`
	Note        string `json:"note"`
}

type toilRef struct {
	URI  string `json:"uri"`
	Note string `json:"note"`
}

type toilHistoryJSON struct {
	ID             string             `json:"id"`
	CanonicalKey   string             `json:"canonical_key"`
	CreatedAt      time.Time          `json:"created_at"`
	LatestRevision int                `json:"latest_revision"`
	Activity       string             `json:"activity"`
	Status         string             `json:"status"`
	Scope          string             `json:"scope"`
	Applicability  jsontext.Value     `json:"applicability"`
	UpdatedAt      time.Time          `json:"updated_at"`
	Revisions      []toilRevisionJSON `json:"revisions"`
}

type toilListJSON struct {
	ToilItems []toilItemJSON `json:"toil_items"`
	Next      string         `json:"next"`
}

// toilWorld holds the records toil items link to.
type toilWorld struct {
	repoA, repoB string // repository IDs; A also has the alias mirror.example/o/a
	steps        string // a procedure whose version 1 instructions list steps
	phases       string // a procedure whose instructions have no steps member
	localB       string // a procedure local to B
	runA         string // a succeeded run in A, recorded under A's alias
	failedA      string // a failed run in A
	runB         string // a succeeded run in B
}

func newToilWorld(t *testing.T, s *testServer) toilWorld {
	t.Helper()
	w := toilWorld{repoA: s.register(t, "github.com/o/a"), repoB: s.register(t, "github.com/o/b")}
	s.mustPost(t, "/v1/repositories/"+w.repoA+"/aliases", `{"identifier":"mirror.example/o/a","reason":"The mirror of A."}`)
	w.steps = idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("p.steps", "")))
	w.phases = idOf(t, s.mustPost(t, "/v1/procedures", `{"canonical_key":"p.phases","version":{"philosophy":"p","method":"m",`+
		`"contract":{},"instructions":{"phases":[{"name":"build"},{"name":"check"}]},"revision_reason":"r"}}`))
	w.localB = idOf(t, s.mustPost(t, "/v1/procedures", procedureBody("p.local-b", `"applicability":{"repository":"`+w.repoB+`"},`)))
	w.runA = idOf(t, s.mustPost(t, "/v1/executions", runBody(w.steps, 1, "mirror.example/o/a", "")))
	w.failedA = idOf(t, s.mustPost(t, "/v1/executions", strings.Replace(runBody(w.steps, 1, "github.com/o/a", ""), `"succeeded"`, `"failed"`, 1)))
	w.runB = idOf(t, s.mustPost(t, "/v1/executions", runBody(w.localB, 1, "github.com/o/b", "")))
	return w
}

var (
	sharedScope = map[string]any{"shared": map[string]any{}}
	toilClaims  = map[string]any{
		"repetition": map[string]any{"status": "observed", "evidence": "three runs"},
		"burden":     map[string]any{"status": "hypothesis", "would_establish": "timing the activity"},
		"error_rate": map[string]any{"status": "unknown"},
	}
	toilProposal = map[string]any{
		"summary": "A script applies the rules.", "inputs": map[string]any{"base": "commit", "rules": "file"},
		"outputs": "classified files as JSON", "remaining_judgment": []any{"writing the rules"},
		"failure_and_recovery": []any{"fall back to the manual steps"}, "acceptance_criteria": []any{"reproduces recorded ranges"},
	}
	toilReplacement = map[string]any{"summary": "scripts/classify.sh", "references": []any{map[string]any{"uri": "https://example.invalid/pr/1", "note": "the change"}}}
)

func (w toilWorld) source(id string, steps ...string) []any {
	src := map[string]any{"procedure_id": id, "version": 1}
	if steps != nil {
		src["steps"] = steps
	}
	return []any{src}
}

// toilContent is a valid observed revision citing w.steps and run A; kv
// overrides members, a nil value removes one.
func (w toilWorld) content(kv ...any) map[string]any {
	base := map[string]any{
		"activity":         "Classify changed files by hand",
		"applicability":    sharedScope,
		"sources":          w.source(w.steps, "files"),
		"observations":     []any{map[string]any{"execution_id": w.runA, "note": "classified 4 files by hand"}},
		"assessment":       toilClaims,
		"status":           "observed",
		"status_rationale": "Seen in recorded runs.",
		"revision_reason":  "First assessment.",
		"author":           "test.agent",
	}
	return with(base, kv...)
}

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func createToil(key string, content map[string]any) map[string]any {
	return map[string]any{"canonical_key": key, "revision": content}
}

func reviseToil(base int, content map[string]any) map[string]any {
	return map[string]any{"base_revision": base, "revision": content}
}

func (s *testServer) toilItem(t *testing.T, key string, content map[string]any) toilHistoryJSON {
	t.Helper()
	return decodeStrict[toilHistoryJSON](t, s.mustPost(t, "/v1/toil-items", encode(t, createToil(key, content))).body)
}

func (s *testServer) reviseToil(t *testing.T, id string, base int, content map[string]any) toilRevisionJSON {
	t.Helper()
	return decodeStrict[toilRevisionJSON](t, s.mustPost(t, "/v1/toil-items/"+id+"/revisions", encode(t, reviseToil(base, content))).body)
}

func (s *testServer) toilList(t *testing.T, query string) toilListJSON {
	t.Helper()
	resp := s.call(t, http.MethodGet, "/v1/toil-items"+query, "")
	expect(t, resp, http.StatusOK, "")
	return decodeStrict[toilListJSON](t, resp.body)
}

func keysOfToil(l toilListJSON) []string {
	var out []string
	for _, it := range l.ToilItems {
		out = append(out, it.CanonicalKey)
	}
	return out
}

// TestToilItemIdentityHistoryAndConflicts covers creation, retrieval by ID,
// key and revision, exact-key conflicts, stale bases and immutable history
// (ADR-0034).
func TestToilItemIdentityHistoryAndConflicts(t *testing.T) {
	s := newTestServer(t)
	w := newToilWorld(t, s)

	created := s.mustPost(t, "/v1/toil-items", encode(t, createToil("toil.classify", w.content())))
	h := decodeStrict[toilHistoryJSON](t, created.body)
	if got := created.header.Get("Location"); got != "/v1/toil-items/"+h.ID {
		t.Fatalf("Location = %q", got)
	}
	r1 := h.Revisions[0]
	if h.LatestRevision != 1 || h.Status != "observed" || h.Scope != "shared" || len(h.Revisions) != 1 ||
		r1.Revision != 1 || r1.ToilItemID != h.ID || r1.Author != "test.agent" || r1.Proposal != nil ||
		!slices.Equal(r1.Sources[0].Steps, []string{"files"}) || r1.Observations[0].ExecutionID != w.runA || !h.UpdatedAt.Equal(h.CreatedAt) {
		t.Fatalf("created: %s", created.body)
	}
	if encodeCompact(t, r1.Assessment) != encodeCompact(t, toilClaims) {
		t.Fatalf("assessment not kept: %s", r1.Assessment)
	}
	for _, path := range []string{"/v1/toil-items/" + h.ID, "/v1/toil-items/by-key/toil.classify"} {
		got := s.call(t, http.MethodGet, path, "")
		expect(t, got, http.StatusOK, "")
		if string(got.body) != string(created.body) {
			t.Fatalf("GET %s = %s, want the create response", path, got.body)
		}
	}
	expect(t, s.call(t, http.MethodPost, "/v1/toil-items", encode(t, createToil("toil.classify", w.content()))), http.StatusConflict, "canonical_key_exists")
	// A procedure may share the key: the namespaces are separate.
	s.mustPost(t, "/v1/procedures", procedureBody("toil.classify", ""))

	rev2 := s.mustPost(t, "/v1/toil-items/"+h.ID+"/revisions", encode(t, reviseToil(1, w.content("proposal", toilProposal, "status", "candidate"))))
	if got := rev2.header.Get("Location"); got != "/v1/toil-items/"+h.ID+"/revisions/2" {
		t.Fatalf("Location = %q", got)
	}
	for _, base := range []int{1, 3} {
		stale := s.call(t, http.MethodPost, "/v1/toil-items/"+h.ID+"/revisions", encode(t, reviseToil(base, w.content())))
		expect(t, stale, http.StatusConflict, "revision_conflict")
		if errorOf(t, stale).LatestRevision != 2 {
			t.Fatalf("stale base %d: %s", base, stale.body)
		}
	}
	got := s.call(t, http.MethodGet, "/v1/toil-items/"+h.ID+"/revisions/1", "")
	expect(t, got, http.StatusOK, "")
	if want := encodeCompact(t, h.Revisions[0]); encodeCompact(t, decodeStrict[toilRevisionJSON](t, got.body)) != want {
		t.Fatalf("revision 1 changed: %s", got.body)
	}
	if got := s.call(t, http.MethodGet, "/v1/toil-items/"+h.ID+"/revisions/2", "").body; string(got) != string(rev2.body) {
		t.Fatalf("revision 2 = %s, want %s", got, rev2.body)
	}
	expect(t, s.call(t, http.MethodGet, "/v1/toil-items/"+h.ID+"/revisions/3", ""), http.StatusNotFound, "not_found")
	expect(t, s.call(t, http.MethodGet, "/v1/toil-items/"+h.ID+"/revisions/x", ""), http.StatusBadRequest, "invalid_request")
	expect(t, s.call(t, http.MethodGet, "/v1/toil-items/missing", ""), http.StatusNotFound, "not_found")
	expect(t, s.call(t, http.MethodPost, "/v1/toil-items/missing/revisions", encode(t, reviseToil(1, w.content()))), http.StatusNotFound, "not_found")
	// Server-assigned and unknown members are refused.
	expect(t, s.call(t, http.MethodPost, "/v1/toil-items", encode(t, createToil("toil.other", w.content("verified", true)))), http.StatusBadRequest, "invalid_request")
	expect(t, s.call(t, http.MethodPost, "/v1/toil-items", encode(t, with(createToil("toil.other", w.content()), "id", "x"))), http.StatusBadRequest, "invalid_request")
}

func encodeCompact(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	c := jsontext.Value(b)
	if err := c.Canonicalize(); err != nil {
		t.Fatal(err)
	}
	return string(c)
}

// TestToilLifecycle covers the allowed transitions, the structure each
// status requires and refuses, revisions without a status change, and
// reassessment after a validated replacement fails (ADR-0034).
func TestToilLifecycle(t *testing.T) {
	s := newTestServer(t)
	w := newToilWorld(t, s)
	path := func(id string) string { return "/v1/toil-items/" + id + "/revisions" }

	s.refuse(t, "/v1/toil-items", encode(t, createToil("toil.early", w.content("status", "implemented", "proposal", toilProposal, "replacement", toilReplacement))), "revision.status")
	s.refuse(t, "/v1/toil-items", encode(t, createToil("toil.early", w.content("status", "candidate"))), "revision.proposal")
	h := s.toilItem(t, "toil.life", w.content())
	id := h.ID

	steps := []struct {
		name    string
		content map[string]any
		fields  []string // refused with these fields; nil means stored
	}{
		{"observed may not jump to implemented", w.content("status", "implemented", "proposal", toilProposal, "replacement", toilReplacement), []string{"revision.status"}},
		{"observed may not carry a replacement", w.content("replacement", toilReplacement), []string{"revision.replacement"}},
		{"an assessment revision keeps the status", w.content("status_rationale", "Seen again in a fourth run."), nil},
		{"candidate needs a proposal", w.content("status", "candidate"), []string{"revision.proposal"}},
		{"candidate", w.content("status", "candidate", "proposal", toilProposal), nil},
		{"candidate may not carry a validation", w.content("status", "candidate", "proposal", toilProposal,
			"validation", map[string]any{"rationale": "r", "executions": []any{map[string]any{"execution_id": w.runA, "note": "n"}}}), []string{"revision.validation"}},
		{"candidate may not jump to validated", w.content("status", "validated", "proposal", toilProposal, "replacement", toilReplacement,
			"validation", map[string]any{"rationale": "r", "references": []any{map[string]any{"uri": "u"}}}), []string{"revision.status"}},
		{"implemented needs a replacement", w.content("status", "implemented", "proposal", toilProposal), []string{"revision.replacement"}},
		{"a replacement needs a reference", w.content("status", "implemented", "proposal", toilProposal,
			"replacement", map[string]any{"summary": "s", "references": []any{}}), []string{"revision.replacement.references"}},
		{"implemented", w.content("status", "implemented", "proposal", toilProposal, "replacement", toilReplacement), nil},
		{"validated needs validation", w.content("status", "validated", "proposal", toilProposal, "replacement", toilReplacement), []string{"revision.validation"}},
		{"validation needs evidence", w.content("status", "validated", "proposal", toilProposal, "replacement", toilReplacement,
			"validation", map[string]any{"rationale": "Looks fine."}), []string{"revision.validation"}},
		{"validated, citing a failed run as well", w.content("status", "validated", "proposal", toilProposal, "replacement", toilReplacement,
			"validation", map[string]any{"rationale": "Reproduced the recorded ranges.", "executions": []any{
				map[string]any{"execution_id": w.failedA, "note": "an earlier attempt that failed"},
				map[string]any{"execution_id": w.runA, "note": "the run that reproduced them"}},
				"references": []any{map[string]any{"uri": "ci://run/7"}}}), nil},
		{"validated may not go back to observed", w.content(), []string{"revision.status"}},
		{"reassessment after the replacement failed", w.content("status", "candidate", "proposal", toilProposal,
			"status_rationale", "The script misclassified renames in run X; back to candidate.",
			"observations", []any{map[string]any{"execution_id": w.failedA, "note": "the replacement failed here"}}), nil},
		{"retired", w.content("status", "retired", "status_rationale", "Superseded by toil.other."), nil},
		{"retired does not reopen as candidate", w.content("status", "candidate", "proposal", toilProposal), []string{"revision.status"}},
		{"retired reopens as observed", w.content(), nil},
	}
	stored := 1
	for _, st := range steps {
		body := encode(t, reviseToil(stored, st.content))
		if st.fields != nil {
			resp := s.call(t, http.MethodPost, path(id), body)
			expect(t, resp, http.StatusBadRequest, "invalid_request")
			if got := fieldsOf(t, resp); !slices.Equal(got, st.fields) {
				t.Fatalf("%s: fields %v, want %v; %s", st.name, got, st.fields, resp.body)
			}
			continue
		}
		r := s.reviseToil(t, id, stored, st.content)
		stored++
		if r.Revision != stored {
			t.Fatalf("%s: revision %d, want %d", st.name, r.Revision, stored)
		}
	}
	final := decodeStrict[toilHistoryJSON](t, s.call(t, http.MethodGet, "/v1/toil-items/"+id, "").body)
	var statuses []string
	for _, r := range final.Revisions {
		statuses = append(statuses, r.Status)
	}
	want := []string{"observed", "observed", "candidate", "implemented", "validated", "candidate", "retired", "observed"}
	if !slices.Equal(statuses, want) || final.Status != "observed" || final.LatestRevision != len(want) {
		t.Fatalf("history statuses %v (latest %s), want %v", statuses, final.Status, want)
	}
	v := final.Revisions[4].Validation
	if v == nil || len(v.Executions) != 2 || v.Executions[0].ExecutionID != w.failedA || len(v.References) != 1 || final.Revisions[4].Replacement == nil {
		t.Fatalf("the validated revision lost its evidence: %+v", final.Revisions[4])
	}
	if final.Revisions[5].Validation != nil || final.Revisions[5].Replacement != nil {
		t.Fatalf("the reassessed revision carries the old replacement: %+v", final.Revisions[5])
	}
	if final.Revisions[0].Revision != 1 || string(encodeCompact(t, final.Revisions[0])) != encodeCompact(t, h.Revisions[0]) {
		t.Fatalf("revision 1 changed")
	}
}

// TestToilLinksAndShapes covers referential integrity, failed executions as
// support, declared step identifiers on procedures with and without steps,
// and the shape rules of the free-form members (ADR-0034).
func TestToilLinksAndShapes(t *testing.T) {
	s := newTestServer(t)
	w := newToilWorld(t, s)
	create := func(content map[string]any) string { return encode(t, createToil("toil.links", content)) }

	s.refuse(t, "/v1/toil-items", create(w.content("sources", []any{map[string]any{"procedure_id": "missing", "version": 1}})), "revision.sources[0].procedure_id")
	s.refuse(t, "/v1/toil-items", create(w.content("sources", []any{map[string]any{"procedure_id": w.steps, "version": 2}})), "revision.sources[0].version")
	s.refuse(t, "/v1/toil-items", create(w.content("observations", []any{map[string]any{"execution_id": "missing", "note": "n"}})), "revision.observations[0].execution_id")
	s.refuse(t, "/v1/toil-items", create(w.content("sources", []any{})), "revision.sources")
	s.refuse(t, "/v1/toil-items", create(w.content("sources", []any{map[string]any{"procedure_id": w.steps, "version": 1}, map[string]any{"procedure_id": w.steps, "version": 1}})), "revision.sources[1]")
	s.refuse(t, "/v1/toil-items", create(w.content("sources", w.source(w.steps, "a", "a"))), "revision.sources[0].steps[1]")
	s.refuse(t, "/v1/toil-items", create(w.content("sources", w.source(w.steps, " "))), "revision.sources[0].steps[0]")
	s.refuse(t, "/v1/toil-items", create(w.content("observations", []any{map[string]any{"execution_id": w.runA, "note": " "}})), "revision.observations[0].note")
	s.refuse(t, "/v1/toil-items", create(w.content("assessment", map[string]any{"repetition": map[string]any{"status": "observed"}})), "revision.assessment.burden")
	s.refuse(t, "/v1/toil-items", create(w.content("assessment", map[string]any{"repetition": map[string]any{"status": "established"}, "burden": map[string]any{"status": "unknown"}})), "revision.assessment.repetition.status")
	s.refuse(t, "/v1/toil-items", create(w.content("assessment", []any{})), "revision.assessment")
	s.refuse(t, "/v1/toil-items", create(w.content("proposal", with(toilProposal, "outputs", nil, "acceptance_criteria", []any{}))),
		"revision.proposal.outputs", "revision.proposal.acceptance_criteria")
	s.refuse(t, "/v1/toil-items", create(w.content("applicability", nil)), "revision.applicability")
	s.refuse(t, "/v1/toil-items", create(w.content("activity", "two\nlines", "author", "Test Agent")), "revision.activity", "revision.author")

	// A failed run supports an observation; steps are declared, never
	// checked against instructions, whatever their shape.
	h := s.toilItem(t, "toil.links", w.content(
		"sources", []any{
			map[string]any{"procedure_id": w.steps, "version": 1, "steps": []any{"files", "not-a-step-of-this-version"}},
			map[string]any{"procedure_id": w.phases, "version": 1, "steps": []any{"build"}},
			map[string]any{"procedure_id": w.localB, "version": 1, "steps": []any{}},
		},
		"observations", []any{map[string]any{"execution_id": w.failedA, "note": "failed while classifying"}, map[string]any{"execution_id": w.runB, "note": "the same work in B"}}))
	got := h.Revisions[0]
	if len(got.Sources) != 3 || !slices.Equal(got.Sources[0].Steps, []string{"files", "not-a-step-of-this-version"}) ||
		!slices.Equal(got.Sources[1].Steps, []string{"build"}) || got.Sources[2].Steps != nil || got.Observations[0].ExecutionID != w.failedA {
		t.Fatalf("links: %+v", got)
	}
	if body := s.call(t, http.MethodGet, "/v1/toil-items/"+h.ID, "").body; strings.Contains(string(body), `"steps":[]`) {
		t.Fatalf("an empty step list is not omitted: %s", body)
	}
	// Validation executions are checked like observations.
	s.reviseToil(t, h.ID, 1, w.content("status", "candidate", "proposal", toilProposal))
	s.reviseToil(t, h.ID, 2, w.content("status", "implemented", "proposal", toilProposal, "replacement", toilReplacement))
	s.refuse(t, "/v1/toil-items/"+h.ID+"/revisions", encode(t, reviseToil(3, w.content("status", "validated", "proposal", toilProposal, "replacement", toilReplacement,
		"validation", map[string]any{"rationale": "r", "executions": []any{map[string]any{"execution_id": "missing", "note": "n"}}}))), "revision.validation.executions[0].execution_id")
}

// TestToilScopesAndListFilters covers shared and local items, repository
// identity with aliases, and the status, repository and procedure filters
// on the latest revision (ADR-0034).
func TestToilScopesAndListFilters(t *testing.T) {
	s := newTestServer(t)
	w := newToilWorld(t, s)
	localA := map[string]any{"repository": w.repoA}

	s.refuse(t, "/v1/toil-items", encode(t, createToil("toil.x", w.content("applicability", map[string]any{"repository": "unregistered"}))), "revision.applicability.repository")
	s.refuse(t, "/v1/toil-items", encode(t, createToil("toil.x", w.content("applicability", localA,
		"observations", []any{map[string]any{"execution_id": w.runB, "note": "n"}}))), "revision.observations[0].execution_id")
	s.refuse(t, "/v1/toil-items", encode(t, createToil("toil.x", w.content("applicability", localA, "sources", w.source(w.localB)))), "revision.sources[0]")

	// Run A was recorded under A's alias: it belongs to A.
	local := s.toilItem(t, "toil.a-local", w.content("applicability", localA))
	if local.Scope != "local" || string(local.Applicability) != `{"repository":"`+w.repoA+`"}` {
		t.Fatalf("local item: %+v", local)
	}
	// A shared item may cite procedures and runs from several repositories.
	s.toilItem(t, "toil.shared", w.content("sources", append(w.source(w.steps), w.source(w.localB)...),
		"observations", []any{map[string]any{"execution_id": w.runA, "note": "in A"}, map[string]any{"execution_id": w.runB, "note": "in B"}}))
	phases := s.toilItem(t, "toil.phases", w.content("sources", w.source(w.phases), "proposal", toilProposal, "status", "candidate"))

	for _, c := range []struct {
		query string
		want  []string
	}{
		{"", []string{"toil.a-local", "toil.phases", "toil.shared"}},
		{"?repository=github.com/o/a", []string{"toil.a-local", "toil.phases", "toil.shared"}},
		{"?repository=mirror.example/o/a", []string{"toil.a-local", "toil.phases", "toil.shared"}},
		{"?repository=github.com/o/b", []string{"toil.phases", "toil.shared"}},
		{"?repository=unregistered.example/x", []string{"toil.phases", "toil.shared"}},
		{"?status=candidate", []string{"toil.phases"}},
		{"?status=observed&repository=github.com/o/b", []string{"toil.shared"}},
		{"?procedure_id=" + w.localB, []string{"toil.shared"}},
		{"?procedure_id=" + w.phases, []string{"toil.phases"}},
		{"?procedure_id=" + w.steps, []string{"toil.a-local", "toil.shared"}},
	} {
		if got := keysOfToil(s.toilList(t, c.query)); !slices.Equal(got, c.want) {
			t.Fatalf("GET /v1/toil-items%s = %v, want %v", c.query, got, c.want)
		}
	}
	// Filters follow the latest revision only.
	s.reviseToil(t, phases.ID, 1, w.content("sources", w.source(w.steps), "proposal", toilProposal, "status", "candidate"))
	if got := keysOfToil(s.toilList(t, "?procedure_id="+w.phases)); len(got) != 0 {
		t.Fatalf("an item no longer citing p.phases is listed: %v", got)
	}
	summary := s.toilList(t, "?status=candidate").ToilItems[0]
	if summary.LatestRevision != 2 || summary.Status != "candidate" || !summary.UpdatedAt.After(summary.CreatedAt) {
		t.Fatalf("summary: %+v", summary)
	}
	for _, bad := range []struct{ query, field string }{
		{"?status=verified", "status"}, {"?status=", "status"}, {"?repository=Not/Valid", "repository"}, {"?procedure_id=", "procedure_id"},
		{"?limit=0", "limit"}, {"?after=x&limit=1", "after"},
	} {
		resp := s.call(t, http.MethodGet, "/v1/toil-items"+bad.query, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if got := fieldsOf(t, resp); !slices.Equal(got, []string{bad.field}) {
			t.Fatalf("%s: fields %v", bad.query, got)
		}
	}
	expect(t, s.call(t, http.MethodGet, "/v1/toil-items?owner=x", ""), http.StatusBadRequest, "invalid_request")
}

// TestToilPagesUnderConcurrentRevisions covers ADR-0023's guarantees on the
// toil item list: a live traversal returns every item that existed
// throughout exactly once, as it is when its page is read; a snapshot
// traversal returns the first page's items as they were then.
func TestToilPagesUnderConcurrentRevisions(t *testing.T) {
	s := newTestServer(t)
	w := newToilWorld(t, s)
	ids := map[string]string{}
	for _, key := range []string{"t.b", "t.d", "t.f", "t.h", "t.j"} {
		ids[key] = s.toilItem(t, key, w.content()).ID
	}
	between := func() {
		for _, key := range []string{"t.b", "t.h"} {
			s.reviseToil(t, ids[key], 1, w.content("status", "candidate", "proposal", toilProposal))
		}
		s.toilItem(t, "t.a", w.content())
		s.toilItem(t, "t.z", w.content())
	}
	traverse := func(query string, between func()) []string {
		var out []string
		for after, first := "", true; ; first = false {
			q := query
			if after != "" {
				q += "&after=" + url.QueryEscape(after)
			}
			page := s.toilList(t, q)
			for _, it := range page.ToilItems {
				out = append(out, fmt.Sprintf("%s@%d:%s", it.CanonicalKey, it.LatestRevision, it.Status))
			}
			if page.Next == "" {
				return out
			}
			if first && between != nil {
				between()
			}
			after = page.Next
		}
	}
	snap := traverse("?limit=2&snapshot=true", between)
	if want := []string{"t.b@1:observed", "t.d@1:observed", "t.f@1:observed", "t.h@1:observed", "t.j@1:observed"}; !slices.Equal(snap, want) {
		t.Fatalf("snapshot traversal %v, want %v", snap, want)
	}
	live := traverse("?limit=2", nil)
	if want := []string{"t.a@1:observed", "t.b@2:candidate", "t.d@1:observed", "t.f@1:observed", "t.h@2:candidate", "t.j@1:observed", "t.z@1:observed"}; !slices.Equal(live, want) {
		t.Fatalf("live traversal %v, want %v", live, want)
	}
	// A live traversal with a status filter: an item revised out of the
	// filter ahead of the cursor is not returned; nothing is repeated.
	var observed []string
	first := s.toilList(t, "?limit=2&status=observed")
	for _, it := range first.ToilItems {
		observed = append(observed, it.CanonicalKey)
	}
	s.reviseToil(t, ids["t.f"], 1, w.content("status", "retired"))
	for after := first.Next; after != ""; {
		page := s.toilList(t, "?limit=2&status=observed&after="+url.QueryEscape(after))
		for _, it := range page.ToilItems {
			observed = append(observed, it.CanonicalKey)
		}
		after = page.Next
	}
	if want := []string{"t.a", "t.d", "t.j", "t.z"}; !slices.Equal(observed, want) {
		t.Fatalf("live status traversal %v, want %v", observed, want)
	}
	cursor := s.toilList(t, "?limit=1").Next
	expect(t, s.call(t, http.MethodGet, "/v1/toil-items?limit=1&status=observed&after="+url.QueryEscape(cursor), ""), http.StatusBadRequest, "invalid_request")
	expect(t, s.call(t, http.MethodGet, "/v1/procedures?limit=1&after="+url.QueryEscape(cursor), ""), http.StatusBadRequest, "invalid_request")
}

// TestConcurrentToilWrites: of concurrent revisions from one base exactly
// one is stored, and of concurrent creations with one key exactly one.
func TestConcurrentToilWrites(t *testing.T) {
	s := newTestServer(t)
	w := newToilWorld(t, s)
	id := s.toilItem(t, "toil.race", w.content()).ID
	const writers = 8
	race := func(path string, body func(i int) string) map[int]int {
		var wg sync.WaitGroup
		start := make(chan struct{})
		statuses := make([]int, writers)
		for i := range writers {
			wg.Go(func() {
				<-start
				resp, err := s.send(http.MethodPost, path, body(i), nil)
				if err != nil {
					t.Error(err)
					return
				}
				statuses[i] = resp.status
			})
		}
		close(start)
		wg.Wait()
		count := map[int]int{}
		for _, st := range statuses {
			count[st]++
		}
		return count
	}
	revised := race("/v1/toil-items/"+id+"/revisions", func(i int) string {
		return encode(t, reviseToil(1, w.content("status_rationale", fmt.Sprintf("writer %d", i))))
	})
	if revised[http.StatusCreated] != 1 || revised[http.StatusConflict] != writers-1 {
		t.Fatalf("concurrent revisions: %v", revised)
	}
	if h := decodeStrict[toilHistoryJSON](t, s.call(t, http.MethodGet, "/v1/toil-items/"+id, "").body); h.LatestRevision != 2 {
		t.Fatalf("latest revision %d, want 2", h.LatestRevision)
	}
	created := race("/v1/toil-items", func(int) string { return encode(t, createToil("toil.same-key", w.content())) })
	if created[http.StatusCreated] != 1 || created[http.StatusConflict] != writers-1 {
		t.Fatalf("concurrent creations: %v", created)
	}
}

// TestToilItemsLeaveVerificationAndResolutionUnchanged: citing executions
// and versions changes nothing that Polaroid derives from them.
func TestToilItemsLeaveVerificationAndResolutionUnchanged(t *testing.T) {
	s := newTestServer(t)
	w := newToilWorld(t, s)
	reads := []string{
		"/v1/executions/" + w.runA + "/verification",
		"/v1/executions/" + w.failedA + "/verification",
		"/v1/procedures/" + w.steps + "/versions/1/verifications",
		"/v1/procedures/" + w.steps + "/versions/1/graph?repository=github.com/o/a&environment=ci",
		"/v1/procedures/" + w.steps,
		"/v1/executions/" + w.runA,
		"/v1/procedures",
	}
	snapshot := func() []string {
		var out []string
		for _, p := range reads {
			out = append(out, string(s.call(t, http.MethodGet, p, "").body))
		}
		return out
	}
	before := snapshot()
	h := s.toilItem(t, "toil.inert", w.content("observations", []any{map[string]any{"execution_id": w.failedA, "note": "failed"}}))
	s.reviseToil(t, h.ID, 1, w.content("status", "candidate", "proposal", toilProposal))
	s.reviseToil(t, h.ID, 2, w.content("status", "implemented", "proposal", toilProposal, "replacement", toilReplacement))
	s.reviseToil(t, h.ID, 3, w.content("status", "validated", "proposal", toilProposal, "replacement", toilReplacement,
		"validation", map[string]any{"rationale": "r", "executions": []any{map[string]any{"execution_id": w.failedA, "note": "a failed run cited as validation"}}}))
	if after := snapshot(); !slices.Equal(after, before) {
		t.Fatalf("toil items changed derived records:\nbefore %v\nafter  %v", before, after)
	}
}
