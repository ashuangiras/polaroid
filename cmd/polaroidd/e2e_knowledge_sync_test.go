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
)

// TestKnowledgeSyncBetweenDaemons is Milestone D's two-daemon acceptance
// scenario (ADR-0048): two independently running polaroidd instances
// exchange knowledge through a real, local Git remote, entirely through
// the operator's explicit HTTP surface — fetch, validate and activate are
// separate steps, and nothing happens without an operator request.
func TestKnowledgeSyncBetweenDaemons(t *testing.T) {
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	remote := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.CommandContext(context.Background(), bin, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	start := func(name string) (base, token string, stop func() error) {
		t.Helper()
		kdir := filepath.Join(t.TempDir(), "knowledge")
		sources := kdir + "-sources.json"
		policy, err := json.Marshal([]map[string]any{{"name": "team", "url": remote, "publishers": []string{"alice"}, "publish": name == "a"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(sources), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sources, policy, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := config{addr: "127.0.0.1:0", dbPath: filepath.Join(t.TempDir(), "polaroid.db"), dbSource: dbFromFlag, knowledgeDir: kdir}
		var logs bytes.Buffer
		b, stop := startConfig(t, cfg, &logs)
		tok, err := os.ReadFile(kdir + "-operator-token")
		if err != nil {
			t.Fatalf("%s: operator token: %v\nlogs: %s", name, err, logs.String())
		}
		return b, strings.TrimSpace(string(tok)), stop
	}
	baseA, tokenA, stopA := start("a")
	t.Cleanup(func() { _ = stopA() })
	baseB, tokenB, stopB := start("b")
	t.Cleanup(func() { _ = stopB() })

	op := func(base, token, method, path string, body any) (int, map[string]any) {
		t.Helper()
		var r *bytes.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = bytes.NewReader(b)
		} else {
			r = bytes.NewReader(nil)
		}
		req, err := http.NewRequestWithContext(context.Background(), method, base+path, r)
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

	// Instance A publishes and pushes.
	status, pub := op(baseA, tokenA, http.MethodPost, "/v1/knowledge/publications", map[string]any{
		"base": "",
		"changes": []map[string]any{{
			"key": "go.module.tidy", "goal": "Tidy a module", "philosophy": "p", "method": "m",
			"contract": map[string]any{}, "instructions": map[string]any{},
		}},
		"author": "alice", "summary": "seed", "rationale": "initial knowledge",
	})
	if status != http.StatusCreated {
		t.Fatalf("A publish = %d %v", status, pub)
	}
	if status, res := op(baseA, tokenA, http.MethodPost, "/v1/knowledge/sources/team/push", nil); status != http.StatusOK {
		t.Fatalf("A push = %d %v", status, res)
	}

	// Instance B fetches, validates and activates, as three separate,
	// explicit operator steps.
	status, fetched := op(baseB, tokenB, http.MethodPost, "/v1/knowledge/sources/team/fetch", nil)
	if status != http.StatusOK || fetched["status"] != "fetched" {
		t.Fatalf("B fetch = %d %v", status, fetched)
	}
	status, validated := op(baseB, tokenB, http.MethodPost, "/v1/knowledge/sources/team/validation", nil)
	if status != http.StatusOK || validated["status"] != "fast-forward" {
		t.Fatalf("B validate = %d %v", status, validated)
	}
	status, activated := op(baseB, tokenB, http.MethodPost, "/v1/knowledge/sources/team/activation", map[string]any{
		"author": "bob", "summary": "accept the team's knowledge", "rationale": "validated and authorised",
	})
	if status != http.StatusCreated || activated["snapshot"] == "" {
		t.Fatalf("B activate = %d %v", status, activated)
	}

	status, st := op(baseB, tokenB, http.MethodGet, "/v1/knowledge/status", nil)
	if status != http.StatusOK || st["keys"] != 1.0 || st["empty"] == true {
		t.Fatalf("B status after activation = %d %v", status, st)
	}
	status, acts := op(baseB, tokenB, http.MethodGet, "/v1/knowledge/activations", nil)
	if status != http.StatusOK {
		t.Fatalf("B activations = %d %v", status, acts)
	}
	if list, ok := acts["activations"].([]any); !ok || len(list) != 1 {
		t.Fatalf("B activations list = %v", acts)
	}
}
