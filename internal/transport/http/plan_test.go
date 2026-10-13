package http_test

import (
	"encoding/json/jsontext"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// #131: fields= on a version read, and format=plan / prepare_execution's
// compact, dependency-ordered projection of a composition graph.

type versionProjectionJSON struct {
	ProcedureID    string         `json:"procedure_id"`
	Version        int            `json:"version"`
	Scope          string         `json:"scope"`
	CreatedAt      time.Time      `json:"created_at"`
	Philosophy     *string        `json:"philosophy"`
	Method         *string        `json:"method"`
	Goal           *string        `json:"goal"`
	Applicability  jsontext.Value `json:"applicability"`
	Contract       jsontext.Value `json:"contract"`
	Instructions   jsontext.Value `json:"instructions"`
	References     jsontext.Value `json:"references"`
	RevisionReason *string        `json:"revision_reason"`
}

type planStepJSON struct {
	Path              string `json:"path"`
	ProcedureID       string `json:"procedure_id"`
	Version           int    `json:"version"`
	SelectedBy        string `json:"selected_by"`
	Decision          string `json:"decision"`
	Status            string `json:"status"`
	LatestExecutionID string `json:"latest_execution_id"`
}

type planJSON struct {
	BindingID       string `json:"binding_id"`
	BindingRevision int    `json:"binding_revision"`
	Repository      string `json:"repository"`
	Environment     *struct {
		Name string `json:"name"`
	} `json:"environment"`
	Steps      []planStepJSON `json:"steps"`
	NextAction *string        `json:"next_action"`
}

func TestVersionFieldsProjection(t *testing.T) {
	s := newTestServer(t)
	p := s.create(t).ID

	full := s.call(t, http.MethodGet, "/v1/procedures/"+p+"/versions/1", "")
	expect(t, full, http.StatusOK, "")

	resp := s.call(t, http.MethodGet, "/v1/procedures/"+p+"/versions/1?"+url.Values{"fields": {"goal, instructions"}}.Encode(), "")
	expect(t, resp, http.StatusOK, "")
	body := decodeStrict[versionProjectionJSON](t, resp.body)
	if body.ProcedureID != p || body.Version != 1 || body.Scope != "unspecified" {
		t.Fatalf("identity = %+v", body)
	}
	if body.Goal == nil || *body.Goal != "" {
		t.Fatalf("goal = %v, want a pointer to \"\" (requested, legitimately absent in createBody)", body.Goal)
	}
	if body.Instructions == nil || string(body.Instructions) != `{"steps":["justify","license","pin","record"]}` {
		t.Fatalf("instructions = %s", body.Instructions)
	}
	if body.Philosophy != nil || body.Method != nil || body.Contract != nil || body.References != nil || body.RevisionReason != nil {
		t.Fatalf("members outside fields must be absent: %+v", body)
	}

	for _, bad := range []string{"bogus", "", "contract,philosophy,bogus"} {
		resp := s.call(t, http.MethodGet, "/v1/procedures/"+p+"/versions/1?"+url.Values{"fields": {bad}}.Encode(), "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
	}
	// Unaffected: without fields, the response is the complete version, byte
	// for byte identical to before #131.
	again := s.call(t, http.MethodGet, "/v1/procedures/"+p+"/versions/1", "")
	if string(again.body) != string(full.body) {
		t.Fatalf("default response changed:\n%s\nwant\n%s", again.body, full.body)
	}
}

// planWorld is a binding of a version with one required reference (checks)
// and one conditional reference (integration), mirroring verify-change's
// own shape (AGENTS.md), bound in repoA.
type planWorld struct {
	leaf, verify, binding string
}

func newPlanWorld(t *testing.T, s *testServer) planWorld {
	t.Helper()
	w := newConditionalWorld(t, s)
	b := s.bind(t, repoA, "plan-verify", w.verify, `{"contextual": {}}`)
	return planWorld{leaf: w.leaf, verify: w.verify, binding: b.ID}
}

const planInputs = `{"module": "modernc.org/sqlite"}`

func (w planWorld) target(decisions string) url.Values {
	q := url.Values{"environment": {ubuntu}, "commit": {fullCommit}, "inputs": {planInputs}}
	if decisions != "" {
		q.Set("decisions", decisions)
	}
	return q
}

func TestGraphPlanFormatOrdering(t *testing.T) {
	s := newTestServer(t)
	leaf := s.create(t).ID
	mid := s.createWith(t, "compose.mid", "["+latestRef("leaf", leaf)+"]")
	root := s.createWith(t, "compose.root", "["+latestRef("mid", mid)+"]")

	resp := s.call(t, http.MethodGet, "/v1/procedures/"+root+"/versions/1/graph?format=plan", "")
	expect(t, resp, http.StatusOK, "")
	p := decodeStrict[planJSON](t, resp.body)
	if len(p.Steps) != 3 {
		t.Fatalf("steps = %+v", p.Steps)
	}
	if p.Steps[0].Path != "mid/leaf" || p.Steps[1].Path != "mid" || p.Steps[2].Path != "" {
		t.Fatalf("order = %q, %q, %q; want the leaf, then mid, then the root (children before the parent)", p.Steps[0].Path, p.Steps[1].Path, p.Steps[2].Path)
	}
	for _, step := range p.Steps {
		if step.Status != "selected" {
			t.Fatalf("without a target, every step is just selected: %+v", step)
		}
	}
	if p.BindingID != "" {
		t.Fatalf("a bare graph plan carries no binding identity: %+v", p)
	}

	// format=full (and no format) are the existing, unaffected response.
	full := s.call(t, http.MethodGet, "/v1/procedures/"+root+"/versions/1/graph", "")
	explicit := s.call(t, http.MethodGet, "/v1/procedures/"+root+"/versions/1/graph?format=full", "")
	if string(full.body) != string(explicit.body) {
		t.Fatalf("format=full must equal the default:\n%s\nwant\n%s", explicit.body, full.body)
	}

	expect(t, s.call(t, http.MethodGet, "/v1/procedures/"+root+"/versions/1/graph?format=bogus", ""), http.StatusBadRequest, "invalid_request")
}

func TestResolveBindingPlanAndPrepareExecution(t *testing.T) {
	s := newTestServer(t)
	w := newPlanWorld(t, s)

	// Before anything ran, and with no decisions given: integration
	// (conditional) needs one; checks (required) has not run, so it is
	// pending; root is pending too.
	resp := s.call(t, http.MethodGet, "/v1/bindings/"+w.binding+"/resolution?"+w.target("").Encode()+"&format=plan", "")
	expect(t, resp, http.StatusOK, "")
	p := decodeStrict[planJSON](t, resp.body)
	if p.BindingID != w.binding || p.BindingRevision != 1 || p.Repository != repoA || p.Environment == nil || p.Environment.Name != ubuntu {
		t.Fatalf("binding plan identity = %+v", p)
	}
	byPath := map[string]planStepJSON{}
	for _, step := range p.Steps {
		byPath[step.Path] = step
	}
	if byPath["checks"].Status != "pending" {
		t.Fatalf("checks = %+v", byPath["checks"])
	}
	if byPath["integration"].Status != "decision_required" || byPath["integration"].Decision != "undecided" {
		t.Fatalf("integration = %+v", byPath["integration"])
	}
	if p.NextAction == nil {
		t.Fatalf("next_action should name the first step needing attention")
	}

	// prepare_execution is the same projection, with its target required.
	prep := s.call(t, http.MethodGet, "/v1/bindings/"+w.binding+"/preparation?"+w.target("").Encode(), "")
	expect(t, prep, http.StatusOK, "")
	if string(prep.body) != string(resp.body) {
		t.Fatalf("prepare_execution must equal resolve_binding?format=plan at the same target:\n%s\nwant\n%s", prep.body, resp.body)
	}
	for _, missing := range []string{"environment", "commit", "inputs"} {
		q := w.target("")
		q.Del(missing)
		resp := s.call(t, http.MethodGet, "/v1/bindings/"+w.binding+"/preparation?"+q.Encode(), "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
	}

	// Record checks, decide integration not applicable, and record the
	// parent: now resolve again WITH that same decision (as the procedure's
	// own "confirm" step documents, AGENTS.md/ADR-0029): the plan must agree
	// with the full graph's own target_verification, the derived
	// verification itself, never a fabricated status.
	checks := s.record(t, w.leaf, 1, map[string]string{"inputs": planInputs})
	s.record(t, w.verify, 1, map[string]string{
		"commit": `"` + fullCommit + `"`, "inputs": planInputs,
		"children":  childrenJSON("checks", checks),
		"decisions": `[{"reference": "integration", "applicable": false, "rationale": "docs only"}]`,
	})

	confirmDecisions := `{"integration": false}`
	planResp := s.call(t, http.MethodGet, "/v1/bindings/"+w.binding+"/resolution?"+w.target(confirmDecisions).Encode()+"&format=plan", "")
	expect(t, planResp, http.StatusOK, "")
	plan := decodeStrict[planJSON](t, planResp.body)
	full := s.call(t, http.MethodGet, "/v1/bindings/"+w.binding+"/resolution?"+w.target(confirmDecisions).Encode(), "")
	expect(t, full, http.StatusOK, "")
	fullBody := decodeStrict[bindingResolutionJSON](t, full.body)

	for _, step := range plan.Steps {
		var node *graphNodeJSON
		switch step.Path {
		case "":
			node = &fullBody.Graph
		case "checks":
			node = &fullBody.Graph.References[0].Node
		case "integration":
			node = &fullBody.Graph.References[1].Node
		}
		wantVerified := node.TargetVerification != nil && node.TargetVerification.Verified
		gotVerified := step.Status == "verified"
		if gotVerified != wantVerified {
			t.Fatalf("step %q: plan says verified=%v, full graph's target_verification says %v", step.Path, gotVerified, wantVerified)
		}
		if node.TargetVerification != nil && step.LatestExecutionID != node.TargetVerification.LatestExecutionID {
			t.Fatalf("step %q: latest_execution_id = %q, want %q", step.Path, step.LatestExecutionID, node.TargetVerification.LatestExecutionID)
		}
	}
	if byStatus := plan.Steps; byStatus[0].Status != "verified" || byStatus[1].Status != "skipped" || byStatus[2].Status != "verified" {
		t.Fatalf("after recording, steps = %+v", plan.Steps)
	}
	if plan.NextAction != nil {
		t.Fatalf("next_action = %v, want nil: the target is fully verified and nothing is skipped-pending", *plan.NextAction)
	}

	expect(t, s.call(t, http.MethodGet, "/v1/bindings/"+w.binding+"/resolution?"+w.target(confirmDecisions).Encode()+"&format=bogus", ""), http.StatusBadRequest, "invalid_request")
}
