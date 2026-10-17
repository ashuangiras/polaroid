package reasoning_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/reasoning"
)

// publishProcedures publishes records, each also a matching legacy
// procedure document (a typed Procedure record describes a published
// procedure, knowledge.checkRecord): the same pairing
// internal/knowledge/precedes_test.go's own fixture uses.
func publishProcedures(t *testing.T, w *world, records ...*knowledge.Record) {
	t.Helper()
	var changes []knowledge.Change
	var recordChanges []knowledge.RecordChange
	for _, r := range records {
		if r.Type == knowledge.TypeProcedure {
			changes = append(changes, knowledge.Change{Key: r.Key, Procedure: knowledgetest.SharedProc(r.Key, r.Label)})
		}
		recordChanges = append(recordChanges, knowledge.RecordChange{Key: r.Key, Record: r})
	}
	s, err := w.kernel.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{Base: w.snap, Changes: changes, Records: recordChanges,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "composition fixture", Rationale: "test"}}))
	if err != nil {
		t.Fatal(err)
	}
	w.snap = s
}

// checkComposition runs reasoning.CheckComposition against w's current
// snapshot.
func (w *world) checkComposition(t *testing.T) []knowledge.Problem {
	t.Helper()
	cat, err := w.kernel.At(context.Background(), w.snap)
	if err != nil {
		t.Fatal(err)
	}
	problems, err := reasoning.CheckComposition(context.Background(), cat, ev, reasoning.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return problems
}

func hasProblem(problems []knowledge.Problem, field string) bool {
	return slices.ContainsFunc(problems, func(p knowledge.Problem) bool { return p.Field == field })
}

// TestCheckComposition_VacuousPairReported proves a precedes-ordered pair
// whose producer declares no postcondition, output or invariant at all is
// reported vacuous: exactly the live corpus's own shape found while
// designing ADR-0059 (every Compose finding undetermined).
func TestCheckComposition_VacuousPairReported(t *testing.T) {
	w := newWorld(t)
	producer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.vacuous-producer", Label: "p", Definition: "d",
		Precedes: []string{"comp.vacuous-consumer"}, Requires: []string{"check.vet"}}
	consumer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.vacuous-consumer", Label: "c", Definition: "d",
		Preconditions: []string{"comp.some-precondition"}}
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.vacuous-parent", Label: "parent", Definition: "d",
		Composes: []string{"comp.vacuous-producer", "comp.vacuous-consumer"}, Requires: []string{"check.vet"}}
	concept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "comp.some-precondition", Label: "x", Definition: "d"}
	publishProcedures(t, w, producer, consumer, parent, concept)

	problems := w.checkComposition(t)
	field := "procedures[comp.vacuous-parent].composes[comp.vacuous-producer->comp.vacuous-consumer]"
	if !hasProblem(problems, field) {
		t.Fatalf("vacuous pair not reported, got %+v", problems)
	}
	for _, p := range problems {
		if p.Field == field && strings.Contains(strings.ToLower(p.Message), "exception") {
			t.Fatalf("message must never require an Exception (AC4): %q", p.Message)
		}
	}
}

// TestCheckComposition_EstablishedPairAccepted proves a producer that
// formally establishes the consumer's precondition is never reported, with
// no Exception or Rule involved anywhere (AC4).
func TestCheckComposition_EstablishedPairAccepted(t *testing.T) {
	w := newWorld(t)
	concept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "comp.state-ready", Label: "ready", Definition: "d"}
	producer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.good-producer", Label: "p", Definition: "d",
		Precedes: []string{"comp.good-consumer"}, Postconditions: []string{"comp.state-ready"}}
	consumer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.good-consumer", Label: "c", Definition: "d",
		Preconditions: []string{"comp.state-ready"}}
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.good-parent", Label: "parent", Definition: "d",
		Composes: []string{"comp.good-producer", "comp.good-consumer"}, Requires: []string{"check.vet"}}
	publishProcedures(t, w, concept, producer, consumer, parent)

	problems := w.checkComposition(t)
	if hasProblem(problems, "procedures[comp.good-parent].composes[comp.good-producer->comp.good-consumer]") {
		t.Fatalf("established pair must not be reported, got %+v", problems)
	}
}

// TestCheckComposition_ContradictedPairAccepted proves a formally
// contradicted precondition also counts as non-vacuous: this rule never
// requires an established finding specifically, only that something
// formal decided it, established or contradicted, one way or the other.
func TestCheckComposition_ContradictedPairAccepted(t *testing.T) {
	w := newWorld(t)
	a := &knowledge.Record{Type: knowledge.TypeConcept, Key: "comp.state-a", Label: "a", Definition: "d", Contradicts: []string{"comp.state-b"}}
	b := &knowledge.Record{Type: knowledge.TypeConcept, Key: "comp.state-b", Label: "b", Definition: "d"}
	producer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.contra-producer", Label: "p", Definition: "d",
		Precedes: []string{"comp.contra-consumer"}, Postconditions: []string{"comp.state-a"}}
	consumer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.contra-consumer", Label: "c", Definition: "d",
		Preconditions: []string{"comp.state-b"}}
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.contra-parent", Label: "parent", Definition: "d",
		Composes: []string{"comp.contra-producer", "comp.contra-consumer"}, Requires: []string{"check.vet"}}
	publishProcedures(t, w, a, b, producer, consumer, parent)

	problems := w.checkComposition(t)
	if hasProblem(problems, "procedures[comp.contra-parent].composes[comp.contra-producer->comp.contra-consumer]") {
		t.Fatalf("contradicted pair must not be reported vacuous, got %+v", problems)
	}
}

// TestCheckComposition_ParallelSiblingsSkipped proves two composed children
// with no precedes edge between them are never checked: nothing claims
// either runs before the other, so nothing can be vacuous between them.
// This is why the check follows direct precedes edges, never "adjacent in
// the deterministic tie-broken order" (reasoning.Plan's own ordering, which
// can place unrelated siblings next to each other).
func TestCheckComposition_ParallelSiblingsSkipped(t *testing.T) {
	w := newWorld(t)
	a := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.parallel-a", Label: "a", Definition: "d", Requires: []string{"check.vet"}}
	b := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.parallel-b", Label: "b", Definition: "d", Requires: []string{"check.vet"}}
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.parallel-parent", Label: "parent", Definition: "d",
		Composes: []string{"comp.parallel-a", "comp.parallel-b"}, Requires: []string{"check.vet"}}
	publishProcedures(t, w, a, b, parent)

	problems := w.checkComposition(t)
	for _, p := range problems {
		if strings.Contains(p.Field, "comp.parallel-parent") {
			t.Fatalf("parallel siblings with no precedes edge must never be checked, got %+v", p)
		}
	}
}

// TestCheckComposition_NotYetEnforced mirrors ADR-0057's own two-phase
// regression test (TestCompletenessIsNotEnforcedYet): CheckComposition
// exists and correctly detects a vacuous pair when called directly, but
// Kernel.Publish does not call it, so a publish introducing one succeeds
// today (#173 AC6; activation is a separate, later change, #176).
func TestCheckComposition_NotYetEnforced(t *testing.T) {
	w := newWorld(t)
	producer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.unenforced-producer", Label: "p", Definition: "d",
		Precedes: []string{"comp.unenforced-consumer"}, Requires: []string{"check.vet"}}
	consumer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.unenforced-consumer", Label: "c", Definition: "d",
		Preconditions: []string{"comp.unenforced-precondition"}}
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.unenforced-parent", Label: "parent", Definition: "d",
		Composes: []string{"comp.unenforced-producer", "comp.unenforced-consumer"}, Requires: []string{"check.vet"}}
	concept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "comp.unenforced-precondition", Label: "x", Definition: "d"}
	publishProcedures(t, w, producer, consumer, parent, concept) // must succeed: not yet wired into Kernel.publish

	problems := w.checkComposition(t)
	if !hasProblem(problems, "procedures[comp.unenforced-parent].composes[comp.unenforced-producer->comp.unenforced-consumer]") {
		t.Fatalf("CheckComposition itself must still detect the vacuous pair it was not asked to enforce, got %+v", problems)
	}
}
