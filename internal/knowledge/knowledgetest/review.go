package knowledgetest

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"strings"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

// WithFullReview is for fixture publications only. It supplies a complete
// replace/clear receipt so tests that exercise unrelated catalog behavior
// continue to focus on their own assertions.
func WithFullReview(prop knowledge.Proposal) knowledge.Proposal {
	receipt := knowledge.FieldReviewReceipt{Version: 1}
	for _, change := range prop.Changes {
		if change.Procedure == nil {
			continue
		}
		entity := knowledge.EntityReview{Kind: knowledge.ReviewProcedureDocument, Key: change.Key}
		for _, spec := range knowledge.ProcedureDocumentFieldSpecs() {
			value, _, err := knowledge.ProcedureFieldValue(change.Procedure, spec.Name)
			if err != nil {
				return prop
			}
			entity.Fields = append(entity.Fields, fixtureDecision(spec, value))
		}
		receipt.Entities = append(receipt.Entities, entity)
	}
	for _, change := range prop.Records {
		if change.Record == nil {
			continue
		}
		entity := knowledge.EntityReview{Kind: knowledge.ReviewTypedRecord, Key: change.Key, Type: change.Record.Type}
		for _, spec := range knowledge.RecordFieldSpecs(change.Record.Type) {
			value, _, err := knowledge.RecordFieldValue(change.Record, spec.Name)
			if err != nil {
				return prop
			}
			entity.Fields = append(entity.Fields, fixtureDecision(spec, value))
		}
		receipt.Entities = append(receipt.Entities, entity)
	}
	prop.Review = receipt
	return prop
}

// ReviewCatalog makes a full-state receipt for every entity in cat. It is
// useful for rollback tests, where the kernel computes the restored state.
func ReviewCatalog(cat *knowledge.Catalog) knowledge.FieldReviewReceipt {
	var prop knowledge.Proposal
	for _, key := range cat.Keys() {
		procedure, err := cat.Get(key)
		if err != nil {
			panic(err)
		}
		prop.Changes = append(prop.Changes, knowledge.Change{Key: key, Procedure: procedure})
	}
	for _, key := range cat.RecordKeys() {
		record, err := cat.Record(key)
		if err != nil {
			panic(err)
		}
		prop.Records = append(prop.Records, knowledge.RecordChange{Key: key, Record: record})
	}
	return WithFullReview(prop).Review
}

func fixtureDecision(spec knowledge.ReviewFieldSpec, value jsontext.Value) knowledge.ReviewedField {
	if fixtureEmpty(value) && !spec.Required {
		return knowledge.ReviewedField{Field: spec.Name, Operation: knowledge.ReviewClear}
	}
	decision := knowledge.ReviewedField{Field: spec.Name, Operation: knowledge.ReviewReplace}
	if spec.Grounded {
		if text := fixtureText(value); strings.TrimSpace(text) != "" {
			decision.Citations = []knowledge.ReviewCitation{{Source: "test-fixture", Quote: text}}
		}
	}
	return decision
}

func fixtureEmpty(value jsontext.Value) bool {
	if len(value) == 0 || value.Kind() == jsontext.KindNull {
		return true
	}
	switch value.Kind() {
	case jsontext.KindString:
		return string(value) == `""`
	case jsontext.KindBeginArray:
		return string(value) == "[]"
	case jsontext.KindTrue, jsontext.KindFalse:
		return string(value) == "false"
	case jsontext.KindNumber:
		return string(value) == "0"
	default:
		return false
	}
}

func fixtureText(value jsontext.Value) string {
	if len(value) == 0 || !value.IsValid() {
		return ""
	}
	switch value.Kind() {
	case jsontext.KindString:
		var text string
		_ = json.Unmarshal(value, &text)
		return text
	case jsontext.KindBeginArray:
		var values []jsontext.Value
		_ = json.Unmarshal(value, &values)
		var parts []string
		for _, v := range values {
			if part := fixtureText(v); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, "\n")
	case jsontext.KindBeginObject:
		var values map[string]jsontext.Value
		_ = json.Unmarshal(value, &values)
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		var parts []string
		for _, key := range keys {
			v := values[key]
			if part := fixtureText(v); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}
