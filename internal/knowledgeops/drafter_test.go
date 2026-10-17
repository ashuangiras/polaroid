package knowledgeops_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func TestLegacyProposalToolsRejectIncompleteReview(t *testing.T) {
	kernel := knowledge.New(knowledgetest.NewMemoryStore())
	proposer := knowledgeops.NewProposer(kernel, datalog.Evaluator{}, policy.Trust{})
	_, err := proposer.Propose(context.Background(), wire.ProposeRequest{})
	if err == nil || !strings.Contains(err.Error(), "/mcp/agent/v6") {
		t.Fatalf("legacy propose error = %v, want migration guidance to v6", err)
	}

	store, err := sqlite.Open(context.Background(), t.TempDir()+"/legacy-draft.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	drafter := knowledgeops.NewDrafter(kernel, proposer, memory.NewDrafts(store))
	if _, err := drafter.Start(context.Background(), wire.DraftStartRequest{}); err == nil || !strings.Contains(err.Error(), "/mcp/agent/v6") {
		t.Fatalf("legacy propose_start error = %v, want migration guidance to v6", err)
	}
	if _, err := drafter.Advance(context.Background(), wire.DraftAdvanceRequest{}); err == nil || !strings.Contains(err.Error(), "/mcp/agent/v6") {
		t.Fatalf("legacy propose_advance error = %v, want migration guidance to v6", err)
	}
}
