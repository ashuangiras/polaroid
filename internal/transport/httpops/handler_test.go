package httpops_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/semantic"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/knowledgesync"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	"github.com/ashuangiras/polaroid/internal/transport/httpops"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

const token = "test-operator-token"

func newServer(t *testing.T) string {
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
	syncer, err := knowledgesync.New(repo, knowledgesync.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	op := knowledgeops.NewOperator(syncer, knowledgesync.Policy{}, memory.NewService(store))
	h := httpops.NewHandler(op, token, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func do(t *testing.T, method, url, tok string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set(httpops.TokenHeader, tok)
	}
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

func TestOperatorRequiresToken(t *testing.T) {
	url := newServer(t)
	if status, _ := do(t, http.MethodGet, url+"/v1/knowledge/status", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d", status)
	}

	if status, _ := do(t, http.MethodGet, url+"/v1/knowledge/status", "wrong", nil); status != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d", status)
	}
	if status, body := do(t, http.MethodGet, url+"/v1/knowledge/status", token, nil); status != http.StatusOK {
		t.Fatalf("correct token: status = %d, body = %v", status, body)
	}
}

func TestOperatorApprovesExactReviewedDraft(t *testing.T) {
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
	syncer, err := knowledgesync.New(repo, knowledgesync.Policy{}, knowledge.WithRecordValidator(sem))
	if err != nil {
		t.Fatal(err)
	}
	kernel := syncer.Kernel()
	drafter := knowledgeops.NewFieldDrafter(kernel, knowledgeops.NewProposer(kernel, datalog.Evaluator{}, policy.Trust{}), memory.NewFieldDrafts(store))
	srv := httptest.NewServer(httpops.NewHandlerWithDrafts(
		knowledgeops.NewOperator(syncer, knowledgesync.Policy{}, memory.NewService(store)), drafter, token,
		slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	start, err := drafter.StartV8(ctx, wire.FieldDraftStartRequestV8{
		Key: "concept.approved", RecordType: string(knowledge.TypeConcept), New: true, Proposer: "agent", Rationale: "owner review",
	})
	if err != nil {
		t.Fatal(err)
	}
	var ready wire.FieldDraftResult
	for _, field := range start.Fields {
		answer := &wire.FieldDraftOutcomeArg{Entity: field.Entity, Field: field.Field, Operation: "clear"}
		switch field.Field {
		case "label":
			answer.Operation, answer.Value = "replace", "approved concept"
			answer.Citations = []wire.ProposeCitation{{Source: "test", Quote: "approved concept"}}
		case "definition":
			answer.Operation, answer.Value = "replace", "A reviewed definition"
			answer.Citations = []wire.ProposeCitation{{Source: "test", Quote: "reviewed definition"}}
		}
		ready, err = drafter.Advance(ctx, wire.FieldDraftAdvanceRequest{Token: start.Token, Outcome: answer})
		if err != nil {
			t.Fatal(err)
		}
	}
	url := srv.URL + "/v2/knowledge/drafts/" + start.Token
	if status, _ := do(t, http.MethodGet, url, "", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated preview = %d", status)
	}
	if status, preview := do(t, http.MethodGet, url, token, nil); status != http.StatusOK || preview["candidate_hash"] != ready.CandidateHash ||
		preview["rationale"] != "owner review" || len(preview["fields"].([]any)) == 0 {
		t.Fatalf("operator preview = %d %v", status, preview)
	}
	endpoint := url + "/publication"
	if status, _ := do(t, http.MethodPost, endpoint, "", map[string]any{"candidate_hash": ready.CandidateHash}); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated approval = %d", status)
	}
	if status, _ := do(t, http.MethodPost, endpoint, token, map[string]any{"candidate_hash": "fabricated"}); status != http.StatusBadRequest {
		t.Fatalf("wrong candidate = %d", status)
	}
	before, err := kernel.Active(ctx)
	if err != nil || before.Snapshot() != knowledge.Snapshot(start.Snapshot) {
		t.Fatalf("unauthorized request moved snapshot: %v", err)
	}
	if status, result := do(t, http.MethodPost, endpoint, token, map[string]any{"candidate_hash": ready.CandidateHash}); status != http.StatusCreated || result["status"] != "complete" {
		t.Fatalf("approval = %d %v", status, result)
	}
	if status, _ := do(t, http.MethodPost, endpoint, token, map[string]any{"candidate_hash": ready.CandidateHash}); status != http.StatusNotFound {
		t.Fatalf("replayed approval = %d", status)
	}
}

func TestOperatorStatusAndPublish(t *testing.T) {
	url := newServer(t)
	status, body := do(t, http.MethodGet, url+"/v1/knowledge/status", token, nil)
	if status != http.StatusOK || body["empty"] != true {
		t.Fatalf("status = %d %v", status, body)
	}

	publishBody := map[string]any{
		"base": "",
		"changes": []map[string]any{{
			"key": "go.module.tidy", "goal": "Tidy a module", "philosophy": "p", "method": "m",
			"contract": map[string]any{}, "instructions": map[string]any{},
		}},
		"author": "operator", "summary": "add tidy", "rationale": "requested",
		"review": procedureReview("go.module.tidy", "Tidy a module", "p", "m"),
	}
	status, body = do(t, http.MethodPost, url+"/v1/knowledge/publications", token, map[string]any{
		"base": "", "changes": publishBody["changes"], "author": "operator", "summary": "add tidy", "rationale": "requested",
	})
	if status != http.StatusBadRequest || body["error"] == nil {
		t.Fatalf("legacy v1 publish = %d %v, want migration error", status, body)
	}
	status, body = do(t, http.MethodPost, url+"/v2/knowledge/publications", token, publishBody)
	if status != http.StatusCreated || body["snapshot"] == "" {
		t.Fatalf("publish = %d %v", status, body)
	}

	status, body = do(t, http.MethodGet, url+"/v1/knowledge/status", token, nil)
	if status != http.StatusOK || body["empty"] == true || body["keys"] != 1.0 {
		t.Fatalf("status after publish = %d %v", status, body)
	}

	status, body = do(t, http.MethodGet, url+"/v1/knowledge/activations", token, nil)
	if status != http.StatusOK {
		t.Fatalf("activations = %d %v", status, body)
	}

	status, _ = do(t, http.MethodPost, url+"/v1/knowledge/sources/no-such-source/fetch", token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("fetch from an unknown source = %d", status)
	}

	status, _ = do(t, http.MethodPost, url+"/v1/knowledge/reconciliations/does-not-exist", token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("reconcile an unknown feedback id = %d", status)
	}
}

func TestOperatorRollback(t *testing.T) {
	url := newServer(t)
	publish := func(base, goal string) string {
		t.Helper()
		status, body := do(t, http.MethodPost, url+"/v2/knowledge/publications", token, map[string]any{
			"base": base, "changes": []map[string]any{{"key": "go.module.tidy", "goal": goal, "philosophy": "p", "method": "m",
				"contract": map[string]any{}, "instructions": map[string]any{}}},
			"author": "operator", "summary": goal, "rationale": "requested",
			"review": procedureReview("go.module.tidy", goal, "p", "m")})
		if status != http.StatusCreated {
			t.Fatalf("publish = %d %v", status, body)
		}
		return body["snapshot"].(string)
	}
	s1 := publish("", "Tidy a module")
	s2 := publish(s1, "Tidy a module, twice")
	rollback := func(base, to string) (int, map[string]any) {
		return do(t, http.MethodPost, url+"/v2/knowledge/rollbacks", token, map[string]any{
			"base": base, "to": to, "author": "operator", "summary": "undo", "rationale": "the second tidy was wrong",
			"review": procedureReview("go.module.tidy", "Tidy a module", "p", "m")})
	}
	if status, body := rollback(s1, s2); status != http.StatusConflict {
		t.Fatalf("rollback on a stale base = %d %v", status, body)
	}
	if status, body := rollback(s2, "0000000000000000000000000000000000000000"); status != http.StatusNotFound {
		t.Fatalf("rollback to an unknown snapshot = %d %v", status, body)
	}
	status, body := rollback(s2, s1)
	if status != http.StatusCreated || body["snapshot"] == s2 || body["snapshot"] == "" || len(body) != 1 {
		t.Fatalf("rollback = %d %v", status, body)
	}
	if status, _ := do(t, http.MethodPost, url+"/v1/knowledge/rollbacks", "", map[string]any{"base": s2, "to": s1}); status != http.StatusUnauthorized {
		t.Fatalf("rollback without a token = %d", status)
	}
}

func procedureReview(key, goal, philosophy, method string) map[string]any {
	citation := func(text string) []map[string]string {
		return []map[string]string{{"source": key, "quote": text}}
	}
	return map[string]any{
		"version": 1,
		"entities": []map[string]any{{
			"kind": "procedure", "key": key,
			"fields": []map[string]any{
				{"field": "goal", "operation": "replace", "citations": citation(goal)},
				{"field": "philosophy", "operation": "replace", "citations": citation(philosophy)},
				{"field": "method", "operation": "replace", "citations": citation(method)},
				{"field": "applicability", "operation": "clear"},
				{"field": "contract", "operation": "replace"},
				{"field": "instructions", "operation": "replace"},
				{"field": "references", "operation": "clear"},
			},
		}},
	}
}
