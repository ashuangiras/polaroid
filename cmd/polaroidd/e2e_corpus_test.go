package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
)

// TestCorpusLogicAcceptance (ADR-0050): an operator publishes typed records
// and a signed exception through /v1/knowledge; /mcp/agent v1 is unchanged;
// /mcp/agent/v2 resolves deterministically with the exception verified
// against operator-held trust material.
func TestCorpusLogicAcceptance(t *testing.T) {
	dir := t.TempDir()
	cfg := config{addr: "127.0.0.1:0", dbPath: filepath.Join(dir, "polaroid.db"), dbSource: dbFromFlag, knowledgeDir: filepath.Join(dir, "knowledge")}
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	repo := "github.com/team/app"
	trust := `{"keys":[{"id":"team.lead","public_key":"` + base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)) +
		`","authority":{"level":"project","repository":"` + repo + `"}}]}`
	if err := os.WriteFile(cfg.knowledgeDir+"-authorities.json", []byte(trust), 0o600); err != nil {
		t.Fatal(err)
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
		t.Fatalf("operator token: %v\n%s", err, logs.String())
	}
	token := strings.TrimSpace(string(tokenBytes))
	ctx := context.Background()

	op := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, base+path, r)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Polaroid-Operator-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}

	var changes []map[string]any
	for _, c := range knowledgetest.CorpusProcedures() {
		p := c.Procedure
		changes = append(changes, map[string]any{"key": p.Key, "scope": "shared", "goal": p.Goal, "philosophy": p.Philosophy,
			"method": p.Method, "contract": json.RawMessage(p.Contract), "instructions": json.RawMessage(p.Instructions)})
	}
	var vetRule *knowledge.Record
	for _, r := range knowledgetest.CorpusRecords() {
		if r.Key == "rule.go.vet" {
			vetRule = r
		}
	}
	exc := &knowledge.Record{Type: knowledge.TypeException, Key: "exc.team.vet", Label: "Team waives go vet", Excepts: []string{"rule.go.vet"},
		Revision: 1, RuleDigest: knowledge.RuleDigest(vetRule), AppliesTo: []string{"go.verify"}, Waives: "check.vet", Justification: "The team runs staticcheck instead.",
		Authority: &knowledge.Authority{Level: knowledge.AuthorityProject, Repository: repo},
		Approval: &knowledge.Approval{Reference: "team-2026-10", Key: "team.lead",
			ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ValidUntil: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}}
	if exc.Approval.Signature, err = policy.Sign(priv, exc); err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, r := range append(knowledgetest.CorpusRecords(), exc) {
		doc, err := knowledge.EncodeRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, map[string]any{"key": r.Key, "document": json.RawMessage(doc)})
	}
	status, pub := op(http.MethodPost, "/v1/knowledge/publications", map[string]any{"base": "", "changes": changes, "records": records,
		"author": "operator.test", "summary": "Corpus Logic fixtures", "rationale": "acceptance"})
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("publish = %d %v", status, pub)
	}
	snap, _ := pub["snapshot"].(string)
	status, st := op(http.MethodGet, "/v1/knowledge/status", nil)
	if status != http.StatusOK || st["format"] != float64(2) || st["records"] != float64(len(records)) || st["keys"] != float64(len(changes)) {
		t.Fatalf("status = %d %v", status, st)
	}

	// An unsafe rule is refused at publication with a precise reason.
	bad := &knowledge.Record{Type: knowledge.TypeRule, Key: "rule.bad", Label: "bad", Statement: "bad", Revision: 1,
		Program: `obligation(P, "x") :- record(P, "Procedure"), !specializes(P, "y").`}
	badDoc, _ := knowledge.EncodeRecord(bad)
	status, refused := op(http.MethodPost, "/v1/knowledge/publications", map[string]any{"base": snap, "changes": []any{},
		"records": []map[string]any{{"key": bad.Key, "document": json.RawMessage(badDoc)}}, "author": "operator.test", "summary": "bad", "rationale": "test"})
	if status != http.StatusBadRequest || !strings.Contains(refused["error"].(map[string]any)["message"].(string), "negation is not admitted") {
		t.Fatalf("unsafe rule = %d %v", status, refused)
	}

	connect := func(path string) *sdk.ClientSession {
		t.Helper()
		c := sdk.NewClient(&sdk.Implementation{Name: "corpus-acceptance", Version: "v0"}, nil)
		s, err := c.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: base + path, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
		if err != nil {
			t.Fatalf("connect %s: %v", path, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	tools := func(s *sdk.ClientSession) map[string]string {
		t.Helper()
		res, err := s.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, tl := range res.Tools {
			b, _ := json.Marshal(tl.InputSchema)
			out[tl.Name] = string(b)
		}
		return out
	}
	v1, v2 := connect("/mcp/agent"), connect("/mcp/agent/v2")
	t1, t2 := tools(v1), tools(v2)
	if names := slices.Sorted(func(yield func(string) bool) {
		for k := range t1 {
			if !yield(k) {
				return
			}
		}
	}); !slices.Equal(names, []string{"discover", "feedback", "read"}) {
		t.Fatalf("v1 tools %v", names)
	}
	for name, schema := range t1 {
		if t2[name] != schema {
			t.Fatalf("v2 changed %s's arguments:\n%s\n%s", name, schema, t2[name])
		}
	}
	if len(t2) != 4 || t2["resolve"] == "" {
		t.Fatalf("v2 tools %v", t2)
	}

	call := func(s *sdk.ClientSession, name string, args map[string]any) string {
		t.Helper()
		res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		text := res.Content[0].(*sdk.TextContent).Text
		if res.IsError {
			t.Fatalf("%s(%v): %s", name, args, text)
		}
		return text
	}
	if r := call(v1, "read", map[string]any{"key": "go.verify"}); !strings.Contains(r, `"key":"go.verify"`) {
		t.Fatalf("v1 read %s", r)
	}
	// discover(kind="tools") differs only in whether resolve is listed,
	// the one place the two endpoints' shared discover handler (ADR-0051)
	// behaves differently, since only the transport layer, not the
	// knowledge-only Agent behind both, knows which endpoint is calling.
	v1Tools := call(v1, "discover", map[string]any{"kind": "tools"})
	if strings.Contains(v1Tools, `"resolve"`) {
		t.Fatalf("discover(kind=tools) at v1 must not list resolve: %s", v1Tools)
	}
	v2Tools := call(v2, "discover", map[string]any{"kind": "tools"})
	if !strings.Contains(v2Tools, `"resolve"`) {
		t.Fatalf("discover(kind=tools) at v2 must list resolve: %s", v2Tools)
	}
	args := map[string]any{"goal": "go.verify.concurrent", "context": map[string]any{"repository": repo, "concurrency": "true"}, "at": "2026-10-14T12:00:00Z"}
	first := call(v2, "resolve", args)
	if again := call(v2, "resolve", args); again != first {
		t.Fatalf("resolve is not deterministic:\n%s\n%s", first, again)
	}
	var res struct {
		Snapshot    string `json:"snapshot"`
		Complete    bool   `json:"complete"`
		Obligations []struct {
			Requirement string `json:"requirement"`
			Status      string `json:"status"`
			Derivations []struct {
				Source, Via, Status, Exception string
			} `json:"derivations"`
		} `json:"obligations"`
		Exceptions []struct{ Key, Status, Reason string } `json:"exceptions"`
	}
	if err := json.Unmarshal([]byte(first), &res); err != nil {
		t.Fatal(err)
	}
	// The agent interface establishes no trusted project identity, so the
	// caller's assertion of the repository never activates the waiver.
	if res.Snapshot != snap || !res.Complete || len(res.Exceptions) != 1 || res.Exceptions[0].Status != "not_applicable" ||
		!strings.Contains(res.Exceptions[0].Reason, "a caller's assertion of its repository does not authorise a waiver") {
		t.Fatalf("resolution %s", first)
	}
	for _, o := range res.Obligations {
		if o.Requirement == "check.vet" && (o.Status != "required" || len(o.Derivations) != 2 ||
			slices.ContainsFunc(o.Derivations, func(d struct{ Source, Via, Status, Exception string }) bool { return d.Status != "active" })) {
			t.Fatalf("check.vet must stay required through every derivation: %+v", o)
		}
	}

	// Composition through resolve: the goal, then the next procedure.
	type composition struct {
		Composition struct {
			Verdict  string `json:"verdict"`
			Producer struct {
				Key     string `json:"key"`
				Outputs []struct{ Name, Concept string }
			} `json:"producer"`
			Findings []struct{ Kind, Item, Status string } `json:"findings"`
		} `json:"composition"`
	}
	for then, want := range map[string]string{"artifact.smoke": "compatible", "artifact.deploy": "incompatible", "artifact.review": "undetermined"} {
		r := call(v2, "resolve", map[string]any{"goal": "artifact.build", "then": then, "at": "2026-10-14T12:00:00Z"})
		var comp composition
		if err := json.Unmarshal([]byte(r), &comp); err != nil {
			t.Fatal(err)
		}
		if comp.Composition.Verdict != want || comp.Composition.Producer.Key != "artifact.build" || len(comp.Composition.Producer.Outputs) != 1 {
			t.Fatalf("then %s: %s", then, r)
		}
		if then == "artifact.deploy" && !slices.ContainsFunc(comp.Composition.Findings, func(f struct{ Kind, Item, Status string }) bool {
			return f.Kind == "input" && f.Item == "image" && f.Status == "missing"
		}) {
			t.Fatalf("deploy findings: %s", r)
		}
	}
	if r := call(v2, "resolve", map[string]any{"goal": "go.verify"}); !strings.Contains(r, `"at":"`) || !strings.Contains(r, `"unknowns"`) {
		t.Fatalf("resolve without context or time: %s", r)
	}
}
