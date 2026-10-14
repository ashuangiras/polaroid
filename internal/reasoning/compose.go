package reasoning

import (
	"context"
	"fmt"
	"slices"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

// Composition verdicts.
const (
	Compatible   = "compatible"
	Incompatible = "incompatible"
	Undetermined = "undetermined"
)

// Finding is one reason for a composition verdict.
type Finding struct {
	Kind   string `json:"kind"` // input, precondition, informal
	Item   string `json:"item"`
	Status string `json:"status"` // satisfied, established, missing, contradicted, undetermined
	Reason string `json:"reason"`
}

// Port is a procedure input or output in a contract summary.
type Port struct {
	Name    string `json:"name"`
	Concept string `json:"concept,omitzero"`
}

// Contract is a procedure's formal contract as composition reads it.
type Contract struct {
	Key                string   `json:"key"`
	Inputs             []Port   `json:"inputs,omitzero"`
	Outputs            []Port   `json:"outputs,omitzero"`
	OutputsComplete    bool     `json:"outputs_complete"`
	Preconditions      []string `json:"preconditions,omitzero"`
	Postconditions     []string `json:"postconditions,omitzero"`
	Invariants         []string `json:"invariants,omitzero"`
	InformalConditions []string `json:"informal_conditions,omitzero"`
}

func contractOf(r *knowledge.Record) Contract {
	c := Contract{Key: r.Key, OutputsComplete: r.OutputsComplete, Preconditions: r.Preconditions, Postconditions: r.Postconditions,
		Invariants: r.Invariants, InformalConditions: r.InformalConditions}
	for _, p := range r.Inputs {
		c.Inputs = append(c.Inputs, Port(p))
	}
	for _, p := range r.Outputs {
		c.Outputs = append(c.Outputs, Port(p))
	}
	return c
}

// CompositionResult is the verdict on running producer, then consumer:
// both formal contracts, and one finding per consumer input, precondition
// and informal condition. Findings that are not satisfied or established
// are the missing prerequisites.
type CompositionResult struct {
	Version  string    `json:"version"`
	Snapshot string    `json:"snapshot"`
	Producer Contract  `json:"producer"`
	Consumer Contract  `json:"consumer"`
	Verdict  string    `json:"verdict"`
	Findings []Finding `json:"findings"`
}

// Compose decides whether the consumer may follow the producer, from their
// formal contracts only: typed inputs and outputs, and pre-, post-conditions
// and invariants named as concepts. It never executes either procedure and
// reads no caller context: nothing a caller asserts establishes a
// precondition. A requirement is incompatible only when it is formally
// contradicted, or when a typed input matches no output of a producer that
// declares its outputs complete; anything informal or not formally
// established is undetermined.
func Compose(ctx context.Context, cat *knowledge.Catalog, ev Evaluator, producer, consumer string, limits Limits) (*CompositionResult, error) {
	p, err := cat.Record(producer)
	if err != nil || p.Type != knowledge.TypeProcedure {
		return nil, fmt.Errorf("%w: producer %s is not a Procedure record", ErrUnknownGoal, producer)
	}
	q, err := cat.Record(consumer)
	if err != nil || q.Type != knowledge.TypeProcedure {
		return nil, fmt.Errorf("%w: consumer %s is not a Procedure record", ErrUnknownGoal, consumer)
	}
	if limits == (Limits{}) {
		limits = DefaultLimits
	}
	facts, _ := Facts(cat, nil, "")
	facts = append(facts, Fact{Predicate: "composition", Args: []string{producer, consumer}})
	res, err := ev.Evaluate(ctx, Program{System: System, Heads: Heads, EDB: EDB, Facts: facts,
		Query: []string{"established", "contradicted", "supplied"}, Limits: limits})
	if err != nil {
		return nil, err
	}
	has := func(pred string, a, b string) bool {
		return slices.ContainsFunc(res[pred], func(row []string) bool { return row[0] == a && row[1] == b })
	}
	out := &CompositionResult{Version: Version, Snapshot: string(cat.Snapshot()), Producer: contractOf(p), Consumer: contractOf(q), Verdict: Compatible}
	worsen := func(v string) {
		if v == Incompatible || (v == Undetermined && out.Verdict == Compatible) {
			out.Verdict = v
		}
	}
	for _, in := range q.Inputs {
		f := Finding{Kind: "input", Item: in.Name}
		switch {
		case in.Concept == "":
			f.Status, f.Reason = "undetermined", "the input has no formal type"
		case has("supplied", consumer, in.Name):
			f.Status, f.Reason = "satisfied", fmt.Sprintf("%s outputs %s", producer, in.Concept)
		case p.OutputsComplete && !slices.ContainsFunc(p.Outputs, func(o knowledge.Port) bool { return o.Concept == "" }):
			f.Status, f.Reason = "missing", fmt.Sprintf("%s declares all its outputs and none is %s", producer, in.Concept)
		default:
			f.Status, f.Reason = "undetermined", fmt.Sprintf("%s does not formally output %s, and does not declare its outputs complete", producer, in.Concept)
		}
		out.Findings = append(out.Findings, f)
	}
	for _, c := range q.Preconditions {
		f := Finding{Kind: "precondition", Item: c}
		switch {
		case has("contradicted", consumer, c):
			f.Status, f.Reason = "contradicted", fmt.Sprintf("a postcondition of %s contradicts %s", producer, c)
		case has("established", consumer, c):
			f.Status, f.Reason = "established", fmt.Sprintf("%s establishes %s", producer, c)
		default:
			f.Status, f.Reason = "undetermined", fmt.Sprintf("nothing formal establishes %s", c)
		}
		out.Findings = append(out.Findings, f)
	}
	for _, c := range q.InformalConditions {
		out.Findings = append(out.Findings, Finding{Kind: "informal", Item: c, Status: "undetermined", Reason: "an informal condition cannot be proven"})
	}
	for _, f := range out.Findings {
		switch f.Status {
		case "missing", "contradicted":
			worsen(Incompatible)
		case "undetermined":
			worsen(Undetermined)
		}
	}
	return out, nil
}
