package mcp_test

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/gitstore"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledge/semantic"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	mcptransport "github.com/ashuangiras/polaroid/internal/transport/mcp"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// knowledgeHarness runs the v8 MCP handler over a real
// Git-core repository (for knowledge) and a real SQLite file (for
// feedback), over real HTTP.
type knowledgeHarness struct {
	*harness
	kernel *knowledge.Kernel
}

func (kh *knowledgeHarness) publish(t *testing.T, procs ...*knowledge.Procedure) knowledge.Snapshot {
	t.Helper()
	ctx := context.Background()
	base, err := kh.kernel.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changes := make([]knowledge.Change, len(procs))
	for i, p := range procs {
		changes[i] = knowledge.Change{Key: p.Key, Procedure: p}
	}
	snap, err := kh.kernel.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{
		Base: base.Snapshot(), Changes: changes,
		Provenance: knowledge.Provenance{Author: "test", Summary: "test publish", Rationale: "test fixture"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// publishRecords commits procs and typed records atop the active snapshot.
func (kh *knowledgeHarness) publishRecords(t *testing.T, procs []*knowledge.Procedure, records []*knowledge.Record) (knowledge.Snapshot, error) {
	t.Helper()
	ctx := context.Background()
	base, err := kh.kernel.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changes := make([]knowledge.Change, len(procs))
	for i, p := range procs {
		changes[i] = knowledge.Change{Key: p.Key, Procedure: p}
	}
	recChanges := make([]knowledge.RecordChange, len(records))
	for i, r := range records {
		recChanges[i] = knowledge.RecordChange{Key: r.Key, Record: r}
	}
	return kh.kernel.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{
		Base: base.Snapshot(), Changes: changes, Records: recChanges,
		Provenance: knowledge.Provenance{Author: "test", Summary: "test publish", Rationale: "test fixture"},
	}))
}

// The v8 harness includes semantic validation, Git-native knowledge and
// SQLite-backed feedback, walkthroughs and drafts.
func newKnowledgeHarnessV8(t *testing.T) (*knowledgeHarness, *knowledgeops.FieldDrafter) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "polaroid.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo, err := gitcore.Open(filepath.Join(t.TempDir(), "knowledge"), gitcore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ev := datalog.Evaluator{}
	sem, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	kernel := knowledge.New(gitstore.New(repo), knowledge.WithRecordValidator(sem), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev}))
	guide := knowledgeops.NewGuide(kernel, memory.NewWalkthroughs(store), ev, policy.Trust{})
	drafter := knowledgeops.NewFieldDrafter(kernel, knowledgeops.NewProposer(kernel, ev, policy.Trust{}), memory.NewFieldDrafts(store), guide)
	logs := &syncBuffer{}
	srv := httptest.NewServer(mcptransport.NewAgentHandlerV8(knowledgeops.NewAgent(kernel, memory.NewService(store)),
		knowledgeops.NewResolver(kernel, ev, policy.Trust{}), guide, drafter, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)
	h := &harness{url: srv.URL, store: store, logs: logs}
	h.session = h.connect(t, "")
	return &knowledgeHarness{harness: h, kernel: kernel}, drafter
}

func decodeJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	return m
}

func hasCandidate(discovery map[string]any, key string) bool {
	candidates, ok := discovery["candidates"].([]any)
	if !ok {
		return false
	}
	for _, item := range candidates {
		candidate, ok := item.(map[string]any)
		if ok && candidate["key"] == key {
			return true
		}
	}
	return false
}

func TestAgentDiscoverEmptyCatalog(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	d := decodeJSON(t, agent.ok(t, "discover", `{"task":"anything"}`))
	if d["empty"] != true {
		t.Fatalf("discover of an empty catalog = %v", d)
	}
	if _, ok := d["candidates"]; ok {
		t.Fatalf("empty discover must not carry candidates: %v", d)
	}
}

func TestAgentDiscoverAndRead(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	leaf := &knowledge.Procedure{
		Key: "go.module.tidy", Goal: "Tidy a Go module's dependencies.", Philosophy: "Keep dependencies minimal.", Method: "Run go mod tidy.",
		Contract: jsontext.Value(`{"inputs":{"module":"path"}}`), Instructions: jsontext.Value(`{"steps":["go mod tidy"]}`),
	}
	cond := "Only when dependencies changed."
	root := &knowledge.Procedure{
		Key: "go.module.change", Goal: "Safely modify a Go module.", Philosophy: "Why it works.", Method: "Change, tidy, check.",
		Contract: jsontext.Value(`{"inputs":{"module":"path"}}`), Instructions: jsontext.Value(`{"steps":["change","tidy"]}`),
		References: []knowledge.Reference{
			{Name: "tidy", Key: "go.module.tidy", Inputs: jsontext.Value(`{"module":{"input":"module"}}`)},
			{Name: "tidy-conditional", Key: "go.module.tidy", Condition: &cond, Inputs: jsontext.Value(`{"module":{"input":"module"}}`)},
		},
	}
	snap := agent.publish(t, leaf, root)

	d := decodeJSON(t, agent.ok(t, "discover", `{"task":"safely modify a Go module"}`))
	if d["snapshot"] != string(snap) {
		t.Fatalf("discover snapshot = %v, want %s", d["snapshot"], snap)
	}
	cands := d["candidates"].([]any)
	if len(cands) == 0 {
		t.Fatalf("no candidates: %v", d)
	}
	first := cands[0].(map[string]any)
	if first["key"] != "go.module.change" || first["goal"] != "Safely modify a Go module." {
		t.Fatalf("first candidate = %v", first)
	}
	for _, c := range cands {
		for k := range c.(map[string]any) {
			if !slices.Contains([]string{"key", "goal", "score"}, k) {
				t.Fatalf("candidate member %q is not compact: %v", k, c)
			}
		}
	}

	text := agent.ok(t, "read", `{"key":"go.module.change","snapshot":"`+string(snap)+`","inputs":{"module":"example.com/m"}}`)
	g := decodeJSON(t, text)
	if g["snapshot"] != string(snap) || g["key"] != "go.module.change" || g["method"] != "Change, tidy, check." {
		t.Fatalf("read = %v", g)
	}
	if _, ok := g["procedure_id"]; ok {
		t.Fatalf("read must not expose procedure_id: %v", g)
	}
	if _, ok := g["version"]; ok {
		t.Fatalf("read must not expose version: %v", g)
	}
	if _, ok := g["philosophy"]; ok {
		t.Fatalf("read without full must not expose philosophy: %v", g)
	}
	refs := g["references"].([]any)
	if len(refs) != 2 {
		t.Fatalf("references = %v", refs)
	}
	plain := refs[0].(map[string]any)
	if plain["key"] != "go.module.tidy" || plain["inputs"].(map[string]any)["module"] != "example.com/m" {
		t.Fatalf("plain reference = %v", plain)
	}
	conditional := refs[1].(map[string]any)
	if conditional["condition"] != cond {
		t.Fatalf("conditional reference = %v", conditional)
	}

	full := decodeJSON(t, agent.ok(t, "read", `{"key":"go.module.change","snapshot":"`+string(snap)+`","full":true}`))
	if full["philosophy"] != "Why it works." {
		t.Fatalf("full read = %v", full)
	}
	noInputs := decodeJSON(t, agent.ok(t, "read", `{"key":"go.module.change","snapshot":"`+string(snap)+`"}`))
	if m, ok := noInputs["references"].([]any)[0].(map[string]any)["input_mapping"]; !ok || m == nil {
		t.Fatalf("read without inputs must carry input_mapping: %v", noInputs)
	}

	errRes := agent.call(t, "read", `{"key":"does.not.exist","snapshot":"`+string(snap)+`"}`)
	if !errRes.IsError {
		t.Fatalf("read of an unknown key must fail")
	}
}

func TestAgentFeedback(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	proc := &knowledge.Procedure{
		Key: "go.module.tidy", Goal: "Tidy a Go module's dependencies.", Philosophy: "p", Method: "m",
		Contract: jsontext.Value(`{}`), Instructions: jsontext.Value(`{}`),
	}
	snap := agent.publish(t, proc)

	text := agent.ok(t, "feedback", `{"key":"go.module.tidy","snapshot":"`+string(snap)+`","classification":"incorrect_instruction","summary":"the step is wrong","details":"it does not account for vendoring","reporter":"test.agent"}`)
	f := decodeJSON(t, text)
	if f["classification"] != "incorrect_instruction" || f["id"] == "" {
		t.Fatalf("feedback result = %v", f)
	}

	if res := agent.call(t, "feedback", `{"key":"go.module.tidy","snapshot":"`+string(snap)+`","classification":"environment_problem","summary":"s","reporter":"r"}`); !res.IsError {
		t.Fatalf("feedback must refuse a non-procedural classification")
	}
	if res := agent.call(t, "feedback", `{"key":"go.module.tidy","snapshot":"not-a-real-snapshot","classification":"incorrect_instruction","summary":"s","reporter":"r"}`); !res.IsError {
		t.Fatalf("feedback must refuse a snapshot that never served the key")
	}
}

func TestAgentDiscoverStats(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	shared := &knowledge.Procedure{
		Key: "go.module.tidy", Goal: "Tidy a Go module's dependencies.", Philosophy: "p", Method: "m",
		Applicability: knowledge.Applicability{Scope: knowledge.ScopeShared},
		Contract:      jsontext.Value(`{}`), Instructions: jsontext.Value(`{}`),
	}
	local := &knowledge.Procedure{
		Key: "team.deploy", Goal: "Deploy, the team way.", Philosophy: "p", Method: "m",
		Applicability: knowledge.Applicability{Scope: knowledge.ScopeLocal, Repository: "github.com/team/app"},
		Contract:      jsontext.Value(`{}`), Instructions: jsontext.Value(`{}`),
	}
	snap := agent.publish(t, shared, local)

	d := decodeJSON(t, agent.ok(t, "discover", `{"kind":"stats"}`))
	if d["snapshot"] != string(snap) || d["kind"] != "stats" {
		t.Fatalf("stats = %v", d)
	}
	if _, ok := d["generated_at"]; !ok {
		t.Fatalf("stats must carry generated_at: %v", d)
	}
	if _, ok := d["candidates"]; ok {
		t.Fatalf("stats must not carry candidates: %v", d)
	}
	byType, ok := d["by_type"].(map[string]any)
	if !ok {
		t.Fatalf("stats must carry by_type: %v", d)
	}
	for _, rt := range []string{"Domain", "Concept", "Claim", "Rule", "Procedure", "Evidence", "Exception"} {
		if byType[rt] != float64(0) {
			t.Fatalf("by_type[%s] = %v, want 0 (no typed records published)", rt, byType[rt])
		}
	}
	procs, ok := d["procedures"].(map[string]any)
	if !ok {
		t.Fatalf("stats must carry procedures: %v", d)
	}
	if procs["total"] != float64(2) {
		t.Fatalf("procedures.total = %v, want 2", procs["total"])
	}
	byRepo := procs["by_repository"].(map[string]any)
	if byRepo["shared"] != float64(1) || byRepo["github.com/team/app"] != float64(1) {
		t.Fatalf("procedures.by_repository = %v", byRepo)
	}
	completeness, ok := d["completeness"].(map[string]any)
	if !ok {
		t.Fatalf("stats must carry completeness: %v", d)
	}
	if completeness["missing_definition"] != float64(0) || completeness["missing_obligation"] != float64(0) {
		t.Fatalf("completeness = %v, want zero (no typed records published)", completeness)
	}

	scoped := decodeJSON(t, agent.ok(t, "discover", `{"kind":"stats","repository":"github.com/team/app"}`))
	if sp := scoped["procedures"].(map[string]any); sp["total"] != float64(2) {
		t.Fatalf("scoped procedures.total = %v, want 2 (shared + that repository's local)", sp["total"])
	}
	elsewhere := decodeJSON(t, agent.ok(t, "discover", `{"kind":"stats","repository":"github.com/other/app"}`))
	if ep := elsewhere["procedures"].(map[string]any); ep["total"] != float64(1) {
		t.Fatalf("procedures.total elsewhere = %v, want 1 (shared only, not another repository's local)", ep["total"])
	}
}

func TestAgentDiscoverTools(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	d := decodeJSON(t, agent.ok(t, "discover", `{"kind":"tools"}`))
	if d["kind"] != "tools" {
		t.Fatalf("discover(kind=tools) = %v", d)
	}
	var names []string
	for _, tl := range d["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"advance", "attach_child", "discover", "feedback", "propose_advance", "propose_publish_capture", "propose_start", "read", "resolve", "start"}) {
		t.Fatalf("discover(kind=tools) at v8 = %v", names)
	}
}

func TestAgentDiscoverKindValidation(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	if res := agent.call(t, "discover", `{"kind":"bogus"}`); !res.IsError {
		t.Fatalf("discover with an unknown kind must fail")
	}
	if res := agent.call(t, "discover", `{"kind":"stats","limit":5}`); !res.IsError {
		t.Fatalf("discover(kind=stats) with limit must fail")
	}
	if res := agent.call(t, "discover", `{"kind":"tools","limit":5}`); !res.IsError {
		t.Fatalf("discover(kind=tools) with limit must fail")
	}
	if res := agent.call(t, "discover", `{}`); !res.IsError {
		t.Fatalf("discover(kind=procedures, the default) without task must fail")
	}
	if res := agent.call(t, "discover", `{"kind":"stats"}`); res.IsError {
		t.Fatalf("discover(kind=stats) without task must succeed: %v", res)
	}
}

// planFixture publishes a parent Procedure record composing three children.
func planFixture(t *testing.T, agent *knowledgeHarness) knowledge.Snapshot {
	t.Helper()
	procs := []*knowledge.Procedure{
		knowledgetest.SharedProc("plan.parent", "Parent"),
		knowledgetest.SharedProc("plan.x", "x"), knowledgetest.SharedProc("plan.y", "y"), knowledgetest.SharedProc("plan.z", "z"),
	}
	records := []*knowledge.Record{
		// test.obligation satisfies ADR-0057's completeness bar (#164):
		// this fixture is about plan ordering, not obligation reasoning.
		{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"},
		{Type: knowledge.TypeProcedure, Key: "plan.parent", Label: "Parent", Definition: "A parent procedure.", Composes: []string{"plan.x", "plan.y", "plan.z"}, Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: "plan.x", Label: "x", Definition: "Step x.", Precedes: []string{"plan.y"}, Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: "plan.y", Label: "y", Definition: "Step y.", Precedes: []string{"plan.z"}, Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: "plan.z", Label: "z", Definition: "Step z.", Requires: []string{"test.obligation"}},
	}
	s, err := agent.publishRecords(t, procs, records)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func dynamicWalkthroughFixture(t *testing.T, agent *knowledgeHarness) knowledge.Snapshot {
	t.Helper()
	local := knowledgetest.SharedProc("dynamic.local", "A procedure local to another repository.")
	local.Applicability = knowledge.Applicability{Scope: knowledge.ScopeLocal, Repository: "github.com/other/app"}
	procs := []*knowledge.Procedure{
		knowledgetest.SharedProc("polaroid.using", "Use Polaroid knowledge for repository work."),
		knowledgetest.SharedProc("gate.check.test", "Run this module's Go tests."),
		knowledgetest.SharedProc("gate.check.test.cached", "Run the cache-enabled test packages."),
		knowledgetest.SharedProc("gate.check.test.fresh", "Run the always-fresh test packages."),
		knowledgetest.SharedProc("dynamic.late", "A later task-specific child."),
		knowledgetest.SharedProc("dynamic.cycle", "A child whose static composition reaches its parent."),
		local,
	}
	records := []*knowledge.Record{
		{Type: knowledge.TypeConcept, Key: "concept.repository-task-context", Label: "Repository task context", Definition: "An identified task and its repository context."},
		{Type: knowledge.TypeConcept, Key: "dynamic.test-obligation", Label: "Test obligation", Definition: "The test workflow has an explicit obligation."},
		{Type: knowledge.TypeConcept, Key: "dynamic.local-obligation", Label: "Local obligation", Definition: "A local procedure requirement."},
		{Type: knowledge.TypeConcept, Key: "dynamic.not-procedure", Label: "Not a Procedure", Definition: "This record cannot be attached as a Procedure child."},
		{Type: knowledge.TypeProcedure, Key: "polaroid.using", Label: "Using Polaroid", Definition: "Guide an agent through the Polaroid knowledge workflow.", Preconditions: []string{"concept.repository-task-context"}},
		{Type: knowledge.TypeProcedure, Key: "gate.check.test", Label: "Go tests", Definition: "Run the test procedure.", Requires: []string{"dynamic.test-obligation"}, Composes: []string{"gate.check.test.cached", "gate.check.test.fresh"}},
		{Type: knowledge.TypeProcedure, Key: "gate.check.test.cached", Label: "Cached test set", Definition: "Run the cache-enabled packages.", Requires: []string{"dynamic.test-obligation"}, Precedes: []string{"gate.check.test.fresh"}},
		{Type: knowledge.TypeProcedure, Key: "gate.check.test.fresh", Label: "Fresh test set", Definition: "Run the always-fresh packages.", Requires: []string{"dynamic.test-obligation"}},
		{Type: knowledge.TypeProcedure, Key: "dynamic.late", Label: "Late child", Definition: "A child attached after the parent starts.", Requires: []string{"dynamic.test-obligation"}},
		{Type: knowledge.TypeProcedure, Key: "dynamic.cycle", Label: "Cycle child", Definition: "Its static child is the dynamic parent.", Requires: []string{"dynamic.test-obligation"}, Composes: []string{"polaroid.using"}},
		{Type: knowledge.TypeProcedure, Key: "dynamic.local", Label: "Other repository only", Definition: "A procedure outside this repository.", Requires: []string{"dynamic.local-obligation"}},
	}
	snapshot, err := agent.publishRecords(t, procs, records)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestResolvePlanOrderedByPrecedes(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	snap := planFixture(t, agent)

	d := decodeJSON(t, agent.ok(t, "resolve", `{"goal":"plan.parent","snapshot":"`+string(snap)+`","at":"2026-10-15T12:00:00Z"}`))
	plan, ok := d["plan"].([]any)
	if !ok || len(plan) != 3 {
		t.Fatalf("plan = %v, want 3 entries", d["plan"])
	}
	first := plan[0].(map[string]any)
	if first["key"] != "plan.x" || first["status"] != "next" {
		t.Fatalf("first entry = %v, want plan.x next", first)
	}
	for _, e := range plan[1:] {
		if _, has := e.(map[string]any)["status"]; has {
			t.Fatalf("entry beyond next must carry no status: %v", e)
		}
	}

	// completed=[plan.x]: x is done, y is next.
	d2 := decodeJSON(t, agent.ok(t, "resolve", `{"goal":"plan.parent","snapshot":"`+string(snap)+`","at":"2026-10-15T12:00:00Z","completed":["plan.x"]}`))
	plan2 := d2["plan"].([]any)
	if e := plan2[0].(map[string]any); e["status"] != "done" {
		t.Fatalf("plan.x = %v, want done", e)
	}
	if e := plan2[1].(map[string]any); e["status"] != "next" {
		t.Fatalf("plan.y = %v, want next", e)
	}
}

func TestResolveNoPlanWithoutComposition(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	leaf := []*knowledge.Procedure{knowledgetest.SharedProc("plan.leaf", "Leaf")}
	records := []*knowledge.Record{
		{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"},
		{Type: knowledge.TypeProcedure, Key: "plan.leaf", Label: "Leaf", Definition: "A leaf, nothing composed.", Requires: []string{"test.obligation"}},
	}
	snap, err := agent.publishRecords(t, leaf, records)
	if err != nil {
		t.Fatal(err)
	}
	d := decodeJSON(t, agent.ok(t, "resolve", `{"goal":"plan.leaf","snapshot":"`+string(snap)+`","at":"2026-10-15T12:00:00Z"}`))
	if _, ok := d["plan"]; ok {
		t.Fatalf("resolve of a goal with no composed children must carry no plan member: %v", d)
	}
}

func TestResolveInvalidCompleted(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	snap := planFixture(t, agent)
	res := agent.call(t, "resolve", `{"goal":"plan.parent","snapshot":"`+string(snap)+`","at":"2026-10-15T12:00:00Z","completed":["not-a-child"]}`)
	if !res.IsError {
		t.Fatalf("completed naming a key that is not an immediate composed child must fail")
	}
	if !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"invalid_request"`) {
		t.Fatalf("must be invalid_request: %s", res.Content[0].(*sdk.TextContent).Text)
	}
}

func TestAgentV8FieldCompleteProposalOverRealTransport(t *testing.T) {
	agent, drafter := newKnowledgeHarnessV8(t)
	draftFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "propose_start", `{"key":"draft.producer","record_type":"Procedure","proposer":"test.agent","rationale":"ground the producer's outcome"}`))
	fields, ok := start["fields"].([]any)
	if !ok || len(fields) != len(knowledge.ProcedureDocumentFieldSpecs())+len(knowledge.RecordFieldSpecs(knowledge.TypeProcedure)) {
		t.Fatalf("v8 propose_start fields = %v", start["fields"])
	}
	if start["next_entity"] != "procedure" || start["next_field"] != "goal" {
		t.Fatalf("v8 first field = %v.%v, want procedure.goal", start["next_entity"], start["next_field"])
	}
	toolNames := map[string]bool{}
	for tool, err := range agent.session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		toolNames[tool.Name] = true
	}
	for _, name := range []string{"propose_start", "propose_advance", "propose_publish_capture"} {
		if !toolNames[name] {
			t.Fatalf("v8 missing tool %s: %v", name, toolNames)
		}
	}

	token := start["token"].(string)
	var result map[string]any
	for _, value := range fields {
		field := value.(map[string]any)
		outcome := map[string]any{"entity": field["entity"], "field": field["field"], "operation": "keep"}
		if field["entity"] == "record" && field["field"] == "postconditions" {
			outcome["operation"] = "add"
			outcome["value"] = []string{"draft.some-precondition"}
			outcome["citations"] = []map[string]string{{"source": "draft.consumer", "quote": "draft.some-precondition"}}
		}
		args, err := json.Marshal(map[string]any{"token": token, "outcome": outcome})
		if err != nil {
			t.Fatal(err)
		}
		result = decodeJSON(t, agent.ok(t, "propose_advance", string(args)))
	}
	if result["status"] != "ready" || result["key"] != "draft.producer" || result["candidate_hash"] == "" {
		t.Fatalf("v8 final proposal result = %v", result)
	}
	before, err := agent.kernel.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(before.Snapshot()) != start["snapshot"] {
		t.Fatal("final field answer published without authorization")
	}
	if _, err := drafter.PublishCapture(context.Background(), wire.FieldDraftPublishRequest{
		Token: token, CandidateHash: result["candidate_hash"].(string),
	}); err == nil {
		t.Fatal("ordinary draft published as capture")
	}
	published, err := drafter.PublishApproved(context.Background(), wire.FieldDraftPublishRequest{
		Token: token, CandidateHash: result["candidate_hash"].(string),
	})
	if err != nil || published.Status != "complete" {
		t.Fatalf("operator approval: %+v, %v", published, err)
	}
	cat, err := agent.kernel.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, err := cat.Record("draft.producer")
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Postconditions) != 1 || record.Postconditions[0] != "draft.some-precondition" {
		t.Fatalf("v8 published postconditions = %v", record.Postconditions)
	}
}

func TestAgentV8CaptureAuthorizationAndReplay(t *testing.T) {
	agent, _ := newKnowledgeHarnessV8(t)
	draftFixture(t, agent)
	root := knowledgetest.SharedProc("polaroid.using", "guide repository work")
	capture := knowledgetest.SharedProc("polaroid.session-close-capture", "capture verified learning")
	capture.Applicability = knowledge.Applicability{Scope: knowledge.ScopeLocal, Repository: "github.com/ashuangiras/polaroid"}
	task := knowledgetest.SharedProc("task.verified", "verify work")
	if _, err := agent.publishRecords(t, []*knowledge.Procedure{root, capture, task}, []*knowledge.Record{
		{Type: knowledge.TypeProcedure, Key: root.Key, Label: "root", Definition: "d", Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: capture.Key, Label: "capture", Definition: "d", Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: task.Key, Label: "task", Definition: "d", Requires: []string{"test.obligation"}},
	}); err != nil {
		t.Fatal(err)
	}
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"polaroid.using","repository":"github.com/ashuangiras/polaroid","allow_dynamic_children":true}`))
	walk := start["token"].(string)
	agent.ok(t, "attach_child", fmt.Sprintf(`{"token":%q,"key":"polaroid.session-close-capture"}`, walk))
	agent.ok(t, "attach_child", fmt.Sprintf(`{"token":%q,"key":"task.verified"}`, walk))
	startDraft := func(parent string) map[string]any {
		t.Helper()
		return decodeJSON(t, agent.ok(t, "propose_start", fmt.Sprintf(
			`{"key":"draft.some-precondition","record_type":"Concept","proposer":"test.agent","rationale":"verified learning","walkthrough_token":%q}`, parent)))
	}
	review := func(draft map[string]any, definition string) map[string]any {
		t.Helper()
		for _, value := range draft["fields"].([]any) {
			field := value.(map[string]any)
			outcome := map[string]any{"entity": field["entity"], "field": field["field"], "operation": "keep"}
			if field["field"] == "definition" {
				outcome["operation"], outcome["value"] = "replace", definition
				outcome["citations"] = []map[string]string{{"source": "test", "quote": definition}}
			}
			arg, err := json.Marshal(map[string]any{"token": draft["token"], "outcome": outcome})
			if err != nil {
				t.Fatal(err)
			}
			draft = decodeJSON(t, agent.ok(t, "propose_advance", string(arg)))
		}
		return draft
	}
	agent.fail(t, "propose_start", `{"key":"draft.some-precondition","record_type":"Concept","proposer":"agent","rationale":"r","walkthrough_token":"fabricated"}`, "not_found")
	wrongRepo := decodeJSON(t, agent.ok(t, "start", `{"goal":"polaroid.using","repository":"github.com/another/repo","allow_dynamic_children":true}`))
	agent.fail(t, "propose_start", fmt.Sprintf(
		`{"key":"draft.some-precondition","record_type":"Concept","proposer":"agent","rationale":"r","walkthrough_token":%q}`, wrongRepo["token"]), "invalid_request")
	wrongChild := decodeJSON(t, agent.ok(t, "start", `{"goal":"polaroid.using","repository":"github.com/ashuangiras/polaroid","allow_dynamic_children":true}`))
	agent.ok(t, "attach_child", fmt.Sprintf(`{"token":%q,"key":"task.verified"}`, wrongChild["token"]))
	agent.fail(t, "propose_start", fmt.Sprintf(
		`{"key":"draft.some-precondition","record_type":"Concept","proposer":"agent","rationale":"r","walkthrough_token":%q}`, wrongChild["token"]), "invalid_request")
	wrongGoal := decodeJSON(t, agent.ok(t, "start", `{"goal":"draft.parent","repository":"github.com/ashuangiras/polaroid","allow_dynamic_children":true}`))
	agent.ok(t, "attach_child", fmt.Sprintf(`{"token":%q,"key":"polaroid.session-close-capture"}`, wrongGoal["token"]))
	agent.fail(t, "propose_start", fmt.Sprintf(
		`{"key":"draft.some-precondition","record_type":"Concept","proposer":"agent","rationale":"r","walkthrough_token":%q}`, wrongGoal["token"]), "invalid_request")
	draft := review(startDraft(walk), "a verified concept")
	stale := review(startDraft(walk), "a stale concept")
	token := draft["token"].(string)
	if draft["status"] != "ready" || draft["candidate_hash"] == "" {
		t.Fatalf("draft = %v", draft)
	}
	request := fmt.Sprintf(`{"token":%q,"candidate_hash":%q}`, token, draft["candidate_hash"])
	before, err := agent.kernel.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	agent.fail(t, "propose_publish_capture", request, "invalid_request")
	wrong := fmt.Sprintf(`{"token":%q,"candidate_hash":"%s"}`, token, strings.Repeat("0", 64))
	agent.ok(t, "advance", fmt.Sprintf(`{"token":%q,"outcome":{"key":"task.verified","status":"done"}}`, walk))
	agent.fail(t, "propose_publish_capture", wrong, "invalid_request")
	current, err := agent.kernel.Active(context.Background())
	if err != nil || current.Snapshot() != before.Snapshot() {
		t.Fatalf("unauthorized attempts moved snapshot: %v", err)
	}
	published := decodeJSON(t, agent.ok(t, "propose_publish_capture", request))
	if published["status"] != "complete" || published["snapshot"] == draft["snapshot"] {
		t.Fatalf("capture = %v", published)
	}
	agent.fail(t, "propose_publish_capture", request, "not_found")
	second := review(startDraft(walk), "second verified concept")
	secondResult := decodeJSON(t, agent.ok(t, "propose_publish_capture",
		fmt.Sprintf(`{"token":%q,"candidate_hash":%q}`, second["token"], second["candidate_hash"])))
	if secondResult["status"] != "complete" || secondResult["snapshot"] == published["snapshot"] {
		t.Fatalf("sequential capture = %v", secondResult)
	}
	agent.fail(t, "propose_publish_capture", fmt.Sprintf(`{"token":%q,"candidate_hash":%q}`, stale["token"], stale["candidate_hash"]), "stale_snapshot")
	skippedParent := decodeJSON(t, agent.ok(t, "start", `{"goal":"polaroid.using","repository":"github.com/ashuangiras/polaroid","allow_dynamic_children":true}`))
	agent.ok(t, "attach_child", fmt.Sprintf(`{"token":%q,"key":"polaroid.session-close-capture"}`, skippedParent["token"]))
	agent.ok(t, "attach_child", fmt.Sprintf(`{"token":%q,"key":"task.verified"}`, skippedParent["token"]))
	skipped := review(startDraft(skippedParent["token"].(string)), "skipped candidate")
	agent.ok(t, "advance", fmt.Sprintf(`{"token":%q,"outcome":{"key":"task.verified","status":"done"}}`, skippedParent["token"]))
	agent.ok(t, "advance", fmt.Sprintf(`{"token":%q,"outcome":{"key":"polaroid.session-close-capture","status":"skipped"}}`, skippedParent["token"]))
	agent.fail(t, "propose_publish_capture", fmt.Sprintf(`{"token":%q,"candidate_hash":%q}`, skipped["token"], skipped["candidate_hash"]), "invalid_request")
	pending := review(startDraft(walk), "another verified concept")
	agent.ok(t, "advance", fmt.Sprintf(`{"token":%q,"close":true}`, walk))
	agent.fail(t, "propose_publish_capture", fmt.Sprintf(`{"token":%q,"candidate_hash":%q}`, pending["token"], pending["candidate_hash"]), "not_found")
}

// draftFixture publishes the same vacuous producer/consumer shape
// proposeFixture uses, plus a standalone procedure with every content
// field genuinely undecided -- for walking propose_start/propose_advance
// over the real transport.
func draftFixture(t *testing.T, agent *knowledgeHarness) knowledge.Snapshot {
	t.Helper()
	procs := []*knowledge.Procedure{
		knowledgetest.SharedProc("draft.producer", "producer"), knowledgetest.SharedProc("draft.consumer", "consumer"),
		knowledgetest.SharedProc("draft.parent", "parent"),
	}
	records := []*knowledge.Record{
		{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"},
		{Type: knowledge.TypeConcept, Key: "draft.some-precondition", Label: "x", Definition: "d"},
		{Type: knowledge.TypeProcedure, Key: "draft.producer", Label: "producer", Definition: "d",
			Precedes: []string{"draft.consumer"}, Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: "draft.consumer", Label: "consumer", Definition: "d",
			Preconditions: []string{"draft.some-precondition"}, Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: "draft.parent", Label: "parent", Definition: "d",
			Composes: []string{"draft.producer", "draft.consumer"}, Requires: []string{"test.obligation"}},
	}
	s, err := agent.publishRecords(t, procs, records)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWalkthroughStartPinsSnapshotAndReturnsPlan(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	snap := planFixture(t, agent)

	res := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	if res["snapshot"] != string(snap) || res["token"] == "" {
		t.Fatalf("start = %v, want snapshot %s", res, snap)
	}
	plan := res["plan"].([]any)
	if len(plan) != 3 || plan[0].(map[string]any)["key"] != "plan.x" || plan[0].(map[string]any)["status"] != "next" {
		t.Fatalf("start plan = %v", plan)
	}

	// Publishing again does not change an already started walkthrough's
	// plan: it stays pinned at the snapshot start resolved.
	later := []*knowledge.Procedure{knowledgetest.SharedProc("plan.later", "later")}
	laterRecords := []*knowledge.Record{{Type: knowledge.TypeProcedure, Key: "plan.later", Label: "later", Definition: "d", Requires: []string{"test.obligation"}}}
	if _, err := agent.publishRecords(t, later, laterRecords); err != nil {
		t.Fatal(err)
	}
	token := res["token"].(string)
	peek := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`"}`))
	if peek["snapshot"] != string(snap) {
		t.Fatalf("advance after a later publish must keep the pinned snapshot: %v, want %s", peek["snapshot"], snap)
	}
}

func TestAgentV8DynamicChildCompletesAndCanReopenParentFlow(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	snapshot := dynamicWalkthroughFixture(t, agent)
	const repository = "github.com/ashuangiras/polaroid"

	rootDiscovery := decodeJSON(t, agent.ok(t, "discover", `{"task":"polaroid.using: run Go tests","repository":"`+repository+`"}`))
	if !hasCandidate(rootDiscovery, "polaroid.using") {
		t.Fatalf("root was not discovered: %v", rootDiscovery)
	}
	root := decodeJSON(t, agent.ok(t, "read", `{"key":"polaroid.using","repository":"`+repository+`","snapshot":"`+string(snapshot)+`"}`))
	if root["scope"] != "shared" {
		t.Fatalf("root scope = %v, want shared", root["scope"])
	}
	resolvedRoot := decodeJSON(t, agent.ok(t, "resolve", `{"goal":"polaroid.using","snapshot":"`+string(snapshot)+`","context":{"repository":"`+repository+`"}}`))
	if resolvedRoot["complete"] != true {
		t.Fatalf("root resolution = %v", resolvedRoot)
	}

	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"polaroid.using","snapshot":"`+string(snapshot)+`","repository":"`+repository+`","allow_dynamic_children":true,"context":{"task":"run Go tests"}}`))
	if start["status"] != "in_progress" {
		t.Fatalf("dynamic parent without a child = %v, want in_progress", start)
	}
	if plan, ok := start["plan"].([]any); ok && len(plan) != 0 {
		t.Fatalf("dynamic parent unexpectedly has plan steps before a child is attached: %v", plan)
	}
	token := start["token"].(string)

	childDiscovery := decodeJSON(t, agent.ok(t, "discover", `{"task":"Run this module's Go tests, including cached and always-fresh packages","repository":"`+repository+`","snapshot":"`+string(snapshot)+`"}`))
	if !hasCandidate(childDiscovery, "gate.check.test") {
		t.Fatalf("test procedure was not discovered: %v", childDiscovery)
	}
	child := decodeJSON(t, agent.ok(t, "read", `{"key":"gate.check.test","repository":"`+repository+`","snapshot":"`+string(snapshot)+`","full":true}`))
	if child["key"] != "gate.check.test" {
		t.Fatalf("child guidance = %v", child)
	}

	attached := decodeJSON(t, agent.ok(t, "attach_child", `{"token":"`+token+`","key":"gate.check.test"}`))
	plan := attached["plan"].([]any)
	rootStep := plan[0].(map[string]any)
	if rootStep["key"] != "gate.check.test" || rootStep["status"] != "next" {
		t.Fatalf("attached child plan = %v", plan)
	}
	path := rootStep["path"].([]any)
	if len(path) != 2 || path[0] != "gate.check.test" || path[1] != "gate.check.test.cached" {
		t.Fatalf("first child path = %v, want [gate.check.test gate.check.test.cached]", path)
	}
	agent.fail(t, "attach_child", `{"token":"`+token+`","key":"gate.check.test.cached"}`, "invalid_request")
	unchanged := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`"}`))
	if plan := unchanged["plan"].([]any); len(plan) != 1 || plan[0].(map[string]any)["key"] != "gate.check.test" {
		t.Fatalf("attaching an already nested step changed the parent plan: %v", plan)
	}

	first := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"path":["gate.check.test","gate.check.test.cached"],"status":"done"}}`))
	if first["status"] != "in_progress" {
		t.Fatalf("parent status after first nested step = %v, want in_progress", first)
	}
	next := first["plan"].([]any)[0].(map[string]any)["path"].([]any)
	if len(next) != 2 || next[1] != "gate.check.test.fresh" {
		t.Fatalf("next child path = %v, want fresh test step", next)
	}
	completed := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"path":["gate.check.test","gate.check.test.fresh"],"status":"done"}}`))
	if completed["status"] != "complete" {
		t.Fatalf("parent after all child steps = %v, want complete", completed)
	}

	reopened := decodeJSON(t, agent.ok(t, "attach_child", `{"token":"`+token+`","key":"dynamic.late"}`))
	if reopened["status"] != "in_progress" {
		t.Fatalf("attaching after complete did not reopen parent: %v", reopened)
	}
	latePlan := reopened["plan"].([]any)
	var late map[string]any
	for _, item := range latePlan {
		entry := item.(map[string]any)
		if entry["key"] == "dynamic.late" {
			late = entry
		}
	}
	if late == nil || late["status"] != "next" {
		t.Fatalf("late child plan = %v", latePlan)
	}
	final := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"key":"dynamic.late","status":"done"}}`))
	if final["status"] != "complete" {
		t.Fatalf("parent after late child = %v, want complete", final)
	}
	closed := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","close":true}`))
	if closed["status"] != "complete" {
		t.Fatalf("closed completed parent = %v", closed)
	}
}

func TestAgentV8DynamicChildValidationAndPinnedSnapshot(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	snapshot := dynamicWalkthroughFixture(t, agent)
	const repository = "github.com/ashuangiras/polaroid"
	static := decodeJSON(t, agent.ok(t, "start", `{"goal":"polaroid.using","snapshot":"`+string(snapshot)+`","repository":"`+repository+`"}`))
	if static["status"] != "complete" {
		t.Fatalf("ordinary v8 start of a leaf = %v, want complete", static)
	}
	agent.fail(t, "attach_child", `{"token":"`+static["token"].(string)+`","key":"dynamic.late"}`, "invalid_request")

	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"polaroid.using","snapshot":"`+string(snapshot)+`","repository":"`+repository+`","allow_dynamic_children":true}`))
	token := start["token"].(string)

	agent.fail(t, "attach_child", `{"token":"`+token+`","key":"missing.procedure"}`, "not_found")
	agent.fail(t, "attach_child", `{"token":"`+token+`","key":"dynamic.not-procedure"}`, "invalid_request")
	agent.fail(t, "attach_child", `{"token":"`+token+`","key":"dynamic.local"}`, "not_applicable")
	agent.fail(t, "attach_child", `{"token":"`+token+`","key":"dynamic.cycle"}`, "invalid_request")

	agent.ok(t, "attach_child", `{"token":"`+token+`","key":"dynamic.late"}`)
	agent.fail(t, "attach_child", `{"token":"`+token+`","key":"dynamic.late"}`, "invalid_request")

	later := []*knowledge.Procedure{knowledgetest.SharedProc("dynamic.after-snapshot", "Published after the parent started.")}
	laterRecords := []*knowledge.Record{{Type: knowledge.TypeProcedure, Key: "dynamic.after-snapshot", Label: "Later", Definition: "Not in the pinned snapshot.", Requires: []string{"dynamic.test-obligation"}}}
	if _, err := agent.publishRecords(t, later, laterRecords); err != nil {
		t.Fatal(err)
	}
	agent.fail(t, "attach_child", `{"token":"`+token+`","key":"dynamic.after-snapshot"}`, "not_found")
	agent.ok(t, "advance", `{"token":"`+token+`","close":true}`)
	agent.fail(t, "attach_child", `{"token":"`+token+`","key":"dynamic.late"}`, "not_found")
}

func TestWalkthroughAdvancePeekIsUnchanged(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)

	peek := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`"}`))
	peekPlan, _ := json.Marshal(peek["plan"])
	startPlan, _ := json.Marshal(start["plan"])
	if string(peekPlan) != string(startPlan) {
		t.Fatalf("a peek must leave the plan unchanged: %s, want %s", peekPlan, startPlan)
	}
}

func TestWalkthroughAdvanceFoldsPerKeyAndEscalateFreezesNext(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)

	escalated := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"key":"plan.x","status":"escalate"}}`))
	plan := escalated["plan"].([]any)
	if plan[0].(map[string]any)["status"] != "escalate" {
		t.Fatalf("an escalated leaf must be reported as escalate, freezing the plan there: %v", plan)
	}

	done := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"key":"plan.x","status":"done"}}`))
	plan2 := done["plan"].([]any)
	if plan2[0].(map[string]any)["status"] != "done" || plan2[1].(map[string]any)["status"] != "next" {
		t.Fatalf("a later done must override the earlier escalate and unfreeze next: %v", plan2)
	}
}

func TestWalkthroughAdvanceInvalidOutcomeKey(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)

	res := agent.call(t, "advance", `{"token":"`+token+`","outcome":{"key":"not-a-child","status":"done"}}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"invalid_request"`) {
		t.Fatalf("outcome.key outside the goal's composed children must be invalid_request: %v", res)
	}
}

func TestWalkthroughAdvanceUnknownTokenIsNotFound(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	res := agent.call(t, "advance", `{"token":"never-started"}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"not_found"`) {
		t.Fatalf("an unknown token must be not_found: %v", res)
	}
}

func TestWalkthroughStartUnknownGoalIsNotFound(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	res := agent.call(t, "start", `{"goal":"nope"}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"not_found"`) {
		t.Fatalf("an unknown goal must be not_found: %v", res)
	}
}

// deepPlanFixture publishes goal -> a -> b (a leaf).
func deepPlanFixture(t *testing.T, agent *knowledgeHarness) {
	t.Helper()
	procs := []*knowledge.Procedure{
		knowledgetest.SharedProc("deep.goal", "goal"), knowledgetest.SharedProc("deep.a", "a"), knowledgetest.SharedProc("deep.b", "b"),
	}
	records := []*knowledge.Record{
		// test.obligation satisfies ADR-0057's completeness bar (#164):
		// this fixture is about multi-level plans, not obligation
		// reasoning.
		{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"},
		{Type: knowledge.TypeProcedure, Key: "deep.goal", Label: "goal", Definition: "d", Composes: []string{"deep.a"}, Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: "deep.a", Label: "a", Definition: "d", Composes: []string{"deep.b"}, Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: "deep.b", Label: "b", Definition: "d", Requires: []string{"test.obligation"}},
	}
	if _, err := agent.publishRecords(t, procs, records); err != nil {
		t.Fatal(err)
	}
}

func TestWalkthroughStartDescendsToDeepLeaf(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	deepPlanFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"deep.goal"}`))
	plan := start["plan"].([]any)
	entry := plan[0].(map[string]any)
	if entry["key"] != "deep.a" || entry["status"] != "next" {
		t.Fatalf("plan = %v", plan)
	}
	path := entry["path"].([]any)
	if len(path) != 2 || path[0] != "deep.a" || path[1] != "deep.b" {
		t.Fatalf("path = %v, want [deep.a deep.b]", path)
	}
}

func TestWalkthroughAdvanceDeepLeafByPath(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	deepPlanFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"deep.goal"}`))
	token := start["token"].(string)

	res := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"path":["deep.a","deep.b"],"status":"done"}}`))
	plan := res["plan"].([]any)
	if plan[0].(map[string]any)["status"] != "done" {
		t.Fatalf("advancing the deep leaf by its full path must complete it: %v", plan)
	}
}

func TestWalkthroughAdvanceComposedTargetIsInvalidRequest(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	deepPlanFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"deep.goal"}`))
	token := start["token"].(string)

	res := agent.call(t, "advance", `{"token":"`+token+`","outcome":{"key":"deep.a","status":"done"}}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"invalid_request"`) {
		t.Fatalf("a composed key (deep.a is not a leaf) must never be accepted as a target: %v", res)
	}
}

func TestWalkthroughAdvanceKeyAndPathBothGivenIsInvalidRequest(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	deepPlanFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"deep.goal"}`))
	token := start["token"].(string)

	res := agent.call(t, "advance", `{"token":"`+token+`","outcome":{"key":"deep.b","path":["deep.a","deep.b"],"status":"done"}}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"invalid_request"`) {
		t.Fatalf("key and path must not both be given: %v", res)
	}
}

// Walkthrough completion and close behavior over real v8 HTTP.

func TestWalkthroughStatusCompleteAndInProgress(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	if start["status"] != "in_progress" {
		t.Fatalf("start status = %v, want in_progress", start["status"])
	}
	token := start["token"].(string)
	for _, key := range []string{"plan.x", "plan.y", "plan.z"} {
		res := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"key":"`+key+`","status":"done"}}`))
		if key != "plan.z" && res["status"] != "in_progress" {
			t.Fatalf("after %s: status = %v, want in_progress", key, res["status"])
		}
		if key == "plan.z" && res["status"] != "complete" {
			t.Fatalf("after every leaf done: status = %v, want complete", res["status"])
		}
	}
}

func TestWalkthroughStatusReopenAfterComplete(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	deepPlanFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"deep.goal"}`))
	token := start["token"].(string)

	done := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"path":["deep.a","deep.b"],"status":"done"}}`))
	if done["status"] != "complete" {
		t.Fatalf("status = %v, want complete", done["status"])
	}
	reopened := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"path":["deep.a","deep.b"],"status":"escalate"}}`))
	if reopened["status"] != "in_progress" {
		t.Fatalf("a later escalate on an already-done leaf must reopen status: %v", reopened["status"])
	}
}

func TestWalkthroughAdvanceCloseEndsItImmediately(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)

	closed := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","close":true}`))
	if closed["token"] != token {
		t.Fatalf("a close call must still return the final state: %v", closed)
	}
	res := agent.call(t, "advance", `{"token":"`+token+`"}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"not_found"`) {
		t.Fatalf("a call after close must be not_found: %v", res)
	}
}

func TestWalkthroughAdvanceCloseWithOutcomeAppliesThenCloses(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)

	res := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"key":"plan.x","status":"done"},"close":true}`))
	plan := res["plan"].([]any)
	if plan[0].(map[string]any)["status"] != "done" {
		t.Fatalf("the outcome must be reflected in the close call's own response: %v", res)
	}
	if res2 := agent.call(t, "advance", `{"token":"`+token+`"}`); !res2.IsError {
		t.Fatalf("the walkthrough must be gone after close")
	}
}

func TestWalkthroughAdvanceCloseAlreadyClosedIsNotFound(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)
	agent.ok(t, "advance", `{"token":"`+token+`","close":true}`)

	res := agent.call(t, "advance", `{"token":"`+token+`","close":true}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"not_found"`) {
		t.Fatalf("closing an already-closed token must be not_found: %v", res)
	}
}

// Blocked status, reason and expanded plan over real v8 HTTP.

func TestWalkthroughAdvanceBlockedWithReason(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)

	res := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"key":"plan.x","status":"blocked","reason":"missing required tool"}}`))
	plan := res["plan"].([]any)
	if plan[0].(map[string]any)["status"] != "blocked" {
		t.Fatalf("plan.x must report blocked: %v", plan)
	}
	if res["status"] != "in_progress" {
		t.Fatalf("status = %v, want in_progress (a blocked leaf is not done)", res["status"])
	}
}

func TestWalkthroughAdvanceInvalidStatusIsInvalidRequest(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)

	res := agent.call(t, "advance", `{"token":"`+token+`","outcome":{"key":"plan.x","status":"pending"}}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"invalid_request"`) {
		t.Fatalf("an unknown status must be invalid_request: %v", res)
	}
}

func TestWalkthroughStartExpandAttachesNestedChildren(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	deepPlanFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"deep.goal","expand":true}`))
	plan := start["plan"].([]any)
	entry := plan[0].(map[string]any)
	if entry["key"] != "deep.a" || entry["status"] != "next" {
		t.Fatalf("expand's top level must match the non-expand plan: %v", plan)
	}
	children := entry["children"].([]any)
	if len(children) != 1 || children[0].(map[string]any)["key"] != "deep.b" || children[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("deep.a's children = %v, want deep.b pending", children)
	}
}

func TestWalkthroughAdvanceExpandReflectsReportedChild(t *testing.T) {
	agent := mustKnowledgeHarnessV8(t)
	deepPlanFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"deep.goal"}`))
	token := start["token"].(string)

	res := decodeJSON(t, agent.ok(t, "advance", `{"token":"`+token+`","outcome":{"path":["deep.a","deep.b"],"status":"done"},"expand":true}`))
	plan := res["plan"].([]any)
	children := plan[0].(map[string]any)["children"].([]any)
	if len(children) != 1 || children[0].(map[string]any)["key"] != "deep.b" || children[0].(map[string]any)["status"] != "done" {
		t.Fatalf("expand must reflect the reported child's own status: %v", children)
	}
}
func mustKnowledgeHarnessV8(t *testing.T) *knowledgeHarness {
	h, _ := newKnowledgeHarnessV8(t)
	return h
}
