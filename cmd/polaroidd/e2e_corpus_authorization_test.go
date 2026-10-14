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
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
)

// TestCorpusAuthorizationAcceptance (ADR-0050), against a real daemon:
// project and conditional exceptions apply only with signed context
// assertions an operator-trusted attestation key issued for that project;
// protected invariants cannot be excepted; a domain approval follows the
// domain path it signed as the taxonomy changes; a past snapshot resolves
// as it did; a rollback never revalidates an earlier approval.
func TestCorpusAuthorizationAcceptance(t *testing.T) {
	dir := t.TempDir()
	cfg := config{addr: "127.0.0.1:0", dbPath: filepath.Join(dir, "polaroid.db"), dbSource: dbFromFlag, knowledgeDir: filepath.Join(dir, "knowledge")}
	repo, otherRepo := "github.com/team/app", "github.com/other/app"
	keys := map[string]ed25519.PrivateKey{}
	var entries []string
	key := func(id string, seed byte, role string) {
		keys[id] = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
		entries = append(entries, `{"id":"`+id+`","public_key":"`+base64.StdEncoding.EncodeToString(keys[id].Public().(ed25519.PublicKey))+`",`+role+`}`)
	}
	key("team.lead", 1, `"authority":{"level":"project","repository":"`+repo+`"}`)
	key("corpus.owner", 2, `"authority":{"level":"corpus"}`)
	key("software.owner", 3, `"authority":{"level":"domain","domain":"domain.software"}`)
	key("team.ci", 4, `"attests":{"repository":"`+repo+`","names":["ci"]}`)
	key("other.ci", 5, `"attests":{"repository":"`+otherRepo+`","names":["ci"]}`)
	if err := os.WriteFile(cfg.knowledgeDir+"-authorities.json", []byte(`{"keys":[`+strings.Join(entries, ",")+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	base, stop := startConfig(t, cfg, &logs)
	t.Cleanup(func() { _ = stop() })
	tok, err := os.ReadFile(cfg.knowledgeDir + "-operator-token")
	if err != nil {
		t.Fatalf("%v\n%s", err, logs.String())
	}
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
		req.Header.Set("Polaroid-Operator-Token", strings.TrimSpace(string(tok)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	rules := map[string]*knowledge.Record{}
	for _, r := range knowledgetest.CorpusRecords() {
		rules[r.Key] = r
	}
	valid := knowledge.Approval{ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ValidUntil: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	exception := func(k, signer string, auth knowledge.Authority, rule *knowledge.Record, waives string, appliesTo ...string) *knowledge.Record {
		t.Helper()
		a := valid
		a.Reference, a.Key = "approval-"+k, signer
		e := &knowledge.Record{Type: knowledge.TypeException, Key: k, Label: k, Excepts: []string{rule.Key}, Revision: rule.Revision,
			RuleDigest: knowledge.RuleDigest(rule), AppliesTo: appliesTo, Waives: waives, Justification: "Approved for " + k, Authority: &auth, Approval: &a}
		return e
	}
	sign := func(e *knowledge.Record) *knowledge.Record {
		t.Helper()
		sig, err := policy.Sign(keys[e.Approval.Key], e)
		if err != nil {
			t.Fatal(err)
		}
		e.Approval.Signature = sig
		return e
	}
	team := sign(exception("exc.team.vet", "team.lead", knowledge.Authority{Level: knowledge.AuthorityProject, Repository: repo}, rules["rule.go.vet"], "check.vet", "go.verify"))
	cond := exception("exc.corpus.race.ci", "corpus.owner", knowledge.Authority{Level: knowledge.AuthorityCorpus}, rules["rule.go.race"], "check.race", "go.verify.concurrent")
	cond.Conditions = []knowledge.Condition{{Name: "ci", Value: "true"}}
	sign(cond)
	domain := sign(exception("exc.software.vet", "software.owner", knowledge.Authority{Level: knowledge.AuthorityDomain, Domain: "domain.software",
		Path: []string{"domain.software.go", "domain.software"}}, rules["rule.go.vet"], "check.vet", "go.verify.concurrent"))
	docs := func(rs ...*knowledge.Record) []map[string]any {
		var out []map[string]any
		for _, r := range rs {
			doc, err := knowledge.EncodeRecord(r)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, map[string]any{"key": r.Key, "document": json.RawMessage(doc)})
		}
		return out
	}
	var changes []map[string]any
	for _, c := range knowledgetest.CorpusProcedures() {
		p := c.Procedure
		changes = append(changes, map[string]any{"key": p.Key, "scope": "shared", "goal": p.Goal, "philosophy": p.Philosophy,
			"method": p.Method, "contract": json.RawMessage(p.Contract), "instructions": json.RawMessage(p.Instructions)})
	}
	publish := func(base string, records []map[string]any, procs []map[string]any) (int, map[string]any) {
		if procs == nil {
			procs = []map[string]any{}
		}
		return op(http.MethodPost, "/v1/knowledge/publications", map[string]any{"base": base, "changes": procs, "records": records,
			"author": "operator.test", "summary": "acceptance", "rationale": "acceptance"})
	}
	status, pub := publish("", docs(append(knowledgetest.CorpusRecords(), team, cond, domain)...), changes)
	if status != http.StatusCreated {
		t.Fatalf("publish = %d %v", status, pub)
	}
	original := pub["snapshot"].(string)

	// Protected invariants: no authority can except one.
	protected := sign(exception("exc.corpus.tests", "corpus.owner", knowledge.Authority{Level: knowledge.AuthorityCorpus}, rules["rule.software.tests"], "check.tests", "go.verify"))
	if status, res := publish(original, docs(protected), nil); status != http.StatusBadRequest || !strings.Contains(jsonText(res), "protected invariant") {
		t.Fatalf("exception to a protected invariant = %d %v", status, res)
	}

	c := sdk.NewClient(&sdk.Implementation{Name: "authorization-acceptance", Version: "v0"}, nil)
	s, err := c.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: base + "/mcp/agent/v2", DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	resolve := func(args map[string]any) (string, bool) {
		t.Helper()
		args["at"] = "2026-10-14T12:00:00Z"
		res, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "resolve", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*sdk.TextContent).Text, res.IsError
	}
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	assertion := func(id, repository string, facts map[string]string, from, until time.Time) map[string]any {
		t.Helper()
		a := policy.Assertion{Reference: "assert-" + id, Key: id, Repository: repository, Facts: facts, ValidFrom: from, ValidUntil: until}
		sig, err := policy.SignAssertion(keys[id], a)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{"reference": a.Reference, "key": id, "repository": repository, "facts": facts,
			"valid_from": from.Format(time.RFC3339), "valid_until": until.Format(time.RFC3339), "signature": base64.StdEncoding.EncodeToString(sig)}
	}
	ciTrue := assertion("team.ci", repo, map[string]string{"ci": "true"}, at.Add(-time.Hour), at.Add(time.Hour))
	status_ := func(text, key string) (string, string) {
		t.Helper()
		var r struct {
			Exceptions []struct{ Key, Status, Reason string }
		}
		if err := json.Unmarshal([]byte(text), &r); err != nil {
			t.Fatalf("%v: %s", err, text)
		}
		for _, e := range r.Exceptions {
			if e.Key == key {
				return e.Status, e.Reason
			}
		}
		return "", ""
	}
	expect := func(step string, args map[string]any, key, want, reason string) string {
		t.Helper()
		text, isErr := resolve(args)
		if isErr {
			t.Fatalf("%s: %s", step, text)
		}
		if got, why := status_(text, key); got != want || !strings.Contains(why, reason) {
			t.Fatalf("%s: %s is %s (%s), want %s", step, key, got, why, want)
		}
		return text
	}
	refused := func(step string, args map[string]any, reason string) {
		t.Helper()
		text, isErr := resolve(args)
		if !isErr || !strings.Contains(text, reason) {
			t.Fatalf("%s: %v %s", step, isErr, text)
		}
	}

	// Project-scoped exception.
	expect("caller-asserted repository", map[string]any{"goal": "go.verify", "context": map[string]any{"repository": repo}},
		"exc.team.vet", "not_applicable", "a caller's assertion of its repository does not authorise a waiver")
	text := expect("signed assertion for the project", map[string]any{"goal": "go.verify", "assertions": []any{ciTrue}}, "exc.team.vet", "applied", "")
	if !strings.Contains(text, `"trusted":{"ci":"true","repository":"`+repo+`"}`) {
		t.Fatalf("trusted context not reported in a deterministic order: %s", text)
	}
	for range 20 {
		if again := expect("repeat", map[string]any{"goal": "go.verify", "assertions": []any{ciTrue}}, "exc.team.vet", "applied", ""); again != text {
			t.Fatalf("resolve is not deterministic:\n%s\n%s", text, again)
		}
	}
	expect("assertion for another project", map[string]any{"goal": "go.verify",
		"assertions": []any{assertion("other.ci", otherRepo, nil, at.Add(-time.Hour), at.Add(time.Hour))}},
		"exc.team.vet", "not_applicable", "granted for repository "+repo)
	refused("attestation key used for another project", map[string]any{"goal": "go.verify",
		"assertions": []any{assertion("team.ci", otherRepo, nil, at.Add(-time.Hour), at.Add(time.Hour))}}, "attests only for "+repo)
	refused("expired assertion", map[string]any{"goal": "go.verify",
		"assertions": []any{assertion("team.ci", repo, nil, at.Add(-2*time.Hour), at.Add(-time.Hour))}}, "expired at")
	forged := assertion("team.ci", repo, map[string]string{"ci": "false"}, at.Add(-time.Hour), at.Add(time.Hour))
	forged["facts"] = map[string]string{"ci": "true"}
	refused("tampered assertion", map[string]any{"goal": "go.verify", "assertions": []any{forged}}, "the signature does not verify")

	// Conditional exception: a known trusted fact, an unknown one, and a contradicting caller.
	concurrent := map[string]any{"concurrency": "true"}
	expect("known trusted fact", map[string]any{"goal": "go.verify.concurrent", "context": concurrent, "assertions": []any{ciTrue}},
		"exc.corpus.race.ci", "applied", "")
	expect("unknown fact", map[string]any{"goal": "go.verify.concurrent", "context": map[string]any{"concurrency": "true", "ci": "true"},
		"assertions": []any{assertion("team.ci", repo, nil, at.Add(-time.Hour), at.Add(time.Hour))}},
		"exc.corpus.race.ci", "not_applicable", "requires trusted context ci")
	refused("contradicting caller", map[string]any{"goal": "go.verify.concurrent", "context": map[string]any{"concurrency": "true", "ci": "false"},
		"assertions": []any{ciTrue}}, "in the context but")

	// Domain authority follows the path it signed.
	domainArgs := func() map[string]any { return map[string]any{"goal": "go.verify.concurrent", "context": concurrent} }
	first := expect("domain approval", domainArgs(), "exc.software.vet", "applied", "")
	head := original
	reparent := func(broader ...string) {
		t.Helper()
		d := &knowledge.Record{}
		for _, r := range knowledgetest.CorpusRecords() {
			if r.Key == "domain.software.go" {
				d = r
			}
		}
		d.Broader = broader
		status, res := publish(head, docs(d), nil)
		if status != http.StatusCreated {
			t.Fatalf("reparent = %d %v", status, res)
		}
		head = res["snapshot"].(string)
	}
	reparent("domain.mathematics")
	expect("rule's domain moved beneath mathematics", domainArgs(), "exc.software.vet", "rejected", "a changed hierarchy needs a new approval")
	reparent()
	expect("domain ancestry removed", domainArgs(), "exc.software.vet", "rejected", "domain.software is not directly broader than domain.software.go")
	reparent("domain.software")
	expect("taxonomy restored", domainArgs(), "exc.software.vet", "applied", "")
	if again, _ := resolve(map[string]any{"goal": "go.verify.concurrent", "context": concurrent, "snapshot": original}); again != first {
		t.Fatalf("the original snapshot resolves differently:\n%s\n%s", first, again)
	}

	// A rollback restores semantics under a new revision; the approval stays stale.
	vet := rules["rule.go.vet"].Clone()
	vet.Program, vet.Revision = `obligation(P, "check.vet") :- record(P, "Procedure"), in_domain(P, "domain.software").`, 2
	status, res := publish(head, docs(vet), nil)
	if status != http.StatusCreated {
		t.Fatalf("revise = %d %v", status, res)
	}
	expect("revised rule", domainArgs(), "exc.software.vet", "rejected", "approved for revision 1 of rule.go.vet, which is now at revision 2")
	status, res = op(http.MethodPost, "/v1/knowledge/rollbacks", map[string]any{"base": res["snapshot"], "to": head,
		"author": "operator.test", "summary": "undo", "rationale": "acceptance"})
	if status != http.StatusCreated {
		t.Fatalf("rollback = %d %v", status, res)
	}
	expect("after rollback", domainArgs(), "exc.software.vet", "rejected", "approved for revision 1 of rule.go.vet, which is now at revision 3")
}
