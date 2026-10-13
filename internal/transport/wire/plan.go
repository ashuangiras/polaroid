package wire

import "github.com/ashuangiras/polaroid/internal/memory"

// PlanStep is one node of a composition graph, projected to only what an
// agent needs to decide what to do next (format=plan, prepare_execution;
// #131): no canonical_key, applicability or retirement, and the selected
// child is not nested in full — only procedure_id and version, enough to
// fetch its full record (get_version, get_procedure) when actually needed.
// path is the reference names from the root joined by "/", "" for the root
// itself. decision is present only for a conditional reference with a
// target (ADR-0029), empty for a required reference or without one.
//
// status is one of:
//   - "selected": no target was given, so nothing has been checked against one.
//   - "decision_required": a conditional reference's applicability is undecided.
//   - "skipped": a conditional reference was decided not applicable.
//   - "verified": the selected combination is verified at the target.
//   - "pending": a target was given and it is not verified there yet (never
//     run, or run without reaching verification); it still needs to run
//     before the parent that composes it can be verified.
//
// A "verified" step is the only one that is evidence of a successful run;
// the others are what to do, never a claim that it already happened.
type PlanStep struct {
	Path              string `json:"path"`
	ProcedureID       string `json:"procedure_id"`
	Version           int    `json:"version"`
	SelectedBy        string `json:"selected_by"`
	Decision          string `json:"decision,omitzero"`
	Status            string `json:"status"`
	LatestExecutionID string `json:"latest_execution_id,omitzero"`
}

// Plan is a flattened, dependency-ordered projection of a composition
// graph: every step's dependencies appear earlier in steps (a node's
// children before the node itself), the order the referenced procedures'
// own instructions already document running them in. next_action names the
// path of the first step still needing a decision or a run (status
// decision_required or pending, in that order), omitted when every step is
// already verified or skipped: there is nothing left to do for this target.
// A Plan is never evidence by itself; get_verification (or a step's own
// "verified" status, which reads the same derived verification) is.
type Plan struct {
	BindingID       string           `json:"binding_id,omitzero"`
	BindingRevision int              `json:"binding_revision,omitzero"`
	Repository      string           `json:"repository,omitzero"`
	Environment     *EnvironmentName `json:"environment,omitzero"`
	Steps           []PlanStep       `json:"steps"`
	NextAction      *string          `json:"next_action,omitzero"`
}

// NewPlan flattens a composition graph in dependency order. rootSelectedBy
// is how the root's own version was selected (a binding resolution's
// SelectedBy); pass "" for a graph read directly by an exact version, which
// has no selection of its own to report.
func NewPlan(n memory.GraphNode, rootSelectedBy string) []PlanStep {
	return appendPlanSteps(nil, "", n, rootSelectedBy, "", "")
}

func appendPlanSteps(steps []PlanStep, path string, n memory.GraphNode, selectedBy, decision, skippedBy string) []PlanStep {
	for _, e := range n.Edges {
		steps = appendPlanSteps(steps, joinPlanPath(path, e.Reference.Name), e.Node, string(e.SelectedBy), string(e.Decision), e.SkippedBy)
	}
	var latest string
	if n.Target != nil {
		latest = n.Target.LatestExecutionID
	}
	return append(steps, PlanStep{
		Path:              path,
		ProcedureID:       n.ProcedureID,
		Version:           n.Version,
		SelectedBy:        selectedBy,
		Decision:          decision,
		Status:            planStatus(decision, n.Target),
		LatestExecutionID: latest,
	})
}

func planStatus(decision string, target *memory.CombinationStatus) string {
	switch decision {
	case string(memory.DecisionUndecided):
		return "decision_required"
	case string(memory.DecisionNotApplicable):
		return "skipped"
	}
	if target == nil {
		return "selected"
	}
	if target.Verified {
		return "verified"
	}
	return "pending"
}

func joinPlanPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "/" + name
}

func nextAction(steps []PlanStep) *string {
	for _, s := range steps {
		if s.Status == "pending" || s.Status == "decision_required" {
			path := s.Path
			return &path
		}
	}
	return nil
}

// NewBindingPlan projects a binding's resolution (ResolveBinding) to a Plan.
func NewBindingPlan(r memory.BindingResolution) Plan {
	p := Plan{
		BindingID:       r.Binding.ID,
		BindingRevision: r.Revision.Number,
		Repository:      r.Binding.Repository,
		Environment:     &EnvironmentName{Name: r.Environment},
		Steps:           NewPlan(r.Graph, string(r.SelectedBy)),
	}
	p.NextAction = nextAction(p.Steps)
	return p
}

// NewGraphPlan projects a bare composition graph (CompositionGraph, no
// binding) to a Plan.
func NewGraphPlan(n memory.GraphNode) Plan {
	p := Plan{Steps: NewPlan(n, "")}
	p.NextAction = nextAction(p.Steps)
	return p
}
