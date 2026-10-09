// Package wire holds the JSON shapes of Polaroid's records and errors, shared
// by the HTTP and MCP transports so that both return identical documents.
// The shapes are the contract in docs/architecture/records.md and
// http-api.md.
package wire

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// MaxRequestBytes bounds every request body, on every transport.
const MaxRequestBytes = 1 << 20

// Decode unmarshals data into dst, rejecting unknown and duplicate members.
// Its error describes the failure in terms of the document, without Go type
// names, so it can be returned to clients.
func Decode(data []byte, dst any) error {
	err := json.Unmarshal(data, dst, json.RejectUnknownMembers(true))
	if err == nil {
		return nil
	}
	var syntax *jsontext.SyntacticError
	if errors.As(err, &syntax) {
		where := ""
		if syntax.JSONPointer != "" {
			where = fmt.Sprintf(" in %q", string(syntax.JSONPointer))
		}
		return fmt.Errorf("malformed JSON at byte offset %d%s: %w", syntax.ByteOffset, where, syntax.Err)
	}
	var semantic *json.SemanticError
	if errors.As(err, &semantic) {
		where := "request body"
		if semantic.JSONPointer != "" {
			where = fmt.Sprintf("%q", string(semantic.JSONPointer))
		}
		switch {
		case errors.Is(semantic.Err, json.ErrUnknownName):
			return fmt.Errorf("unknown field %s", where)
		case semantic.Err != nil:
			return fmt.Errorf("invalid value for %s: %w", where, semantic.Err)
		default:
			return fmt.Errorf("%s has the wrong JSON type (%s)", where, kindName(semantic.JSONKind))
		}
	}
	return errors.New("malformed JSON")
}

func kindName(k jsontext.Kind) string {
	switch k {
	case jsontext.KindBeginObject:
		return "object"
	case jsontext.KindBeginArray:
		return "array"
	case jsontext.KindString:
		return "string"
	case jsontext.KindNumber:
		return "number"
	case jsontext.KindTrue, jsontext.KindFalse:
		return "boolean"
	case jsontext.KindNull:
		return "null"
	default:
		return "unknown"
	}
}

// Definition is the client-supplied content of a procedure version.
// Optional request members are tagged omitzero so that generated schemas
// do not require them; decoding ignores the tag.
type Definition struct {
	Philosophy     string         `json:"philosophy"`
	Method         string         `json:"method"`
	Contract       jsontext.Value `json:"contract"`
	Instructions   jsontext.Value `json:"instructions"`
	References     []Reference    `json:"references,omitzero"`
	RevisionReason string         `json:"revision_reason"`
}

// Domain returns d in domain form.
func (d Definition) Domain() memory.Definition {
	return memory.Definition{
		Philosophy:     d.Philosophy,
		Method:         d.Method,
		Contract:       d.Contract,
		Instructions:   d.Instructions,
		References:     references(d.References),
		RevisionReason: d.RevisionReason,
	}
}

// Reference is one named subprocedure reference, in requests and responses
// alike.
type Reference struct {
	Name          string         `json:"name"`
	ProcedureID   string         `json:"procedure_id"`
	VersionPolicy VersionPolicy  `json:"version_policy"`
	Inputs        jsontext.Value `json:"inputs"`
}

func references(body []Reference) []memory.Reference {
	if len(body) == 0 {
		return nil
	}
	refs := make([]memory.Reference, len(body))
	for i, r := range body {
		refs[i] = memory.Reference{Name: r.Name, ProcedureID: r.ProcedureID, VersionPolicy: r.VersionPolicy.Domain(), Inputs: r.Inputs}
	}
	return refs
}

func newReferences(refs []memory.Reference) []Reference {
	if len(refs) == 0 {
		return nil
	}
	body := make([]Reference, len(refs))
	for i, r := range refs {
		body[i] = Reference{Name: r.Name, ProcedureID: r.ProcedureID, VersionPolicy: NewVersionPolicy(r.VersionPolicy), Inputs: r.Inputs}
	}
	return body
}

// VersionPolicy is {"pin": N} or {"contextual": {}}. It is used both ways,
// so a response returns a policy exactly as it was accepted.
type VersionPolicy struct {
	Pin        *int              `json:"pin,omitzero"`
	Contextual *ContextualPolicy `json:"contextual,omitzero"`
}

// ContextualPolicy has no members yet; any member is rejected.
type ContextualPolicy struct{}

// Domain maps p to the domain form. Anything other than exactly one member
// becomes a zero policy, which validation rejects.
func (p VersionPolicy) Domain() memory.VersionPolicy {
	switch {
	case p.Pin != nil && p.Contextual == nil:
		return memory.VersionPolicy{Kind: memory.PolicyPin, Pin: *p.Pin}
	case p.Contextual != nil && p.Pin == nil:
		return memory.VersionPolicy{Kind: memory.PolicyContextual}
	default:
		return memory.VersionPolicy{}
	}
}

// NewVersionPolicy returns p in wire form.
func NewVersionPolicy(p memory.VersionPolicy) VersionPolicy {
	if p.Kind == memory.PolicyPin {
		pin := p.Pin
		return VersionPolicy{Pin: &pin}
	}
	return VersionPolicy{Contextual: &ContextualPolicy{}}
}

// BindingConfig is the client-supplied content of a binding revision.
type BindingConfig struct {
	Inputs         jsontext.Value `json:"inputs"`
	VersionPolicy  VersionPolicy  `json:"version_policy"`
	RevisionReason string         `json:"revision_reason"`
}

// Domain returns c in domain form.
func (c BindingConfig) Domain() memory.BindingConfig {
	return memory.BindingConfig{Inputs: c.Inputs, VersionPolicy: c.VersionPolicy.Domain(), RevisionReason: c.RevisionReason}
}

type Procedure struct {
	ID            string    `json:"id"`
	CanonicalKey  string    `json:"canonical_key"`
	CreatedAt     time.Time `json:"created_at"`
	LatestVersion int       `json:"latest_version"`
}

func NewProcedure(p memory.Procedure) Procedure {
	return Procedure{ID: p.ID, CanonicalKey: p.CanonicalKey, CreatedAt: p.CreatedAt, LatestVersion: p.LatestVersion}
}

type ProcedureList struct {
	Procedures []Procedure `json:"procedures"`
}

func NewProcedureList(procedures []memory.Procedure) ProcedureList {
	body := ProcedureList{Procedures: make([]Procedure, len(procedures))}
	for i, p := range procedures {
		body.Procedures[i] = NewProcedure(p)
	}
	return body
}

type Version struct {
	ProcedureID  string         `json:"procedure_id"`
	Version      int            `json:"version"`
	Philosophy   string         `json:"philosophy"`
	Method       string         `json:"method"`
	Contract     jsontext.Value `json:"contract"`
	Instructions jsontext.Value `json:"instructions"`
	// References is omitted when there are none, so versions stored before
	// references existed are served unchanged.
	References     []Reference `json:"references,omitzero"`
	RevisionReason string      `json:"revision_reason"`
	CreatedAt      time.Time   `json:"created_at"`
}

func NewVersion(v memory.Version) Version {
	return Version{
		ProcedureID:    v.ProcedureID,
		Version:        v.Number,
		Philosophy:     v.Philosophy,
		Method:         v.Method,
		Contract:       v.Contract,
		Instructions:   v.Instructions,
		References:     newReferences(v.References),
		RevisionReason: v.RevisionReason,
		CreatedAt:      v.CreatedAt,
	}
}

type History struct {
	ID            string    `json:"id"`
	CanonicalKey  string    `json:"canonical_key"`
	CreatedAt     time.Time `json:"created_at"`
	LatestVersion int       `json:"latest_version"`
	Versions      []Version `json:"versions"`
}

func NewHistory(h memory.History) History {
	body := History{
		ID:            h.Procedure.ID,
		CanonicalKey:  h.Procedure.CanonicalKey,
		CreatedAt:     h.Procedure.CreatedAt,
		LatestVersion: h.Procedure.LatestVersion,
		Versions:      make([]Version, len(h.Versions)),
	}
	for i, v := range h.Versions {
		body.Versions[i] = NewVersion(v)
	}
	return body
}

type Binding struct {
	ID             string    `json:"id"`
	Repository     string    `json:"repository"`
	Name           string    `json:"name"`
	ProcedureID    string    `json:"procedure_id"`
	CreatedAt      time.Time `json:"created_at"`
	LatestRevision int       `json:"latest_revision"`
}

func NewBinding(b memory.Binding) Binding {
	return Binding{
		ID:             b.ID,
		Repository:     b.Repository,
		Name:           b.Name,
		ProcedureID:    b.ProcedureID,
		CreatedAt:      b.CreatedAt,
		LatestRevision: b.LatestRevision,
	}
}

type BindingList struct {
	Bindings []Binding `json:"bindings"`
}

func NewBindingList(bindings []memory.Binding) BindingList {
	body := BindingList{Bindings: make([]Binding, len(bindings))}
	for i, b := range bindings {
		body.Bindings[i] = NewBinding(b)
	}
	return body
}

type BindingRevision struct {
	BindingID      string         `json:"binding_id"`
	Revision       int            `json:"revision"`
	Inputs         jsontext.Value `json:"inputs"`
	VersionPolicy  VersionPolicy  `json:"version_policy"`
	RevisionReason string         `json:"revision_reason"`
	CreatedAt      time.Time      `json:"created_at"`
}

func NewBindingRevision(r memory.BindingRevision) BindingRevision {
	return BindingRevision{
		BindingID:      r.BindingID,
		Revision:       r.Number,
		Inputs:         r.Inputs,
		VersionPolicy:  NewVersionPolicy(r.VersionPolicy),
		RevisionReason: r.RevisionReason,
		CreatedAt:      r.CreatedAt,
	}
}

type BindingHistory struct {
	ID             string            `json:"id"`
	Repository     string            `json:"repository"`
	Name           string            `json:"name"`
	ProcedureID    string            `json:"procedure_id"`
	CreatedAt      time.Time         `json:"created_at"`
	LatestRevision int               `json:"latest_revision"`
	Revisions      []BindingRevision `json:"revisions"`
}

func NewBindingHistory(h memory.BindingHistory) BindingHistory {
	body := BindingHistory{
		ID:             h.Binding.ID,
		Repository:     h.Binding.Repository,
		Name:           h.Binding.Name,
		ProcedureID:    h.Binding.ProcedureID,
		CreatedAt:      h.Binding.CreatedAt,
		LatestRevision: h.Binding.LatestRevision,
		Revisions:      make([]BindingRevision, len(h.Revisions)),
	}
	for i, r := range h.Revisions {
		body.Revisions[i] = NewBindingRevision(r)
	}
	return body
}

// GraphNode is one node of a composition graph. References is always
// present, empty for a leaf. verified_by is omitted without evidence.
type GraphNode struct {
	ProcedureID  string      `json:"procedure_id"`
	CanonicalKey string      `json:"canonical_key"`
	Version      int         `json:"version"`
	VerifiedBy   string      `json:"verified_by,omitzero"`
	References   []GraphEdge `json:"references"`
}

type GraphEdge struct {
	Name          string         `json:"name"`
	VersionPolicy VersionPolicy  `json:"version_policy"`
	SelectedBy    string         `json:"selected_by"`
	Inputs        jsontext.Value `json:"inputs"`
	Node          GraphNode      `json:"node"`
}

func NewGraphNode(n memory.GraphNode) GraphNode {
	body := GraphNode{
		ProcedureID:  n.ProcedureID,
		CanonicalKey: n.CanonicalKey,
		Version:      n.Version,
		VerifiedBy:   n.VerifiedBy,
		References:   make([]GraphEdge, len(n.Edges)),
	}
	for i, e := range n.Edges {
		body.References[i] = GraphEdge{
			Name:          e.Reference.Name,
			VersionPolicy: NewVersionPolicy(e.Reference.VersionPolicy),
			SelectedBy:    string(e.SelectedBy),
			Inputs:        e.Reference.Inputs,
			Node:          NewGraphNode(e.Node),
		}
	}
	return body
}

type BindingResolution struct {
	BindingID       string          `json:"binding_id"`
	BindingRevision int             `json:"binding_revision"`
	Repository      string          `json:"repository"`
	Environment     EnvironmentName `json:"environment"`
	VersionPolicy   VersionPolicy   `json:"version_policy"`
	SelectedBy      string          `json:"selected_by"`
	Graph           GraphNode       `json:"graph"`
}

func NewBindingResolution(r memory.BindingResolution) BindingResolution {
	return BindingResolution{
		BindingID:       r.Binding.ID,
		BindingRevision: r.Revision.Number,
		Repository:      r.Binding.Repository,
		Environment:     EnvironmentName{Name: r.Environment},
		VersionPolicy:   NewVersionPolicy(r.Revision.VersionPolicy),
		SelectedBy:      string(r.SelectedBy),
		Graph:           NewGraphNode(r.Graph),
	}
}

type Environment struct {
	Name       string         `json:"name"`
	Attributes jsontext.Value `json:"attributes"`
}

type EnvironmentName struct {
	Name string `json:"name"`
}

// ExecutionRecord is the client-supplied content of an execution.
type ExecutionRecord struct {
	ProcedureID     string         `json:"procedure_id"`
	Version         int            `json:"version"`
	BindingID       string         `json:"binding_id,omitzero"`
	BindingRevision int            `json:"binding_revision,omitzero"`
	Repository      string         `json:"repository"`
	Commit          string         `json:"commit"`
	Environment     Environment    `json:"environment"`
	Inputs          jsontext.Value `json:"inputs"`
	Outcome         string         `json:"outcome"`
	Evidence        jsontext.Value `json:"evidence"`
	Children        []Child        `json:"children,omitzero"`
}

// Child links a child execution to the reference it fulfilled.
type Child struct {
	Reference   string `json:"reference"`
	ExecutionID string `json:"execution_id"`
}

// Domain returns r in domain form.
func (r ExecutionRecord) Domain() memory.ExecutionRecord {
	children := make([]memory.ChildExecution, len(r.Children))
	for i, c := range r.Children {
		children[i] = memory.ChildExecution{Reference: c.Reference, ExecutionID: c.ExecutionID}
	}
	return memory.ExecutionRecord{
		ProcedureID:     r.ProcedureID,
		Version:         r.Version,
		BindingID:       r.BindingID,
		BindingRevision: r.BindingRevision,
		Repository:      r.Repository,
		Commit:          r.Commit,
		Environment:     memory.Environment{Name: r.Environment.Name, Attributes: r.Environment.Attributes},
		Inputs:          r.Inputs,
		Outcome:         memory.Outcome(r.Outcome),
		Evidence:        r.Evidence,
		Children:        children,
	}
}

// ExecutionSummary is an execution without its inputs and evidence, as
// listed. binding_id and binding_revision are omitted when no binding was used.
type ExecutionSummary struct {
	ID              string      `json:"id"`
	ProcedureID     string      `json:"procedure_id"`
	Version         int         `json:"version"`
	BindingID       string      `json:"binding_id,omitzero"`
	BindingRevision int         `json:"binding_revision,omitzero"`
	Repository      string      `json:"repository"`
	Commit          string      `json:"commit"`
	Environment     Environment `json:"environment"`
	Outcome         string      `json:"outcome"`
	CreatedAt       time.Time   `json:"created_at"`
}

func NewExecutionSummary(e memory.Execution) ExecutionSummary {
	return ExecutionSummary{
		ID:              e.ID,
		ProcedureID:     e.ProcedureID,
		Version:         e.Version,
		BindingID:       e.BindingID,
		BindingRevision: e.BindingRevision,
		Repository:      e.Repository,
		Commit:          e.Commit,
		Environment:     Environment{Name: e.Environment.Name, Attributes: e.Environment.Attributes},
		Outcome:         string(e.Outcome),
		CreatedAt:       e.CreatedAt,
	}
}

type ExecutionList struct {
	Executions []ExecutionSummary `json:"executions"`
}

func NewExecutionList(executions []memory.Execution) ExecutionList {
	body := ExecutionList{Executions: make([]ExecutionSummary, len(executions))}
	for i, e := range executions {
		body.Executions[i] = NewExecutionSummary(e)
	}
	return body
}

type Execution struct {
	ID              string         `json:"id"`
	ProcedureID     string         `json:"procedure_id"`
	Version         int            `json:"version"`
	BindingID       string         `json:"binding_id,omitzero"`
	BindingRevision int            `json:"binding_revision,omitzero"`
	Repository      string         `json:"repository"`
	Commit          string         `json:"commit"`
	Environment     Environment    `json:"environment"`
	Inputs          jsontext.Value `json:"inputs"`
	Outcome         string         `json:"outcome"`
	Evidence        jsontext.Value `json:"evidence"`
	// Children is omitted when there are none, so executions recorded before
	// child links existed are served unchanged.
	Children  []Child   `json:"children,omitzero"`
	CreatedAt time.Time `json:"created_at"`
}

func NewExecution(e memory.Execution) Execution {
	s := NewExecutionSummary(e)
	var children []Child
	for _, c := range e.Children {
		children = append(children, Child{Reference: c.Reference, ExecutionID: c.ExecutionID})
	}
	return Execution{
		ID:              s.ID,
		ProcedureID:     s.ProcedureID,
		Version:         s.Version,
		BindingID:       s.BindingID,
		BindingRevision: s.BindingRevision,
		Repository:      s.Repository,
		Commit:          s.Commit,
		Environment:     s.Environment,
		Inputs:          e.Inputs,
		Outcome:         s.Outcome,
		Evidence:        e.Evidence,
		Children:        children,
		CreatedAt:       s.CreatedAt,
	}
}

// Combination is the context an execution verifies. children is omitted
// when the version has no linked children, at any level.
type Combination struct {
	Repository  string          `json:"repository"`
	Commit      string          `json:"commit"`
	Environment EnvironmentName `json:"environment"`
	Inputs      jsontext.Value  `json:"inputs"`
	Children    []ChildVersion  `json:"children,omitzero"`
}

type ChildVersion struct {
	Reference string         `json:"reference"`
	Version   int            `json:"version"`
	Children  []ChildVersion `json:"children,omitzero"`
}

func newCombination(c memory.Combination) Combination {
	return Combination{
		Repository:  c.Repository,
		Commit:      c.Commit,
		Environment: EnvironmentName{Name: c.Environment},
		Inputs:      c.Inputs,
		Children:    newChildVersions(c.Children),
	}
}

func newChildVersions(children []memory.ChildVersion) []ChildVersion {
	var out []ChildVersion
	for _, c := range children {
		out = append(out, ChildVersion{Reference: c.Reference, Version: c.Version, Children: newChildVersions(c.Children)})
	}
	return out
}

// Verification judges one execution. problems is omitted when it is
// verified.
type Verification struct {
	ExecutionID string      `json:"execution_id"`
	ProcedureID string      `json:"procedure_id"`
	Version     int         `json:"version"`
	Verified    bool        `json:"verified"`
	Problems    []Problem   `json:"problems,omitzero"`
	Combination Combination `json:"combination"`
}

type Problem struct {
	Code        string `json:"code"`
	Reference   string `json:"reference,omitzero"`
	ExecutionID string `json:"execution_id,omitzero"`
}

func NewVerification(v memory.Verification) Verification {
	body := Verification{
		ExecutionID: v.ExecutionID,
		ProcedureID: v.ProcedureID,
		Version:     v.Version,
		Verified:    v.Verified,
		Combination: newCombination(v.Combination),
	}
	for _, p := range v.Problems {
		body.Problems = append(body.Problems, Problem{Code: string(p.Code), Reference: p.Reference, ExecutionID: p.ExecutionID})
	}
	return body
}

type CombinationStatus struct {
	Combination       Combination `json:"combination"`
	Verified          bool        `json:"verified"`
	LatestExecutionID string      `json:"latest_execution_id"`
	ExecutionIDs      []string    `json:"execution_ids"`
}

type VerificationList struct {
	Verifications []CombinationStatus `json:"verifications"`
}

func NewVerificationList(statuses []memory.CombinationStatus) VerificationList {
	body := VerificationList{Verifications: make([]CombinationStatus, len(statuses))}
	for i, s := range statuses {
		body.Verifications[i] = CombinationStatus{
			Combination:       newCombination(s.Combination),
			Verified:          s.Verified,
			LatestExecutionID: s.LatestExecutionID,
			ExecutionIDs:      s.ExecutionIDs,
		}
	}
	return body
}

// FeedbackRecord is the client-supplied content of a feedback report.
// context is optional.
type FeedbackRecord struct {
	Kind     string         `json:"kind"`
	Summary  string         `json:"summary"`
	Details  string         `json:"details"`
	Reporter string         `json:"reporter"`
	Context  jsontext.Value `json:"context,omitzero"`
}

// Domain returns r in domain form.
func (r FeedbackRecord) Domain() memory.FeedbackRecord {
	return memory.FeedbackRecord{
		Kind:     memory.FeedbackKind(r.Kind),
		Summary:  r.Summary,
		Details:  r.Details,
		Reporter: r.Reporter,
		Context:  r.Context,
	}
}

// Feedback is a stored report. context is always present, {} when none was
// given.
type Feedback struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Summary   string         `json:"summary"`
	Details   string         `json:"details"`
	Reporter  string         `json:"reporter"`
	Context   jsontext.Value `json:"context"`
	CreatedAt time.Time      `json:"created_at"`
}

func NewFeedback(f memory.Feedback) Feedback {
	return Feedback{
		ID:        f.ID,
		Kind:      string(f.Kind),
		Summary:   f.Summary,
		Details:   f.Details,
		Reporter:  f.Reporter,
		Context:   f.Context,
		CreatedAt: f.CreatedAt,
	}
}

type FeedbackList struct {
	Feedback []Feedback `json:"feedback"`
}

func NewFeedbackList(reports []memory.Feedback) FeedbackList {
	body := FeedbackList{Feedback: make([]Feedback, len(reports))}
	for i, f := range reports {
		body.Feedback[i] = NewFeedback(f)
	}
	return body
}

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Fields lists invalid input fields for invalid_request errors.
	Fields []FieldProblem `json:"fields,omitzero"`
	// LatestVersion is the procedure's latest version for version_conflict
	// errors, so the client can re-read it and revise again.
	LatestVersion int `json:"latest_version,omitzero"`
	// LatestRevision is the binding's latest revision for revision_conflict
	// errors.
	LatestRevision int `json:"latest_revision,omitzero"`
	// Cycle is the path of a reference_cycle error.
	Cycle []CycleStep `json:"cycle,omitzero"`
}

type CycleStep struct {
	ProcedureID string `json:"procedure_id"`
	Version     int    `json:"version"`
	Reference   string `json:"reference,omitzero"`
}

type FieldProblem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// InvalidRequest is an invalid_request error, naming a field when one is
// given.
func InvalidRequest(message, field, problem string) ErrorDetail {
	d := ErrorDetail{Code: "invalid_request", Message: message}
	if field != "" {
		d.Fields = []FieldProblem{{Field: field, Message: problem}}
	}
	return d
}

// Internal is the only detail ever returned for an unexpected failure.
var Internal = ErrorDetail{Code: "internal", Message: "internal error"}

// Classify maps a domain error to its error detail and the HTTP status that
// goes with it. ok is false for anything unrecognised, which callers must
// log and report as Internal.
func Classify(err error) (status int, detail ErrorDetail, ok bool) {
	var invalid *memory.ValidationError
	var conflict *memory.VersionConflictError
	var revisionConflict *memory.RevisionConflictError
	var cycle *memory.ReferenceCycleError
	var limit *memory.GraphLimitError
	switch {
	case errors.As(err, &invalid):
		detail = ErrorDetail{Code: "invalid_request", Message: invalid.Error()}
		for _, p := range invalid.Problems {
			detail.Fields = append(detail.Fields, FieldProblem(p))
		}
		return http.StatusBadRequest, detail, true
	case errors.As(err, &conflict):
		return http.StatusConflict, ErrorDetail{Code: "version_conflict", Message: conflict.Error(), LatestVersion: conflict.LatestVersion}, true
	case errors.As(err, &revisionConflict):
		return http.StatusConflict, ErrorDetail{Code: "revision_conflict", Message: revisionConflict.Error(), LatestRevision: revisionConflict.LatestRevision}, true
	case errors.As(err, &cycle):
		detail = ErrorDetail{Code: "reference_cycle", Message: cycle.Error()}
		for _, s := range cycle.Cycle {
			detail.Cycle = append(detail.Cycle, CycleStep(s))
		}
		return http.StatusConflict, detail, true
	case errors.As(err, &limit):
		return http.StatusUnprocessableEntity, ErrorDetail{Code: "graph_too_large", Message: limit.Error()}, true
	case errors.Is(err, memory.ErrCanonicalKeyExists):
		return http.StatusConflict, ErrorDetail{Code: "canonical_key_exists", Message: err.Error()}, true
	case errors.Is(err, memory.ErrBindingExists):
		return http.StatusConflict, ErrorDetail{Code: "binding_exists", Message: err.Error()}, true
	case errors.Is(err, memory.ErrNotFound):
		return http.StatusNotFound, ErrorDetail{Code: "not_found", Message: err.Error()}, true
	default:
		return http.StatusInternalServerError, Internal, false
	}
}
