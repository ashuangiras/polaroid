package knowledgeops_test

import (
	"context"
	"errors"
	"fmt"
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
	// ADR-0054's PlanTree reports an escalated leaf as "escalate"
	// directly (not masked as "next", the way Milestone 2's one-level
	// Plan, with no escalate vocabulary, had to); step.b, after it in
	// precedes order, must still read as not reached.
	if res.Plan[0].Status != "escalate" || len(res.Plan[0].Path) != 1 || res.Plan[0].Path[0] != "step.a" || res.Plan[1].Status != "" {
		t.Fatalf("an escalated key must freeze the plan there, reported as escalate: %+v", res.Plan)
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

// Multi-level plans, path-addressed leaves (ADR-0054, issue #161).

// nestedSpec is one record for nestedGuideFixture: a typed Procedure
// with Composes, and a matching legacy document.
type nestedSpec struct {
	Key      string
	Composes []string
}

// nestedGuideFixture publishes every record in specs and returns a Guide
// over the resulting catalog, letting a fixture build composition of any
// depth, unlike guideFixture's single level.
func nestedGuideFixture(t *testing.T, specs ...nestedSpec) *knowledgeops.Guide {
	t.Helper()
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: datalog.Evaluator{}}))
	var changes []knowledge.Change
	var records []knowledge.RecordChange
	for _, s := range specs {
		changes = append(changes, knowledge.Change{Key: s.Key, Procedure: knowledgetest.SharedProc(s.Key, s.Key)})
		records = append(records, knowledge.RecordChange{Key: s.Key, Record: &knowledge.Record{
			Type: knowledge.TypeProcedure, Key: s.Key, Label: s.Key, Definition: "d", Composes: s.Composes,
		}})
	}
	if _, err := k.Publish(context.Background(), knowledge.Proposal{Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "s", Rationale: "r"}}); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/polaroid.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return knowledgeops.NewGuide(k, memory.NewWalkthroughs(store))
}

// AC2: a nested plan's next correctly descends to the true next leaf,
// with the full path; advancing it by that path works end to end.
func TestGuideAdvanceDeepLeafByPath(t *testing.T) {
	g := nestedGuideFixture(t,
		nestedSpec{Key: "goal", Composes: []string{"a"}},
		nestedSpec{Key: "a", Composes: []string{"b"}},
		nestedSpec{Key: "b"},
	)
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b"}
	if len(start.Plan) != 1 || start.Plan[0].Status != "next" || fmt.Sprint(start.Plan[0].Path) != fmt.Sprint(want) {
		t.Fatalf("start plan = %+v, want next at %v", start.Plan, want)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Path: []string{"a", "b"}, Status: "done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan[0].Status != "done" {
		t.Fatalf("advancing the deep leaf by its full path must complete it: %+v", res.Plan)
	}
}

// AC3: the same sub-procedure key reused in two branches of one plan is
// reported and advanced unambiguously via path: completing one
// occurrence must not be confused with the other.
func TestGuideAdvanceSameLeafReusedInTwoBranches(t *testing.T) {
	g := nestedGuideFixture(t,
		nestedSpec{Key: "goal", Composes: []string{"branch1", "branch2"}},
		nestedSpec{Key: "branch1", Composes: []string{"shared"}},
		nestedSpec{Key: "branch2", Composes: []string{"shared"}},
		nestedSpec{Key: "shared"},
	)
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	if len(start.Plan) != 2 || start.Plan[0].Key != "branch1" || fmt.Sprint(start.Plan[0].Path) != fmt.Sprint([]string{"branch1", "shared"}) {
		t.Fatalf("start plan = %+v", start.Plan)
	}
	// Completing branch1's own occurrence of "shared" must not also
	// complete branch2's: they are two distinct leaves, addressed by
	// two distinct paths that happen to share a final key.
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Path: []string{"branch1", "shared"}, Status: "done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan[0].Status != "done" {
		t.Fatalf("branch1 = %+v, want done", res.Plan[0])
	}
	want := []string{"branch2", "shared"}
	if res.Plan[1].Status != "next" || fmt.Sprint(res.Plan[1].Path) != fmt.Sprint(want) {
		t.Fatalf("branch2's own occurrence of shared must still be outstanding: %+v, want next at %v", res.Plan[1], want)
	}
}

// AC4: a composed (non-leaf) key is never accepted as outcome's target,
// whether named by key or by a path whose final element is composed.
func TestGuideAdvanceComposedTargetIsInvalidRequest(t *testing.T) {
	g := nestedGuideFixture(t,
		nestedSpec{Key: "goal", Composes: []string{"a"}},
		nestedSpec{Key: "a", Composes: []string{"b"}},
		nestedSpec{Key: "b"},
	)
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []*wire.WalkthroughOutcomeArg{
		{Key: "a", Status: "done"},            // a is composed, not a leaf
		{Path: []string{"a"}, Status: "done"}, // same, by path
	} {
		_, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token, Outcome: outcome})
		var we *wire.KnowledgeError
		if !errors.As(err, &we) || we.Detail.Code != "invalid_request" {
			t.Fatalf("outcome %+v: err = %v, want invalid_request (a composed node is never a valid target)", outcome, err)
		}
	}
}

func TestGuideAdvanceWrongPathChainIsInvalidRequest(t *testing.T) {
	g := nestedGuideFixture(t,
		nestedSpec{Key: "goal", Composes: []string{"a"}},
		nestedSpec{Key: "a", Composes: []string{"b"}},
		nestedSpec{Key: "b"},
		nestedSpec{Key: "outsider"},
	)
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Path: []string{"a", "outsider"}, Status: "done"},
	})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" {
		t.Fatalf("a path naming a key that is not actually a's composed child must be invalid_request, got %v", err)
	}
}

func TestGuideAdvanceOutcomeRequiresExactlyOneOfKeyOrPath(t *testing.T) {
	g := nestedGuideFixture(t, nestedSpec{Key: "goal", Composes: []string{"a"}}, nestedSpec{Key: "a"})
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []*wire.WalkthroughOutcomeArg{
		{Status: "done"}, // neither given
		{Key: "a", Path: []string{"a"}, Status: "done"}, // both given
	} {
		_, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token, Outcome: outcome})
		var we *wire.KnowledgeError
		if !errors.As(err, &we) || we.Detail.Code != "invalid_request" {
			t.Fatalf("outcome %+v: err = %v, want invalid_request", outcome, err)
		}
	}
}

// AC5: an escalated leaf several levels deep freezes next there, and
// every composed ancestor above it also reports escalate.
func TestGuideAdvanceDeepEscalateFreezesAncestors(t *testing.T) {
	g := nestedGuideFixture(t,
		nestedSpec{Key: "goal", Composes: []string{"a"}},
		nestedSpec{Key: "a", Composes: []string{"b"}},
		nestedSpec{Key: "b", Composes: []string{"c"}},
		nestedSpec{Key: "c"},
	)
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Path: []string{"a", "b", "c"}, Status: "escalate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if res.Plan[0].Key != "a" || res.Plan[0].Status != "escalate" || fmt.Sprint(res.Plan[0].Path) != fmt.Sprint(want) {
		t.Fatalf("a (its grandchild c is escalated) = %+v, want escalate at %v", res.Plan[0], want)
	}
}

// Walkthrough completion semantics and an explicit close (ADR-0055,
// issue #162).

// AC1: status is "complete" exactly when plan has no next, "in_progress"
// otherwise, on both start and advance.
func TestGuideStatusCompleteAndInProgress(t *testing.T) {
	g, _ := guideFixture(t, "step.a", "step.b")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if start.Status != "in_progress" {
		t.Fatalf("start status = %q, want in_progress (step.a is next)", start.Status)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "in_progress" {
		t.Fatalf("status = %q, want in_progress (step.b is next)", res.Status)
	}
	res, err = g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Key: "step.b", Status: "done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "complete" {
		t.Fatalf("status = %q, want complete (every leaf done)", res.Status)
	}
}

func TestGuideStatusTriviallyCompleteWithNoComposedChildren(t *testing.T) {
	g, _ := guideFixture(t)
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if start.Status != "complete" {
		t.Fatalf("status = %q, want complete (no composed children, nothing to do)", start.Status)
	}
}

func TestGuideStatusEscalateIsInProgress(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
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
	if res.Status != "in_progress" {
		t.Fatalf("status = %q, want in_progress (an escalated leaf is not done)", res.Status)
	}
}

// AC2: a later advance reopening an already-done leaf moves status back
// to in_progress.
func TestGuideStatusReopenAfterComplete(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
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
	if res.Status != "complete" {
		t.Fatalf("status = %q, want complete", res.Status)
	}
	// A later correction reopens it.
	res, err = g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "escalate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "in_progress" {
		t.Fatalf("status = %q, want in_progress (a later report reopened a done leaf)", res.Status)
	}
}

// AC3: advance(token, close=true) ends the walkthrough; a subsequent
// call with that token is not_found.
func TestGuideAdvanceCloseEndsWalkthrough(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token, Close: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token != start.Token {
		t.Fatalf("a close call must still return the final state: %+v", res)
	}
	_, err = g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "not_found" {
		t.Fatalf("a call after close must be not_found, got %v", err)
	}
}

// AC4: close combined with an outcome in the same call applies the
// outcome, then closes.
func TestGuideAdvanceCloseWithOutcomeAppliesThenCloses(t *testing.T) {
	g, _ := guideFixture(t, "step.a", "step.b")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "done"}, Close: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan[0].Status != "done" || res.Plan[1].Status != "next" || res.Status != "in_progress" {
		t.Fatalf("the outcome must be reflected in the same, final response: %+v", res)
	}
	if _, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token}); err == nil {
		t.Fatalf("the walkthrough must be gone after close, even though it was not complete")
	}
}

// AC5: close on an already-closed or expired token is not_found.
func TestGuideAdvanceCloseAlreadyClosedIsNotFound(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token, Close: true}); err != nil {
		t.Fatal(err)
	}
	_, err = g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token, Close: true})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "not_found" {
		t.Fatalf("closing an already-closed token must be not_found, got %v", err)
	}
}

func TestGuideAdvanceCloseUnknownTokenIsNotFound(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
	_, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: "never-started", Close: true})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "not_found" {
		t.Fatalf("closing an unknown token must be not_found, got %v", err)
	}
}
