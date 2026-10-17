package reasoning_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/reasoning"
)

// treeSpec is one record to publish for a PlanTree fixture: a typed
// Procedure with Composes/Precedes, and a matching legacy document.
type treeSpec struct {
	Key      string
	Composes []string
	Precedes []string
}

// publishTree publishes every record in specs and returns the resulting
// Catalog, from which any key's record can be fetched with cat.Record.
func publishTree(t *testing.T, specs []treeSpec) *knowledge.Catalog {
	t.Helper()
	store := knowledgetest.NewMemoryStore()
	k := knowledge.New(store, knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev}))
	var changes []knowledge.Change
	// test.obligation satisfies ADR-0057's completeness bar (#164): this
	// fixture is about plan-tree shape, not obligation reasoning.
	records := []knowledge.RecordChange{{Key: "test.obligation", Record: &knowledge.Record{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"}}}
	for _, s := range specs {
		changes = append(changes, knowledge.Change{Key: s.Key, Procedure: knowledgetest.SharedProc(s.Key, s.Key)})
		records = append(records, knowledge.RecordChange{Key: s.Key, Record: &knowledge.Record{
			Type: knowledge.TypeProcedure, Key: s.Key, Label: s.Key, Definition: "d", Composes: s.Composes, Precedes: s.Precedes, Requires: []string{"test.obligation"},
		}})
	}
	snap, err := k.Publish(context.Background(), knowledge.Proposal{Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "s", Rationale: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := k.At(context.Background(), snap)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// noneReported is a leafStatus that has nothing recorded for any path.
func noneReported([]string) reasoning.LeafStatus { return reasoning.LeafStatus{} }

// statusOf builds a leafStatus from an explicit map keyed by the "/"-
// joined path, mirroring how internal/knowledgeops encodes a walkthrough
// outcome's path for internal/memory's flat, per-key fold.
func statusOf(done, escalate, blocked [][]string) func([]string) reasoning.LeafStatus {
	doneSet := map[string]bool{}
	for _, p := range done {
		doneSet[fmt.Sprint(p)] = true
	}
	escalateSet := map[string]bool{}
	for _, p := range escalate {
		escalateSet[fmt.Sprint(p)] = true
	}
	blockedSet := map[string]bool{}
	for _, p := range blocked {
		blockedSet[fmt.Sprint(p)] = true
	}
	return func(path []string) reasoning.LeafStatus {
		return reasoning.LeafStatus{Done: doneSet[fmt.Sprint(path)], Escalate: escalateSet[fmt.Sprint(path)], Blocked: blockedSet[fmt.Sprint(path)]}
	}
}

func TestPlanTreeNoComposition(t *testing.T) {
	cat := publishTree(t, []treeSpec{{Key: "goal"}})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reasoning.PlanTree(cat, goal, noneReported)
	if err != nil || plan != nil {
		t.Fatalf("PlanTree of a goal with no composed children = %v, %v, want nil, nil", plan, err)
	}
}

// AC1: a one-level plan (no nested composition) behaves identically to
// Milestone 2: next's path is a single-element array equal to the bare
// key case.
func TestPlanTreeOneLevelMatchesMilestone2Shape(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"x", "y"}},
		{Key: "x", Precedes: []string{"y"}},
		{Key: "y"},
	})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reasoning.PlanTree(cat, goal, noneReported)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || plan[0].Key != "x" || plan[0].Status != "next" || len(plan[0].Path) != 1 || plan[0].Path[0] != "x" {
		t.Fatalf("plan = %+v", plan)
	}
	if plan[1].Key != "y" || plan[1].Status != "" || plan[1].Path != nil {
		t.Fatalf("y (not reached) = %+v", plan[1])
	}
}

// AC2: a nested plan's next correctly descends to the true next leaf,
// with the full path.
func TestPlanTreeDescendsToDeepLeaf(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"a"}},
		{Key: "a", Composes: []string{"b"}},
		{Key: "b", Composes: []string{"c"}},
		{Key: "c"},
	})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reasoning.PlanTree(cat, goal, noneReported)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if len(plan) != 1 || plan[0].Key != "a" || plan[0].Status != "next" || fmt.Sprint(plan[0].Path) != fmt.Sprint(want) {
		t.Fatalf("plan = %+v, want next at %v", plan, want)
	}
}

// AC3: the same sub-procedure key reused in two branches of one plan is
// reported and advanced unambiguously via path — its two occurrences'
// recorded outcomes must not be confused with each other.
func TestPlanTreeSameLeafReusedInTwoBranches(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"branch1", "branch2"}, Precedes: nil},
		{Key: "branch1", Composes: []string{"shared"}},
		{Key: "branch2", Composes: []string{"shared"}},
		{Key: "shared"},
	})
	// goal.Composes has no precedes among branch1/branch2; order is by
	// key (deterministic tie-break), so branch1 first.
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	// branch1's own occurrence of "shared" (path branch1/shared) is
	// done; branch2's own occurrence (path branch2/shared) is not.
	status := statusOf([][]string{{"branch1", "shared"}}, nil, nil)
	plan, err := reasoning.PlanTree(cat, goal, status)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan[0].Key != "branch1" || plan[0].Status != "done" {
		t.Fatalf("branch1 (its only leaf is done) = %+v, want done", plan[0])
	}
	want := []string{"branch2", "shared"}
	if plan[1].Key != "branch2" || plan[1].Status != "next" || fmt.Sprint(plan[1].Path) != fmt.Sprint(want) {
		t.Fatalf("branch2 (its own occurrence of shared is not done) = %+v, want next at %v", plan[1], want)
	}
}

// AC4 (structural half; outcome-target validation itself is
// internal/knowledgeops's responsibility): a composed node's status is
// never directly settable — only a leaf's recorded outcome ever
// contributes, however it is addressed.
func TestPlanTreeComposedStatusIsPurelyStructural(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"parent"}},
		{Key: "parent", Composes: []string{"a", "b"}, Precedes: nil},
		{Key: "a"},
		{Key: "b"},
	})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	// Only "a" (one of parent's two leaves) is done; "parent" itself is
	// never reported (it is not a leaf, so advance could never target
	// it) — its status must still reflect that "b" is outstanding.
	status := statusOf([][]string{{"parent", "a"}}, nil, nil)
	plan, err := reasoning.PlanTree(cat, goal, status)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"parent", "b"}
	if len(plan) != 1 || plan[0].Key != "parent" || plan[0].Status != "next" || fmt.Sprint(plan[0].Path) != fmt.Sprint(want) {
		t.Fatalf("plan = %+v, want parent next at %v (b still outstanding)", plan, want)
	}
}

// AC5: an escalated leaf several levels deep freezes next there, and
// every composed ancestor above it also reports escalate.
func TestPlanTreeEscalatedDeepLeafFreezesAncestors(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"a", "z"}, Precedes: nil},
		{Key: "a", Composes: []string{"b", "c"}, Precedes: nil},
		{Key: "b", Precedes: []string{"c"}},
		{Key: "c"},
		{Key: "z"},
	})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	status := statusOf(nil, [][]string{{"a", "b"}}, nil)
	plan, err := reasoning.PlanTree(cat, goal, status)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b"}
	if len(plan) != 2 || plan[0].Key != "a" || plan[0].Status != "escalate" || fmt.Sprint(plan[0].Path) != fmt.Sprint(want) {
		t.Fatalf("a (its leaf b is escalated) = %+v, want escalate at %v", plan[0], want)
	}
	if plan[1].Key != "z" || plan[1].Status != "" {
		t.Fatalf("z (not reached: a precedes it and is not done) = %+v", plan[1])
	}

	// A later done for b overrides the escalate; a's next outstanding
	// leaf, c (which b precedes), becomes the new target — a is not yet
	// fully done, so z stays frozen behind it.
	status2 := statusOf([][]string{{"a", "b"}}, nil, nil)
	plan2, err := reasoning.PlanTree(cat, goal, status2)
	if err != nil {
		t.Fatal(err)
	}
	want2 := []string{"a", "c"}
	if plan2[0].Status != "next" || fmt.Sprint(plan2[0].Path) != fmt.Sprint(want2) || plan2[1].Status != "" {
		t.Fatalf("after b is done, plan = %+v, want a next at %v (c still outstanding) and z not reached", plan2, want2)
	}

	// Only once c is also done does a become fully done, unfreezing z.
	status3 := statusOf([][]string{{"a", "b"}, {"a", "c"}}, nil, nil)
	plan3, err := reasoning.PlanTree(cat, goal, status3)
	if err != nil {
		t.Fatal(err)
	}
	if plan3[0].Status != "done" || plan3[1].Status != "next" {
		t.Fatalf("once every leaf under a is done, plan = %+v, want a done and z next", plan3)
	}
}

func TestPlanTreeTooDeepIsDefended(t *testing.T) {
	const depth = 40 // beyond maxPlanDepth (knowledge.MaxGraphDepth, 32):
	// composes has no publish-time depth bound today (unlike the legacy
	// reference graph), so this chain publishes successfully; PlanTree's
	// own defensive cap must still refuse to recurse into it forever.
	var specs []treeSpec
	specs = append(specs, treeSpec{Key: "goal", Composes: []string{"n0"}})
	for i := range depth {
		next := fmt.Sprintf("n%d", i+1)
		specs = append(specs, treeSpec{Key: fmt.Sprintf("n%d", i), Composes: []string{next}})
	}
	specs = append(specs, treeSpec{Key: fmt.Sprintf("n%d", depth)}) // the leaf
	cat := publishTree(t, specs)
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reasoning.PlanTree(cat, goal, noneReported); !errors.Is(err, reasoning.ErrPlanTooDeep) {
		t.Fatalf("a %d-level chain must be refused as too deep, got %v", depth, err)
	}
}

// blocked, kept distinct from escalate (ADR-0056, issue #163).

func TestPlanTreeBlockedFreezesNext(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"a", "z"}},
		{Key: "a", Precedes: []string{"z"}},
		{Key: "z"},
	})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	status := statusOf(nil, nil, [][]string{{"a"}})
	plan, err := reasoning.PlanTree(cat, goal, status)
	if err != nil {
		t.Fatal(err)
	}
	if plan[0].Status != "blocked" || plan[1].Status != "" {
		t.Fatalf("a blocked leaf must freeze next, reported as blocked, distinct from escalate: %+v", plan)
	}
}

func TestPlanTreeBlockedPropagatesToAncestors(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"a"}},
		{Key: "a", Composes: []string{"b"}},
		{Key: "b"},
	})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	status := statusOf(nil, nil, [][]string{{"a", "b"}})
	plan, err := reasoning.PlanTree(cat, goal, status)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b"}
	if plan[0].Status != "blocked" || fmt.Sprint(plan[0].Path) != fmt.Sprint(want) {
		t.Fatalf("a (its leaf b is blocked) = %+v, want blocked at %v", plan[0], want)
	}
}

// ExpandTree (ADR-0056's expand), issue #163.

func TestExpandTreeMatchesPlanTreeAtTopLevel(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"x", "y"}},
		{Key: "x", Precedes: []string{"y"}},
		{Key: "y"},
	})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reasoning.PlanTree(cat, goal, noneReported)
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := reasoning.ExpandTree(cat, goal, noneReported)
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan {
		if plan[i].Key != expanded[i].Key || plan[i].Status != expanded[i].Status || fmt.Sprint(plan[i].Path) != fmt.Sprint(expanded[i].Path) {
			t.Fatalf("expand must keep the top level identical to PlanTree: %+v vs %+v", plan, expanded)
		}
	}
}

func TestExpandTreeAttachesNestedChildren(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"a"}},
		{Key: "a", Composes: []string{"b", "c"}, Precedes: nil},
		{Key: "b"},
		{Key: "c"},
	})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	status := statusOf([][]string{{"a", "b"}}, nil, [][]string{{"a", "c"}})
	plan, err := reasoning.ExpandTree(cat, goal, status)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Key != "a" {
		t.Fatalf("plan = %+v", plan)
	}
	children := plan[0].Children
	if len(children) != 2 || children[0].Key != "b" || children[0].Status != "done" || children[1].Key != "c" || children[1].Status != "blocked" {
		t.Fatalf("children = %+v, want b done and c blocked (both visible, not gated by order)", children)
	}
	// a's own rollup status reflects blocked, even though b is done first.
	if plan[0].Status != "blocked" {
		t.Fatalf("a's own status = %q, want blocked", plan[0].Status)
	}
}

func TestExpandTreeUnreportedLeafIsPending(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"a"}},
		{Key: "a", Composes: []string{"b"}},
		{Key: "b"},
	})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reasoning.ExpandTree(cat, goal, noneReported)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan[0].Children) != 1 || plan[0].Children[0].Status != "pending" {
		t.Fatalf("an unreported leaf in an expanded tree must read pending, not blank: %+v", plan[0].Children)
	}
}

func TestExpandTreeRecursesToAnyDepth(t *testing.T) {
	cat := publishTree(t, []treeSpec{
		{Key: "goal", Composes: []string{"a"}},
		{Key: "a", Composes: []string{"b"}},
		{Key: "b", Composes: []string{"c"}},
		{Key: "c"},
	})
	goal, err := cat.Record("goal")
	if err != nil {
		t.Fatal(err)
	}
	status := statusOf(nil, nil, [][]string{{"a", "b", "c"}})
	plan, err := reasoning.ExpandTree(cat, goal, status)
	if err != nil {
		t.Fatal(err)
	}
	b := plan[0].Children
	if len(b) != 1 || b[0].Key != "b" || b[0].Status != "blocked" {
		t.Fatalf("b = %+v", b)
	}
	c := b[0].Children
	if len(c) != 1 || c[0].Key != "c" || c[0].Status != "blocked" {
		t.Fatalf("c = %+v", c)
	}
}
