package http_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// walkthroughJSON restates the documented operator-facing walkthrough
// shape (ADR-0056).
type walkthroughJSON struct {
	Token      string            `json:"token"`
	Goal       string            `json:"goal"`
	Snapshot   string            `json:"snapshot"`
	Repository string            `json:"repository"`
	Context    map[string]string `json:"context"`
	Status     string            `json:"status"`
	CreatedAt  time.Time         `json:"created_at"`
	ExpiresAt  time.Time         `json:"expires_at"`
	Leaves     []struct {
		Key    string `json:"key"`
		Status string `json:"status"`
		Reason string `json:"reason"`
		Note   string `json:"note"`
	} `json:"leaves"`
}

// startWalkthrough creates a walkthrough directly against the test
// server's own store: list_walkthroughs and get_walkthrough are
// operator-facing, read-only HTTP endpoints (ADR-0056); a walkthrough
// itself is only ever created by /mcp/agent/v3's start (internal/knowledgeops.Guide),
// which this package's tests do not run.
func startWalkthrough(t *testing.T, s *testServer, goal, repository string) string {
	t.Helper()
	st, err := memory.NewWalkthroughs(s.store).Start(context.Background(), goal, "snap-1", repository, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st.Token
}

func TestGetWalkthroughOverHTTP(t *testing.T) {
	s := newTestServer(t)
	token := startWalkthrough(t, s, "dev.change.verify", "ashuangiras/polaroid")
	w := memory.NewWalkthroughs(s.store)
	if _, err := w.Advance(context.Background(), token, &memory.WalkthroughOutcome{
		Key: "gate.make-check", Status: memory.WalkthroughBlocked, Reason: "golangci-lint is not installed",
	}, nil); err != nil {
		t.Fatal(err)
	}

	resp := s.call(t, http.MethodGet, "/v1/walkthroughs/"+token, "")
	expect(t, resp, http.StatusOK, "")
	got := decodeStrict[walkthroughJSON](t, resp.body)
	if got.Token != token || got.Goal != "dev.change.verify" || got.Repository != "ashuangiras/polaroid" || got.Status != "blocked" {
		t.Fatalf("walkthrough = %+v", got)
	}
	if len(got.Leaves) != 1 || got.Leaves[0].Key != "gate.make-check" || got.Leaves[0].Status != "blocked" ||
		got.Leaves[0].Reason != "golangci-lint is not installed" {
		t.Fatalf("leaves = %+v", got.Leaves)
	}

	expect(t, s.call(t, http.MethodGet, "/v1/walkthroughs/never-started", ""), http.StatusNotFound, "not_found")
}

func TestListWalkthroughsQuery(t *testing.T) {
	s := newTestServer(t)
	w := memory.NewWalkthroughs(s.store)
	active := startWalkthrough(t, s, "goal.a", "repo-1")
	escalated := startWalkthrough(t, s, "goal.b", "repo-1")
	if _, err := w.Advance(context.Background(), escalated, &memory.WalkthroughOutcome{Key: "step", Status: memory.WalkthroughEscalate}, nil); err != nil {
		t.Fatal(err)
	}
	blocked := startWalkthrough(t, s, "goal.c", "repo-2")
	if _, err := w.Advance(context.Background(), blocked, &memory.WalkthroughOutcome{Key: "step", Status: memory.WalkthroughBlocked}, nil); err != nil {
		t.Fatal(err)
	}

	list := func(query string) []string {
		resp := s.call(t, http.MethodGet, "/v1/walkthroughs"+query, "")
		expect(t, resp, http.StatusOK, "")
		var got []string
		for _, wt := range decodeStrict[struct {
			Walkthroughs []walkthroughJSON `json:"walkthroughs"`
		}](t, resp.body).Walkthroughs {
			got = append(got, wt.Token)
		}
		return got
	}
	for query, want := range map[string][]string{
		"":                   {active, escalated, blocked},
		"?repository=repo-1": {active, escalated},
		"?goal=goal.c":       {blocked},
		"?status=active":     {active},
		"?status=escalate":   {escalated},
		"?status=blocked":    {blocked},
	} {
		if got := list(query); !slices.Equal(got, want) {
			t.Errorf("query %q: %v, want %v", query, got, want)
		}
	}

	for query, field := range map[string]string{
		"?status=done": "status",
		"?status=":     "status",
		"?limit=501":   "limit",
		"?repository=": "repository",
	} {
		resp := s.call(t, http.MethodGet, "/v1/walkthroughs"+query, "")
		expect(t, resp, http.StatusBadRequest, "invalid_request")
		if e := errorOf(t, resp); field != "" && (len(e.Fields) != 1 || e.Fields[0].Field != field) {
			t.Errorf("query %q: fields %+v, want [%s]", query, e.Fields, field)
		}
	}
	expect(t, s.call(t, http.MethodPost, "/v1/walkthroughs", ""), http.StatusMethodNotAllowed, "method_not_allowed")
}
