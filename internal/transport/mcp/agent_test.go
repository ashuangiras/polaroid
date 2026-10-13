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
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/memory"
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
	snap, err := kh.kernel.Publish(ctx, knowledge.Proposal{
		Base: base.Snapshot(), Changes: changes,
		Provenance: knowledge.Provenance{Author: "test", Summary: "test publish", Rationale: "test fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func decodeJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	return m
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
