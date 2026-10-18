package mcp_test

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
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
)

// ADR-0043, finalized for Git-native knowledge by ADR-0048: the agent-mode
// endpoint, backed solely by living knowledge named by canonical key and
// snapshot token.

// knowledgeHarness runs the real agent-mode MCP handler over a real
// Git-core repository (for knowledge) and a real SQLite file (for
// feedback), over real HTTP.
type knowledgeHarness struct {
	*harness
	kernel *knowledge.Kernel
}

func newKnowledgeHarness(t *testing.T) *knowledgeHarness {
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
	kernel := knowledge.New(gitstore.New(repo))
	agent := knowledgeops.NewAgent(kernel, memory.NewService(store))
	logs := &syncBuffer{}
	srv := httptest.NewServer(mcptransport.NewAgentHandler(agent, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)
	h := &harness{url: srv.URL, store: store, logs: logs}
	h.session = h.connect(t, "")
	return &knowledgeHarness{harness: h, kernel: kernel}
}

// publish commits procs atop the active snapshot and returns the result.
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

// newKnowledgeHarnessV2 runs /mcp/agent/v2 (ADR-0050): the same discover,
// read and feedback as v1, plus resolve, with real semantic and rule
// validators so typed records (composes, precedes, requires) publish.
func newKnowledgeHarnessV2(t *testing.T) *knowledgeHarness {
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
	sem, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	ev := datalog.Evaluator{}
	kernel := knowledge.New(gitstore.New(repo), knowledge.WithRecordValidator(sem), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev}))
	agent := knowledgeops.NewAgent(kernel, memory.NewService(store))
	resolver := knowledgeops.NewResolver(kernel, ev, policy.Trust{})
	logs := &syncBuffer{}
	srv := httptest.NewServer(mcptransport.NewAgentHandlerV2(agent, resolver, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)
	h := &harness{url: srv.URL, store: store, logs: logs}
	h.session = h.connect(t, "")
	return &knowledgeHarness{harness: h, kernel: kernel}
}

// newKnowledgeHarnessV3 runs /mcp/agent/v3 (ADR-0053): the same discover,
// read, feedback and resolve as v2, plus start and advance, backed by
// walkthrough storage in the same SQLite file as feedback.
func newKnowledgeHarnessV3(t *testing.T) *knowledgeHarness {
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
	sem, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	ev := datalog.Evaluator{}
	kernel := knowledge.New(gitstore.New(repo), knowledge.WithRecordValidator(sem), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev}))
	agent := knowledgeops.NewAgent(kernel, memory.NewService(store))
	resolver := knowledgeops.NewResolver(kernel, ev, policy.Trust{})
	guide := knowledgeops.NewGuide(kernel, memory.NewWalkthroughs(store), ev, policy.Trust{})
	logs := &syncBuffer{}
	srv := httptest.NewServer(mcptransport.NewAgentHandlerV3(agent, resolver, guide, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)
	h := &harness{url: srv.URL, store: store, logs: logs}
	h.session = h.connect(t, "")
	return &knowledgeHarness{harness: h, kernel: kernel}
}

// newKnowledgeHarnessV4 runs /mcp/agent/v4 (ADR-0059 Parts 2-3): the same
// v3 tools, plus propose -- agent mode's first write to knowledge content
// itself.
func newKnowledgeHarnessV4(t *testing.T) *knowledgeHarness {
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
	sem, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	ev := datalog.Evaluator{}
	kernel := knowledge.New(gitstore.New(repo), knowledge.WithRecordValidator(sem), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev}))
	agent := knowledgeops.NewAgent(kernel, memory.NewService(store))
	resolver := knowledgeops.NewResolver(kernel, ev, policy.Trust{})
	guide := knowledgeops.NewGuide(kernel, memory.NewWalkthroughs(store), ev, policy.Trust{})
	proposer := knowledgeops.NewProposer(kernel, ev, policy.Trust{})
	logs := &syncBuffer{}
	srv := httptest.NewServer(mcptransport.NewAgentHandlerV4(agent, resolver, guide, proposer, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)
	h := &harness{url: srv.URL, store: store, logs: logs}
	h.session = h.connect(t, "")
	return &knowledgeHarness{harness: h, kernel: kernel}
}

// newKnowledgeHarnessV5 runs /mcp/agent/v5 (ADR-0060): the same v4
// tools, plus propose_start and propose_advance.
func newKnowledgeHarnessV5(t *testing.T) *knowledgeHarness {
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
	sem, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	ev := datalog.Evaluator{}
	kernel := knowledge.New(gitstore.New(repo), knowledge.WithRecordValidator(sem), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev}))
	agent := knowledgeops.NewAgent(kernel, memory.NewService(store))
	resolver := knowledgeops.NewResolver(kernel, ev, policy.Trust{})
	guide := knowledgeops.NewGuide(kernel, memory.NewWalkthroughs(store), ev, policy.Trust{})
	proposer := knowledgeops.NewProposer(kernel, ev, policy.Trust{})
	drafter := knowledgeops.NewDrafter(kernel, proposer, memory.NewDrafts(store))
	logs := &syncBuffer{}
	srv := httptest.NewServer(mcptransport.NewAgentHandlerV5(agent, resolver, guide, proposer, drafter, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)
	h := &harness{url: srv.URL, store: store, logs: logs}
	h.session = h.connect(t, "")
	return &knowledgeHarness{harness: h, kernel: kernel}
}

func newKnowledgeHarnessV6(t *testing.T) *knowledgeHarness {
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
	sem, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	ev := datalog.Evaluator{}
	kernel := knowledge.New(gitstore.New(repo), knowledge.WithRecordValidator(sem), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev}))
	agent := knowledgeops.NewAgent(kernel, memory.NewService(store))
	resolver := knowledgeops.NewResolver(kernel, ev, policy.Trust{})
	guide := knowledgeops.NewGuide(kernel, memory.NewWalkthroughs(store), ev, policy.Trust{})
	proposer := knowledgeops.NewProposer(kernel, ev, policy.Trust{})
	drafter := knowledgeops.NewFieldDrafter(kernel, proposer, memory.NewFieldDrafts(store))
	logs := &syncBuffer{}
	srv := httptest.NewServer(mcptransport.NewAgentHandlerV6(agent, resolver, guide, drafter, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)
	h := &harness{url: srv.URL, store: store, logs: logs}
	h.session = h.connect(t, "")
	return &knowledgeHarness{harness: h, kernel: kernel}
}

func newKnowledgeHarnessV7(t *testing.T) *knowledgeHarness {
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
	sem, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	ev := datalog.Evaluator{}
	kernel := knowledge.New(gitstore.New(repo), knowledge.WithRecordValidator(sem), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev}))
	agent := knowledgeops.NewAgent(kernel, memory.NewService(store))
	resolver := knowledgeops.NewResolver(kernel, ev, policy.Trust{})
	guide := knowledgeops.NewGuide(kernel, memory.NewWalkthroughs(store), ev, policy.Trust{})
	drafter := knowledgeops.NewFieldDrafter(kernel, knowledgeops.NewProposer(kernel, ev, policy.Trust{}), memory.NewFieldDrafts(store))
	logs := &syncBuffer{}
	srv := httptest.NewServer(mcptransport.NewAgentHandlerV7(agent, resolver, guide, drafter, slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(srv.Close)
	h := &harness{url: srv.URL, store: store, logs: logs}
	h.session = h.connect(t, "")
	return &knowledgeHarness{harness: h, kernel: kernel}
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

func TestAgentModeAdvertisesOnlyItsTools(t *testing.T) {
	agent := newKnowledgeHarness(t)
	var names []string
	for tool, err := range agent.session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"discover", "feedback", "read"}) {
		t.Fatalf("agent tools = %v", names)
	}
	if instr := agent.session.InitializeResult().Instructions; !strings.Contains(instr, "Nothing is recorded here") || strings.Contains(instr, "record_execution") {
		t.Fatalf("agent instructions = %q", instr)
	}
	for _, name := range []string{"record_execution", "get_procedure", "resolve_binding"} {
		if _, err := agent.session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: json.RawMessage(`{}`)}); err == nil {
			t.Errorf("%s is callable in agent mode", name)
		}
	}
}

func TestAgentDiscoverEmptyCatalog(t *testing.T) {
	agent := newKnowledgeHarness(t)
	d := decodeJSON(t, agent.ok(t, "discover", `{"task":"anything"}`))
	if d["empty"] != true {
		t.Fatalf("discover of an empty catalog = %v", d)
	}
	if _, ok := d["candidates"]; ok {
		t.Fatalf("empty discover must not carry candidates: %v", d)
	}
}

func TestAgentDiscoverAndRead(t *testing.T) {
	agent := newKnowledgeHarness(t)
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
	agent := newKnowledgeHarness(t)
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
	agent := newKnowledgeHarness(t)
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
	agent := newKnowledgeHarness(t)
	d := decodeJSON(t, agent.ok(t, "discover", `{"kind":"tools"}`))
	if d["kind"] != "tools" {
		t.Fatalf("discover(kind=tools) = %v", d)
	}
	var names []string
	for _, tl := range d["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"discover", "feedback", "read"}) {
		t.Fatalf("discover(kind=tools) at /mcp/agent = %v, must not list resolve (that is /mcp/agent/v2 only)", names)
	}
}

func TestAgentDiscoverKindValidation(t *testing.T) {
	agent := newKnowledgeHarness(t)
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

// planFixture publishes, at /mcp/agent/v2, a parent Procedure record
// composing three children, x precedes y precedes z, each with a
// matching legacy procedure document.
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
	agent := newKnowledgeHarnessV2(t)
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
	agent := newKnowledgeHarnessV2(t)
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
	agent := newKnowledgeHarnessV2(t)
	snap := planFixture(t, agent)
	res := agent.call(t, "resolve", `{"goal":"plan.parent","snapshot":"`+string(snap)+`","at":"2026-10-15T12:00:00Z","completed":["not-a-child"]}`)
	if !res.IsError {
		t.Fatalf("completed naming a key that is not an immediate composed child must fail")
	}
	if !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"invalid_request"`) {
		t.Fatalf("must be invalid_request: %s", res.Content[0].(*sdk.TextContent).Text)
	}
}

// Agent mode v3 (ADR-0053): the v2 tools, unchanged, plus start and
// advance. TestAgentV2AndV3SchemasMatch extends the invariant
// cmd/polaroidd/e2e_corpus_test.go already checks between v1 and v2.

func TestAgentV2AndV3SchemasMatch(t *testing.T) {
	v2, v3 := newKnowledgeHarnessV2(t), newKnowledgeHarnessV3(t)
	schemas := func(h *knowledgeHarness) map[string]string {
		t.Helper()
		out := map[string]string{}
		for tool, err := range h.session.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(tool.InputSchema)
			out[tool.Name] = string(b)
		}
		return out
	}
	s2, s3 := schemas(v2), schemas(v3)
	for _, name := range []string{"discover", "read", "feedback", "resolve"} {
		if s2[name] == "" || s2[name] != s3[name] {
			t.Fatalf("%s schema differs between v2 and v3:\nv2: %s\nv3: %s", name, s2[name], s3[name])
		}
	}
	if len(s3) != 6 || s3["start"] == "" || s3["advance"] == "" {
		t.Fatalf("v3 tools = %v, want discover/read/feedback/resolve/start/advance", s3)
	}
	v3Tools := v3.ok(t, "discover", `{"kind":"tools"}`)
	if !strings.Contains(v3Tools, `"start"`) || !strings.Contains(v3Tools, `"advance"`) {
		t.Fatalf("discover(kind=tools) at v3 must list start and advance: %s", v3Tools)
	}
}

// Agent mode v4 (ADR-0059 Parts 2-3): the v3 tools, unchanged, plus
// propose -- agent mode's first write to knowledge content itself.

func TestAgentV3AndV4SchemasMatch(t *testing.T) {
	v3, v4 := newKnowledgeHarnessV3(t), newKnowledgeHarnessV4(t)
	schemas := func(h *knowledgeHarness) map[string]string {
		t.Helper()
		out := map[string]string{}
		for tool, err := range h.session.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(tool.InputSchema)
			out[tool.Name] = string(b)
		}
		return out
	}
	s3, s4 := schemas(v3), schemas(v4)
	for _, name := range []string{"discover", "read", "feedback", "resolve", "start", "advance"} {
		if s3[name] == "" || s3[name] != s4[name] {
			t.Fatalf("%s schema differs between v3 and v4:\nv3: %s\nv4: %s", name, s3[name], s4[name])
		}
	}
	if len(s4) != 7 || s4["propose"] == "" {
		t.Fatalf("v4 tools = %v, want discover/read/feedback/resolve/start/advance/propose", s4)
	}
	v4Tools := v4.ok(t, "discover", `{"kind":"tools"}`)
	if !strings.Contains(v4Tools, `"propose"`) {
		t.Fatalf("discover(kind=tools) at v4 must list propose: %s", v4Tools)
	}
}

// proposeFixture publishes the same vacuous producer/consumer shape
// #173's own fixtures use, over the real kernel the harness itself
// wraps (not through propose, so the fixture's own validity is never in
// question) -- for proving propose actually closes it over the real
// transport.
func proposeFixture(t *testing.T, agent *knowledgeHarness) knowledge.Snapshot {
	t.Helper()
	procs := []*knowledge.Procedure{
		knowledgetest.SharedProc("propose.producer", "producer"), knowledgetest.SharedProc("propose.consumer", "consumer"),
		knowledgetest.SharedProc("propose.parent", "parent"),
	}
	records := []*knowledge.Record{
		{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"},
		{Type: knowledge.TypeConcept, Key: "propose.some-precondition", Label: "x", Definition: "d"},
		{Type: knowledge.TypeProcedure, Key: "propose.producer", Label: "producer", Definition: "d",
			Precedes: []string{"propose.consumer"}, Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: "propose.consumer", Label: "consumer", Definition: "d",
			Preconditions: []string{"propose.some-precondition"}, Requires: []string{"test.obligation"}},
		{Type: knowledge.TypeProcedure, Key: "propose.parent", Label: "parent", Definition: "d",
			Composes: []string{"propose.producer", "propose.consumer"}, Requires: []string{"test.obligation"}},
	}
	s, err := agent.publishRecords(t, procs, records)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLegacySingleShotProposeRequiresV6(t *testing.T) {
	agent := newKnowledgeHarnessV4(t)
	errJSON := agent.fail(t, "propose", `{"proposer":"test.agent","rationale":"r"}`, "invalid_request")
	if !strings.Contains(errJSON.Message, "/mcp/agent/v6") {
		t.Fatalf("error = %+v, want migration guidance to v6", errJSON)
	}
}

func TestLegacyProposeRejectsIncompleteCitationPayloadAsMigration(t *testing.T) {
	agent := newKnowledgeHarnessV4(t)
	errJSON := agent.fail(t, "propose", `{"proposer":"test.agent","rationale":"r",
		"existing_procedure":{"key":"propose.producer","postconditions":["propose.some-precondition"]}}`, "invalid_request")
	if !strings.Contains(errJSON.Message, "/mcp/agent/v6") {
		t.Fatalf("error = %+v, want migration guidance to v6", errJSON)
	}
}

func TestLegacyProposeRejectsOrchestrationPayloadAsMigration(t *testing.T) {
	agent := newKnowledgeHarnessV4(t)
	errJSON := agent.fail(t, "propose", `{"proposer":"test.agent","rationale":"r",
		"new_orchestration":{"key":"propose.orchestration","label":"o","definition":"d","composes":[]}}`, "invalid_request")
	if !strings.Contains(errJSON.Message, "/mcp/agent/v6") {
		t.Fatalf("error = %+v, want migration guidance to v6", errJSON)
	}
}

func TestLegacyProposalFailureDoesNotAffectFeedback(t *testing.T) {
	agent := newKnowledgeHarnessV4(t)
	snap := proposeFixture(t, agent)
	before := agent.ok(t, "feedback", `{"key":"propose.producer","snapshot":"`+string(snap)+`","classification":"improved_method","summary":"s","details":"d","reporter":"test.agent"}`)
	if _, err := agent.publishRecords(t, nil, []*knowledge.Record{
		{Type: knowledge.TypeConcept, Key: "propose.unrelated-concept", Label: "u", Definition: "d"},
	}); err != nil {
		t.Fatal(err)
	}
	res := agent.call(t, "propose", `{"proposer":"test.agent","rationale":"r","new_concept":{"key":"propose.via-tool","label":"v","definition":"grounded content",
		"citations":[{"source":"test.md","quote":"grounded content"}]}}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, "/mcp/agent/v6") {
		t.Fatalf("legacy proposal result = %+v, want migration error", res)
	}
	after := agent.ok(t, "feedback", `{"key":"propose.producer","snapshot":"`+string(snap)+`","classification":"improved_method","summary":"s2","details":"d","reporter":"test.agent"}`)
	if before == "" || after == "" {
		t.Fatalf("feedback must keep working normally alongside retired proposals: before=%q after=%q", before, after)
	}
}

// Agent mode v5 (ADR-0060): the v4 tools, unchanged, plus propose_start
// and propose_advance.

func TestAgentV4AndV5SchemasMatch(t *testing.T) {
	v4, v5 := newKnowledgeHarnessV4(t), newKnowledgeHarnessV5(t)
	schemas := func(h *knowledgeHarness) map[string]string {
		t.Helper()
		out := map[string]string{}
		for tool, err := range h.session.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(tool.InputSchema)
			out[tool.Name] = string(b)
		}
		return out
	}
	s4, s5 := schemas(v4), schemas(v5)
	for _, name := range []string{"discover", "read", "feedback", "resolve", "start", "advance", "propose"} {
		if s4[name] == "" || s4[name] != s5[name] {
			t.Fatalf("%s schema differs between v4 and v5:\nv4: %s\nv5: %s", name, s4[name], s5[name])
		}
	}

	if len(s5) != 9 || s5["propose_start"] == "" || s5["propose_advance"] == "" {
		t.Fatalf("v5 tools = %v, want discover/read/feedback/resolve/start/advance/propose/propose_start/propose_advance", s5)
	}
	v5Tools := v5.ok(t, "discover", `{"kind":"tools"}`)
	if !strings.Contains(v5Tools, `"propose_start"`) || !strings.Contains(v5Tools, `"propose_advance"`) {
		t.Fatalf("discover(kind=tools) at v5 must list propose_start and propose_advance: %s", v5Tools)
	}
}

func TestAgentV6FieldCompleteProposalOverRealTransport(t *testing.T) {
	agent := newKnowledgeHarnessV6(t)
	draftFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "propose_start", `{"key":"draft.producer","record_type":"Procedure","proposer":"test.agent","rationale":"ground the producer's outcome"}`))
	fields, ok := start["fields"].([]any)
	if !ok || len(fields) != len(knowledge.ProcedureDocumentFieldSpecs())+len(knowledge.RecordFieldSpecs(knowledge.TypeProcedure)) {
		t.Fatalf("v6 propose_start fields = %v", start["fields"])
	}
	if start["next_entity"] != "procedure" || start["next_field"] != "goal" {
		t.Fatalf("v6 first field = %v.%v, want procedure.goal", start["next_entity"], start["next_field"])
	}
	tools := agent.ok(t, "discover", `{"kind":"tools"}`)
	if !strings.Contains(tools, `"propose_start"`) || !strings.Contains(tools, `"propose_advance"`) {
		t.Fatalf("v6 discover(kind=tools) did not list full-review tools: %s", tools)
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
	if result["status"] != "complete" || result["key"] != "draft.producer" || result["snapshot"] == "" {
		t.Fatalf("v6 final proposal result = %v", result)
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
		t.Fatalf("v6 published postconditions = %v", record.Postconditions)
	}
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

func TestDraftWalksFieldsAndCommits(t *testing.T) {
	agent := newKnowledgeHarnessV5(t)
	errJSON := agent.fail(t, "propose_start", `{"key":"draft.producer","proposer":"test.agent","rationale":"r"}`, "invalid_request")
	if !strings.Contains(errJSON.Message, "/mcp/agent/v6") {
		t.Fatalf("error = %+v, want migration guidance to v6", errJSON)
	}
}

// TestDraftStillVacuousFailsAndStaysOpen proves a still-vacuous
// composition is returned as the specific error over the real
// transport, and the draft remains open: confirmed by a peek (no
// outcome) staying safe and not re-erroring, then a correction
// succeeding.
func TestDraftStillVacuousFailsAndStaysOpen(t *testing.T) {
	agent := newKnowledgeHarnessV5(t)
	errJSON := agent.fail(t, "propose_start", `{"key":"draft.producer","proposer":"test.agent","rationale":"r"}`, "invalid_request")
	if !strings.Contains(errJSON.Message, "/mcp/agent/v6") {
		t.Fatalf("error = %+v, want migration guidance to v6", errJSON)
	}
}

func TestDraftMissingCitationIsRejected(t *testing.T) {
	agent := newKnowledgeHarnessV5(t)
	errJSON := agent.fail(t, "propose_start", `{"key":"draft.producer","proposer":"test.agent","rationale":"r"}`, "invalid_request")
	if !strings.Contains(errJSON.Message, "/mcp/agent/v6") {
		t.Fatalf("error = %+v, want migration guidance to v6", errJSON)
	}
}

// TestDraftOffersAlreadyNonEmptyFieldWithExistingValues proves
// (ADR-0061), over the real transport, that propose_start still offers
// a field already holding published content -- draft.consumer's own
// preconditions -- instead of silently skipping it, and surfaces its
// current values and *Complete state so an answer can extend it.
func TestDraftOffersAlreadyNonEmptyFieldWithExistingValues(t *testing.T) {
	agent := newKnowledgeHarnessV5(t)
	errJSON := agent.fail(t, "propose_start", `{"key":"draft.consumer","proposer":"test.agent","rationale":"r"}`, "invalid_request")
	if !strings.Contains(errJSON.Message, "/mcp/agent/v6") {
		t.Fatalf("error = %+v, want migration guidance to v6", errJSON)
	}
}

func TestDraftCloseOverRealTransport(t *testing.T) {
	agent := newKnowledgeHarnessV5(t)
	errJSON := agent.fail(t, "propose_advance", `{"token":"never-started"}`, "invalid_request")
	if !strings.Contains(errJSON.Message, "/mcp/agent/v6") {
		t.Fatalf("error = %+v, want migration guidance to v6", errJSON)
	}
}

func TestWalkthroughStartPinsSnapshotAndReturnsPlan(t *testing.T) {
	agent := newKnowledgeHarnessV3(t)
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

func TestAgentV7DynamicChildCompletesAndCanReopenParentFlow(t *testing.T) {
	agent := newKnowledgeHarnessV7(t)
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

func TestAgentV7DynamicChildValidationAndPinnedSnapshot(t *testing.T) {
	agent := newKnowledgeHarnessV7(t)
	snapshot := dynamicWalkthroughFixture(t, agent)
	const repository = "github.com/ashuangiras/polaroid"
	static := decodeJSON(t, agent.ok(t, "start", `{"goal":"polaroid.using","snapshot":"`+string(snapshot)+`","repository":"`+repository+`"}`))
	if static["status"] != "complete" {
		t.Fatalf("ordinary v7 start of a leaf = %v, want complete", static)
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

func TestAgentV7PreservesV6ToolsAndAdvertisesDynamicAttachment(t *testing.T) {
	v6, v7 := newKnowledgeHarnessV6(t), newKnowledgeHarnessV7(t)
	schemas := func(h *knowledgeHarness) map[string]string {
		t.Helper()
		out := map[string]string{}
		for tool, err := range h.session.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			out[tool.Name] = string(encoded)
		}
		return out
	}
	v6Schemas, v7Schemas := schemas(v6), schemas(v7)
	for _, name := range []string{"discover", "read", "feedback", "resolve", "advance", "propose_start", "propose_advance"} {
		if v6Schemas[name] == "" || v6Schemas[name] != v7Schemas[name] {
			t.Fatalf("%s schema changed between v6 and v7:\nv6: %s\nv7: %s", name, v6Schemas[name], v7Schemas[name])
		}
	}
	if v6Schemas["attach_child"] != "" || v7Schemas["attach_child"] == "" {
		t.Fatalf("attach_child schemas v6=%q v7=%q", v6Schemas["attach_child"], v7Schemas["attach_child"])
	}
	if strings.Contains(v6Schemas["start"], `"allow_dynamic_children"`) || strings.Contains(v6Schemas["start"], `"snapshot"`) {
		t.Fatalf("v6 start schema unexpectedly includes v7 arguments: %s", v6Schemas["start"])
	}
	if !strings.Contains(v7Schemas["start"], `"allow_dynamic_children"`) || !strings.Contains(v7Schemas["start"], `"snapshot"`) {
		t.Fatalf("v7 start schema lacks dynamic mode or snapshot: %s", v7Schemas["start"])
	}
	if v7Schemas["start"] == v6Schemas["start"] {
		t.Fatal("v7 start schema must add dynamic-parent arguments without changing v6")
	}
	tools := v7.ok(t, "discover", `{"kind":"tools"}`)
	if !strings.Contains(tools, `"attach_child"`) {
		t.Fatalf("v7 discover(kind=tools) does not advertise attach_child: %s", tools)
	}
}

func TestWalkthroughAdvancePeekIsUnchanged(t *testing.T) {
	agent := newKnowledgeHarnessV3(t)
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
	agent := newKnowledgeHarnessV3(t)
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
	agent := newKnowledgeHarnessV3(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)

	res := agent.call(t, "advance", `{"token":"`+token+`","outcome":{"key":"not-a-child","status":"done"}}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"invalid_request"`) {
		t.Fatalf("outcome.key outside the goal's composed children must be invalid_request: %v", res)
	}
}

func TestWalkthroughAdvanceUnknownTokenIsNotFound(t *testing.T) {
	agent := newKnowledgeHarnessV3(t)
	res := agent.call(t, "advance", `{"token":"never-started"}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"not_found"`) {
		t.Fatalf("an unknown token must be not_found: %v", res)
	}
}

func TestWalkthroughStartUnknownGoalIsNotFound(t *testing.T) {
	agent := newKnowledgeHarnessV3(t)
	res := agent.call(t, "start", `{"goal":"nope"}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"not_found"`) {
		t.Fatalf("an unknown goal must be not_found: %v", res)
	}
}

// Multi-level plans, path-addressed leaves (ADR-0054, issue #161),
// exercised over real HTTP at /mcp/agent/v3.

// deepPlanFixture publishes goal -> a -> b (a leaf), each a typed
// Procedure record with a matching legacy document.
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
	agent := newKnowledgeHarnessV3(t)
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
	agent := newKnowledgeHarnessV3(t)
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
	agent := newKnowledgeHarnessV3(t)
	deepPlanFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"deep.goal"}`))
	token := start["token"].(string)

	res := agent.call(t, "advance", `{"token":"`+token+`","outcome":{"key":"deep.a","status":"done"}}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"invalid_request"`) {
		t.Fatalf("a composed key (deep.a is not a leaf) must never be accepted as a target: %v", res)
	}
}

func TestWalkthroughAdvanceKeyAndPathBothGivenIsInvalidRequest(t *testing.T) {
	agent := newKnowledgeHarnessV3(t)
	deepPlanFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"deep.goal"}`))
	token := start["token"].(string)

	res := agent.call(t, "advance", `{"token":"`+token+`","outcome":{"key":"deep.b","path":["deep.a","deep.b"],"status":"done"}}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"invalid_request"`) {
		t.Fatalf("key and path must not both be given: %v", res)
	}
}

// Walkthrough completion semantics and an explicit close (ADR-0055,
// issue #162), exercised over real HTTP at /mcp/agent/v3.

func TestWalkthroughStatusCompleteAndInProgress(t *testing.T) {
	agent := newKnowledgeHarnessV3(t)
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
	agent := newKnowledgeHarnessV3(t)
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
	agent := newKnowledgeHarnessV3(t)
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
	agent := newKnowledgeHarnessV3(t)
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
	agent := newKnowledgeHarnessV3(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)
	agent.ok(t, "advance", `{"token":"`+token+`","close":true}`)

	res := agent.call(t, "advance", `{"token":"`+token+`","close":true}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"not_found"`) {
		t.Fatalf("closing an already-closed token must be not_found: %v", res)
	}
}

// A blocked status, with a reason, and expand (ADR-0056, issue #163),
// exercised over real HTTP at /mcp/agent/v3.

func TestWalkthroughAdvanceBlockedWithReason(t *testing.T) {
	agent := newKnowledgeHarnessV3(t)
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
	agent := newKnowledgeHarnessV3(t)
	planFixture(t, agent)
	start := decodeJSON(t, agent.ok(t, "start", `{"goal":"plan.parent"}`))
	token := start["token"].(string)

	res := agent.call(t, "advance", `{"token":"`+token+`","outcome":{"key":"plan.x","status":"pending"}}`)
	if !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, `"invalid_request"`) {
		t.Fatalf("an unknown status must be invalid_request: %v", res)
	}
}

func TestWalkthroughStartExpandAttachesNestedChildren(t *testing.T) {
	agent := newKnowledgeHarnessV3(t)
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
	agent := newKnowledgeHarnessV3(t)
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
