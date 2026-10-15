package knowledge_test

import (
	"context"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

func TestCompletenessGapsEmpty(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore())
	cat, err := k.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := cat.CompletenessGaps()
	if len(g.MissingDefinition) != 0 || len(g.MissingObligation) != 0 {
		t.Fatalf("gaps of an empty catalog = %+v, want none", g)
	}
	if len(cat.CheckCompleteness()) != 0 {
		t.Fatalf("CheckCompleteness of an empty catalog found problems")
	}
}

func TestCompletenessGaps(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(&acceptAll{}))

	// Complete: a Domain and a Concept with definitions, and a Procedure
	// with both a definition and its own Requires.
	domain := &knowledge.Record{Type: knowledge.TypeDomain, Key: "domain.complete", Label: "Complete", Definition: "A domain with a definition."}
	concept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "concept.complete", Label: "Complete", Definition: "A concept with a definition."}
	completeProc := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "proc.complete", Label: "Complete", Definition: "A complete procedure.", Requires: []string{"concept.complete"}}

	// Gaps: a Domain and a Concept with no definition.
	gapDomain := &knowledge.Record{Type: knowledge.TypeDomain, Key: "domain.gap", Label: "Gap"}
	gapConcept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "concept.gap", Label: "Gap"}

	// A Procedure with a definition but no obligation source of its own,
	// and no specializes parent: a gap.
	noObligation := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "proc.no-obligation", Label: "No obligation", Definition: "Has a definition, nothing else."}

	// A Procedure that inherits its obligation via specializes: not a gap,
	// even though it has no Requires/Preconditions/Postconditions itself.
	inherits := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "proc.inherits", Label: "Inherits", Definition: "Inherits its obligation.",
		Specializes: []string{"proc.complete"}}

	// A Procedure with a Postconditions-only obligation: not a gap.
	postOnly := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "proc.post-only", Label: "Post only", Definition: "Has a postcondition.",
		Postconditions: []string{"concept.complete"}}

	all := []*knowledge.Record{domain, concept, completeProc, gapDomain, gapConcept, noObligation, inherits, postOnly}
	var changes []knowledge.RecordChange
	for _, r := range all {
		changes = append(changes, knowledge.RecordChange{Key: r.Key, Record: r})
	}
	// Every typed Procedure record needs a matching legacy procedure
	// document, sharing its key (checkRecords): minimal shared stubs.
	var procs []knowledge.Change
	for _, key := range []string{"proc.complete", "proc.no-obligation", "proc.inherits", "proc.post-only"} {
		procs = append(procs, put(knowledgetest.SharedProc(key, key)))
	}
	s, err := k.Publish(context.Background(), knowledge.Proposal{Changes: procs, Records: changes, Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := k.At(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}

	g := cat.CompletenessGaps()
	wantMissingDefinition := []string{"concept.gap", "domain.gap"}
	if !equalStrings(g.MissingDefinition, wantMissingDefinition) {
		t.Errorf("MissingDefinition = %v, want %v", g.MissingDefinition, wantMissingDefinition)
	}
	wantMissingObligation := []string{"proc.no-obligation"}
	if !equalStrings(g.MissingObligation, wantMissingObligation) {
		t.Errorf("MissingObligation = %v, want %v", g.MissingObligation, wantMissingObligation)
	}

	problems := cat.CheckCompleteness()
	if len(problems) != len(wantMissingDefinition)+len(wantMissingObligation) {
		t.Fatalf("CheckCompleteness = %+v, want %d problems", problems, len(wantMissingDefinition)+len(wantMissingObligation))
	}
}

// TestCompletenessIsNotEnforcedYet is ADR-0057's acceptance criterion 1:
// the rule is written and tested, but nothing in the publish path calls
// it, so a record missing a definition still publishes successfully.
func TestCompletenessIsNotEnforcedYet(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(&acceptAll{}))
	incomplete := &knowledge.Record{Type: knowledge.TypeDomain, Key: "domain.undefined", Label: "Undefined"}
	s, err := k.Publish(context.Background(), knowledge.Proposal{
		Records: []knowledge.RecordChange{{Key: incomplete.Key, Record: incomplete}}, Provenance: prov(),
	})
	if err != nil {
		t.Fatalf("a record missing definition must still publish today (ADR-0057 is not yet activated): %v", err)
	}
	cat, err := k.At(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.CompletenessGaps().MissingDefinition) != 1 {
		t.Fatalf("the published gap must still be detectable by CompletenessGaps")
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
