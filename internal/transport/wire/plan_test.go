package wire_test

import (
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func leafNode(id string, version int, target *memory.CombinationStatus) memory.GraphNode {
	return memory.GraphNode{ProcedureID: id, CanonicalKey: "go.dependency.add", Version: version, Target: target}
}

// TestPlanOrdering: a node's own references (postorder) come before the
// node itself, so every dependency already appears earlier in a flattened
// plan than the step that composes it, matching the order the procedures'
// own instructions already document running them in.
func TestPlanOrdering(t *testing.T) {
	leaf := leafNode("leaf", 1, nil)
	mid := memory.GraphNode{ProcedureID: "mid", Version: 1, Edges: []memory.GraphEdge{
		{Reference: memory.Reference{Name: "leaf"}, SelectedBy: memory.SelectedByLatest, Node: leaf},
	}}
	root := memory.GraphNode{ProcedureID: "root", Version: 1, Edges: []memory.GraphEdge{
		{Reference: memory.Reference{Name: "mid"}, SelectedBy: memory.SelectedByLatest, Node: mid},
	}}
	steps := wire.NewPlan(root, "evidence")
	if len(steps) != 3 {
		t.Fatalf("steps = %+v, want 3", steps)
	}
	if steps[0].Path != "mid/leaf" || steps[1].Path != "mid" || steps[2].Path != "" {
		t.Fatalf("paths = %q, %q, %q; want mid/leaf, mid, \"\" (children before the parent)", steps[0].Path, steps[1].Path, steps[2].Path)
	}
	if steps[2].ProcedureID != "root" || steps[2].Version != 1 || steps[2].SelectedBy != "evidence" {
		t.Fatalf("root step = %+v", steps[2])
	}
	if steps[0].Status != "selected" || steps[1].Status != "selected" || steps[2].Status != "selected" {
		t.Fatalf("without a target, every step is just selected: %+v", steps)
	}
}

// TestPlanStatus: every status NewPlan/planStatus can report, and that
// next_action names the first step (in dependency order) still needing a
// decision or a run, never a step that is already verified or skipped.
func TestPlanStatus(t *testing.T) {
	verified := &memory.CombinationStatus{Verified: true, LatestExecutionID: "exec-verified"}
	pending := &memory.CombinationStatus{Verified: false}
	root := memory.GraphNode{ProcedureID: "root", Version: 1, Target: pending, Edges: []memory.GraphEdge{
		{Reference: memory.Reference{Name: "checks"}, SelectedBy: memory.SelectedByEvidence, Node: leafNode("checks", 1, verified)},
		{Reference: memory.Reference{Name: "integration"}, SelectedBy: memory.SelectedByLatest, Decision: memory.DecisionUndecided, Node: leafNode("integration", 1, nil)},
		{Reference: memory.Reference{Name: "hosted-ci"}, SelectedBy: memory.SelectedByLatest, Decision: memory.DecisionNotApplicable, SkippedBy: "exec-parent", Node: leafNode("hosted-ci", 1, nil)},
	}}
	p := wire.NewBindingPlan(memory.BindingResolution{
		Binding:    memory.Binding{ID: "b1", Repository: "github.com/example/repo"},
		Revision:   memory.BindingRevision{Number: 2},
		Resolution: memory.Resolution{SelectedBy: memory.SelectedByLatest, Graph: root},
	})
	if p.BindingID != "b1" || p.BindingRevision != 2 || p.Repository != "github.com/example/repo" {
		t.Fatalf("plan identity = %+v", p)
	}
	byPath := map[string]wire.PlanStep{}
	for _, s := range p.Steps {
		byPath[s.Path] = s
	}
	if byPath["checks"].Status != "verified" || byPath["checks"].LatestExecutionID != "exec-verified" {
		t.Fatalf("checks = %+v", byPath["checks"])
	}
	if byPath["integration"].Status != "decision_required" || byPath["integration"].Decision != "undecided" {
		t.Fatalf("integration = %+v", byPath["integration"])
	}
	if byPath["hosted-ci"].Status != "skipped" || byPath["hosted-ci"].Decision != "not_applicable" {
		t.Fatalf("hosted-ci = %+v", byPath["hosted-ci"])
	}
	if byPath[""].Status != "pending" {
		t.Fatalf("root = %+v", byPath[""])
	}
	// Dependency order: checks, integration, hosted-ci (each reference, in
	// declared order), then root.
	if p.Steps[0].Path != "checks" || p.Steps[1].Path != "integration" || p.Steps[2].Path != "hosted-ci" || p.Steps[3].Path != "" {
		t.Fatalf("order = %q, %q, %q, %q", p.Steps[0].Path, p.Steps[1].Path, p.Steps[2].Path, p.Steps[3].Path)
	}
	// next_action is the first step needing a decision or a run: here
	// "integration" (decision_required), before the still-"pending" root.
	if p.NextAction == nil || *p.NextAction != "integration" {
		t.Fatalf("next_action = %v, want integration", p.NextAction)
	}
}

// TestPlanNextActionOmittedWhenNothingToDo: every step verified or skipped
// means next_action is absent (nil), not a pointer to an empty path.
func TestPlanNextActionOmittedWhenNothingToDo(t *testing.T) {
	verified := &memory.CombinationStatus{Verified: true, LatestExecutionID: "exec1"}
	root := memory.GraphNode{ProcedureID: "root", Version: 1, Target: verified, Edges: []memory.GraphEdge{
		{Reference: memory.Reference{Name: "optional"}, Decision: memory.DecisionNotApplicable, SkippedBy: "exec1", Node: leafNode("optional", 1, nil)},
	}}
	p := wire.NewGraphPlan(root)
	if p.NextAction != nil {
		t.Fatalf("next_action = %v, want nil (everything is verified or skipped)", *p.NextAction)
	}
	if p.BindingID != "" || p.Environment != nil {
		t.Fatalf("a bare graph plan carries no binding identity: %+v", p)
	}
}

// TestPlanRootPathIsEmptyStringNotOmitted: the root step's own path is "",
// distinguishable from "no next action" (nil), never confused by omitzero.
func TestPlanRootPathIsEmptyStringNotOmitted(t *testing.T) {
	pending := &memory.CombinationStatus{Verified: false}
	root := memory.GraphNode{ProcedureID: "root", Version: 1, Target: pending}
	p := wire.NewGraphPlan(root)
	if len(p.Steps) != 1 || p.Steps[0].Path != "" {
		t.Fatalf("steps = %+v", p.Steps)
	}
	if p.NextAction == nil || *p.NextAction != "" {
		t.Fatalf("next_action = %v, want a pointer to \"\" (the root itself needs a run)", p.NextAction)
	}
}
