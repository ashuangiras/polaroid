package reasoning

import (
	"context"
	"fmt"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

// RuleValidator refuses, at publication and on every load, rule records
// whose programs are outside the safe subset or do not analyse together
// with the built-in program of Version.
type RuleValidator struct{ Evaluator Evaluator }

var _ knowledge.RecordValidator = RuleValidator{}

// ValidateRecords implements knowledge.RecordValidator.
func (v RuleValidator) ValidateRecords(ctx context.Context, docs []knowledge.RecordDocument) error {
	var rules []Rule
	for _, d := range docs {
		if d.Record.Type == knowledge.TypeRule {
			rules = append(rules, Rule{Key: d.Record.Key, Text: d.Record.Program})
		}
	}
	if len(rules) == 0 {
		return nil
	}
	if _, err := v.Evaluator.Evaluate(ctx, Program{System: System, Rules: rules, Heads: Heads, EDB: EDB, Limits: DefaultLimits}); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return &knowledge.ValidationError{Problems: []knowledge.Problem{{Field: "records.program", Message: err.Error()}}}
	}
	concepts := map[string]bool{}
	for _, d := range docs {
		if d.Record.Type == knowledge.TypeConcept {
			concepts[d.Record.Key] = true
		}
	}
	heads, err := v.Evaluator.Literals(rules, "obligation", 2, true)
	if err != nil {
		return &knowledge.ValidationError{Problems: []knowledge.Problem{{Field: "records.program", Message: err.Error()}}}
	}
	var probs []knowledge.Problem
	for _, h := range heads {
		for _, req := range termValues(h.Args[1]) {
			if !concepts[req] {
				probs = append(probs, knowledge.Problem{Field: "records[" + h.Rule + "].program",
					Message: fmt.Sprintf("requires %q, which is not a Concept record", req)})
			}
		}
	}
	if len(probs) > 0 {
		return &knowledge.ValidationError{Problems: probs}
	}
	return nil
}
