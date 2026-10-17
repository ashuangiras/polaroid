package knowledgeops_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// proposerFixture publishes a small base corpus: comp.parent composes
// comp.producer (precedes comp.consumer), comp.consumer has a real
// precondition nothing yet establishes (the same vacuous shape
// #173's own composition_check_test.go uses) -- for exercising the
// non-vacuous composition gate and the novelty-diff from propose's own
// side. Returns a Proposer over it, plus the Kernel itself to read back
// committed results.
func proposerFixture(t *testing.T) (*knowledgeops.Proposer, *knowledge.Kernel) {
	t.Helper()
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: datalog.Evaluator{}}))
	obligation := &knowledge.Record{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"}
	precondition := &knowledge.Record{Type: knowledge.TypeConcept, Key: "comp.some-precondition", Label: "x", Definition: "d"}
	unreferenced := &knowledge.Record{Type: knowledge.TypeConcept, Key: "comp.unreferenced", Label: "u", Definition: "d"}
	standalone := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.standalone", Label: "s", Definition: "d", Requires: []string{"test.obligation"}}
	producer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.producer", Label: "p", Definition: "d",
		Precedes: []string{"comp.consumer"}, Requires: []string{"test.obligation"}}
	consumer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.consumer", Label: "c", Definition: "d",
		Preconditions: []string{"comp.some-precondition"}, Requires: []string{"test.obligation"}}
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "comp.parent", Label: "parent", Definition: "d",
		Composes: []string{"comp.producer", "comp.consumer"}, Requires: []string{"test.obligation"}}
	changes := []knowledge.Change{
		{Key: "comp.producer", Procedure: knowledgetest.SharedProc("comp.producer", "comp.producer")},
		{Key: "comp.consumer", Procedure: knowledgetest.SharedProc("comp.consumer", "comp.consumer")},
		{Key: "comp.parent", Procedure: knowledgetest.SharedProc("comp.parent", "comp.parent")},
		{Key: "comp.standalone", Procedure: knowledgetest.SharedProc("comp.standalone", "comp.standalone")},
	}
	records := []knowledge.RecordChange{
		{Key: obligation.Key, Record: obligation}, {Key: precondition.Key, Record: precondition}, {Key: unreferenced.Key, Record: unreferenced},
		{Key: producer.Key, Record: producer}, {Key: consumer.Key, Record: consumer}, {Key: parent.Key, Record: parent}, {Key: standalone.Key, Record: standalone},
	}
	if _, err := k.Publish(context.Background(), knowledge.Proposal{Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "s", Rationale: "r"}}); err != nil {
		t.Fatal(err)
	}
	return knowledgeops.NewProposer(k, datalog.Evaluator{}, policy.Trust{}), k
}

func activeRecord(t *testing.T, k *knowledge.Kernel, key string) *knowledge.Record {
	t.Helper()
	cat, err := k.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r, err := cat.Record(key)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestProposeMissingCitationRejected(t *testing.T) {
	p, _ := proposerFixture(t)
	_, err := p.Propose(context.Background(), wire.ProposeRequest{Proposer: "test.agent", Rationale: "r",
		ExistingProcedure: &wire.ProposeFieldAddition{Key: "comp.producer", Postconditions: []string{"comp.some-precondition"}}})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" || !strings.Contains(err.Error(), "citations") {
		t.Fatalf("err = %v, want invalid_request naming citations", err)
	}
}

func TestProposeQuoteNotFoundRejected(t *testing.T) {
	p, _ := proposerFixture(t)
	_, err := p.Propose(context.Background(), wire.ProposeRequest{Proposer: "test.agent", Rationale: "r",
		ExistingProcedure: &wire.ProposeFieldAddition{Key: "comp.producer", Postconditions: []string{"comp.some-precondition"},
			Citations: []wire.ProposeCitation{{Source: "test.md", Quote: "this text is not in the new content"}}}})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" || !strings.Contains(err.Error(), "verbatim") {
		t.Fatalf("err = %v, want invalid_request naming the quote mismatch", err)
	}
}

// TestProposeVacuousCompositionRejected proves #173's gate is delegated:
// a postcondition that does not match comp.consumer's own precondition
// leaves the pair just as vacuous, and is refused -- even though it is a
// well-formed, citation-grounded, genuinely novel addition.
func TestProposeVacuousCompositionRejected(t *testing.T) {
	p, _ := proposerFixture(t)
	_, err := p.Propose(context.Background(), wire.ProposeRequest{Proposer: "test.agent", Rationale: "r",
		ExistingProcedure: &wire.ProposeFieldAddition{Key: "comp.producer", Postconditions: []string{"comp.unreferenced"},
			Citations: []wire.ProposeCitation{{Source: "test.md", Quote: "comp.unreferenced"}}}})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" || !strings.Contains(err.Error(), "vacuous") {
		t.Fatalf("err = %v, want invalid_request naming the still-vacuous composition", err)
	}
}

// TestProposeValidAdditionCommitsImmediately proves AC4: a valid,
// citation-grounded, novel, non-vacuous candidate commits immediately,
// with no separate approval step, and closes the exact pair #173 would
// otherwise have flagged.
func TestProposeValidAdditionCommitsImmediately(t *testing.T) {
	p, k := proposerFixture(t)
	res, err := p.Propose(context.Background(), wire.ProposeRequest{Proposer: "test.agent", Rationale: "mirrors an existing fact",
		ExistingProcedure: &wire.ProposeFieldAddition{Key: "comp.producer", Postconditions: []string{"comp.some-precondition"},
			Citations: []wire.ProposeCitation{{Source: "test.md", Quote: "comp.some-precondition"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "comp.producer" || res.Snapshot == "" {
		t.Fatalf("result = %+v", res)
	}
	updated := activeRecord(t, k, "comp.producer")
	if len(updated.Postconditions) != 1 || updated.Postconditions[0] != "comp.some-precondition" {
		t.Fatalf("committed record = %+v, want the new postcondition", updated)
	}
	cat, err := k.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	problems, err := reasoning.CheckComposition(context.Background(), cat, datalog.Evaluator{}, reasoning.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, prob := range problems {
		if strings.Contains(prob.Field, "comp.producer->comp.consumer") {
			t.Fatalf("the pair is still reported vacuous after a successful propose: %+v", prob)
		}
	}
}

// TestProposeRedundantRejected proves AC3: a genuinely new (not a
// duplicate -- Kernel.Validate's own "is unchanged" check already catches
// a literal duplicate before this is ever reached) postcondition on a
// standalone procedure (no composed pair to even check) that nonetheless
// changes no resolution anywhere is refused as redundant.
func TestProposeRedundantRejected(t *testing.T) {
	p, _ := proposerFixture(t)
	_, err := p.Propose(context.Background(), wire.ProposeRequest{Proposer: "test.agent", Rationale: "r",
		ExistingProcedure: &wire.ProposeFieldAddition{Key: "comp.standalone", Postconditions: []string{"comp.unreferenced"},
			Citations: []wire.ProposeCitation{{Source: "test.md", Quote: "comp.unreferenced"}}}})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" || !strings.Contains(err.Error(), "redundant") {
		t.Fatalf("err = %v, want invalid_request naming it redundant", err)
	}
}

// TestProposeDefinitionOnlyChangeSkipsNoveltyCheck proves the documented
// carve-out: Definition never appears in any Resolution/CompositionResult
// field, so requiring it to be "novel" there would refuse every single
// one; completeness and the citation check already govern it fully.
func TestProposeDefinitionOnlyChangeSkipsNoveltyCheck(t *testing.T) {
	p, k := proposerFixture(t)
	res, err := p.Propose(context.Background(), wire.ProposeRequest{Proposer: "test.agent", Rationale: "mirrors existing prose",
		ExistingProcedure: &wire.ProposeFieldAddition{Key: "comp.producer", Definition: "a better definition",
			Citations: []wire.ProposeCitation{{Source: "test.md", Quote: "a better definition"}}}})
	if err != nil {
		t.Fatalf("definition-only change must not be refused as redundant: %v", err)
	}
	if updated := activeRecord(t, k, res.Key); updated.Definition != "a better definition" {
		t.Fatalf("committed definition = %q", updated.Definition)
	}
}

func TestProposeNewConceptCitationGrounded(t *testing.T) {
	p, k := proposerFixture(t)
	res, err := p.Propose(context.Background(), wire.ProposeRequest{Proposer: "test.agent", Rationale: "r",
		NewConcept: &wire.ProposeNewRecord{Key: "test.new-concept", Label: "new", Definition: "a genuinely new concept",
			Citations: []wire.ProposeCitation{{Source: "test.md", Quote: "a genuinely new concept"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := activeRecord(t, k, res.Key); got.Type != knowledge.TypeConcept || got.Definition != "a genuinely new concept" {
		t.Fatalf("committed = %+v", got)
	}
}

func TestProposeNewConceptExistingKeyRejected(t *testing.T) {
	p, _ := proposerFixture(t)
	_, err := p.Propose(context.Background(), wire.ProposeRequest{Proposer: "test.agent", Rationale: "r",
		NewConcept: &wire.ProposeNewRecord{Key: "test.obligation", Label: "x", Definition: "d",
			Citations: []wire.ProposeCitation{{Source: "test.md", Quote: "d"}}}})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want invalid_request naming the key collision", err)
	}
}

// TestProposeOrchestrationNeedsNoCitation proves AC5: Tier 1 is accepted
// without any citation or evidence requirement.
func TestProposeOrchestrationNeedsNoCitation(t *testing.T) {
	p, k := proposerFixture(t)
	res, err := p.Propose(context.Background(), wire.ProposeRequest{Proposer: "test.agent", Rationale: "r",
		NewOrchestration: &wire.ProposeOrchestration{Key: "test.orchestration", Label: "o", Definition: "composes producer then consumer",
			Composes: []string{"comp.producer", "comp.consumer"}, Requires: []string{"test.obligation"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := activeRecord(t, k, res.Key)
	if got.Type != knowledge.TypeProcedure || len(got.Composes) != 2 {
		t.Fatalf("committed = %+v", got)
	}
}

// TestProposeOrchestrationUnknownComposeKeyRejected proves AC6: a
// reference to an unknown key is rejected like any other invalid
// reference (ADR-0009).
func TestProposeOrchestrationUnknownComposeKeyRejected(t *testing.T) {
	p, _ := proposerFixture(t)
	_, err := p.Propose(context.Background(), wire.ProposeRequest{Proposer: "test.agent", Rationale: "r",
		NewOrchestration: &wire.ProposeOrchestration{Key: "test.orchestration-bad", Label: "o", Definition: "d",
			Composes: []string{"comp.producer", "nope.does-not-exist"}}})
	var we *wire.KnowledgeError
	if !errors.As(err, &we) || we.Detail.Code != "invalid_request" {
		t.Fatalf("err = %v, want invalid_request", err)
	}
}
