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
	"time"
)

// The structs below restate the documented lifecycle shapes (ADR-0035).

type invalidationStateJSON struct {
	ExecutionID    string `json:"execution_id"`
	EvidenceStatus string `json:"evidence_status"`
	Invalidation   *struct {
		Reason    string    `json:"reason"`
		Actor     string    `json:"actor"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"invalidation"`
}

type lifecycleJSON struct {
	ProcedureID string `json:"procedure_id"`
	Status      string `json:"status"`
	LatestEvent int    `json:"latest_event"`
	Events      []struct {
		Event     int       `json:"event"`
		Status    string    `json:"status"`
		Reason    string    `json:"reason"`
		Actor     string    `json:"actor"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"events"`
}

const invalidateBody = `{"reason": "Recorded against v2, but it only promoted an earlier assessment.", "actor": "copilot.vscode"}`

func lifecycleBody(base int, reason string) string {
	return fmt.Sprintf(`{"base_event": %d, "reason": %q, "actor": "copilot.vscode"}`, base, reason)
}

func (s *testServer) idsOf(t *testing.T, path string) []string {
	t.Helper()
	resp := s.call(t, http.MethodGet, path, "")
	expect(t, resp, http.StatusOK, "")
	page := decodeStrict[struct {
		Executions []executionJSON `json:"executions"`
		Procedures []procedureJSON `json:"procedures"`
		Next       string          `json:"next"`
	}](t, resp.body)
	var ids []string
	for _, e := range page.Executions {
		ids = append(ids, e.ID+":"+e.EvidenceStatus)
	}
	for _, p := range page.Procedures {
		ids = append(ids, p.ID+":"+p.LifecycleStatus)
	}
	return ids
}

func TestExecutionInvalidationOverHTTP(t *testing.T) {
	s := newTestServer(t)
	leaf := s.create(t).ID
	parent := s.createWith(t, "compose.parent", "["+pinRef("leaf", leaf, 1)+"]")
	child := s.record(t, leaf, 1, nil)
	other := s.record(t, leaf, 1, nil)
	run := s.record(t, parent, 1, map[string]string{"children": childrenJSON("leaf", child)})
	path := "/v1/executions/" + child

	resp := s.call(t, http.MethodGet, path+"/invalidation", "")
	expect(t, resp, http.StatusOK, "")
	if got := strings.TrimSpace(string(resp.body)); got != `{"execution_id":"`+child+`","evidence_status":"valid"}` {
		t.Fatalf("valid state = %s", got)
	}
	before := s.call(t, http.MethodGet, path, "").body

	bad := s.call(t, http.MethodPost, path+"/invalidation", `{"reason": " ", "actor": "Copilot VS Code"}`)
	expect(t, bad, http.StatusBadRequest, "invalid_request")
	if f := fieldsOf(t, bad); !slices.Equal(f, []string{"reason", "actor"}) {
		t.Fatalf("fields = %v", f)
	}
	expect(t, s.call(t, http.MethodPost, path+"/invalidation", `{"reason": "x", "actor": "a", "restore": true}`), http.StatusBadRequest, "invalid_request")
	expect(t, s.call(t, http.MethodPost, "/v1/executions/nope/invalidation", invalidateBody), http.StatusNotFound, "not_found")

	created := s.call(t, http.MethodPost, path+"/invalidation", invalidateBody)
	expect(t, created, http.StatusCreated, "")
	if loc := created.header.Get("Location"); loc != path+"/invalidation" {
		t.Fatalf("Location = %q", loc)
	}
	state := decodeStrict[invalidationStateJSON](t, created.body)
	if state.ExecutionID != child || state.EvidenceStatus != "invalidated" || state.Invalidation == nil ||
		state.Invalidation.Actor != "copilot.vscode" || !strings.HasPrefix(state.Invalidation.Reason, "Recorded against v2") {
		t.Fatalf("invalidation = %+v", state)
	}
	again := s.call(t, http.MethodPost, path+"/invalidation", invalidateBody)
	expect(t, again, http.StatusConflict, "execution_invalidated")

	// The record is unchanged; only its evidence status and invalidation are added.
	after := s.call(t, http.MethodGet, path, "").body
	e := decodeStrict[executionJSON](t, after)
	recordOf := func(b []byte) string { return string(b[:strings.Index(string(b), `,"evidence_status":`)]) }
	if e.EvidenceStatus != "invalidated" || e.Invalidation == nil || e.Invalidation.CreatedAt != state.Invalidation.CreatedAt || recordOf(after) != recordOf(before) {
		t.Fatalf("execution after invalidation:\n%s\nbefore:\n%s", after, before)
	}
	if got := decodeStrict[invalidationStateJSON](t, s.call(t, http.MethodGet, path+"/invalidation", "").body); *got.Invalidation != *state.Invalidation {
		t.Fatalf("read back %+v", got.Invalidation)
	}

	v, _ := s.verification(t, child)
	if v.Verified || v.EvidenceStatus != "invalidated" || len(v.Problems) != 1 || v.Problems[0].Code != "invalidated" {
		t.Fatalf("child verification = %+v", v)
	}
	v, _ = s.verification(t, run)
	if v.Verified || v.EvidenceStatus != "valid" || len(v.Problems) != 1 || v.Problems[0].Code != "child_invalidated" || v.Problems[0].ExecutionID != child {
		t.Fatalf("parent verification = %+v", v)
	}
	if v, _ := s.verification(t, other); !v.Verified {
		t.Fatalf("unrelated execution = %+v", v)
	}
	list := s.combinations(t, leaf, 1, repoA).Verifications
	if len(list) != 1 || !list[0].Verified || list[0].LatestExecutionID != other || !slices.Equal(list[0].InvalidatedExecutionIDs, []string{child}) {
		t.Fatalf("combinations = %+v", list)
	}

	for query, want := range map[string][]string{
		"":                               {child + ":invalidated", other + ":valid", run + ":valid"},
		"?evidence_status=invalidated":   {child + ":invalidated"},
		"?evidence_status=valid&limit=1": {other + ":valid"},
		"?procedure_id=" + leaf + "&evidence_status=valid": {other + ":valid"},
	} {
		if got := s.idsOf(t, "/v1/executions"+query); !slices.Equal(got, want) {
			t.Errorf("executions%s = %v, want %v", query, got, want)
		}
	}
	for _, query := range []string{"?evidence_status=", "?evidence_status=retired"} {
		resp := s.call(t, http.MethodGet, "/v1/executions"+query, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if f := fieldsOf(t, resp); !slices.Equal(f, []string{"evidence_status"}) {
			t.Fatalf("%s: fields %v", query, f)
		}
	}
}

func TestConcurrentInvalidationsOverHTTP(t *testing.T) {
	s := newTestServer(t)
	id := s.record(t, s.create(t).ID, 1, nil)
	statuses := concurrently(t, 8, func() response {
		return s.call(t, http.MethodPost, "/v1/executions/"+id+"/invalidation", invalidateBody)
	})
	if created := statuses[http.StatusCreated]; created != 1 || statuses[http.StatusConflict] != 7 {
		t.Fatalf("statuses = %v; want one 201 and seven 409", statuses)
	}
}

// concurrently releases n requests together and counts their statuses.
func concurrently(t *testing.T, n int, do func() response) map[int]int {
	t.Helper()
	start := make(chan struct{})
	results := make([]response, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			results[i] = do()
		})
	}
	close(start)
	wg.Wait()
	counts := map[int]int{}
	for _, r := range results {
		counts[r.status]++
		if r.status == http.StatusConflict {
			expect(t, r, http.StatusConflict, "")
		}
	}
	return counts
}

func TestProcedureRetirementOverHTTP(t *testing.T) {
	s := newTestServer(t)
	leaf := s.create(t).ID
	parent := s.createWith(t, "compose.parent", "["+pinRef("pinned", leaf, 1)+", "+latestRef("latest", leaf)+"]")
	other := s.createWith(t, "compose.other", "[]")
	leafRun := s.record(t, leaf, 1, nil)
	binding := s.bind(t, repoA, "add-dependency", leaf, `{"contextual": {}}`).ID
	lifecycle := "/v1/procedures/" + leaf + "/lifecycle/events"

	resp := s.call(t, http.MethodGet, lifecycle, "")
	expect(t, resp, http.StatusOK, "")
	if got := strings.TrimSpace(string(resp.body)); got != `{"procedure_id":"`+leaf+`","status":"active","latest_event":0,"events":[]}` {
		t.Fatalf("lifecycle = %s", got)
	}
	bad := s.call(t, http.MethodPost, "/v1/procedures/"+leaf+"/retirement", `{"base_event": -1, "reason": "", "actor": "X"}`)
	expect(t, bad, http.StatusBadRequest, "invalid_request")
	if f := fieldsOf(t, bad); !slices.Equal(f, []string{"base_event", "reason", "actor"}) {
		t.Fatalf("fields = %v", f)
	}
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/nope/retirement", lifecycleBody(0, "r")), http.StatusNotFound, "not_found")
	expect(t, s.call(t, http.MethodGet, "/v1/procedures/nope/lifecycle/events", ""), http.StatusNotFound, "not_found")
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+leaf+"/reactivation", lifecycleBody(0, "r")), http.StatusConflict, "lifecycle_unchanged")

	retired := s.call(t, http.MethodPost, "/v1/procedures/"+leaf+"/retirement", lifecycleBody(0, "Superseded by go.module.add."))
	expect(t, retired, http.StatusCreated, "")
	if loc := retired.header.Get("Location"); loc != lifecycle {
		t.Fatalf("Location = %q", loc)
	}
	l := decodeStrict[lifecycleJSON](t, retired.body)
	if l.ProcedureID != leaf || l.Status != "retired" || l.LatestEvent != 1 || len(l.Events) != 1 || l.Events[0].Status != "retired" ||
		l.Events[0].Reason != "Superseded by go.module.add." || l.Events[0].Actor != "copilot.vscode" {
		t.Fatalf("lifecycle = %+v", l)
	}
	stale := s.call(t, http.MethodPost, "/v1/procedures/"+leaf+"/retirement", lifecycleBody(0, "again"))
	expect(t, stale, http.StatusConflict, "lifecycle_conflict")
	if e := errorOf(t, stale); e.LatestEvent != 1 {
		t.Fatalf("latest_event = %d", e.LatestEvent)
	}
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+leaf+"/retirement", lifecycleBody(1, "again")), http.StatusConflict, "lifecycle_unchanged")

	// Exact retrieval still works and says so.
	h := decodeStrict[historyJSON](t, s.call(t, http.MethodGet, "/v1/procedures/"+leaf, "").body)
	if h.LifecycleStatus != "retired" || h.LifecycleEvent != 1 {
		t.Fatalf("history = %+v", h)
	}
	expect(t, s.call(t, http.MethodGet, "/v1/procedures/by-key/go.dependency.add", ""), http.StatusOK, "")
	expect(t, s.call(t, http.MethodGet, "/v1/procedures/"+leaf+"/versions/1", ""), http.StatusOK, "")

	// Lists and discovery leave it out unless asked.
	for query, want := range map[string][]string{
		"":                   {other + ":active", parent + ":active"},
		"?lifecycle=active":  {other + ":active", parent + ":active"},
		"?lifecycle=retired": {leaf + ":retired"},
		"?lifecycle=all":     {other + ":active", parent + ":active", leaf + ":retired"},
	} {
		if got := s.idsOf(t, "/v1/procedures"+query); !slices.Equal(got, want) {
			t.Errorf("procedures%s = %v, want %v", query, got, want)
		}
	}
	expect(t, s.call(t, http.MethodGet, "/v1/procedures?lifecycle=gone", ""), http.StatusBadRequest, "invalid_request")
	if d := s.discover(t, "add a go dependency"); slices.Contains(keysOf(d.Candidates), "go.dependency.add") || d.Considered != 2 {
		t.Fatalf("default discovery found the retired procedure: %+v", d)
	}
	if d := s.discover(t, "add a go dependency", "lifecycle", "all"); d.Considered != 3 || d.Candidates[0].CanonicalKey != "go.dependency.add" || d.Candidates[0].LifecycleStatus != "retired" {
		t.Fatalf("discovery of all = %+v", d)
	}
	expect(t, s.call(t, http.MethodGet, discoveryPath("dependency", "lifecycle", "none"), ""), http.StatusBadRequest, "invalid_request")
	dup := s.duplicates(t, map[string]any{"canonical_key": "go.dependency.add", "method": "Justify, verify the license, pin, record.",
		"philosophy": "A dependency is a long-term liability."})
	if dup.KeyCollision == nil || dup.KeyCollision.ProcedureID != leaf || dup.KeyCollision.LifecycleStatus != "retired" ||
		len(dup.Suggestions) == 0 || dup.Suggestions[0].ProcedureID != leaf || dup.Suggestions[0].LifecycleStatus != "retired" {
		t.Fatalf("duplicates = %+v", dup)
	}

	// New writes that would build on it are refused, naming the reactivation.
	refused := s.call(t, http.MethodPost, "/v1/procedures/"+leaf+"/versions", reviseBody(1, "v2"))
	expect(t, refused, http.StatusConflict, "procedure_retired")
	if !strings.Contains(errorOf(t, refused).Message, "reactivate it first") {
		t.Fatalf("message = %s", refused.body)
	}
	refused = s.call(t, http.MethodPost, "/v1/procedures", procedureWithReferences("compose.new", "["+latestRef("a", other)+", "+pinRef("b", leaf, 1)+"]"))
	expect(t, refused, http.StatusConflict, "procedure_retired")
	if f := fieldsOf(t, refused); !slices.Equal(f, []string{"version.references[1].procedure_id"}) {
		t.Fatalf("fields = %v", f)
	}
	refused = s.call(t, http.MethodPost, "/v1/bindings", bindBody(repoB, "dep", leaf, `{"pin": 1}`))
	expect(t, refused, http.StatusConflict, "procedure_retired")
	if f := fieldsOf(t, refused); !slices.Equal(f, []string{"procedure_id"}) {
		t.Fatalf("fields = %v", f)
	}
	expect(t, s.call(t, http.MethodPost, "/v1/bindings/"+binding+"/revisions", reviseBindingBody(1, `{"pin": 1}`)), http.StatusConflict, "procedure_retired")
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+other+"/versions", reviseWithReferences(1, "["+latestRef("x", leaf)+"]")), http.StatusConflict, "procedure_retired")

	// What exists keeps resolving, and says that the node is retired.
	g := s.target(t, parent, 1, repoA, "ci.ubuntu-latest", fullCommit, `{"module":"modernc.org/sqlite"}`)
	for _, e := range g.References {
		n := e.Node
		if n.ProcedureID != leaf || n.Version != 1 || n.Retirement == nil || n.Retirement.Event != 1 || n.Retirement.Status != "retired" ||
			n.SelectionEvidence == nil || n.SelectionEvidence.ExecutionID != leafRun || !n.TargetVerification.Verified {
			t.Fatalf("edge %s node = %+v", e.Name, n)
		}
	}
	if g.Retirement != nil {
		t.Fatalf("the parent is reported retired: %+v", g.Retirement)
	}
	res := decodeStrict[bindingResolutionJSON](t, s.call(t, http.MethodGet, "/v1/bindings/"+binding+"/resolution?environment=ci.ubuntu-latest", "").body)
	if res.Graph.Retirement == nil || res.Graph.Version != 1 || res.Graph.VerifiedBy != leafRun {
		t.Fatalf("resolution = %+v", res.Graph)
	}
	s.record(t, leaf, 1, nil) // runs of it are still recorded

	reactivated := s.call(t, http.MethodPost, "/v1/procedures/"+leaf+"/reactivation", lifecycleBody(1, "go.module.add was withdrawn."))
	expect(t, reactivated, http.StatusCreated, "")
	if l := decodeStrict[lifecycleJSON](t, reactivated.body); l.Status != "active" || l.LatestEvent != 2 || len(l.Events) != 2 {
		t.Fatalf("after reactivation: %+v", l)
	}
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+leaf+"/versions", reviseBody(1, "v2")), http.StatusCreated, "")
	if g := s.target(t, parent, 1, repoA, "ci.ubuntu-latest", fullCommit, `{"module":"modernc.org/sqlite"}`); g.References[0].Node.Retirement != nil {
		t.Fatalf("a reactivated node reports retirement: %+v", g.References[0].Node)
	}
}

func TestCursorsBelongToTheirLifecycleFilter(t *testing.T) {
	s := newTestServer(t)
	a := s.createWith(t, "a.first", "[]")
	s.createWith(t, "b.second", "[]")
	expect(t, s.call(t, http.MethodPost, "/v1/procedures/"+a+"/retirement", lifecycleBody(0, "r")), http.StatusCreated, "")
	run := s.record(t, a, 1, nil)
	s.record(t, a, 1, nil)
	s.record(t, a, 1, nil)
	expect(t, s.call(t, http.MethodPost, "/v1/executions/"+run+"/invalidation", invalidateBody), http.StatusCreated, "")
	for list, filter := range map[string]string{"/v1/procedures": "lifecycle=all", "/v1/executions": "evidence_status=valid"} {
		var first struct {
			Next string `json:"next"`
		}
		if err := json.Unmarshal(s.call(t, http.MethodGet, list+"?limit=1&"+filter, "").body, &first); err != nil || first.Next == "" {
			t.Fatalf("%s?%s has no next page: %v", list, filter, err)
		}
		expect(t, s.call(t, http.MethodGet, list+"?limit=1&after="+first.Next, ""), http.StatusBadRequest, "invalid_request")
		expect(t, s.call(t, http.MethodGet, list+"?limit=1&"+filter+"&after="+first.Next, ""), http.StatusOK, "")
	}
}

func TestConcurrentRetirementsOverHTTP(t *testing.T) {
	s := newTestServer(t)
	id := s.create(t).ID
	statuses := concurrently(t, 8, func() response {
		return s.call(t, http.MethodPost, "/v1/procedures/"+url.PathEscape(id)+"/retirement", lifecycleBody(0, "retire"))
	})
	if statuses[http.StatusCreated] != 1 || statuses[http.StatusConflict] != 7 {
		t.Fatalf("statuses = %v; want one 201 and seven 409", statuses)
	}
	if l := decodeStrict[lifecycleJSON](t, s.call(t, http.MethodGet, "/v1/procedures/"+id+"/lifecycle/events", "").body); l.LatestEvent != 1 {
		t.Fatalf("lifecycle = %+v", l)
	}
}
