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
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// guideFixture publishes a "parent" procedure composing children, in
// order, and returns a Guide over it plus the active snapshot token.
func guideFixture(t *testing.T, children ...string) (*knowledgeops.Guide, string) {
	t.Helper()
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: datalog.Evaluator{}}))
	var changes []knowledge.Change
	// test.obligation satisfies ADR-0057's completeness bar (#164) for
	// every Procedure record below: this fixture is about walkthrough
	// mechanics, not obligation reasoning, so one shared, trivial Concept
	// is enough.
	records := []knowledge.RecordChange{{Key: "test.obligation", Record: &knowledge.Record{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"}}}
	changes = append(changes, knowledge.Change{Key: "parent", Procedure: knowledgetest.SharedProc("parent", "parent")})
	records = append(records, knowledge.RecordChange{Key: "parent", Record: &knowledge.Record{
		Type: knowledge.TypeProcedure, Key: "parent", Label: "parent", Definition: "d", Composes: children, Requires: []string{"test.obligation"},
	}})
	for _, c := range children {
		changes = append(changes, knowledge.Change{Key: c, Procedure: knowledgetest.SharedProc(c, c)})
		records = append(records, knowledge.RecordChange{Key: c, Record: &knowledge.Record{Type: knowledge.TypeProcedure, Key: c, Label: c, Definition: "d", Requires: []string{"test.obligation"}}})
	}
	s, err := k.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "s", Rationale: "r"}}))
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/polaroid.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return knowledgeops.NewGuide(k, memory.NewWalkthroughs(store), datalog.Evaluator{}, policy.Trust{}), string(s)
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
	records := []knowledge.RecordChange{{Key: "test.obligation", Record: &knowledge.Record{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"}}}
	for _, s := range specs {
		changes = append(changes, knowledge.Change{Key: s.Key, Procedure: knowledgetest.SharedProc(s.Key, s.Key)})
		records = append(records, knowledge.RecordChange{Key: s.Key, Record: &knowledge.Record{
			Type: knowledge.TypeProcedure, Key: s.Key, Label: s.Key, Definition: "d", Composes: s.Composes, Requires: []string{"test.obligation"},
		}})
	}
	if _, err := k.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "s", Rationale: "r"}})); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/polaroid.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return knowledgeops.NewGuide(k, memory.NewWalkthroughs(store), datalog.Evaluator{}, policy.Trust{})
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

// Blocked status, visibility and expand (ADR-0056, issue #163).

// A blocked outcome (with a reason) freezes plan's next there, reported
// as "blocked" (not masked as "escalate"): a technical precondition the
// agent testifies is unmet, not a person's judgment call. status reads
// in_progress, the same as escalate.
func TestGuideAdvanceBlockedWithReasonFreezesNext(t *testing.T) {
	g, _ := guideFixture(t, "step.a", "step.b")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token:   start.Token,
		Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "blocked", Reason: "missing required tool"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan[0].Status != "blocked" || res.Plan[1].Status != "" {
		t.Fatalf("a blocked key must freeze the plan there, reported as blocked: %+v", res.Plan)
	}
	if res.Status != "in_progress" {
		t.Fatalf("status = %q, want in_progress (a blocked leaf is not done)", res.Status)
	}
	// A later done for the same key overrides the blocked and unfreezes next.
	res, err = g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan[0].Status != "done" || res.Plan[1].Status != "next" {
		t.Fatalf("done must override the earlier blocked and unfreeze next: %+v", res.Plan)
	}
}

// A deep blocked leaf propagates to every composed ancestor above it,
// the same way escalate does (ADR-0054's propagation rule, extended to
// the new status).
func TestGuideAdvanceDeepBlockedFreezesAncestors(t *testing.T) {
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
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Path: []string{"a", "b", "c"}, Status: "blocked"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if res.Plan[0].Key != "a" || res.Plan[0].Status != "blocked" || fmt.Sprint(res.Plan[0].Path) != fmt.Sprint(want) {
		t.Fatalf("a (its grandchild c is blocked) = %+v, want blocked at %v", res.Plan[0], want)
	}
}

// outcome.status rejects anything but done, skipped, escalate or blocked.
func TestGuideAdvanceInvalidStatusIsInvalidRequest(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token: start.Token, Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "pending"},
	})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}

// expand on start returns the top level exactly as without expand, plus
// nested Children for a composed entry, with an unreported nested leaf
// read as "pending" (ADR-0056).
func TestGuideStartExpandAttachesNestedChildren(t *testing.T) {
	g := nestedGuideFixture(t,
		nestedSpec{Key: "goal", Composes: []string{"a", "z"}},
		nestedSpec{Key: "a", Composes: []string{"b", "c"}},
		nestedSpec{Key: "b"},
		nestedSpec{Key: "c"},
		nestedSpec{Key: "z"},
	)
	plain, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "goal", Expand: true})
	if err != nil {
		t.Fatal(err)
	}
	// Top level unaffected by expand.
	if len(expanded.Plan) != len(plain.Plan) || expanded.Plan[0].Key != plain.Plan[0].Key || expanded.Plan[0].Status != plain.Plan[0].Status {
		t.Fatalf("expand's top level = %+v, want it to match plain %+v", expanded.Plan, plain.Plan)
	}
	if len(expanded.Plan[0].Children) != 2 || expanded.Plan[0].Children[0].Key != "b" || expanded.Plan[0].Children[0].Status != "pending" {
		t.Fatalf("a's children = %+v, want b and c both pending", expanded.Plan[0].Children)
	}
	if len(expanded.Plan[1].Children) != 0 {
		t.Fatalf("z is a leaf: must have no children, got %+v", expanded.Plan[1].Children)
	}
}

// expand's nested children are not gated by sibling order the way the
// top level's next is: reporting the second child blocked must still
// show it as blocked, regardless of the first child's own status.
func TestGuideAdvanceExpandChildrenNotGatedBySiblingOrder(t *testing.T) {
	g := nestedGuideFixture(t,
		nestedSpec{Key: "goal", Composes: []string{"a"}},
		nestedSpec{Key: "a", Composes: []string{"b", "c"}},
		nestedSpec{Key: "b"},
		nestedSpec{Key: "c"},
	)
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "goal", Expand: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{
		Token:   start.Token,
		Outcome: &wire.WalkthroughOutcomeArg{Path: []string{"a", "c"}, Status: "blocked"},
		Expand:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	children := res.Plan[0].Children
	if len(children) != 2 || children[0].Status != "pending" || children[1].Status != "blocked" {
		t.Fatalf("children = %+v, want b pending and c blocked regardless of order", children)
	}
}

// guideConditionalFixture publishes a parent composing "step.a" (an
// ordinary, unconditional step) then "step.cond" (classified under
// test.conditional-domain, with an obligation that only becomes required
// given context "mode"="special", via a Rule) -- for proving ADR-0058's
// context accumulation and reasoned-obligation behavior, which a plain
// Requires edge (guideFixture's own steps) cannot exercise.
func guideConditionalFixture(t *testing.T) *knowledgeops.Guide {
	t.Helper()
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: datalog.Evaluator{}}))
	domain := &knowledge.Record{Type: knowledge.TypeDomain, Key: "test.conditional-domain", Label: "d", Definition: "d"}
	obligation := &knowledge.Record{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"}
	conditional := &knowledge.Record{Type: knowledge.TypeConcept, Key: "test.conditional-obligation", Label: "c", Definition: "d"}
	rule := &knowledge.Record{Type: knowledge.TypeRule, Key: "test.conditional-rule", Label: "r", Statement: "r", Revision: 1, Waivable: true,
		ClassifiedAs: []string{"test.conditional-domain"},
		Program:      `obligation(P, "test.conditional-obligation") :- record(P, "Procedure"), classified_as(P, "test.conditional-domain"), context("mode", "special").`}
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "parent", Label: "parent", Definition: "d",
		Composes: []string{"step.a", "step.cond"}, Requires: []string{"test.obligation"}}
	stepA := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "step.a", Label: "a", Definition: "d", Requires: []string{"test.obligation"}}
	stepCond := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "step.cond", Label: "cond", Definition: "d",
		ClassifiedAs: []string{"test.conditional-domain"}, Requires: []string{"test.obligation"}}
	changes := []knowledge.Change{
		{Key: "parent", Procedure: knowledgetest.SharedProc("parent", "parent")},
		{Key: "step.a", Procedure: knowledgetest.SharedProc("step.a", "step.a")},
		{Key: "step.cond", Procedure: knowledgetest.SharedProc("step.cond", "step.cond")},
	}
	records := []knowledge.RecordChange{
		{Key: domain.Key, Record: domain}, {Key: obligation.Key, Record: obligation}, {Key: conditional.Key, Record: conditional},
		{Key: rule.Key, Record: rule}, {Key: parent.Key, Record: parent}, {Key: stepA.Key, Record: stepA}, {Key: stepCond.Key, Record: stepCond},
	}
	if _, err := k.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "s", Rationale: "r"}})); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/polaroid.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return knowledgeops.NewGuide(k, memory.NewWalkthroughs(store), datalog.Evaluator{}, policy.Trust{})
}

// TestGuideResolutionForNextLeaf proves ADR-0058's core behavior: start's
// result includes a resolution scoped to plan's own current next leaf.
func TestGuideResolutionForNextLeaf(t *testing.T) {
	g, _ := guideFixture(t, "step.a", "step.b")
	res, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolution == nil || res.Resolution.Goal != "step.a" {
		t.Fatalf("resolution = %+v, want scoped to next leaf step.a", res.Resolution)
	}
	found := false
	for _, o := range res.Resolution.Obligations {
		if o.Requirement == "test.obligation" && o.Status == "required" {
			found = true
		}
	}
	if !found {
		t.Fatalf("resolution.obligations = %+v, want test.obligation required", res.Resolution.Obligations)
	}
}

// TestGuideResolutionOmittedWhenComplete proves resolution is omitted
// once status is "complete": nothing left to resolve.
func TestGuideResolutionOmittedWhenComplete(t *testing.T) {
	g, _ := guideFixture(t, "step.a")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token,
		Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "done"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "complete" || res.Resolution != nil {
		t.Fatalf("result = %+v, want complete with no resolution", res)
	}
}

// TestGuideResolutionAccumulatesContextAcrossCalls proves ADR-0058's
// central claim: context given once at start still applies when a later
// leaf is reached several advance calls later, without resending it.
func TestGuideResolutionAccumulatesContextAcrossCalls(t *testing.T) {
	g := guideConditionalFixture(t)
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent", Context: map[string]string{"mode": "special"}})
	if err != nil {
		t.Fatal(err)
	}
	if start.Resolution == nil || start.Resolution.Goal != "step.a" {
		t.Fatalf("start resolution = %+v, want step.a", start.Resolution)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token,
		Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "done"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolution == nil || res.Resolution.Goal != "step.cond" {
		t.Fatalf("resolution = %+v, want scoped to step.cond", res.Resolution)
	}
	found := false
	for _, o := range res.Resolution.Obligations {
		if o.Requirement == "test.conditional-obligation" && o.Status == "required" {
			found = true
		}
	}
	if !found || len(res.Resolution.Unknowns) != 0 {
		t.Fatalf("resolution = %+v, want test.conditional-obligation required, fully decided from context given at start", res.Resolution)
	}
}

// TestGuideResolutionWithoutContextIsUnknown is the contrast case: without
// "mode=special" ever given, the same conditional obligation is an
// unknown, never silently assumed either way (ADR-0050's open world).
func TestGuideResolutionWithoutContextIsUnknown(t *testing.T) {
	g := guideConditionalFixture(t)
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token,
		Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "done"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolution == nil || len(res.Resolution.Unknowns) != 1 || res.Resolution.Unknowns[0].Requirement != "test.conditional-obligation" {
		t.Fatalf("resolution = %+v, want test.conditional-obligation unknown without context", res.Resolution)
	}
}

// TestGuideResolutionNoRetroactivity proves ADR-0058's "no retroactivity,
// by construction": once step.a is done, a later peek -- even bearing new
// context -- never resolves it again; it stays scoped to the real current
// next leaf.
func TestGuideResolutionNoRetroactivity(t *testing.T) {
	g, _ := guideFixture(t, "step.a", "step.b")
	start, err := g.Start(context.Background(), wire.WalkthroughStartRequest{Goal: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	adv, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token,
		Outcome: &wire.WalkthroughOutcomeArg{Key: "step.a", Status: "done"}})
	if err != nil {
		t.Fatal(err)
	}
	if adv.Resolution == nil || adv.Resolution.Goal != "step.b" {
		t.Fatalf("after step.a done, resolution = %+v, want scoped to step.b", adv.Resolution)
	}
	peek, err := g.Advance(context.Background(), wire.WalkthroughAdvanceRequest{Token: start.Token, Context: map[string]string{"anything": "new"}})
	if err != nil {
		t.Fatal(err)
	}
	if peek.Resolution == nil || peek.Resolution.Goal != "step.b" {
		t.Fatalf("peek resolution = %+v, want still scoped to step.b, never step.a again", peek.Resolution)
	}
}
