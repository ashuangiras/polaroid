package knowledgeops_test

import (
	"context"
	"encoding/json/jsontext"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledgeops"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

func fieldDrafterFixture(t *testing.T) (*knowledgeops.FieldDrafter, *knowledge.Kernel) {
	t.Helper()
	kernel := draftKernelFixture(t)
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/field-drafts.db")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })
	proposer := knowledgeops.NewProposer(kernel, datalog.Evaluator{}, policy.Trust{})
	return knowledgeops.NewFieldDrafter(kernel, proposer, memory.NewFieldDrafts(store)), kernel
}

func approveReviewedDraft(t *testing.T, d *knowledgeops.FieldDrafter, ready wire.FieldDraftResult) wire.FieldDraftResult {
	t.Helper()
	if ready.Status != "ready" || ready.CandidateHash == "" {
		t.Fatalf("reviewed draft = %+v, want ready candidate", ready)
	}
	result, err := d.PublishApproved(context.Background(), wire.FieldDraftPublishRequest{Token: ready.Token, CandidateHash: ready.CandidateHash})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFieldDrafterApprovalCannotApplyToChangedCandidate(t *testing.T) {
	drafter, kernel := fieldDrafterFixture(t)
	ctx := context.Background()
	start, err := drafter.StartV8(ctx, wire.FieldDraftStartRequestV8{
		Key: "draft.some-precondition", RecordType: "Concept", Proposer: "untrusted.agent", Rationale: "review changes",
	})
	if err != nil {
		t.Fatal(err)
	}
	var ready wire.FieldDraftResult
	for _, field := range start.Fields {
		outcome := &wire.FieldDraftOutcomeArg{Entity: field.Entity, Field: field.Field, Operation: "keep"}
		if field.Field == "definition" {
			outcome.Operation, outcome.Value = "replace", "first correction"
			outcome.Citations = []wire.ProposeCitation{{Source: "test", Quote: "first correction"}}
		}
		ready, err = drafter.Advance(ctx, wire.FieldDraftAdvanceRequest{Token: start.Token, Outcome: outcome})
		if err != nil {
			t.Fatal(err)
		}
	}
	firstHash := ready.CandidateHash
	changed, err := drafter.Advance(ctx, wire.FieldDraftAdvanceRequest{Token: start.Token, Outcome: &wire.FieldDraftOutcomeArg{
		Entity: "record", Field: "definition", Operation: "replace", Value: "second correction",
		Citations: []wire.ProposeCitation{{Source: "test", Quote: "second correction"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if changed.CandidateHash == firstHash {
		t.Fatal("modified draft kept the same candidate hash")
	}
	if _, err := drafter.PublishApproved(ctx, wire.FieldDraftPublishRequest{Token: start.Token, CandidateHash: firstHash}); err == nil {
		t.Fatal("approval for a previous candidate was accepted")
	}
	active, err := kernel.Active(ctx)
	if err != nil || active.Snapshot() != knowledge.Snapshot(start.Snapshot) {
		t.Fatalf("failed approval changed active snapshot: %v", err)
	}
	approveReviewedDraft(t, drafter, changed)
}

func TestFieldDrafterReturnsAndReviewsFullProcedureState(t *testing.T) {
	drafter, kernel := fieldDrafterFixture(t)
	ctx := context.Background()
	start, err := drafter.Start(ctx, wire.FieldDraftStartRequest{
		Key: "draft.producer", RecordType: string(knowledge.TypeProcedure),
		Proposer: "test.agent", Rationale: "review every field before changing the composition",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCount := len(knowledge.ProcedureDocumentFieldSpecs()) + len(knowledge.RecordFieldSpecs(knowledge.TypeProcedure))
	if len(start.Fields) != wantCount {
		t.Fatalf("field inventory has %d entries, want %d", len(start.Fields), wantCount)
	}
	if start.NextEntity != "procedure" || start.NextField != "goal" {
		t.Fatalf("next field = %s.%s, want procedure.goal", start.NextEntity, start.NextField)
	}
	var sawCurrentValue bool
	for _, field := range start.Fields {
		if field.Entity == "record" && field.Field == "label" && field.Present && string(field.Value) == `"p"` {
			sawCurrentValue = true
		}
	}
	if !sawCurrentValue {
		t.Fatalf("full field inventory did not include the existing record label: %+v", start.Fields)
	}

	var result wire.FieldDraftResult
	for _, field := range start.Fields {
		outcome := &wire.FieldDraftOutcomeArg{Entity: field.Entity, Field: field.Field, Operation: "keep"}
		if field.Entity == "record" && field.Field == "postconditions" {
			outcome.Operation = "add"
			outcome.Value = jsontext.Value(`["draft.some-precondition"]`)
			outcome.Citations = []wire.ProposeCitation{{Source: "draft.consumer", Quote: "draft.some-precondition"}}
		}
		result, err = drafter.Advance(ctx, wire.FieldDraftAdvanceRequest{Token: start.Token, Outcome: outcome})
		if err != nil {
			t.Fatalf("answer %s.%s: %v", field.Entity, field.Field, err)
		}
	}
	result = approveReviewedDraft(t, drafter, result)
	if result.Status != "complete" || result.Key != "draft.producer" || result.Snapshot == "" {
		t.Fatalf("final result = %+v, want committed proposal", result)
	}
	cat, err := kernel.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, err := cat.Record("draft.producer")
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Postconditions) != 1 || record.Postconditions[0] != "draft.some-precondition" {
		t.Fatalf("published postconditions = %v", record.Postconditions)
	}
}

func TestFieldDrafterCreatesTypedRecordOnlyAfterEveryFieldIsReviewed(t *testing.T) {
	drafter, kernel := fieldDrafterFixture(t)
	ctx := context.Background()
	start, err := drafter.Start(ctx, wire.FieldDraftStartRequest{
		Key: "draft.new-concept", RecordType: string(knowledge.TypeConcept), New: true,
		Proposer: "test.agent", Rationale: "add a grounded concept",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(start.Fields) != len(knowledge.RecordFieldSpecs(knowledge.TypeConcept)) {
		t.Fatalf("new Concept inventory = %d fields, want %d", len(start.Fields), len(knowledge.RecordFieldSpecs(knowledge.TypeConcept)))
	}
	var result wire.FieldDraftResult
	for _, field := range start.Fields {
		if field.Entity != "record" || !field.NewEntity || field.Present {
			t.Fatalf("new Concept field should identify a new record with no published value: %+v", field)
		}
		outcome := &wire.FieldDraftOutcomeArg{Entity: field.Entity, Field: field.Field}
		switch field.Field {
		case "label":
			outcome.Operation, outcome.Value = "replace", "grounded concept"
			outcome.Citations = []wire.ProposeCitation{{Source: "test.md", Quote: "grounded concept"}}
		case "definition":
			outcome.Operation, outcome.Value = "replace", "A concept grounded in source text."
			outcome.Citations = []wire.ProposeCitation{{Source: "test.md", Quote: "grounded in source text"}}
		default:
			outcome.Operation = "clear"
		}
		result, err = drafter.Advance(ctx, wire.FieldDraftAdvanceRequest{Token: start.Token, Outcome: outcome})
		if err != nil {
			t.Fatalf("answer %s: %v", field.Field, err)
		}
	}
	result = approveReviewedDraft(t, drafter, result)
	if result.Status != "complete" || result.Key != "draft.new-concept" {
		t.Fatalf("final result = %+v, want a published Concept", result)
	}
	cat, err := kernel.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, err := cat.Record("draft.new-concept")
	if err != nil {
		t.Fatal(err)
	}
	if record.Type != knowledge.TypeConcept || record.Label != "grounded concept" || record.Definition != "A concept grounded in source text." {
		t.Fatalf("published record = %+v", record)
	}
}

func TestFieldDrafterCreatesProcedureWithBothReviewedLayers(t *testing.T) {
	drafter, kernel := fieldDrafterFixture(t)
	ctx := context.Background()
	start, err := drafter.Start(ctx, wire.FieldDraftStartRequest{
		Key: "draft.new-procedure", RecordType: string(knowledge.TypeProcedure), New: true,
		Proposer: "test.agent", Rationale: "publish complete guidance and typed semantics",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(start.Fields) != len(knowledge.ProcedureDocumentFieldSpecs())+len(knowledge.RecordFieldSpecs(knowledge.TypeProcedure)) {
		t.Fatalf("new Procedure field inventory = %d", len(start.Fields))
	}
	var result wire.FieldDraftResult
	for _, field := range start.Fields {
		outcome := &wire.FieldDraftOutcomeArg{Entity: field.Entity, Field: field.Field}
		switch field.Entity + "/" + field.Field {
		case "procedure/goal":
			outcome.Operation, outcome.Value = "replace", "Verify a new Go module."
			outcome.Citations = []wire.ProposeCitation{{Source: "test.md", Quote: "Verify a new Go module."}}
		case "procedure/philosophy":
			outcome.Operation, outcome.Value = "replace", "Run the declared checks."
			outcome.Citations = []wire.ProposeCitation{{Source: "test.md", Quote: "Run the declared checks."}}
		case "procedure/method":
			outcome.Operation, outcome.Value = "replace", "Check, then verify."
			outcome.Citations = []wire.ProposeCitation{{Source: "test.md", Quote: "Check, then verify."}}
		case "procedure/contract":
			outcome.Operation, outcome.Value = "replace", jsontext.Value(`{}`)
		case "procedure/instructions":
			outcome.Operation, outcome.Value = "replace", jsontext.Value(`{"steps":["Run tests."]}`)
			outcome.Citations = []wire.ProposeCitation{{Source: "test.md", Quote: "Run tests."}}
		case "record/label":
			outcome.Operation, outcome.Value = "replace", "new Go verification"
			outcome.Citations = []wire.ProposeCitation{{Source: "test.md", Quote: "new Go verification"}}
		case "record/definition":
			outcome.Operation, outcome.Value = "replace", "A procedure for verifying a Go module."
			outcome.Citations = []wire.ProposeCitation{{Source: "test.md", Quote: "verifying a Go module"}}
		case "record/requires":
			outcome.Operation, outcome.Value = "add", []string{"test.obligation"}
		default:
			outcome.Operation = "clear"
		}
		result, err = drafter.Advance(ctx, wire.FieldDraftAdvanceRequest{Token: start.Token, Outcome: outcome})
		if err != nil {
			t.Fatalf("answer %s.%s: %v", field.Entity, field.Field, err)
		}
	}
	result = approveReviewedDraft(t, drafter, result)
	if result.Status != "complete" || result.Key != "draft.new-procedure" {
		t.Fatalf("new Procedure result = %+v", result)
	}
	cat, err := kernel.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	procedure, err := cat.Get("draft.new-procedure")
	if err != nil {
		t.Fatal(err)
	}
	record, err := cat.Record("draft.new-procedure")
	if err != nil {
		t.Fatal(err)
	}
	if procedure.Method != "Check, then verify." || record.Type != knowledge.TypeProcedure || len(record.Requires) != 1 || record.Requires[0] != "test.obligation" {
		t.Fatalf("Procedure guidance = %+v; typed record = %+v", procedure, record)
	}
}

func TestFieldDrafterRemovesExistingListEntryWithReceiptDelta(t *testing.T) {
	drafter, kernel := fieldDrafterFixture(t)
	ctx := context.Background()
	start, err := drafter.Start(ctx, wire.FieldDraftStartRequest{
		Key: "draft.some-precondition", RecordType: string(knowledge.TypeConcept),
		Proposer: "test.agent", Rationale: "remove a no-longer-valid broader relation",
	})
	if err != nil {
		t.Fatal(err)
	}
	var result wire.FieldDraftResult
	for _, field := range start.Fields {
		outcome := &wire.FieldDraftOutcomeArg{Entity: field.Entity, Field: field.Field, Operation: "keep"}
		if field.Field == "broader" {
			outcome.Operation = "remove"
			outcome.Value = []string{"test.obligation"}
		}
		result, err = drafter.Advance(ctx, wire.FieldDraftAdvanceRequest{Token: start.Token, Outcome: outcome})
		if err != nil {
			t.Fatalf("answer %s: %v", field.Field, err)
		}
	}
	result = approveReviewedDraft(t, drafter, result)
	if result.Status != "complete" || result.Key != "draft.some-precondition" {
		t.Fatalf("removal result = %+v", result)
	}
	cat, err := kernel.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record, err := cat.Record("draft.some-precondition")
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Broader) != 0 {
		t.Fatalf("broader = %v, want removed entry", record.Broader)
	}
}

func TestFieldDrafterAllowsCorrectionAfterCandidateValidationFails(t *testing.T) {
	drafter, _ := fieldDrafterFixture(t)
	ctx := context.Background()
	start, err := drafter.Start(ctx, wire.FieldDraftStartRequest{
		Key: "draft.producer", RecordType: string(knowledge.TypeProcedure),
		Proposer: "test.agent", Rationale: "correct a vacuous composition",
	})
	if err != nil {
		t.Fatal(err)
	}
	var failed bool
	for _, field := range start.Fields {
		outcome := &wire.FieldDraftOutcomeArg{Entity: field.Entity, Field: field.Field, Operation: "keep"}
		if field.Entity == "record" && field.Field == "postconditions" {
			outcome.Operation = "add"
			outcome.Value = jsontext.Value(`["draft.unrelated"]`)
			outcome.Citations = []wire.ProposeCitation{{Source: "test.md", Quote: "draft.unrelated"}}
		}
		_, err = drafter.Advance(ctx, wire.FieldDraftAdvanceRequest{Token: start.Token, Outcome: outcome})
		if err != nil {
			failed = true
			break
		}
	}
	if !failed {
		t.Fatal("vacuous composition should reject the candidate")
	}
	result, err := drafter.Advance(ctx, wire.FieldDraftAdvanceRequest{Token: start.Token, Outcome: &wire.FieldDraftOutcomeArg{
		Entity: "record", Field: "postconditions", Operation: "replace",
		Value:     jsontext.Value(`["draft.some-precondition"]`),
		Citations: []wire.ProposeCitation{{Source: "draft.consumer", Quote: "draft.some-precondition"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result = approveReviewedDraft(t, drafter, result)
	if result.Status != "complete" || result.Snapshot == "" {
		t.Fatalf("corrected draft = %+v, want published result", result)
	}
}

func TestFieldDrafterAllKeepClosesWithoutPublication(t *testing.T) {
	drafter, _ := fieldDrafterFixture(t)
	ctx := context.Background()
	start, err := drafter.Start(ctx, wire.FieldDraftStartRequest{
		Key: "draft.standalone", RecordType: string(knowledge.TypeProcedure),
		Proposer: "test.agent", Rationale: "review the existing record without changes",
	})
	if err != nil {
		t.Fatal(err)
	}
	var result wire.FieldDraftResult
	for _, field := range start.Fields {
		result, err = drafter.Advance(ctx, wire.FieldDraftAdvanceRequest{Token: start.Token,
			Outcome: &wire.FieldDraftOutcomeArg{Entity: field.Entity, Field: field.Field, Operation: "keep"}})
		if err != nil {
			t.Fatalf("answer %s.%s: %v", field.Entity, field.Field, err)
		}
	}
	if result.Status != "unchanged" || result.Snapshot != start.Snapshot {
		t.Fatalf("all-keep result = %+v, want unchanged at original snapshot", result)
	}
}
