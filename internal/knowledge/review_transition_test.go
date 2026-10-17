package knowledge

import (
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"
)

func TestCheckFieldReviewTransitionRequiresCompleteMatchingReceipt(t *testing.T) {
	prev := newCatalog("", map[string]*Procedure{}, map[string]*Record{})
	procedure := &Procedure{
		Key: "demo.verify", Goal: "Verify the change", Philosophy: "Small steps.", Method: "Run checks.",
		Contract: jsontext.Value(`{}`), Instructions: jsontext.Value(`{"steps":["Run checks."]}`),
	}
	record := &Record{
		Type: TypeProcedure, Key: "demo.verify", Label: "demo.verify",
		Definition: "A procedure that verifies a change.", Requires: []string{"check.tests"},
	}
	next := newCatalog("", map[string]*Procedure{"demo.verify": procedure}, map[string]*Record{"demo.verify": record})
	receipt := completeTestReceipt(t, procedure, record)
	if err := CheckFieldReviewTransition(prev, next, receipt); err != nil {
		t.Fatalf("complete receipt: %v", err)
	}

	missing := receipt
	missing.Entities = append([]EntityReview(nil), receipt.Entities...)
	missing.Entities[0].Fields = append([]ReviewedField(nil), missing.Entities[0].Fields[1:]...)
	if err := CheckFieldReviewTransition(prev, next, missing); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "requires an explicit operation") {
		t.Fatalf("missing field decision: %v", err)
	}
	if err := CheckFieldReviewTransition(prev, next, FieldReviewReceipt{Version: 1}); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "requires a complete field-review receipt") {
		t.Fatalf("missing entity receipt: %v", err)
	}

	wrong := completeTestReceipt(t, procedure, record)
	wrong.Entities[0].Fields[0] = ReviewedField{Field: "goal", Operation: ReviewKeep}
	if err := CheckFieldReviewTransition(prev, next, wrong); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "keep is not valid for new field") {
		t.Fatalf("invalid operation for new entity: %v", err)
	}

	ungrounded := completeTestReceipt(t, procedure, record)
	ungrounded.Entities[0].Fields[0].Citations[0].Quote = "not present"
	if err := CheckFieldReviewTransition(prev, next, ungrounded); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "does not appear verbatim") {
		t.Fatalf("ungrounded replacement: %v", err)
	}
}

func TestCheckFieldReviewTransitionValidatesKeepAndListMutations(t *testing.T) {
	oldProc := &Procedure{Key: "demo.verify", Goal: "Verify", Philosophy: "Small steps.", Method: "Run checks.",
		Contract: jsontext.Value(`{}`), Instructions: jsontext.Value(`{}`)}
	newProc := oldProc.clone()
	newProc.Goal = "Verify the change"
	oldRecord := &Record{Type: TypeConcept, Key: "check.tests", Label: "Tests pass", Definition: "The relevant test suite passes."}
	newRecord := oldRecord.Clone()
	newRecord.Definition = "The relevant test suite passes consistently."
	prev := newCatalog("", map[string]*Procedure{"demo.verify": oldProc}, map[string]*Record{"check.tests": oldRecord})
	next := newCatalog("", map[string]*Procedure{"demo.verify": newProc}, map[string]*Record{"check.tests": newRecord})

	procedureReview := completeTestProcedureReview(t, oldProc, newProc)
	recordReview := completeTestRecordReview(t, oldRecord, newRecord)
	receipt := FieldReviewReceipt{Version: 1, Entities: []EntityReview{procedureReview, recordReview}}
	if err := CheckFieldReviewTransition(prev, next, receipt); err != nil {
		t.Fatalf("matching updates: %v", err)
	}

	receipt.Entities[0].Fields[0] = ReviewedField{Field: "goal", Operation: ReviewKeep}
	if err := CheckFieldReviewTransition(prev, next, receipt); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "does not describe the before/after values") {
		t.Fatalf("wrong keep decision: %v", err)
	}

	oldList := &Record{Type: TypeProcedure, Key: "demo.verify", Label: "demo.verify", Definition: "Verify a change",
		Requires: []string{"check.tests"}, Preconditions: []string{"check.tests"}}
	newList := oldList.Clone()
	newList.Invariants = []string{"check.tests"}
	oldCat := newCatalog("", map[string]*Procedure{"demo.verify": oldProc}, map[string]*Record{"demo.verify": oldList})
	newCat := newCatalog("", map[string]*Procedure{"demo.verify": oldProc}, map[string]*Record{"demo.verify": newList})
	reviews := []EntityReview{
		completeTestProcedureReview(t, oldProc, oldProc),
		completeTestRecordReview(t, oldList, newList),
	}
	for i := range reviews[1].Fields {
		if reviews[1].Fields[i].Field == "invariants" {
			reviews[1].Fields[i] = ReviewedField{
				Field: "invariants", Operation: ReviewAdd,
				Value:     jsontext.Value(`["check.tests"]`),
				Citations: []ReviewCitation{{Source: "check.tests", Quote: "check.tests"}},
			}
		}
	}
	if err := CheckFieldReviewTransition(oldCat, newCat, FieldReviewReceipt{Version: 1, Entities: reviews}); err != nil {
		t.Fatalf("matching list addition: %v", err)
	}
	for i := range reviews[1].Fields {
		if reviews[1].Fields[i].Field == "invariants" {
			reviews[1].Fields[i] = ReviewedField{Field: "invariants", Operation: ReviewRemove}
		}
	}
	if err := CheckFieldReviewTransition(oldCat, newCat, FieldReviewReceipt{Version: 1, Entities: reviews}); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "remove does not describe") {
		t.Fatalf("wrong list operation: %v", err)
	}
}

func TestFieldReviewReceiptCarriesReplayValuesForListOperations(t *testing.T) {
	old := &Record{Type: TypeProcedure, Key: "demo.verify", Label: "Verify", Definition: "Verify a change",
		Preconditions: []string{"check.tests", "check.build"}}
	next := old.Clone()
	next.Preconditions = []string{"check.tests", "check.build", "check.lint"}
	prev := newCatalog("", map[string]*Procedure{}, map[string]*Record{old.Key: old})
	after := newCatalog("", map[string]*Procedure{}, map[string]*Record{next.Key: next})
	review := completeTestRecordReview(t, old, next)
	for i := range review.Fields {
		if review.Fields[i].Field == "preconditions" {
			review.Fields[i] = ReviewedField{
				Field: "preconditions", Operation: ReviewAdd, Value: jsontext.Value(`["check.lint"]`),
				Citations: []ReviewCitation{{Source: "check.lint", Quote: "check.lint"}},
			}
		}
	}
	if err := CheckFieldReviewTransition(prev, after, FieldReviewReceipt{Version: 1, Entities: []EntityReview{review}}); err != nil {
		t.Fatalf("valid replay value: %v", err)
	}
	for i := range review.Fields {
		if review.Fields[i].Field == "preconditions" {
			review.Fields[i].Value = jsontext.Value(`["check.build"]`)
		}
	}
	if err := CheckFieldReviewTransition(prev, after, FieldReviewReceipt{Version: 1, Entities: []EntityReview{review}}); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "operation value does not match") {
		t.Fatalf("wrong replay value: %v", err)
	}
}

func TestFieldReviewRejectsInvalidExtraReceiptForUnchangedEntity(t *testing.T) {
	changed := &Record{Type: TypeConcept, Key: "concept.changed", Label: "changed", Definition: "before"}
	nextChanged := changed.Clone()
	nextChanged.Definition = "after"
	unchanged := &Record{Type: TypeConcept, Key: "concept.unchanged", Label: "unchanged", Definition: "stable"}
	prev := newCatalog("", map[string]*Procedure{}, map[string]*Record{
		changed.Key: changed, unchanged.Key: unchanged,
	})
	next := newCatalog("", map[string]*Procedure{}, map[string]*Record{
		nextChanged.Key: nextChanged, unchanged.Key: unchanged,
	})
	changedReview := completeTestRecordReview(t, changed, nextChanged)
	unchangedReview := completeTestRecordReview(t, unchanged, unchanged)
	unchangedReview.Fields = unchangedReview.Fields[:1]
	err := CheckFieldReviewTransition(prev, next, FieldReviewReceipt{
		Version: 1, Entities: []EntityReview{changedReview, unchangedReview},
	})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "must decide all") {
		t.Fatalf("incomplete extra receipt = %v, want unchanged entity fully validated", err)
	}
}

func TestFieldReviewAllowsCompleteNoOpReviewForUnchangedEntity(t *testing.T) {
	changed := &Record{Type: TypeConcept, Key: "concept.changed", Label: "changed", Definition: "before"}
	nextChanged := changed.Clone()
	nextChanged.Definition = "after"
	unchanged := &Record{Type: TypeConcept, Key: "concept.unchanged", Label: "unchanged", Definition: "stable"}
	prev := newCatalog("", map[string]*Procedure{}, map[string]*Record{
		changed.Key: changed, unchanged.Key: unchanged,
	})
	next := newCatalog("", map[string]*Procedure{}, map[string]*Record{
		nextChanged.Key: nextChanged, unchanged.Key: unchanged,
	})
	changedReview := completeTestRecordReview(t, changed, nextChanged)
	unchangedReview := completeTestRecordReview(t, unchanged, unchanged)
	for i := range unchangedReview.Fields {
		switch unchangedReview.Fields[i].Field {
		case "definition":
			unchangedReview.Fields[i] = ReviewedField{
				Field: "definition", Operation: ReviewReplace,
				Citations: []ReviewCitation{{Source: "concept.unchanged", Quote: unchanged.Definition}},
			}
		case "broader":
			unchangedReview.Fields[i] = ReviewedField{Field: "broader", Operation: ReviewClear}
		}
	}
	if err := CheckFieldReviewTransition(prev, next, FieldReviewReceipt{
		Version: 1, Entities: []EntityReview{changedReview, unchangedReview},
	}); err != nil {
		t.Fatalf("complete no-op review: %v", err)
	}
}

func completeTestReceipt(t *testing.T, procedure *Procedure, record *Record) FieldReviewReceipt {
	t.Helper()
	return FieldReviewReceipt{Version: 1, Entities: []EntityReview{
		completeTestProcedureReview(t, nil, procedure),
		completeTestRecordReview(t, nil, record),
	}}
}

func completeTestProcedureReview(t *testing.T, old, next *Procedure) EntityReview {
	t.Helper()
	entity := EntityReview{Kind: ReviewProcedureDocument, Key: next.Key}
	for _, spec := range ProcedureDocumentFieldSpecs() {
		oldValue := EmptyReviewValue(spec)
		isNew := old == nil
		if old != nil {
			oldValue, _, _ = ProcedureFieldValue(old, spec.Name)
		}
		nextValue, _, err := ProcedureFieldValue(next, spec.Name)
		if err != nil {
			t.Fatal(err)
		}
		entity.Fields = append(entity.Fields, makeTestDecision(t, spec, oldValue, nextValue, isNew))
	}
	return entity
}

func completeTestRecordReview(t *testing.T, old, next *Record) EntityReview {
	t.Helper()
	entity := EntityReview{Kind: ReviewTypedRecord, Key: next.Key, Type: next.Type}
	for _, spec := range RecordFieldSpecs(next.Type) {
		oldValue := EmptyReviewValue(spec)
		isNew := old == nil
		if old != nil {
			oldValue, _, _ = RecordFieldValue(old, spec.Name)
		}
		nextValue, _, err := RecordFieldValue(next, spec.Name)
		if err != nil {
			t.Fatal(err)
		}
		entity.Fields = append(entity.Fields, makeTestDecision(t, spec, oldValue, nextValue, isNew))
	}
	return entity
}

func makeTestDecision(t *testing.T, spec ReviewFieldSpec, old, next jsontext.Value, isNew bool) ReviewedField {
	t.Helper()
	if !isNew && equalReviewValue(old, next) {
		return ReviewedField{Field: spec.Name, Operation: ReviewKeep}
	}
	if isTestEmpty(next) && !spec.Required {
		return ReviewedField{Field: spec.Name, Operation: ReviewClear}
	}
	decision := ReviewedField{Field: spec.Name, Operation: ReviewReplace}
	if spec.Grounded {
		text, err := reviewText(next)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(text) != "" {
			decision.Citations = []ReviewCitation{{Source: "test", Quote: text}}
		}
	}
	return decision
}

func isTestEmpty(value jsontext.Value) bool {
	return len(value) == 0 || value.Kind() == jsontext.KindNull || string(value) == `""` ||
		string(value) == "false" || string(value) == "0" || string(value) == "[]"
}
