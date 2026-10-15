package reasoning_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/reasoning"
)

// planCatalog publishes a parent composing its children, in the order
// given, with precedes edges from edges, and returns the resulting
// Catalog and the parent's typed Procedure record.
func planCatalog(t *testing.T, children []string, edges map[string][]string) (*knowledge.Catalog, *knowledge.Record) {
	t.Helper()
	store := knowledgetest.NewMemoryStore()
	k := knowledge.New(store, knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev}))
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "parent", Label: "parent", Definition: "d", Composes: children}
	var changes []knowledge.Change
	var records []knowledge.RecordChange
	changes = append(changes, knowledge.Change{Key: "parent", Procedure: knowledgetest.SharedProc("parent", "parent")})
	records = append(records, knowledge.RecordChange{Key: "parent", Record: parent})
	for _, c := range children {
		changes = append(changes, knowledge.Change{Key: c, Procedure: knowledgetest.SharedProc(c, c)})
		records = append(records, knowledge.RecordChange{Key: c, Record: &knowledge.Record{Type: knowledge.TypeProcedure, Key: c, Label: c, Definition: "d", Precedes: edges[c]}})
	}
	s, err := k.Publish(context.Background(), knowledge.Proposal{Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "s", Rationale: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := k.At(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := cat.Record("parent")
	if err != nil {
		t.Fatal(err)
	}
	return cat, goal
}

func keysOf(entries []reasoning.PlanEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Key
	}
	return out
}

func TestPlanNoComposition(t *testing.T) {
	cat, goal := planCatalog(t, nil, nil)
	plan, err := reasoning.Plan(cat, goal, nil)
	if err != nil || plan != nil {
		t.Fatalf("Plan of a goal with no composed children = %v, %v, want nil, nil", plan, err)
	}
}

func TestPlanOrdersByPrecedes(t *testing.T) {
	cat, goal := planCatalog(t, []string{"z", "x", "y"}, map[string][]string{"x": {"y"}, "y": {"z"}})
	plan, err := reasoning.Plan(cat, goal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(plan); !equalStrings(got, []string{"x", "y", "z"}) {
		t.Fatalf("order = %v, want [x y z]", got)
	}
	if plan[0].Status != "next" || plan[1].Status != "" || plan[2].Status != "" {
		t.Fatalf("statuses = %+v, want [next, '', '']", plan)
	}
}

func TestPlanWithCompleted(t *testing.T) {
	cat, goal := planCatalog(t, []string{"x", "y", "z"}, map[string][]string{"x": {"y"}, "y": {"z"}})
	plan, err := reasoning.Plan(cat, goal, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"x": "done", "y": "next", "z": ""}
	for _, e := range plan {
		if e.Status != want[e.Key] {
			t.Errorf("%s status = %q, want %q", e.Key, e.Status, want[e.Key])
		}
	}
}

func TestPlanParallelTiebreak(t *testing.T) {
	// No precedes edges at all: order falls back to key order.
	cat, goal := planCatalog(t, []string{"c", "a", "b"}, nil)
	plan, err := reasoning.Plan(cat, goal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(plan); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Fatalf("order = %v, want [a b c] (key order, no precedes edges)", got)
	}
}

func TestPlanInvalidCompleted(t *testing.T) {
	cat, goal := planCatalog(t, []string{"x", "y"}, nil)
	_, err := reasoning.Plan(cat, goal, []string{"not-a-child"})
	if !errors.Is(err, reasoning.ErrInvalidCompleted) {
		t.Fatalf("completed naming a non-child must be ErrInvalidCompleted, got %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
