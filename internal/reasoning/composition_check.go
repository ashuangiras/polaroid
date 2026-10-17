package reasoning

import (
	"context"
	"fmt"
	"slices"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

// AdjacentPair is one producer, consumer pair directly connected by a
// precedes edge within Parent's own composed children (ADR-0052
// restricts precedes to that sibling set; see topoSortByPrecedes).
type AdjacentPair struct{ Parent, Producer, Consumer string }

// PrecedesAdjacentPairs lists every such pair in cat — the exact
// enumeration CheckComposition checks for vacuousness (#173), reused
// unchanged by Proposer's own novelty-diff (ADR-0059), which also needs
// to compare Compose findings before and after a candidate change, not
// just plain Resolve.
func PrecedesAdjacentPairs(cat *knowledge.Catalog) []AdjacentPair {
	var pairs []AdjacentPair
	for _, parent := range cat.Records() {
		if parent.Type != knowledge.TypeProcedure || len(parent.Composes) == 0 {
			continue
		}
		siblings := make(map[string]bool, len(parent.Composes))
		for _, k := range parent.Composes {
			siblings[k] = true
		}
		producers := slices.Clone(parent.Composes)
		slices.Sort(producers)
		for _, producer := range producers {
			rec, err := cat.Record(producer)
			if err != nil {
				// An unresolvable composed child is a reference-graph
				// problem (ADR-0009), not this enumeration's concern.
				continue
			}
			consumers := slices.Clone(rec.Precedes)
			slices.Sort(consumers)
			for _, consumer := range consumers {
				if !siblings[consumer] {
					// precedes is restricted to the sibling set (ADR-0052):
					// a target outside parent's own children is not a
					// handoff this parent's composition claims at all.
					continue
				}
				pairs = append(pairs, AdjacentPair{Parent: parent.Key, Producer: producer, Consumer: consumer})
			}
		}
	}
	return pairs
}

// CheckComposition finds every vacuous pair PrecedesAdjacentPairs lists:
// one whose Compose result establishes or contradicts none of the
// consumer's preconditions (ADR-0059 Part 1, #173). A pair with no
// precedes edge between them is never checked: nothing claims they run
// in that order, so nothing can be vacuous between them.
//
// This never requires an Exception, a conditional obligation, or an actual
// contradiction to exist: one established finding alone is enough. A
// Procedure with no composed children, or whose children have no precedes
// edges between them, has nothing to check and is never reported.
//
// Deliberately not yet wired into Kernel.publish, following ADR-0057's own
// two-phase precedent (#158, then #164): activating this against the live
// corpus today would refuse the next publish of anything, however small
// and unrelated, until #176's authoring pass closes the backlog this
// reveals. Wiring it in will need a new knowledge.CompositionChecker
// interface and Kernel.Option, the same shape knowledge.RecordValidator and
// reasoning.RuleValidator already use to cross this same boundary
// (internal/knowledge may not import internal/reasoning,
// internal/archtest's TestPackageBoundaries) — left to that later,
// separate activation change.
func CheckComposition(ctx context.Context, cat *knowledge.Catalog, ev Evaluator, limits Limits) ([]knowledge.Problem, error) {
	var problems []knowledge.Problem
	for _, pair := range PrecedesAdjacentPairs(cat) {
		res, err := Compose(ctx, cat, ev, pair.Producer, pair.Consumer, limits)
		if err != nil {
			return nil, fmt.Errorf("composition %s -> %s: %w", pair.Producer, pair.Consumer, err)
		}
		if slices.ContainsFunc(res.Findings, func(f Finding) bool { return f.Status == "established" || f.Status == "contradicted" }) {
			continue
		}
		problems = append(problems, knowledge.Problem{
			Field: fmt.Sprintf("procedures[%s].composes[%s->%s]", pair.Parent, pair.Producer, pair.Consumer),
			Message: fmt.Sprintf(
				"is vacuous: %s's own postconditions, outputs and invariants establish or contradict none of %s's preconditions", pair.Producer, pair.Consumer),
		})
	}
	return problems, nil
}
