package knowledge_test

import (
	"encoding/json/jsontext"
	"reflect"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

func TestProcedureDocumentReviewFields(t *testing.T) {
	want := []string{"goal", "philosophy", "method", "applicability", "contract", "instructions", "references"}
	if got := reviewFieldNames(knowledge.ProcedureDocumentFieldSpecs()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Procedure document review fields = %v, want %v", got, want)
	}
}

func TestRecordReviewFieldsByType(t *testing.T) {
	tests := []struct {
		typeOf knowledge.RecordType
		want   []string
	}{
		{knowledge.TypeDomain, []string{"label", "definition", "classified_as", "broader", "supersedes"}},
		{knowledge.TypeConcept, []string{"label", "definition", "classified_as", "broader", "specializes", "uses", "contradicts", "supersedes"}},
		{knowledge.TypeClaim, []string{"label", "statement", "classified_as", "uses", "supports", "contradicts", "supersedes", "asserts"}},
		{knowledge.TypeRule, []string{"label", "statement", "classified_as", "uses", "supersedes", "program", "revision", "protected", "waivable"}},
		{knowledge.TypeProcedure, []string{
			"label", "definition", "classified_as", "specializes", "uses", "composes", "precedes", "requires",
			"supersedes", "inputs", "outputs", "outputs_complete", "preconditions", "postconditions",
			"invariants", "informal_conditions", "preconditions_complete", "postconditions_complete",
			"invariants_complete", "informal_conditions_complete",
		}},
		{knowledge.TypeEvidence, []string{"label", "statement", "classified_as", "supports", "contradicts", "supersedes", "source"}},
		{knowledge.TypeException, []string{
			"label", "classified_as", "supersedes", "revision", "excepts", "rule_digest", "applies_to",
			"conditions", "waives", "replacement", "justification", "authority", "approval",
		}},
	}
	for _, tt := range tests {
		t.Run(string(tt.typeOf), func(t *testing.T) {
			if got := reviewFieldNames(knowledge.RecordFieldSpecs(tt.typeOf)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("review fields = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReviewReceiptCanonicalRoundTrip(t *testing.T) {
	receipt := knowledge.FieldReviewReceipt{
		Version: 1,
		Entities: []knowledge.EntityReview{{
			Kind: knowledge.ReviewTypedRecord, Key: "check.build", Type: knowledge.TypeConcept,
			Fields: []knowledge.ReviewedField{
				{Field: "label", Operation: knowledge.ReviewReplace},
				{Field: "definition", Operation: knowledge.ReviewReplace},
			},
		}},
	}
	encoded, err := receipt.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := knowledge.DecodeFieldReview(encoded)
	if err != nil || !reflect.DeepEqual(decoded, receipt) {
		t.Fatalf("decoded = %#v, err = %v; want %#v", decoded, err, receipt)
	}
	if _, err := knowledge.DecodeFieldReview(append(encoded, ' ')); err == nil {
		t.Fatal("accepted a noncanonical receipt")
	}
	if _, err := knowledge.DecodeFieldReview([]byte(`{"version":1,"entities":[],"unknown":true}`)); err == nil {
		t.Fatal("accepted an unknown receipt member")
	}
}

func reviewFieldNames(fields []knowledge.ReviewFieldSpec) []string {
	names := make([]string, len(fields))
	for i, field := range fields {
		names[i] = field.Name
	}
	return names
}

func TestApplyReviewDecisionOperations(t *testing.T) {
	spec := knowledge.ReviewFieldSpec{Name: "values", Kind: knowledge.ReviewStringList, Set: true}
	current := jsontext.Value(`["b","a"]`)
	tests := []struct {
		name      string
		decision  knowledge.FieldDecision
		isNew     bool
		want      string
		wantError bool
	}{
		{"keep", knowledge.FieldDecision{Field: "values", Operation: knowledge.ReviewKeep}, false, `["b","a"]`, false},
		{"add", knowledge.FieldDecision{Field: "values", Operation: knowledge.ReviewAdd, Value: jsontext.Value(`["c"]`)}, false, `["a","b","c"]`, false},
		{"remove", knowledge.FieldDecision{Field: "values", Operation: knowledge.ReviewRemove, Value: jsontext.Value(`["b"]`)}, false, `["a"]`, false},
		{"replace", knowledge.FieldDecision{Field: "values", Operation: knowledge.ReviewReplace, Value: jsontext.Value(`["c","a"]`)}, false, `["a","c"]`, false},
		{"clear", knowledge.FieldDecision{Field: "values", Operation: knowledge.ReviewClear}, false, `[]`, false},
		{"new cannot keep", knowledge.FieldDecision{Field: "values", Operation: knowledge.ReviewKeep}, true, "", true},
		{"remove missing", knowledge.FieldDecision{Field: "values", Operation: knowledge.ReviewRemove, Value: jsontext.Value(`["missing"]`)}, false, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := knowledge.ApplyReviewDecision(current, spec, tt.decision, tt.isNew)
			if (err != nil) != tt.wantError {
				t.Fatalf("error = %v, wantError %t", err, tt.wantError)
			}
			if !tt.wantError && string(got) != tt.want {
				t.Fatalf("value = %s, want %s", got, tt.want)
			}
		})
	}
}
