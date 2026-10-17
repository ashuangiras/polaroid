package knowledgeops

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// FieldDrafter implements /mcp/agent/v6's field-complete proposal flow.
// It asks about every authorable field for the selected entity type,
// presents the complete existing state at Start, and commits a typed
// review receipt through the shared publication gate.
type FieldDrafter struct {
	kernel   *knowledge.Kernel
	proposer *Proposer
	drafts   *memory.FieldDrafts
}

// NewFieldDrafter builds the v6 full-field proposal drafter.
func NewFieldDrafter(kernel *knowledge.Kernel, proposer *Proposer, drafts *memory.FieldDrafts) *FieldDrafter {
	return &FieldDrafter{kernel: kernel, proposer: proposer, drafts: drafts}
}

type fieldTarget struct {
	procedure    *knowledge.Procedure
	record       *knowledge.Record
	procedureNew bool
	recordNew    bool
}

type fieldItem struct {
	entity string
	spec   knowledge.ReviewFieldSpec
	new    bool
}

func (d *FieldDrafter) Start(ctx context.Context, in wire.FieldDraftStartRequest) (wire.FieldDraftResult, error) {
	if in.Key == "" {
		return wire.FieldDraftResult{}, requiredField("key")
	}
	if in.Proposer == "" {
		return wire.FieldDraftResult{}, requiredField("proposer")
	}
	if strings.TrimSpace(in.Rationale) == "" {
		return wire.FieldDraftResult{}, requiredField("rationale")
	}
	typ := knowledge.RecordType(in.RecordType)
	if !slices.Contains(knowledge.RecordTypes, typ) {
		return wire.FieldDraftResult{}, invalidField("record_type", "must be Domain, Concept, Claim, Rule, Procedure, Evidence or Exception")
	}
	cat, err := d.kernel.Active(ctx)
	if err != nil {
		return wire.FieldDraftResult{}, classify(err)
	}
	target, err := resolveFieldTarget(cat, in.Key, typ, in.New)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	state, err := d.drafts.Start(ctx, in.Key, string(cat.Snapshot()), string(typ), in.New, in.Proposer, in.Rationale)
	if err != nil {
		return wire.FieldDraftResult{}, classify(err)
	}
	fields, err := fieldInventory(cat, state)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	info, err := fieldInfo(fields, state.Answers, target)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	return wire.FieldDraftResult{
		Token: state.Token, Snapshot: state.Snapshot, Key: state.Key, Status: "in_progress",
		NextEntity: fields[0].entity, NextField: fields[0].spec.Name,
		Fields: info,
	}, nil
}

func (d *FieldDrafter) Advance(ctx context.Context, in wire.FieldDraftAdvanceRequest) (wire.FieldDraftResult, error) {
	if in.Token == "" {
		return wire.FieldDraftResult{}, requiredField("token")
	}
	if in.Close {
		if err := d.drafts.Close(ctx, in.Token); err != nil {
			return wire.FieldDraftResult{}, classify(err)
		}
		return wire.FieldDraftResult{Status: "closed"}, nil
	}
	state, err := d.drafts.State(ctx, in.Token)
	if err != nil {
		return wire.FieldDraftResult{}, classify(err)
	}
	cat, err := d.kernel.At(ctx, knowledge.Snapshot(state.Snapshot))
	if err != nil {
		return wire.FieldDraftResult{}, classify(err)
	}
	items, err := fieldInventory(cat, state)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	target, err := resolveFieldTarget(cat, state.Key, knowledge.RecordType(state.RecordType), state.New)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	if in.Outcome == nil {
		return fieldDraftResult(state, items, target)
	}
	next := nextFieldItem(items, state.Answers)
	if next != nil && (in.Outcome.Entity != next.entity || in.Outcome.Field != next.spec.Name) {
		return wire.FieldDraftResult{}, invalidField("outcome.field", fmt.Sprintf("must answer next field %s.%s", next.entity, next.spec.Name))
	}
	item := findFieldItem(items, in.Outcome.Entity, in.Outcome.Field)
	if item == nil {
		return wire.FieldDraftResult{}, invalidField("outcome.field", "is not an applicable field in this draft")
	}
	current, _, err := baseFieldValue(*item, target)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	rawValue, err := encodeFieldDraftValue(in.Outcome.Value)
	if err != nil {
		return wire.FieldDraftResult{}, invalidField("outcome.value", err.Error())
	}
	decision := knowledge.FieldDecision{
		Field: item.spec.Name, Operation: knowledge.ReviewOperation(in.Outcome.Operation),
		Value:     rawValue,
		Citations: make([]knowledge.ReviewCitation, len(in.Outcome.Citations)),
	}
	for i, citation := range in.Outcome.Citations {
		decision.Citations[i] = knowledge.ReviewCitation{Source: citation.Source, Quote: citation.Quote}
	}
	value, err := knowledge.ApplyReviewDecision(current, item.spec, decision, item.new)
	if err != nil {
		return wire.FieldDraftResult{}, invalidField("outcome.operation", err.Error())
	}
	grounded := value
	if decision.Operation == knowledge.ReviewAdd {
		grounded = decision.Value
	}
	if err := knowledge.ValidateReviewCitations(item.spec, decision, grounded); err != nil {
		return wire.FieldDraftResult{}, invalidField("outcome.citations", err.Error())
	}
	answer := &memory.FieldDraftAnswer{
		Entity: item.entity, Field: item.spec.Name, Operation: in.Outcome.Operation,
		Value: append([]byte(nil), rawValue...), Citations: make([]memory.FieldDraftCitation, len(in.Outcome.Citations)),
	}
	for i, citation := range in.Outcome.Citations {
		answer.Citations[i] = memory.FieldDraftCitation{Source: citation.Source, Quote: citation.Quote}
	}
	state, err = d.drafts.Advance(ctx, in.Token, answer)
	if err != nil {
		return wire.FieldDraftResult{}, classify(err)
	}
	items, err = fieldInventory(cat, state)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	target, err = resolveFieldTarget(cat, state.Key, knowledge.RecordType(state.RecordType), state.New)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	if nextFieldItem(items, state.Answers) != nil {
		return fieldDraftResult(state, items, target)
	}
	if !fieldDraftChanges(cat, state, items) {
		if err := d.drafts.Close(ctx, state.Token); err != nil {
			return wire.FieldDraftResult{}, classify(err)
		}
		return wire.FieldDraftResult{Snapshot: state.Snapshot, Key: state.Key, Status: "unchanged"}, nil
	}
	prop, compositionKeys, err := buildFieldProposal(cat, state, items)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	result, err := d.proposer.ProposeReviewed(ctx, prop, state.Key, compositionKeys)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	if err := d.drafts.Close(ctx, state.Token); err != nil {
		return wire.FieldDraftResult{}, classify(err)
	}
	return wire.FieldDraftResult{Snapshot: result.Snapshot, Key: result.Key, Status: "complete"}, nil
}

func encodeFieldDraftValue(value any) (jsontext.Value, error) {
	if value == nil {
		return nil, nil
	}
	if raw, ok := value.(jsontext.Value); ok {
		if !raw.IsValid() {
			return nil, fmt.Errorf("must be valid JSON")
		}
		return raw.Clone(), nil
	}
	encoded, err := json.Marshal(value)
	return jsontext.Value(encoded), err
}

func fieldDraftResult(state memory.FieldDraftState, items []fieldItem, target fieldTarget) (wire.FieldDraftResult, error) {
	next := nextFieldItem(items, state.Answers)
	result := wire.FieldDraftResult{Token: state.Token, Snapshot: state.Snapshot, Key: state.Key, Status: "in_progress"}
	if next != nil {
		result.NextEntity, result.NextField = next.entity, next.spec.Name
	}
	fields, err := fieldInfo(items, state.Answers, target)
	if err != nil {
		return wire.FieldDraftResult{}, err
	}
	result.Fields = fields
	return result, nil
}

func fieldInfo(items []fieldItem, answers map[string]memory.FieldDraftAnswer, target fieldTarget) ([]wire.FieldDraftFieldInfo, error) {
	out := make([]wire.FieldDraftFieldInfo, 0, len(items))
	for _, item := range items {
		allowed := allowedReviewOperations(item.spec, item.new)
		info := wire.FieldDraftFieldInfo{
			Entity: item.entity, Field: item.spec.Name, Kind: string(item.spec.Kind),
			Required: item.spec.Required, Grounded: item.spec.Grounded, Set: item.spec.Set,
			NewEntity: item.new, AllowedOperations: allowed,
		}
		value, present, err := baseFieldValue(item, target)
		if err != nil {
			return nil, err
		}
		info.Value, info.Present = value, present
		answer, answered := answers[memoryFieldKey(item.entity, item.spec.Name)]
		if answered {
			info.Answered, info.Operation = true, answer.Operation
			info.DraftValue, err = reviewValueFromAnswer(item.spec, value, answer, item.new)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func allowedReviewOperations(spec knowledge.ReviewFieldSpec, isNew bool) []string {
	var out []string
	if !isNew {
		out = append(out, string(knowledge.ReviewKeep))
	}
	if spec.Kind == knowledge.ReviewStringList || spec.Kind == knowledge.ReviewObjectList {
		out = append(out, string(knowledge.ReviewAdd))
		if !isNew {
			out = append(out, string(knowledge.ReviewRemove))
		}
	}
	out = append(out, string(knowledge.ReviewReplace))
	if !spec.Required {
		out = append(out, string(knowledge.ReviewClear))
	}
	return out
}

func fieldInventory(cat *knowledge.Catalog, state memory.FieldDraftState) ([]fieldItem, error) {
	target, err := resolveFieldTarget(cat, state.Key, knowledge.RecordType(state.RecordType), state.New)
	if err != nil {
		return nil, err
	}
	var fields []fieldItem
	if target.procedure != nil {
		for _, spec := range knowledge.ProcedureDocumentFieldSpecs() {
			fields = append(fields, fieldItem{entity: "procedure", spec: spec, new: target.procedureNew})
		}
	}
	for _, spec := range knowledge.RecordFieldSpecs(knowledge.RecordType(state.RecordType)) {
		fields = append(fields, fieldItem{entity: "record", spec: spec, new: target.recordNew})
	}
	return fields, nil
}

func resolveFieldTarget(cat *knowledge.Catalog, key string, typ knowledge.RecordType, isNew bool) (fieldTarget, error) {
	procedure, procErr := cat.Get(key)
	record, recordErr := cat.Record(key)
	if isNew {
		if procErr == nil || recordErr == nil {
			return fieldTarget{}, invalidField("key", key+" already exists")
		}
		if !errors.Is(procErr, knowledge.ErrNotFound) || !errors.Is(recordErr, knowledge.ErrNotFound) {
			if procErr != nil && !errors.Is(procErr, knowledge.ErrNotFound) {
				return fieldTarget{}, classify(procErr)
			}
			if recordErr != nil && !errors.Is(recordErr, knowledge.ErrNotFound) {
				return fieldTarget{}, classify(recordErr)
			}
		}
		target := fieldTarget{record: &knowledge.Record{Type: typ, Key: key}, recordNew: true}
		if typ == knowledge.TypeProcedure {
			target.procedure = &knowledge.Procedure{Key: key}
			target.procedureNew = true
		}
		return target, nil
	}
	if typ == knowledge.TypeProcedure {
		if procErr != nil {
			return fieldTarget{}, classify(procErr)
		}
		target := fieldTarget{procedure: procedure}
		if recordErr != nil {
			if !errors.Is(recordErr, knowledge.ErrNotFound) {
				return fieldTarget{}, classify(recordErr)
			}
			target.record, target.recordNew = &knowledge.Record{Type: knowledge.TypeProcedure, Key: key}, true
			return target, nil
		}
		if record.Type != knowledge.TypeProcedure {
			return fieldTarget{}, invalidField("record_type", fmt.Sprintf("%s is a %s, not a Procedure", key, record.Type))
		}
		target.record = record
		return target, nil
	}
	if procErr == nil {
		return fieldTarget{}, invalidField("record_type", "a Procedure key must be reviewed with both its guidance document and typed Procedure record")
	}
	if !errors.Is(procErr, knowledge.ErrNotFound) {
		return fieldTarget{}, classify(procErr)
	}
	if recordErr != nil {
		return fieldTarget{}, classify(recordErr)
	}
	if record.Type != typ {
		return fieldTarget{}, invalidField("record_type", fmt.Sprintf("%s is a %s, not a %s", key, record.Type, typ))
	}
	return fieldTarget{record: record}, nil
}

func fieldValue(entity string, target fieldTarget, field string) (jsontext.Value, bool, error) {
	switch entity {
	case "procedure":
		return knowledge.ProcedureFieldValue(target.procedure, field)
	case "record":
		return knowledge.RecordFieldValue(target.record, field)
	default:
		return nil, false, invalidField("outcome.entity", "must be procedure or record")
	}
}

func baseFieldValue(item fieldItem, target fieldTarget) (jsontext.Value, bool, error) {
	if item.new {
		return knowledge.EmptyReviewValue(item.spec), false, nil
	}
	return fieldValue(item.entity, target, item.spec.Name)
}

func findFieldItem(items []fieldItem, entity, field string) *fieldItem {
	for i := range items {
		if items[i].entity == entity && items[i].spec.Name == field {
			return &items[i]
		}
	}
	return nil
}

func nextFieldItem(items []fieldItem, answers map[string]memory.FieldDraftAnswer) *fieldItem {
	for i := range items {
		if _, ok := answers[memoryFieldKey(items[i].entity, items[i].spec.Name)]; !ok {
			return &items[i]
		}
	}
	return nil
}

func memoryFieldKey(entity, field string) string { return entity + "/" + field }

func fieldDraftChanges(cat *knowledge.Catalog, state memory.FieldDraftState, items []fieldItem) bool {
	target, err := resolveFieldTarget(cat, state.Key, knowledge.RecordType(state.RecordType), state.New)
	if err != nil {
		return true
	}
	var procedure *knowledge.Procedure
	if target.procedure != nil {
		procedure = cloneProcedure(target.procedure)
	}
	var record *knowledge.Record
	if target.record != nil {
		record = target.record.Clone()
	}
	for _, item := range items {
		answer := state.Answers[memoryFieldKey(item.entity, item.spec.Name)]
		value, _, err := baseFieldValue(item, target)
		if err != nil {
			return true
		}
		next, err := reviewValueFromAnswer(item.spec, value, answer, item.new)
		if err != nil {
			return true
		}
		if answer.Operation == string(knowledge.ReviewKeep) {
			continue
		}
		if item.entity == "procedure" {
			if err := knowledge.SetProcedureFieldValue(procedure, item.spec.Name, next); err != nil {
				return true
			}
		} else if err := knowledge.SetRecordFieldValue(record, item.spec.Name, next); err != nil {
			return true
		}
	}
	if target.procedureNew || target.recordNew {
		return true
	}
	if target.procedure != nil {
		if target.procedureNew {
			return true
		}
		oldProcedure, _ := cat.Get(state.Key)
		oldDoc, err := knowledge.Encode(oldProcedure)
		if err != nil {
			return true
		}
		newDoc, err := knowledge.Encode(procedure)
		if err != nil || !bytes.Equal(oldDoc, newDoc) {
			return true
		}
	}
	if target.recordNew {
		return true
	}
	oldRecord, _ := cat.Record(state.Key)
	oldRecordDoc, err := knowledge.EncodeRecord(oldRecord)
	if err != nil {
		return true
	}
	newRecordDoc, err := knowledge.EncodeRecord(record)
	return err != nil || !bytes.Equal(oldRecordDoc, newRecordDoc)
}

func buildFieldProposal(cat *knowledge.Catalog, state memory.FieldDraftState, items []fieldItem) (knowledge.Proposal, []string, error) {
	target, err := resolveFieldTarget(cat, state.Key, knowledge.RecordType(state.RecordType), state.New)
	if err != nil {
		return knowledge.Proposal{}, nil, err
	}
	var procedure *knowledge.Procedure
	if target.procedure != nil {
		procedure = cloneProcedure(target.procedure)
	}
	record := target.record.Clone()
	receipt := knowledge.FieldReviewReceipt{Version: 1}
	procedureReview := knowledge.EntityReview{Kind: knowledge.ReviewProcedureDocument, Key: state.Key}
	recordReview := knowledge.EntityReview{Kind: knowledge.ReviewTypedRecord, Key: state.Key, Type: record.Type}
	var compositionKeys []string
	for _, item := range items {
		answer := state.Answers[memoryFieldKey(item.entity, item.spec.Name)]
		current, _, err := baseFieldValue(item, target)
		if err != nil {
			return knowledge.Proposal{}, nil, err
		}
		decision := knowledge.FieldDecision{
			Field: item.spec.Name, Operation: knowledge.ReviewOperation(answer.Operation),
			Value: jsontext.Value(answer.Value), Citations: make([]knowledge.ReviewCitation, len(answer.Citations)),
		}
		for i, citation := range answer.Citations {
			decision.Citations[i] = knowledge.ReviewCitation{Source: citation.Source, Quote: citation.Quote}
		}
		value, err := reviewValueFromAnswer(item.spec, jsontext.Value(current), answer, item.new)
		if err != nil {
			return knowledge.Proposal{}, nil, invalidField("outcome.operation", err.Error())
		}
		receiptValue, err := knowledge.ReviewReceiptValue(item.spec, decision.Operation, jsontext.Value(current), value)
		if err != nil {
			return knowledge.Proposal{}, nil, invalidField("outcome.operation", err.Error())
		}
		if item.entity == "procedure" {
			if decision.Operation != knowledge.ReviewKeep {
				if err := knowledge.SetProcedureFieldValue(procedure, item.spec.Name, value); err != nil {
					return knowledge.Proposal{}, nil, invalidField("outcome.value", err.Error())
				}
			}
			procedureReview.Fields = append(procedureReview.Fields, knowledge.ReviewedField{
				Field: item.spec.Name, Operation: decision.Operation, Value: receiptValue, Citations: decision.Citations,
			})
		} else {
			if decision.Operation != knowledge.ReviewKeep {
				if err := knowledge.SetRecordFieldValue(record, item.spec.Name, value); err != nil {
					return knowledge.Proposal{}, nil, invalidField("outcome.value", err.Error())
				}
			}
			recordReview.Fields = append(recordReview.Fields, knowledge.ReviewedField{
				Field: item.spec.Name, Operation: decision.Operation, Value: receiptValue, Citations: decision.Citations,
			})
		}
	}
	proposal := knowledge.Proposal{
		Base: knowledge.Snapshot(state.Snapshot),
		Provenance: knowledge.Provenance{
			Author: state.Proposer, Summary: "Field-complete proposal for " + state.Key, Rationale: state.Rationale,
		},
		Review: receipt,
	}
	if target.procedure != nil {
		proposal.Changes = []knowledge.Change{{Key: state.Key, Procedure: procedure}}
		receipt.Entities = append(receipt.Entities, procedureReview)
	}
	proposal.Records = []knowledge.RecordChange{{Key: state.Key, Record: record}}
	receipt.Entities = append(receipt.Entities, recordReview)
	proposal.Review = receipt
	if !target.recordNew && target.record.Type == knowledge.TypeProcedure {
		if touchesComposition(target.record, record) {
			compositionKeys = append(compositionKeys, state.Key)
		}
	} else if target.recordNew && record.Type == knowledge.TypeProcedure {
		compositionKeys = append(compositionKeys, state.Key)
	}
	return proposal, compositionKeys, nil
}

func reviewValueFromAnswer(spec knowledge.ReviewFieldSpec, current jsontext.Value, answer memory.FieldDraftAnswer, isNew bool) (jsontext.Value, error) {
	decision := knowledge.FieldDecision{
		Field: spec.Name, Operation: knowledge.ReviewOperation(answer.Operation), Value: jsontext.Value(answer.Value),
	}
	return knowledge.ApplyReviewDecision(current, spec, decision, isNew)
}

func cloneProcedure(procedure *knowledge.Procedure) *knowledge.Procedure {
	if procedure == nil {
		return nil
	}
	clone := *procedure
	clone.Contract = procedure.Contract.Clone()
	clone.Instructions = procedure.Instructions.Clone()
	clone.References = make([]knowledge.Reference, len(procedure.References))
	for i, ref := range procedure.References {
		clone.References[i] = knowledge.Reference{Name: ref.Name, Key: ref.Key, Inputs: ref.Inputs.Clone()}
		if ref.Condition != nil {
			condition := *ref.Condition
			clone.References[i].Condition = &condition
		}
	}
	return &clone
}

func touchesComposition(old, next *knowledge.Record) bool {
	if old == nil {
		return true
	}
	fields := []string{"requires", "specializes", "composes", "precedes", "inputs", "outputs", "outputs_complete",
		"preconditions", "postconditions", "invariants", "preconditions_complete", "postconditions_complete", "invariants_complete"}
	for _, field := range fields {
		a, _, errA := knowledge.RecordFieldValue(old, field)
		b, _, errB := knowledge.RecordFieldValue(next, field)
		if errA != nil || errB != nil || !bytes.Equal(a, b) {
			return true
		}
	}
	return false
}
