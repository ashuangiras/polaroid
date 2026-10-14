package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

// TestCorpusLogicSyncBetweenDaemons (ADR-0050): format-2 knowledge
// published on one instance travels through a real Git remote, passes the
// other instance's complete validation (records, JSON-LD, SHACL, rules),
// and resolves identically there; a snapshot of an unsupported format is
// rejected at validation and never activated.
func TestCorpusLogicSyncBetweenDaemons(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	gitEnv := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@example.invalid", "GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=alice@example.invalid")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), gitBin, args...)
		cmd.Dir, cmd.Env = dir, gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t.TempDir(), "init", "--quiet", "--bare", "--initial-branch=main", remote)

	type instance struct{ base, token string }
	start := func(name string) instance {
		t.Helper()
		kdir := filepath.Join(t.TempDir(), "knowledge")
		policy, _ := json.Marshal([]map[string]any{{"name": "corpus", "url": remote, "publishers": []string{"alice"}, "publish": name == "a"}})
		if err := os.MkdirAll(filepath.Dir(kdir), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(kdir+"-sources.json", policy, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := config{addr: "127.0.0.1:0", dbPath: filepath.Join(t.TempDir(), "polaroid.db"), dbSource: dbFromFlag, knowledgeDir: kdir}
		var logs bytes.Buffer
		b, stop := startConfig(t, cfg, &logs)
		t.Cleanup(func() { _ = stop() })
		tok, err := os.ReadFile(kdir + "-operator-token")
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, logs.String())
		}
		return instance{b, strings.TrimSpace(string(tok))}
	}
	a, b := start("a"), start("b")

	op := func(in instance, method, path string, body any) (int, map[string]any) {
		t.Helper()
		var buf []byte
		if body != nil {
			buf, _ = json.Marshal(body)
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, in.base+path, bytes.NewReader(buf))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Polaroid-Operator-Token", in.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var m map[string]any
		if resp.ContentLength != 0 {
			_ = json.NewDecoder(resp.Body).Decode(&m)
		}
		return resp.StatusCode, m
	}
	resolve := func(in instance) string {
		t.Helper()
		c := sdk.NewClient(&sdk.Implementation{Name: "corpus-sync", Version: "v0"}, nil)
		s, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: in.base + "/mcp/agent/v2", DisableStandaloneSSE: true, MaxRetries: -1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: "resolve", Arguments: map[string]any{
			"goal": "ci.verify", "context": map[string]any{"concurrency": "true"}, "at": "2026-10-14T12:00:00Z"}})
		if err != nil || res.IsError {
			t.Fatalf("resolve: %v %v", err, res)
		}
		return res.Content[0].(*sdk.TextContent).Text
	}

	var changes, records []map[string]any
	for _, c := range knowledgetest.CorpusProcedures() {
		p := c.Procedure
		changes = append(changes, map[string]any{"key": p.Key, "scope": "shared", "goal": p.Goal, "philosophy": p.Philosophy,
			"method": p.Method, "contract": json.RawMessage(p.Contract), "instructions": json.RawMessage(p.Instructions)})
	}
	for _, r := range knowledgetest.CorpusRecords() {
		doc, _ := knowledge.EncodeRecord(r)
		records = append(records, map[string]any{"key": r.Key, "document": json.RawMessage(doc)})
	}
	if status, pub := op(a, http.MethodPost, "/v1/knowledge/publications", map[string]any{"base": "", "changes": changes, "records": records,
		"author": "alice", "summary": "Corpus Logic fixtures", "rationale": "shared cross-domain knowledge"}); status != http.StatusCreated {
		t.Fatalf("A publish = %d %v", status, pub)
	}
	if status, res := op(a, http.MethodPost, "/v1/knowledge/sources/corpus/push", nil); status != http.StatusOK {
		t.Fatalf("A push = %d %v", status, res)
	}
	if status, res := op(b, http.MethodPost, "/v1/knowledge/sources/corpus/fetch", nil); status != http.StatusOK {
		t.Fatalf("B fetch = %d %v", status, res)
	}
	if status, res := op(b, http.MethodPost, "/v1/knowledge/sources/corpus/validation", nil); status != http.StatusOK || res["status"] != "fast-forward" {
		t.Fatalf("B validate = %d %v", status, res)
	}
	if status, res := op(b, http.MethodPost, "/v1/knowledge/sources/corpus/activation", map[string]any{
		"author": "bob", "summary": "accept corpus", "rationale": "validated"}); status != http.StatusCreated {
		t.Fatalf("B activate = %d %v", status, res)
	}
	if ra, rb := resolve(a), resolve(b); ra != rb {
		t.Fatalf("instances resolve differently:\nA %s\nB %s", ra, rb)
	}

	// A publication of a format this build does not implement arrives.
	work := t.TempDir()
	git(work, "clone", "--quiet", "--branch", "main", remote, ".")
	if err := os.WriteFile(filepath.Join(work, knowledge.ManifestPath), []byte(`{"format":3,"capabilities":["records/2"]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(work, "commit", "--quiet", "-am", "future format", "-m", "a newer instance's knowledge")
	git(work, "push", "--quiet", "origin", "main")
	if status, res := op(b, http.MethodPost, "/v1/knowledge/sources/corpus/fetch", nil); status != http.StatusOK {
		t.Fatalf("B fetch future = %d %v", status, res)
	}
	status, res := op(b, http.MethodPost, "/v1/knowledge/sources/corpus/validation", nil)
	if status != http.StatusOK || res["status"] != "rejected" || !strings.Contains(jsonText(res["problems"]), "format 3 is not supported") {
		t.Fatalf("B validate future format = %d %v", status, res)
	}
	if rb := resolve(b); rb != resolve(a) {
		t.Fatal("B's active knowledge changed after a rejected candidate")
	}
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
