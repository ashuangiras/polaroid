package main

import (
	"encoding/json"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func fullFieldReviewForPayload(t *testing.T, changes, records any) knowledge.FieldReviewReceipt {
	t.Helper()
	var proposal knowledge.Proposal
	encoded, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	var procedureChanges []wire.KnowledgeChange
	if err := json.Unmarshal(encoded, &procedureChanges); err != nil {
		t.Fatal(err)
	}
	for _, change := range procedureChanges {
		if change.Remove {
			continue
		}
		procedure := &knowledge.Procedure{
			Key: change.Key, Goal: change.Goal, Philosophy: change.Philosophy, Method: change.Method,
			Contract: change.Contract.Clone(), Instructions: change.Instructions.Clone(),
		}
		switch change.Scope {
		case "shared":
			procedure.Applicability = knowledge.Applicability{Scope: knowledge.ScopeShared}
		case "local":
			procedure.Applicability = knowledge.Applicability{Scope: knowledge.ScopeLocal, Repository: change.Repository}
		}
		for _, ref := range change.References {
			reference := knowledge.Reference{Name: ref.Name, Key: ref.Key, Inputs: ref.Inputs.Clone()}
			if ref.Condition != "" {
				condition := ref.Condition
				reference.Condition = &condition
			}
			procedure.References = append(procedure.References, reference)
		}
		proposal.Changes = append(proposal.Changes, knowledge.Change{Key: change.Key, Procedure: procedure})
	}
	encoded, err = json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	var recordChanges []wire.KnowledgeRecordChange
	if err := json.Unmarshal(encoded, &recordChanges); err != nil {
		t.Fatal(err)
	}
	for _, change := range recordChanges {
		if change.Remove {
			continue
		}
		record, err := knowledge.ParseRecord(change.Document)
		if err != nil {
			t.Fatal(err)
		}
		proposal.Records = append(proposal.Records, knowledge.RecordChange{Key: change.Key, Record: record})
	}
	return knowledgetest.WithFullReview(proposal).Review
}
