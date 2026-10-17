package knowledgeops_test

import (
	"context"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
)

func draftKernelFixture(t *testing.T) *knowledge.Kernel {
	t.Helper()
	kernel := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: datalog.Evaluator{}}))
	obligation := &knowledge.Record{Type: knowledge.TypeConcept, Key: "test.obligation", Label: "done", Definition: "d"}
	precondition := &knowledge.Record{Type: knowledge.TypeConcept, Key: "draft.some-precondition", Label: "x", Definition: "d",
		Broader: []string{"test.obligation"}}
	standalone := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "draft.standalone", Label: "s", Definition: "d", Requires: []string{"test.obligation"}}
	producer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "draft.producer", Label: "p", Definition: "d",
		Precedes: []string{"draft.consumer"}, Requires: []string{"test.obligation"}}
	consumer := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "draft.consumer", Label: "c", Definition: "d",
		Preconditions: []string{"draft.some-precondition"}, Requires: []string{"test.obligation"}}
	parent := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "draft.parent", Label: "parent", Definition: "d",
		Composes: []string{"draft.producer", "draft.consumer"}, Requires: []string{"test.obligation"}}
	changes := []knowledge.Change{
		{Key: "draft.standalone", Procedure: knowledgetest.SharedProc("draft.standalone", "draft.standalone")},
		{Key: "draft.producer", Procedure: knowledgetest.SharedProc("draft.producer", "draft.producer")},
		{Key: "draft.consumer", Procedure: knowledgetest.SharedProc("draft.consumer", "draft.consumer")},
		{Key: "draft.parent", Procedure: knowledgetest.SharedProc("draft.parent", "draft.parent")},
	}
	records := []knowledge.RecordChange{
		{Key: obligation.Key, Record: obligation}, {Key: precondition.Key, Record: precondition},
		{Key: standalone.Key, Record: standalone}, {Key: producer.Key, Record: producer},
		{Key: consumer.Key, Record: consumer}, {Key: parent.Key, Record: parent},
	}
	if _, err := kernel.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{
		Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "seed draft fixture", Rationale: "test base"},
	})); err != nil {
		t.Fatal(err)
	}
	return kernel
}

func proposerFixture(t *testing.T) (*knowledgeops.Proposer, *knowledge.Kernel) {
	t.Helper()
	kernel := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: datalog.Evaluator{}}))
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
		{Key: obligation.Key, Record: obligation}, {Key: precondition.Key, Record: precondition},
		{Key: unreferenced.Key, Record: unreferenced}, {Key: producer.Key, Record: producer},
		{Key: consumer.Key, Record: consumer}, {Key: parent.Key, Record: parent},
		{Key: standalone.Key, Record: standalone},
	}
	if _, err := kernel.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{
		Changes: changes, Records: records,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "seed propose fixture", Rationale: "test base"},
	})); err != nil {
		t.Fatal(err)
	}
	return knowledgeops.NewProposer(kernel, datalog.Evaluator{}, policy.Trust{}), kernel
}
