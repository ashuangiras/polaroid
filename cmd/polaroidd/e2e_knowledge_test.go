package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestKnowledgeAcceptance is Milestone D's 12-step acceptance scenario
// (ADR-0048), against a real daemon: an agent discovers, reads and reports
// feedback on Git-native living knowledge entirely through MCP agent mode,
// and an operator publishes, reconciles and improves it entirely through
// the explicit, token-guarded HTTP surface — never through a direct Git
// operation, a procedure version or a binding.
func TestKnowledgeAcceptance(t *testing.T) {
	cfg := config{
		addr: "127.0.0.1:0", dbPath: filepath.Join(t.TempDir(), "polaroid.db"), dbSource: dbFromFlag,
		knowledgeDir: filepath.Join(t.TempDir(), "knowledge"),
	}
	var logs bytes.Buffer
	base, stop := startConfig(t, cfg, &logs)
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	})

	tokenBytes, err := os.ReadFile(cfg.knowledgeDir + "-operator-token")
	if err != nil {
		t.Fatalf("operator token was not created: %v\nlogs: %s", err, logs.String())
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		t.Fatal("operator token is empty")
	}

	ctx := context.Background()
	client := sdk.NewClient(&sdk.Implementation{Name: "acceptance-test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: base + "/mcp/agent", DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect agent mode: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	v6Client := sdk.NewClient(&sdk.Implementation{Name: "acceptance-test-v6", Version: "v0"}, nil)
	v6Session, err := v6Client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: base + "/mcp/agent/v6", DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect agent mode v6: %v", err)
	}
	t.Cleanup(func() { _ = v6Session.Close() })
	v6Tools, err := v6Session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list v6 tools: %v", err)
	}
	var hasFullProposalStart, hasFullProposalAdvance bool
	for _, tool := range v6Tools.Tools {
		hasFullProposalStart = hasFullProposalStart || tool.Name == "propose_start"
		hasFullProposalAdvance = hasFullProposalAdvance || tool.Name == "propose_advance"
	}
	if !hasFullProposalStart || !hasFullProposalAdvance {
		t.Fatalf("v6 endpoint tools = %v, want full proposal tools", v6Tools.Tools)
	}

	callTool := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: protocol error: %v", name, err)
		}
		text := res.Content[0].(*sdk.TextContent).Text
		if res.IsError {
			t.Fatalf("%s(%v) failed: %s", name, args, text)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			t.Fatalf("%s: decode %s: %v", name, text, err)
		}
		return m
	}

	op := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, r)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Polaroid-Operator-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var m map[string]any
		if resp.ContentLength != 0 {
			if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, m
	}

	// 1. An agent discovers before anything is published: an explicit,
	// well-formed empty result, never an error.
	d := callTool("discover", map[string]any{"task": "tidy a go module"})
	if d["empty"] != true {
		t.Fatalf("1. discover before publication = %v", d)
	}

	// 2. An operator confirms the backend is ready but empty, with the
	// token-guarded status endpoint: no scheduler, every operation explicit.
	status, body := op(http.MethodGet, "/v1/knowledge/status", token, nil)
	if status != http.StatusOK || body["empty"] != true {
		t.Fatalf("2. operator status before publication = %d %v", status, body)
	}

	// 3. An operator publishes the first procedure directly, as task
	// knowledge: no code change, just a record.
	changes1 := []map[string]any{{
		"key": "go.module.tidy", "goal": "Tidy a Go module's dependencies.",
		"philosophy": "Keep dependencies minimal.", "method": "Run go mod tidy.",
		"contract": map[string]any{"inputs": map[string]any{"module": "path"}}, "instructions": map[string]any{"steps": []string{"go mod tidy"}},
	}}
	status, pub1 := op(http.MethodPost, "/v2/knowledge/publications", token, map[string]any{
		"base":    "",
		"changes": changes1,
		"author":  "operator.alice", "summary": "add tidy procedure", "rationale": "seed the catalog",
		"review": fullFieldReviewForPayload(t, changes1, []any{}),
	})
	if status != http.StatusCreated || pub1["snapshot"] == "" {
		t.Fatalf("3. publish = %d %v", status, pub1)
	}
	snap1 := pub1["snapshot"].(string)

	// 4. The agent discovers the published knowledge, by key and goal, at
	// the published snapshot: no procedure_id or version anywhere.
	d = callTool("discover", map[string]any{"task": "tidy a go module"})
	if d["empty"] == true || d["snapshot"] != snap1 {
		t.Fatalf("4. discover after publication = %v", d)
	}
	cands := d["candidates"].([]any)
	if len(cands) == 0 || cands[0].(map[string]any)["key"] != "go.module.tidy" {
		t.Fatalf("4. candidates = %v", cands)
	}

	// 5. The agent reads it, by canonical key and snapshot, for executable
	// guidance.
	g := callTool("read", map[string]any{"key": "go.module.tidy", "snapshot": snap1})
	if g["snapshot"] != snap1 || g["key"] != "go.module.tidy" {
		t.Fatalf("5. read = %v", g)
	}
	for _, forbidden := range []string{"procedure_id", "version", "binding", "selected_by", "latest_version"} {
		if _, ok := g[forbidden]; ok {
			t.Fatalf("5. read must never expose %q: %v", forbidden, g)
		}
	}

	// 6. Having done the work and found the procedure wrong, the agent
	// reports feedback naming exactly the key and snapshot it read.
	fb := callTool("feedback", map[string]any{
		"key": "go.module.tidy", "snapshot": snap1, "classification": "incorrect_instruction",
		"summary": "does not handle vendored modules", "details": "go mod tidy alone leaves vendor/ stale",
		"reporter": "copilot.agent",
	})
	findingID, _ := fb["id"].(string)
	if findingID == "" {
		t.Fatalf("6. feedback = %v", fb)
	}

	// 7. The operator confirms the backend is still ready (a sanity check
	// between the agent's report and the operator's review).
	if status, body := op(http.MethodGet, "/v1/knowledge/status", token, nil); status != http.StatusOK || body["keys"] != 1.0 {
		t.Fatalf("7. operator status before the fix = %d %v", status, body)
	}

	// 8. The operator, having reviewed the finding, publishes an improved
	// procedure addressing it, naming the finding: reconciliation is
	// attempted automatically and succeeds.
	changes2 := []map[string]any{{
		"key": "go.module.tidy", "goal": "Tidy a Go module's dependencies, including vendoring.",
		"philosophy": "Keep dependencies minimal.", "method": "Run go mod tidy, then go mod vendor if vendor/ exists.",
		"contract":     map[string]any{"inputs": map[string]any{"module": "path"}},
		"instructions": map[string]any{"steps": []string{"go mod tidy", "test -d vendor && go mod vendor"}},
	}}
	status, pub2 := op(http.MethodPost, "/v2/knowledge/publications", token, map[string]any{
		"base":    snap1,
		"changes": changes2,
		"author":  "operator.alice", "summary": "handle vendored modules", "rationale": "addresses the agent's finding", "finding": findingID,
		"review": fullFieldReviewForPayload(t, changes2, []any{}),
	})
	if status != http.StatusCreated {
		t.Fatalf("8. publish the fix = %d %v", status, pub2)
	}
	snap2, _ := pub2["snapshot"].(string)
	recon, _ := pub2["reconciliation"].(map[string]any)
	if snap2 == "" || snap2 == snap1 || recon == nil || recon["status"] != "resolved" {
		t.Fatalf("8. publish with finding = %v", pub2)
	}

	// 9. Reconciling again, independently of publishing, is idempotent: the
	// finding is already resolved.
	status, again := op(http.MethodPost, "/v1/knowledge/reconciliations/"+findingID, token, nil)
	if status != http.StatusOK || again["status"] != "already_resolved" {
		t.Fatalf("9. repeat reconciliation = %d %v", status, again)
	}

	// 10. The agent discovers again: the active snapshot has moved on.
	d = callTool("discover", map[string]any{"task": "tidy a go module"})
	if d["snapshot"] != snap2 {
		t.Fatalf("10. discover after the fix = %v", d)
	}

	// 11. The agent re-reads its original snapshot: unchanged, exactly as
	// first served, proving the snapshot token pins the knowledge however
	// much is published later.
	original := callTool("read", map[string]any{"key": "go.module.tidy", "snapshot": snap1})
	if original["snapshot"] != snap1 || original["method"] != "Run go mod tidy." {
		t.Fatalf("11. re-read of the original snapshot = %v", original)
	}

	// 12. The agent reads the active knowledge (no snapshot given): the
	// approved improvement, consumed without ever handling Git operations,
	// procedure versions or historical selection.
	improved := callTool("read", map[string]any{"key": "go.module.tidy"})
	if improved["snapshot"] != snap2 || !strings.Contains(improved["method"].(string), "vendor") {
		t.Fatalf("12. read of the active (improved) knowledge = %v", improved)
	}
}
