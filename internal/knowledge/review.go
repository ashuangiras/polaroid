package knowledge

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type ReviewEntityKind string

const (
	ReviewProcedureDocument ReviewEntityKind = "procedure"
	ReviewTypedRecord       ReviewEntityKind = "record"
)

type ReviewOperation string

const (
	ReviewKeep    ReviewOperation = "keep"
	ReviewAdd     ReviewOperation = "add"
	ReviewRemove  ReviewOperation = "remove"
	ReviewReplace ReviewOperation = "replace"
	ReviewClear   ReviewOperation = "clear"
)

const TrailerFieldReview = "Polaroid-Field-Review"

var ErrInvalidFieldReview = errors.New("invalid field review")

type ReviewValueKind string

const (
	ReviewString     ReviewValueKind = "string"
	ReviewBoolean    ReviewValueKind = "boolean"
	ReviewInteger    ReviewValueKind = "integer"
	ReviewObject     ReviewValueKind = "object"
	ReviewStringList ReviewValueKind = "string_list"
	ReviewObjectList ReviewValueKind = "object_list"
)

// ReviewFieldSpec is one authorable top-level field and its editing contract.
// It is derived from the same type-specific vocabulary checkRecord enforces.
type ReviewFieldSpec struct {
	Name     string          `json:"name"`
	Kind     ReviewValueKind `json:"kind"`
	Required bool            `json:"required"`
	Grounded bool            `json:"grounded"`
	Set      bool            `json:"set"`
}

type ReviewCitation struct {
	Source string `json:"source"`
	Quote  string `json:"quote"`
}

// FieldDecision is the operation the author selected for one applicable
// field. Value is the replacement value, the entries to add, or the entries
// to remove, according to Operation. Citations ground new content values.
type FieldDecision struct {
	Field     string           `json:"field"`
	Operation ReviewOperation  `json:"operation"`
	Value     jsontext.Value   `json:"value,omitzero"`
	Citations []ReviewCitation `json:"citations,omitzero"`
}

type ReviewedField struct {
	Field     string           `json:"field"`
	Operation ReviewOperation  `json:"operation"`
	Value     jsontext.Value   `json:"value,omitzero"`
	Citations []ReviewCitation `json:"citations,omitzero"`
}

type EntityReview struct {
	Kind   ReviewEntityKind `json:"kind"`
	Key    string           `json:"key"`
	Type   RecordType       `json:"type,omitzero"`
	Fields []ReviewedField  `json:"fields"`
}

// FieldReviewReceipt is immutable publication evidence that each changed
// entity's full, type-specific field inventory was explicitly reviewed.
type FieldReviewReceipt struct {
	Version  int            `json:"version"`
	Entities []EntityReview `json:"entities"`
}

var procedureDocumentFieldSpecs = []ReviewFieldSpec{
	{Name: "goal", Kind: ReviewString, Grounded: true},
	{Name: "philosophy", Kind: ReviewString, Required: true, Grounded: true},
	{Name: "method", Kind: ReviewString, Required: true, Grounded: true},
	{Name: "applicability", Kind: ReviewObject},
	{Name: "contract", Kind: ReviewObject, Required: true, Grounded: true},
	{Name: "instructions", Kind: ReviewObject, Required: true, Grounded: true},
	{Name: "references", Kind: ReviewObjectList},
}

var recordFieldOrder = []string{
	"label", "definition", "statement", "classified_as", "broader", "specializes", "uses",
	"composes", "precedes", "requires", "supports", "contradicts", "supersedes", "asserts",
	"source", "program", "revision", "protected", "waivable", "inputs", "outputs",
	"outputs_complete", "preconditions", "postconditions", "invariants", "informal_conditions",
	"preconditions_complete", "postconditions_complete", "invariants_complete",
	"informal_conditions_complete", "excepts", "rule_digest", "applies_to", "conditions",
	"waives", "replacement", "justification", "authority", "approval",
}

func ProcedureDocumentFieldSpecs() []ReviewFieldSpec {
	return slices.Clone(procedureDocumentFieldSpecs)
}

// RecordFieldSpecs returns every authorable property admitted for t, in
// canonical document order. Identity, type and @context are system-managed.
func RecordFieldSpecs(t RecordType) []ReviewFieldSpec {
	if !slices.Contains(RecordTypes, t) {
		return nil
	}
	out := make([]ReviewFieldSpec, 0, len(recordFieldOrder))
	for _, name := range recordFieldOrder {
		if spec, ok := recordFieldSpec(t, name); ok {
			out = append(out, spec)
		}
	}
	return out
}

func recordFieldSpec(t RecordType, name string) (ReviewFieldSpec, bool) {
	if name == "label" {
		return ReviewFieldSpec{Name: name, Kind: ReviewString, Required: true, Grounded: true}, true
	}
	if name == "authority" || name == "approval" {
		return scalarReviewSpec(t, name)
	}
	for _, s := range scalars {
		if s.name == name && slices.Contains(s.sources, t) {
			kind, grounded, set := recordFieldKind(name)
			required := slices.Contains(s.required, t)
			if name == "definition" && (t == TypeDomain || t == TypeConcept || t == TypeProcedure) {
				required = true
			}
			return ReviewFieldSpec{Name: name, Kind: kind, Required: required, Grounded: grounded, Set: set}, true
		}
	}
	for _, r := range relations {
		if r.name != name || !slices.Contains(r.sources, t) || name == "authority.domain" || name == "authority.path" {
			continue
		}
		kind, grounded, set := recordFieldKind(name)
		return ReviewFieldSpec{Name: name, Kind: kind, Grounded: grounded, Set: set}, true
	}
	return ReviewFieldSpec{}, false
}

func scalarReviewSpec(t RecordType, name string) (ReviewFieldSpec, bool) {
	for _, s := range scalars {
		if s.name != name || !slices.Contains(s.sources, t) {
			continue
		}
		kind, grounded, set := recordFieldKind(name)
		return ReviewFieldSpec{Name: name, Kind: kind, Required: slices.Contains(s.required, t), Grounded: grounded, Set: set}, true
	}
	return ReviewFieldSpec{}, false
}

func recordFieldKind(name string) (ReviewValueKind, bool, bool) {
	switch name {
	case "protected", "waivable", "outputs_complete", "preconditions_complete",
		"postconditions_complete", "invariants_complete", "informal_conditions_complete":
		return ReviewBoolean, false, false
	case "revision":
		return ReviewInteger, false, false
	case "asserts", "inputs", "outputs":
		return ReviewObjectList, false, true
	case "conditions":
		return ReviewObjectList, false, true
	case "authority", "approval":
		return ReviewObject, false, false
	case "classified_as", "broader", "specializes", "uses", "composes", "precedes",
		"requires", "supports", "contradicts", "supersedes", "preconditions",
		"postconditions", "invariants", "excepts", "applies_to":
		return ReviewStringList, name == "preconditions" || name == "postconditions" || name == "invariants", true
	case "informal_conditions":
		return ReviewStringList, true, true
	case "definition", "statement", "program", "justification":
		return ReviewString, true, false
	case "waives", "replacement", "source", "rule_digest":
		return ReviewString, false, false
	default:
		return ReviewString, false, false
	}
}

func EmptyReviewValue(spec ReviewFieldSpec) jsontext.Value {
	switch spec.Kind {
	case ReviewBoolean:
		return jsontext.Value("false")
	case ReviewInteger:
		return jsontext.Value("0")
	case ReviewObject:
		return jsontext.Value("null")
	case ReviewStringList, ReviewObjectList:
		return jsontext.Value("[]")
	default:
		return jsontext.Value(`""`)
	}
}

// ApplyReviewDecision computes the field's final value for one explicit
// operation. Catalog validation remains responsible for checking the
// resulting value's full domain constraints.
func ApplyReviewDecision(current jsontext.Value, spec ReviewFieldSpec, decision FieldDecision, isNew bool) (jsontext.Value, error) {
	if decision.Field != spec.Name {
		return nil, fmt.Errorf("%w: decision field %q does not match %q", ErrInvalidFieldReview, decision.Field, spec.Name)
	}
	isList := spec.Kind == ReviewStringList || spec.Kind == ReviewObjectList
	switch decision.Operation {
	case ReviewKeep:
		if isNew {
			return nil, fmt.Errorf("%w: keep is not valid for new field %q", ErrInvalidFieldReview, spec.Name)
		}
		if len(decision.Value) > 0 || len(decision.Citations) > 0 {
			return nil, fmt.Errorf("%w: keep does not accept a value or citations", ErrInvalidFieldReview)
		}
		return compactReviewValue(current)
	case ReviewAdd, ReviewRemove:
		if !isList {
			return nil, fmt.Errorf("%w: %s is only valid for list field %q", ErrInvalidFieldReview, decision.Operation, spec.Name)
		}
		if decision.Operation == ReviewRemove && isNew {
			return nil, fmt.Errorf("%w: remove is not valid for new field %q", ErrInvalidFieldReview, spec.Name)
		}
		if len(decision.Value) == 0 {
			return nil, fmt.Errorf("%w: %s requires a non-empty values array", ErrInvalidFieldReview, decision.Operation)
		}
		items, err := reviewArray(decision.Value)
		if err != nil || len(items) == 0 {
			return nil, fmt.Errorf("%w: %s requires a non-empty JSON array", ErrInvalidFieldReview, decision.Operation)
		}
		oldItems, err := reviewArrayOrEmpty(current)
		if err != nil {
			return nil, fmt.Errorf("%w: current value of %q is not a list", ErrInvalidFieldReview, spec.Name)
		}
		if decision.Operation == ReviewAdd {
			oldItems = append(oldItems, items...)
			return marshalReviewArray(normalizeReviewItems(oldItems, spec.Set))
		}
		remaining := slices.Clone(oldItems)
		for _, remove := range items {
			i := slices.IndexFunc(remaining, func(v jsontext.Value) bool { return equalReviewValue(v, remove) })
			if i < 0 {
				return nil, fmt.Errorf("%w: cannot remove a value absent from %q", ErrInvalidFieldReview, spec.Name)
			}
			remaining = slices.Delete(remaining, i, i+1)
		}
		return marshalReviewArray(normalizeReviewItems(remaining, spec.Set))
	case ReviewReplace:
		if len(decision.Value) == 0 || !decision.Value.IsValid() {
			return nil, fmt.Errorf("%w: replace requires a valid JSON value", ErrInvalidFieldReview)
		}
		if isList && spec.Set {
			items, err := reviewArray(decision.Value)
			if err != nil {
				return nil, fmt.Errorf("%w: replacement for %q must be a JSON array", ErrInvalidFieldReview, spec.Name)
			}
			return marshalReviewArray(normalizeReviewItems(items, true))
		}
		return compactReviewValue(decision.Value)
	case ReviewClear:
		if len(decision.Value) > 0 || len(decision.Citations) > 0 {
			return nil, fmt.Errorf("%w: clear does not accept a value or citations", ErrInvalidFieldReview)
		}
		if spec.Required {
			return nil, fmt.Errorf("%w: required field %q cannot be cleared", ErrInvalidFieldReview, spec.Name)
		}
		return EmptyReviewValue(spec), nil
	default:
		return nil, fmt.Errorf("%w: unknown operation %q", ErrInvalidFieldReview, decision.Operation)
	}
}

func ValidateReviewCitations(spec ReviewFieldSpec, decision FieldDecision, newContent jsontext.Value) error {
	if decision.Operation != ReviewAdd && decision.Operation != ReviewReplace {
		if len(decision.Citations) != 0 {
			return fmt.Errorf("%w: citations are only accepted for add or replace", ErrInvalidFieldReview)
		}
		return nil
	}
	if !spec.Grounded {
		if len(decision.Citations) != 0 {
			return fmt.Errorf("%w: field %q does not take content citations", ErrInvalidFieldReview, spec.Name)
		}
		return nil
	}
	content, err := reviewText(newContent)
	if err != nil {
		return fmt.Errorf("%w: read new content for %q: %w", ErrInvalidFieldReview, spec.Name, err)
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if len(decision.Citations) == 0 {
		return fmt.Errorf("%w: citations are required for new content in %q", ErrInvalidFieldReview, spec.Name)
	}
	for i, c := range decision.Citations {
		if strings.TrimSpace(c.Source) == "" || strings.TrimSpace(c.Quote) == "" {
			return fmt.Errorf("%w: citations[%d] requires source and quote", ErrInvalidFieldReview, i)
		}
		if !strings.Contains(content, c.Quote) {
			return fmt.Errorf("%w: citations[%d].quote does not appear verbatim in field %q", ErrInvalidFieldReview, i, spec.Name)
		}
	}
	return nil
}

func reviewText(v jsontext.Value) (string, error) {
	if len(v) == 0 || !v.IsValid() {
		return "", nil
	}
	switch v.Kind() {
	case jsontext.KindString:
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", err
		}
		return s, nil
	case jsontext.KindBeginArray:
		var values []jsontext.Value
		if err := json.Unmarshal(v, &values); err != nil {
			return "", err
		}
		var parts []string
		for _, value := range values {
			s, err := reviewText(value)
			if err != nil {
				return "", err
			}
			if s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n"), nil
	case jsontext.KindBeginObject:
		var values map[string]jsontext.Value
		if err := json.Unmarshal(v, &values); err != nil {
			return "", err
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		var parts []string
		for _, key := range keys {
			value := values[key]
			s, err := reviewText(value)
			if err != nil {
				return "", err
			}
			if s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n"), nil
	default:
		return "", nil
	}
}

func reviewArray(v jsontext.Value) ([]jsontext.Value, error) {
	var values []jsontext.Value
	if err := json.Unmarshal(v, &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = []jsontext.Value{}
	}
	return values, nil
}

func reviewArrayOrEmpty(v jsontext.Value) ([]jsontext.Value, error) {
	if len(v) == 0 || v.Kind() == jsontext.KindNull {
		return []jsontext.Value{}, nil
	}
	return reviewArray(v)
}

func normalizeReviewItems(values []jsontext.Value, set bool) []jsontext.Value {
	values = slices.Clone(values)
	for i := range values {
		values[i] = canonicalReviewValue(values[i])
	}
	if !set {
		return values
	}
	slices.SortFunc(values, func(a, b jsontext.Value) int { return bytes.Compare(a, b) })
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || !equalReviewValue(out[len(out)-1], value) {
			out = append(out, value)
		}
	}
	return out
}

func marshalReviewArray(values []jsontext.Value) (jsontext.Value, error) {
	if values == nil {
		values = []jsontext.Value{}
	}
	b, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	return jsontext.Value(b), nil
}

func compactReviewValue(v jsontext.Value) (jsontext.Value, error) {
	if len(v) == 0 {
		return nil, fmt.Errorf("%w: missing JSON value", ErrInvalidFieldReview)
	}
	c := v.Clone()
	if err := c.Compact(); err != nil {
		return nil, err
	}
	return c, nil
}

func canonicalReviewValue(v jsontext.Value) jsontext.Value {
	c := v.Clone()
	if err := c.Canonicalize(); err != nil {
		return v
	}
	return c
}

func equalReviewValue(a, b jsontext.Value) bool {
	return bytes.Equal(canonicalReviewValue(a), canonicalReviewValue(b))
}

func ProcedureFieldValue(p *Procedure, field string) (jsontext.Value, bool, error) {
	if p == nil || !slices.ContainsFunc(procedureDocumentFieldSpecs, func(s ReviewFieldSpec) bool { return s.Name == field }) {
		return nil, false, fmt.Errorf("%w: unknown Procedure field %q", ErrInvalidFieldReview, field)
	}
	b, err := Encode(p)
	if err != nil {
		return nil, false, err
	}
	_, present, err := encodedFieldValue(b, field, procedureDocumentFieldSpecs)
	if err != nil {
		return nil, false, err
	}
	value, err := procedureReviewValue(p, field)
	return value, present, err
}

func SetProcedureFieldValue(p *Procedure, field string, value jsontext.Value) error {
	if p == nil {
		return fmt.Errorf("%w: missing Procedure document", ErrInvalidFieldReview)
	}
	return setProcedureReviewValue(p, field, value)
}

func RecordFieldValue(r *Record, field string) (jsontext.Value, bool, error) {
	if r == nil {
		return nil, false, fmt.Errorf("%w: missing record", ErrInvalidFieldReview)
	}
	specs := RecordFieldSpecs(r.Type)
	if !slices.ContainsFunc(specs, func(s ReviewFieldSpec) bool { return s.Name == field }) {
		return nil, false, fmt.Errorf("%w: unknown %s field %q", ErrInvalidFieldReview, r.Type, field)
	}
	b, err := EncodeRecord(r)
	if err != nil {
		return nil, false, err
	}
	_, present, err := encodedFieldValue(b, field, specs)
	if err != nil {
		return nil, false, err
	}
	value, err := recordReviewValue(normalized(r), field)
	return value, present, err
}

func SetRecordFieldValue(r *Record, field string, value jsontext.Value) error {
	if r == nil {
		return fmt.Errorf("%w: missing record", ErrInvalidFieldReview)
	}
	if !slices.ContainsFunc(RecordFieldSpecs(r.Type), func(s ReviewFieldSpec) bool { return s.Name == field }) {
		return fmt.Errorf("%w: unknown %s field %q", ErrInvalidFieldReview, r.Type, field)
	}
	stringList := func(dst *[]string) error { return unmarshalReviewValue(value, dst) }
	switch field {
	case "label":
		return unmarshalReviewValue(value, &r.Label)
	case "definition":
		return unmarshalReviewValue(value, &r.Definition)
	case "statement":
		return unmarshalReviewValue(value, &r.Statement)
	case "classified_as":
		return stringList(&r.ClassifiedAs)
	case "broader":
		return stringList(&r.Broader)
	case "specializes":
		return stringList(&r.Specializes)
	case "uses":
		return stringList(&r.Uses)
	case "composes":
		return stringList(&r.Composes)
	case "precedes":
		return stringList(&r.Precedes)
	case "requires":
		return stringList(&r.Requires)
	case "supports":
		return stringList(&r.Supports)
	case "contradicts":
		return stringList(&r.Contradicts)
	case "supersedes":
		return stringList(&r.Supersedes)
	case "asserts":
		return unmarshalReviewValue(value, &r.Asserts)
	case "source":
		return unmarshalReviewValue(value, &r.Source)
	case "program":
		return unmarshalReviewValue(value, &r.Program)
	case "revision":
		return unmarshalReviewValue(value, &r.Revision)
	case "protected":
		return unmarshalReviewValue(value, &r.Protected)
	case "waivable":
		return unmarshalReviewValue(value, &r.Waivable)
	case "inputs":
		var ports []reviewPort
		if err := json.Unmarshal(value, &ports); err != nil {
			return err
		}
		r.Inputs = make([]Port, len(ports))
		for i, port := range ports {
			r.Inputs[i] = Port(port)
		}
		return nil
	case "outputs":
		var ports []reviewPort
		if err := json.Unmarshal(value, &ports); err != nil {
			return err
		}
		r.Outputs = make([]Port, len(ports))
		for i, port := range ports {
			r.Outputs[i] = Port(port)
		}
		return nil
	case "outputs_complete":
		return unmarshalReviewValue(value, &r.OutputsComplete)
	case "preconditions":
		return stringList(&r.Preconditions)
	case "postconditions":
		return stringList(&r.Postconditions)
	case "invariants":
		return stringList(&r.Invariants)
	case "informal_conditions":
		return stringList(&r.InformalConditions)
	case "preconditions_complete":
		return unmarshalReviewValue(value, &r.PreconditionsComplete)
	case "postconditions_complete":
		return unmarshalReviewValue(value, &r.PostconditionsComplete)
	case "invariants_complete":
		return unmarshalReviewValue(value, &r.InvariantsComplete)
	case "informal_conditions_complete":
		return unmarshalReviewValue(value, &r.InformalConditionsComplete)
	case "excepts":
		return stringList(&r.Excepts)
	case "rule_digest":
		return unmarshalReviewValue(value, &r.RuleDigest)
	case "applies_to":
		return stringList(&r.AppliesTo)
	case "conditions":
		return unmarshalReviewValue(value, &r.Conditions)
	case "waives":
		return unmarshalReviewValue(value, &r.Waives)
	case "replacement":
		return unmarshalReviewValue(value, &r.Replacement)
	case "justification":
		return unmarshalReviewValue(value, &r.Justification)
	case "authority":
		if len(value) == 0 || value.Kind() == jsontext.KindNull {
			r.Authority = nil
			return nil
		}
		var a Authority
		if err := json.Unmarshal(value, &a); err != nil {
			return err
		}
		r.Authority = &a
		return nil
	case "approval":
		if len(value) == 0 || value.Kind() == jsontext.KindNull {
			r.Approval = nil
			return nil
		}
		var a Approval
		if err := json.Unmarshal(value, &a); err != nil {
			return err
		}
		r.Approval = &a
		return nil
	default:
		return fmt.Errorf("%w: unsupported %s field %q", ErrInvalidFieldReview, r.Type, field)
	}
}

type reviewReference struct {
	Name      string         `json:"name"`
	Key       string         `json:"key"`
	Condition *string        `json:"condition,omitzero"`
	Inputs    jsontext.Value `json:"inputs,omitzero"`
}

type reviewPort struct {
	Name    string `json:"name"`
	Concept string `json:"concept,omitzero"`
}

func recordReviewValue(r *Record, field string) (jsontext.Value, error) {
	var value any
	switch field {
	case "label":
		value = r.Label
	case "definition":
		value = r.Definition
	case "statement":
		value = r.Statement
	case "classified_as":
		value = emptyStrings(r.ClassifiedAs)
	case "broader":
		value = emptyStrings(r.Broader)
	case "specializes":
		value = emptyStrings(r.Specializes)
	case "uses":
		value = emptyStrings(r.Uses)
	case "composes":
		value = emptyStrings(r.Composes)
	case "precedes":
		value = emptyStrings(r.Precedes)
	case "requires":
		value = emptyStrings(r.Requires)
	case "supports":
		value = emptyStrings(r.Supports)
	case "contradicts":
		value = emptyStrings(r.Contradicts)
	case "supersedes":
		value = emptyStrings(r.Supersedes)
	case "asserts":
		value = r.Asserts
	case "source":
		value = r.Source
	case "program":
		value = r.Program
	case "revision":
		value = r.Revision
	case "protected":
		value = r.Protected
	case "waivable":
		value = r.Waivable
	case "inputs":
		value = reviewPorts(r.Inputs)
	case "outputs":
		value = reviewPorts(r.Outputs)
	case "outputs_complete":
		value = r.OutputsComplete
	case "preconditions":
		value = emptyStrings(r.Preconditions)
	case "postconditions":
		value = emptyStrings(r.Postconditions)
	case "invariants":
		value = emptyStrings(r.Invariants)
	case "informal_conditions":
		value = emptyStrings(r.InformalConditions)
	case "preconditions_complete":
		value = r.PreconditionsComplete
	case "postconditions_complete":
		value = r.PostconditionsComplete
	case "invariants_complete":
		value = r.InvariantsComplete
	case "informal_conditions_complete":
		value = r.InformalConditionsComplete
	case "excepts":
		value = emptyStrings(r.Excepts)
	case "rule_digest":
		value = r.RuleDigest
	case "applies_to":
		value = emptyStrings(r.AppliesTo)
	case "conditions":
		value = r.Conditions
	case "waives":
		value = r.Waives
	case "replacement":
		value = r.Replacement
	case "justification":
		value = r.Justification
	case "authority":
		value = r.Authority
	case "approval":
		value = r.Approval
	default:
		return nil, fmt.Errorf("%w: unsupported %s field %q", ErrInvalidFieldReview, r.Type, field)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsontext.Value(b), nil
}

func reviewPorts(ports []Port) []reviewPort {
	out := make([]reviewPort, len(ports))
	for i, port := range ports {
		out[i] = reviewPort(port)
	}
	return out
}

func emptyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func unmarshalReviewValue(value jsontext.Value, dst any) error {
	if len(value) == 0 {
		return fmt.Errorf("%w: missing field value", ErrInvalidFieldReview)
	}
	return json.Unmarshal(value, dst)
}

func procedureReviewValue(p *Procedure, field string) (jsontext.Value, error) {
	var value any
	switch field {
	case "goal":
		value = p.Goal
	case "philosophy":
		value = p.Philosophy
	case "method":
		value = p.Method
	case "applicability":
		switch p.Applicability.Scope {
		case ScopeShared:
			value = struct {
				Shared struct{} `json:"shared"`
			}{}
		case ScopeLocal:
			value = struct {
				Repository string `json:"repository"`
			}{Repository: p.Applicability.Repository}
		default:
			return jsontext.Value("null"), nil
		}
	case "contract":
		if len(p.Contract) == 0 {
			return jsontext.Value("null"), nil
		}
		return p.Contract.Clone(), nil
	case "instructions":
		if len(p.Instructions) == 0 {
			return jsontext.Value("null"), nil
		}
		return p.Instructions.Clone(), nil
	case "references":
		refs := make([]reviewReference, len(p.References))
		for i, ref := range p.References {
			refs[i] = reviewReference{Name: ref.Name, Key: ref.Key, Condition: ref.Condition, Inputs: ref.Inputs.Clone()}
		}
		value = refs
	default:
		return nil, fmt.Errorf("%w: unknown Procedure field %q", ErrInvalidFieldReview, field)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsontext.Value(b), nil
}

func setProcedureReviewValue(p *Procedure, field string, value jsontext.Value) error {
	switch field {
	case "goal":
		return unmarshalReviewValue(value, &p.Goal)
	case "philosophy":
		return unmarshalReviewValue(value, &p.Philosophy)
	case "method":
		return unmarshalReviewValue(value, &p.Method)
	case "applicability":
		if value.Kind() == jsontext.KindNull {
			p.Applicability = Applicability{}
			return nil
		}
		var a struct {
			Shared     *struct{} `json:"shared,omitzero"`
			Repository string    `json:"repository,omitzero"`
		}
		if err := json.Unmarshal(value, &a); err != nil {
			return err
		}
		switch {
		case a.Shared != nil && a.Repository == "":
			p.Applicability = Applicability{Scope: ScopeShared}
		case a.Shared == nil && a.Repository != "":
			p.Applicability = Applicability{Scope: ScopeLocal, Repository: a.Repository}
		default:
			return fmt.Errorf("%w: applicability must be shared or local", ErrInvalidFieldReview)
		}
		return nil
	case "contract":
		p.Contract = value.Clone()
		return nil
	case "instructions":
		p.Instructions = value.Clone()
		return nil
	case "references":
		var refs []reviewReference
		if err := json.Unmarshal(value, &refs); err != nil {
			return err
		}
		p.References = make([]Reference, len(refs))
		for i, ref := range refs {
			p.References[i] = Reference{Name: ref.Name, Key: ref.Key, Condition: ref.Condition, Inputs: ref.Inputs.Clone()}
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown Procedure field %q", ErrInvalidFieldReview, field)
	}
}
func encodedFieldValue(document []byte, field string, specs []ReviewFieldSpec) (jsontext.Value, bool, error) {
	var values map[string]jsontext.Value
	if err := json.Unmarshal(document, &values); err != nil {
		return nil, false, err
	}
	if value, ok := values[field]; ok {
		return value.Clone(), true, nil
	}
	for _, spec := range specs {
		if spec.Name == field {
			return EmptyReviewValue(spec), false, nil
		}
	}
	return nil, false, fmt.Errorf("%w: unknown field %q", ErrInvalidFieldReview, field)
}

// CheckFieldReviewTransition requires a complete, matching review receipt
// for each Procedure document or typed record added or changed from prev to
// next. Whole-entity deletions need no field receipt. The same check is used
// for local publishes and each incoming Git transition.
func CheckFieldReviewTransition(prev, next *Catalog, receipt FieldReviewReceipt) error {
	var p problems
	if receipt.Version != 1 && (receipt.Version != 0 || len(receipt.Entities) != 0) {
		p.add("review.version", "must be 1")
	}
	if next == nil {
		p.add("review", "the after catalog is required")
		return p.err()
	}
	if prev == nil {
		prev = newCatalog("", map[string]*Procedure{}, map[string]*Record{})
	}
	seen := map[string]bool{}
	byEntity := map[string]EntityReview{}
	changedEntities := map[string]bool{}
	for i, entity := range receipt.Entities {
		field := fmt.Sprintf("review.entities[%d]", i)
		if !validReviewEntity(entity) {
			p.add(field, "has an invalid entity kind, key or record type")
			continue
		}
		id := reviewEntityID(entity.Kind, entity.Type, entity.Key)
		if seen[id] {
			p.add(field, "duplicates the review for %s", entity.Key)
			continue
		}
		seen[id] = true
		byEntity[id] = entity
	}

	for _, key := range next.keys {
		old, hadOld := prev.procs[key]
		cur := next.procs[key]
		changed := !hadOld || !equalDocs(old, cur)
		if !changed {
			continue
		}
		entity := EntityReview{Kind: ReviewProcedureDocument, Key: key}
		id := reviewEntityID(entity.Kind, "", key)
		changedEntities[id] = true
		review, ok := byEntity[id]
		if !ok {
			p.add("procedures["+key+"]", "requires a complete field-review receipt")
			continue
		}
		validateEntityReview(&p, review, procedureDocumentFieldSpecs, old, cur, hadOld, key)
	}

	for _, key := range next.recordKeys {
		old, hadOld := prev.records[key]
		cur := next.records[key]
		if hadOld && old.Type != cur.Type {
			p.add("records["+key+"].type", "cannot change from %s to %s; record type is system-managed", old.Type, cur.Type)
			continue
		}
		changed := !hadOld || !equalRecords(old, cur)
		if !changed {
			continue
		}
		entity := EntityReview{Kind: ReviewTypedRecord, Key: key, Type: cur.Type}
		id := reviewEntityID(entity.Kind, entity.Type, key)
		changedEntities[id] = true
		review, ok := byEntity[id]
		if !ok {
			p.add("records["+key+"]", "requires a complete field-review receipt")
			continue
		}
		validateEntityReview(&p, review, RecordFieldSpecs(cur.Type), old, cur, hadOld, key)
	}

	for id, review := range byEntity {
		switch review.Kind {
		case ReviewProcedureDocument:
			current := next.procs[review.Key]
			if current == nil {
				p.add("review.entities["+id+"]", "does not name a Procedure document in the resulting catalog")
			} else if !changedEntities[id] {
				validateEntityReview(&p, review, procedureDocumentFieldSpecs, current, current, true, review.Key)
			}
		case ReviewTypedRecord:
			record := next.records[review.Key]
			if record == nil || record.Type != review.Type {
				p.add("review.entities["+id+"]", "does not name a matching typed record in the resulting catalog")
			} else if !changedEntities[id] {
				validateEntityReview(&p, review, RecordFieldSpecs(record.Type), record, record, true, review.Key)
			}
		}
	}
	return p.err()
}

func validReviewEntity(entity EntityReview) bool {
	if !validKey(entity.Key) {
		return false
	}
	switch entity.Kind {
	case ReviewProcedureDocument:
		return entity.Type == ""
	case ReviewTypedRecord:
		return slices.Contains(RecordTypes, entity.Type)
	default:
		return false
	}
}

func reviewEntityID(kind ReviewEntityKind, typ RecordType, key string) string {
	return string(kind) + "/" + string(typ) + "/" + key
}

func validateEntityReview(p *problems, review EntityReview, specs []ReviewFieldSpec, old, next any, hasOld bool, key string) {
	fieldPrefix := "review[" + reviewEntityID(review.Kind, review.Type, key) + "]"
	if len(review.Fields) != len(specs) {
		p.add(fieldPrefix+".fields", "must decide all %d applicable fields; got %d", len(specs), len(review.Fields))
	}
	decisions := make(map[string]ReviewedField, len(review.Fields))
	for i, decision := range review.Fields {
		if _, exists := decisions[decision.Field]; exists {
			p.add(fmt.Sprintf("%s.fields[%d]", fieldPrefix, i), "duplicates field %q", decision.Field)
			continue
		}
		decisions[decision.Field] = decision
	}
	for _, spec := range specs {
		decision, ok := decisions[spec.Name]
		if !ok {
			p.add(fieldPrefix+".fields."+spec.Name, "requires an explicit operation")
			continue
		}
		before, present, err := reviewValue(review.Kind, old, spec.Name)
		if err != nil {
			p.add(fieldPrefix+".fields."+spec.Name, "%v", err)
			continue
		}
		if !present && hasOld {
			before = EmptyReviewValue(spec)
		}
		after, afterPresent, err := reviewValue(review.Kind, next, spec.Name)
		if err != nil {
			p.add(fieldPrefix+".fields."+spec.Name, "%v", err)
			continue
		}
		if !afterPresent {
			after = EmptyReviewValue(spec)
		}
		grounded, changed, err := validateReviewOperation(spec, decision, before, after, !hasOld)
		if err != nil {
			p.add(fieldPrefix+".fields."+spec.Name, "%v", err)
			continue
		}
		expectedValue, err := ReviewReceiptValue(spec, decision.Operation, before, after)
		if err != nil {
			p.add(fieldPrefix+".fields."+spec.Name, "%v", err)
			continue
		}
		if !equalReviewValue(decision.Value, expectedValue) {
			p.add(fieldPrefix+".fields."+spec.Name, "operation value does not match the entries added or removed")
		}
		if decision.Operation == ReviewReplace || changed && decision.Operation == ReviewAdd {
			detailed := FieldDecision(decision)
			if err := ValidateReviewCitations(spec, detailed, grounded); err != nil {
				p.add(fieldPrefix+".fields."+spec.Name, "%v", err)
			}
		} else if len(decision.Citations) > 0 {
			p.add(fieldPrefix+".fields."+spec.Name, "citations require added or changed content")
		}
	}
	for field := range decisions {
		if !slices.ContainsFunc(specs, func(s ReviewFieldSpec) bool { return s.Name == field }) {
			p.add(fieldPrefix+".fields."+field, "is not an applicable field")
		}
	}
}

// ReviewReceiptValue derives the list entries that an add/remove operation
// actually changes. Other operations have no separate replay value in the
// receipt; their result is the complete field in the candidate snapshot.
func ReviewReceiptValue(spec ReviewFieldSpec, operation ReviewOperation, before, after jsontext.Value) (jsontext.Value, error) {
	var values []jsontext.Value
	var ok bool
	switch operation {
	case ReviewAdd:
		values, ok = reviewListAddition(before, after, spec.Set)
	case ReviewRemove:
		values, ok = reviewListRemoval(before, after, spec.Set)
	default:
		return nil, nil
	}
	if !ok {
		return nil, fmt.Errorf("%w: cannot derive %s entries for %q from the before/after values", ErrInvalidFieldReview, operation, spec.Name)
	}
	return marshalReviewArray(values)
}

func validateReviewOperation(spec ReviewFieldSpec, decision ReviewedField, before, after jsontext.Value, isNew bool) (jsontext.Value, bool, error) {
	isList := spec.Kind == ReviewStringList || spec.Kind == ReviewObjectList
	changed := !equalReviewValue(before, after)
	switch decision.Operation {
	case ReviewKeep:
		if isNew {
			return nil, false, fmt.Errorf("%w: keep is not valid for new field %q", ErrInvalidFieldReview, spec.Name)
		}
		if changed {
			return nil, false, fmt.Errorf("%w: keep does not describe the before/after values for %q", ErrInvalidFieldReview, spec.Name)
		}
		return nil, false, nil
	case ReviewReplace:
		if isNew && len(after) == 0 {
			return nil, false, fmt.Errorf("%w: replace for new field %q needs a value", ErrInvalidFieldReview, spec.Name)
		}
		return after, changed || isNew, nil
	case ReviewClear:
		if spec.Required {
			return nil, false, fmt.Errorf("%w: required field %q cannot be cleared", ErrInvalidFieldReview, spec.Name)
		}
		if !isEmptyReviewValue(spec, after) {
			return nil, false, fmt.Errorf("%w: clear does not describe the resulting value for %q", ErrInvalidFieldReview, spec.Name)
		}
		return nil, changed || isNew, nil
	case ReviewAdd:
		if !isList {
			return nil, false, fmt.Errorf("%w: add is only valid for list field %q", ErrInvalidFieldReview, spec.Name)
		}
		added, ok := reviewListAddition(before, after, spec.Set)
		if !ok {
			return nil, false, fmt.Errorf("%w: add does not describe the before/after values for %q", ErrInvalidFieldReview, spec.Name)
		}
		if !isNew && len(added) == 0 {
			return nil, false, fmt.Errorf("%w: add must add at least one value to %q", ErrInvalidFieldReview, spec.Name)
		}
		content, err := marshalReviewArray(added)
		return content, len(added) > 0, err
	case ReviewRemove:
		if !isList || isNew {
			return nil, false, fmt.Errorf("%w: remove is only valid for an existing list field %q", ErrInvalidFieldReview, spec.Name)
		}
		removed, ok := reviewListRemoval(before, after, spec.Set)
		if !ok || len(removed) == 0 {
			return nil, false, fmt.Errorf("%w: remove does not describe the before/after values for %q", ErrInvalidFieldReview, spec.Name)
		}
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("%w: unknown operation %q", ErrInvalidFieldReview, decision.Operation)
	}
}

func isEmptyReviewValue(spec ReviewFieldSpec, value jsontext.Value) bool {
	switch spec.Kind {
	case ReviewString:
		return string(value) == `""`
	case ReviewBoolean:
		return string(value) == "false"
	case ReviewInteger:
		return string(value) == "0"
	case ReviewObject:
		return len(value) == 0 || value.Kind() == jsontext.KindNull
	case ReviewStringList, ReviewObjectList:
		items, err := reviewArrayOrEmpty(value)
		return err == nil && len(items) == 0
	default:
		return false
	}
}

func reviewListAddition(before, after jsontext.Value, set bool) ([]jsontext.Value, bool) {
	oldItems, err := reviewArrayOrEmpty(before)
	if err != nil {
		return nil, false
	}
	newItems, err := reviewArrayOrEmpty(after)
	if err != nil {
		return nil, false
	}
	if !set {
		if len(newItems) <= len(oldItems) {
			return nil, false
		}
		for i := range oldItems {
			if !equalReviewValue(oldItems[i], newItems[i]) {
				return nil, false
			}
		}
		return newItems[len(oldItems):], true
	}
	var added []jsontext.Value
	for _, item := range newItems {
		if !slices.ContainsFunc(oldItems, func(old jsontext.Value) bool { return equalReviewValue(old, item) }) {
			added = append(added, item)
		}
	}
	if len(added) == 0 {
		return nil, false
	}
	for _, old := range oldItems {
		if !slices.ContainsFunc(newItems, func(item jsontext.Value) bool { return equalReviewValue(old, item) }) {
			return nil, false
		}
	}
	return added, true
}

func reviewListRemoval(before, after jsontext.Value, set bool) ([]jsontext.Value, bool) {
	oldItems, err := reviewArrayOrEmpty(before)
	if err != nil {
		return nil, false
	}
	newItems, err := reviewArrayOrEmpty(after)
	if err != nil || len(newItems) >= len(oldItems) {
		return nil, false
	}
	if !set {
		var removed []jsontext.Value
		j := 0
		for _, old := range oldItems {
			if j < len(newItems) && equalReviewValue(old, newItems[j]) {
				j++
			} else {
				removed = append(removed, old)
			}
		}
		return removed, j == len(newItems)
	}
	for _, item := range newItems {
		if !slices.ContainsFunc(oldItems, func(old jsontext.Value) bool { return equalReviewValue(old, item) }) {
			return nil, false
		}
	}
	var removed []jsontext.Value
	for _, old := range oldItems {
		if !slices.ContainsFunc(newItems, func(item jsontext.Value) bool { return equalReviewValue(old, item) }) {
			removed = append(removed, old)
		}
	}
	return removed, true
}

func reviewValue(kind ReviewEntityKind, value any, field string) (jsontext.Value, bool, error) {
	switch v := value.(type) {
	case *Procedure:
		if v == nil {
			return nil, false, nil
		}
		return ProcedureFieldValue(v, field)
	case *Record:
		if v == nil {
			return nil, false, nil
		}
		return RecordFieldValue(v, field)
	default:
		return nil, false, fmt.Errorf("%w: unsupported entity kind %q", ErrInvalidFieldReview, kind)
	}
}

func (r FieldReviewReceipt) Encode() ([]byte, error) {
	if r.Version != 1 {
		return nil, fmt.Errorf("%w: version must be 1", ErrInvalidFieldReview)
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("encode field review: %w", err)
	}
	var compact jsontext.Value = b
	if err := compact.Compact(); err != nil {
		return nil, fmt.Errorf("compact field review: %w", err)
	}
	return bytes.Clone(compact), nil
}

func DecodeFieldReview(b []byte) (FieldReviewReceipt, error) {
	var receipt FieldReviewReceipt
	if err := json.Unmarshal(b, &receipt, json.RejectUnknownMembers(true)); err != nil {
		return FieldReviewReceipt{}, fmt.Errorf("%w: %w", ErrInvalidFieldReview, err)
	}
	canonical, err := receipt.Encode()
	if err != nil {
		return FieldReviewReceipt{}, err
	}
	if !bytes.Equal(canonical, b) {
		return FieldReviewReceipt{}, fmt.Errorf("%w: trailer value is not canonical", ErrInvalidFieldReview)
	}
	return receipt, nil
}
