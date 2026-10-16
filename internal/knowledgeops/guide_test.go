package knowledgeops_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// guideFixture publishes a "parent" procedure composing children, in
// order, and returns a Guide over it plus the active snapshot token.
func guideFixture(t *testing.T, children ...string) (*knowledgeops.Guide, string) {
	t.Helper()
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: datalog.Evaluator{}}))
	var changes []knowledge.Change
	var records []knowledge.RecordChange
	changes = append(changes, knowledge.Change{Key: "parent", Procedure: knowledgetest.SharedProc("parent", "parent")})
	records = append(records, knowledge.RecordChange{Key: "parent", Record: &knowledge.Record{
		Type: knowledge.TypeProcedure, Key: "parent", Label: "parent", Definition: "d", Composes: children,
	}})
	for _, c := range children {
		changes = append(changes, knowledge.Change{Key: c, Procedure: knowledgetest.SharedProc(c, c)})
		records = append(records, knowledge.RecordChange{Key: c, Record: &knowledge.Record{Type: knowledge.TypeProcedure, Key: c, Label: c, Definition: "d"}})
	}
	s, err := k.Publish(context.Background(), knowledge.Proposal{Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "s", Rationale: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/polaroid.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return knowledgeops.NewGuide(k, memory.NewWalkthroughs(store)), string(s)
}

func TestGuideStartPinsSnapshotAndPlan(t *testing.T) {
	g, snap := guideFixture(t, "step.a", "step.b")
	res, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.Snapshot != snap {
		t.Fatalf("start result = %+v, want snapshot %s", res, snap)
	}
	if len(res.Plan) != 2 || res.Plan[0].Key != "step.a" || res.Plan[0].Status != "next" || res.Plan[1].Status != "" {
		t.Fatalf("plan = %+v", res.Plan)
	}
}

func TestGuideStartNoComposedChildrenIsTriviallyComplete(t *testing.T) {
	g, _ := guideFixture(t)
	res, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plan) != 0 {
		t.Fatalf("plan = %+v, want empty", res.Plan)
	}
}

func TestGuideStartUnknownGoalIsNotFound(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
	_, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "nope"})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestGuideStartRequiresGoal(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
	_, err := g.Start(context.Background(), wire.WalkthroughStartRequest{})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}

func TestGuideAdvanceReportsOutcomeAndMarksNext(t *testing.T) {
	g, _ := guideFixture(t, "step.a", "step.b")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan[0].Status != "done" || res.Plan[1].Status != "next" {
		t.Fatalf("plan = %+v", res.Plan)
	}
}

func TestGuideAdvancePeekLeavesStateUnchanged(t *testing.T) {
	g, _ := guideFixture(t, "step.a", "step.b")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan[0].Status != "next" || res.Plan[1].Status != "" {
		t.Fatalf("peek plan = %+v, want unchanged from start", res.Plan)
	}
}

func TestGuideAdvanceEscalateFreezesNext(t *testing.T) {
	g, _ := guideFixture(t, "step.a", "step.b")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "escalate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan[0].Status != "next" || res.Plan[1].Status != "" {
		t.Fatalf("an escalated key must still read as next, freezing the plan: %+v", res.Plan)
	}
	// A later done for the same key overrides the escalate and unfreezes next.
	res, err = g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan[0].Status != "done" || res.Plan[1].Status != "next" {
		t.Fatalf("done must override the earlier escalate and unfreeze next: %+v", res.Plan)
	}
}

func TestGuideAdvanceInvalidOutcomeKeyIsInvalidRequest(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Key: "not.a.child", Status: "done"},
	})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}

func TestGuideAdvanceUnknownTokenIsNotFound(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
	_, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: "never-started"})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
}
