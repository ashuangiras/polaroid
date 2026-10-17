package knowledgeops_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func TestLegacySingleShotProposalRejected(t *testing.T) {
	proposer, _ := proposerFixture(t)
	_, err := proposer.Propose(context.Background(), wire.ProposeRequest{})
	if err == nil || !strings.Contains(err.Error(), "/mcp/agent/v6") {
		t.Fatalf("legacy propose error = %v, want migration guidance to v6", err)
	}
}

func TestProposeReviewedStillRequiresKernelFieldReviewReceipt(t *testing.T) {
	proposer, kernel := proposerFixture(t)
	cat, err := kernel.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, err := cat.Record("comp.standalone")
	if err != nil {
		t.Fatal(err)
	}
	record.Label = "updated label"
	_, err = proposer.ProposeReviewed(context.Background(), knowledge.Proposal{
		Base: cat.Snapshot(), Records: []knowledge.RecordChange{{Key: record.Key, Record: record}},
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "update label", Rationale: "reviewed"},
	}, record.Key, nil)
	if err == nil || !strings.Contains(err.Error(), "complete field-review receipt") {
		t.Fatalf("receipt-less proposal error = %v, want kernel field-review rejection", err)
	}
}
