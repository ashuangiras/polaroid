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
	Goal           string         `json:"goal,omitzero"`
	Applicability  *Applicability `json:"applicability,omitzero"`
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
		Goal:           d.Goal,
		Applicability:  d.Applicability.Domain(),
		Contract:       d.Contract,
		Instructions:   d.Instructions,
		References:     references(d.References),
		RevisionReason: d.RevisionReason,
	}
}

// Applicability is {"shared": {}} or {"repository": "<repository id>"}
// (ADR-0020). A version without one is unspecified.
type Applicability struct {
	Shared     *SharedScope `json:"shared,omitzero"`
	Repository string       `json:"repository,omitzero"`
}

// SharedScope has no members; any member is rejected.
type SharedScope struct{}

// Domain maps a to the domain form: nil is unspecified, and anything other
// than exactly one member is invalid, which validation rejects.
func (a *Applicability) Domain() memory.Applicability {
	switch {
	case a == nil:
		return memory.Applicability{}
	case a.Shared != nil && a.Repository == "":
		return memory.Applicability{Kind: memory.ScopeShared}
	case a.Shared == nil && a.Repository != "":
		return memory.Applicability{Kind: memory.ScopeLocal, RepositoryID: a.Repository}
	default:
		return memory.InvalidApplicability
	}
}

// NewApplicability returns a in wire form, or nil when it is unspecified.
func NewApplicability(a memory.Applicability) *Applicability {
	switch a.Kind {
	case memory.ScopeShared:
		return &Applicability{Shared: &SharedScope{}}
	case memory.ScopeLocal:
		return &Applicability{Repository: a.RepositoryID}
	default:
		return nil
	}
}

// NewOrigin is a client-supplied procedure origin.
type NewOrigin struct {
	RepositoryID string `json:"repository_id"`
	Reason       string `json:"reason"`
}

// Domain returns o in domain form.
func (o NewOrigin) Domain() memory.Origin {
	return memory.Origin{RepositoryID: o.RepositoryID, Reason: o.Reason}
}

// Origin is a stored procedure origin.
type Origin struct {
	RepositoryID string    `json:"repository_id"`
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"created_at"`
}

func newOrigin(o *memory.Origin) *Origin {
	if o == nil {
		return nil
	}
	return &Origin{RepositoryID: o.RepositoryID, Reason: o.Reason, CreatedAt: o.CreatedAt}
}

// Reference is one named subprocedure reference, in requests and responses
// alike. condition is omitted for a required reference (ADR-0029).
type Reference struct {
	Name          string         `json:"name"`
	ProcedureID   string         `json:"procedure_id"`
	VersionPolicy VersionPolicy  `json:"version_policy"`
	Inputs        jsontext.Value `json:"inputs"`
	Condition     *string        `json:"condition,omitzero"`
}

func references(body []Reference) []memory.Reference {
	if len(body) == 0 {
		return nil
	}
	refs := make([]memory.Reference, len(body))
	for i, r := range body {
		refs[i] = memory.Reference{Name: r.Name, ProcedureID: r.ProcedureID, VersionPolicy: r.VersionPolicy.Domain(), Inputs: r.Inputs, Condition: r.Condition}
	}
	return refs
}

func newReferences(refs []memory.Reference) []Reference {
	if len(refs) == 0 {
		return nil
	}
	body := make([]Reference, len(refs))
	for i, r := range refs {
		body[i] = Reference{Name: r.Name, ProcedureID: r.ProcedureID, VersionPolicy: NewVersionPolicy(r.VersionPolicy), Inputs: r.Inputs, Condition: r.Condition}
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

// Procedure is a list item. scope, goal and applicability describe version
// latest_version only (ADR-0023); resolution may select another version,
// whose own scope its graph node reports. goal, applicability and origin
// are omitted when absent.
type Procedure struct {
	ID            string         `json:"id"`
	CanonicalKey  string         `json:"canonical_key"`
	CreatedAt     time.Time      `json:"created_at"`
	LatestVersion int            `json:"latest_version"`
	Scope         string         `json:"scope"`
	Goal          string         `json:"goal,omitzero"`
	Applicability *Applicability `json:"applicability,omitzero"`
	Origin        *Origin        `json:"origin,omitzero"`
}

func NewProcedure(p memory.Procedure) Procedure {
	return Procedure{
		ID: p.ID, CanonicalKey: p.CanonicalKey, CreatedAt: p.CreatedAt, LatestVersion: p.LatestVersion,
		Scope: p.Applicability.Name(), Goal: p.Goal, Applicability: NewApplicability(p.Applicability), Origin: newOrigin(p.Origin),
	}
}

// ProcedureList is a list of procedures. next is the cursor of the next
// page, omitted on the last page and without a limit (ADR-0021).
type ProcedureList struct {
	Procedures []Procedure `json:"procedures"`
	Next       string      `json:"next,omitzero"`
}

func NewProcedureList(procedures []memory.Procedure, next string) ProcedureList {
	body := ProcedureList{Procedures: make([]Procedure, len(procedures)), Next: next}
	for i, p := range procedures {
		body.Procedures[i] = NewProcedure(p)
	}
	return body
}

// Version is one version. scope is this version's own declaration: shared,
// local or unspecified (ADR-0023); applicability is omitted when unspecified.
type Version struct {
	ProcedureID   string         `json:"procedure_id"`
	Version       int            `json:"version"`
	Philosophy    string         `json:"philosophy"`
	Method        string         `json:"method"`
	Scope         string         `json:"scope"`
	Goal          string         `json:"goal,omitzero"`
	Applicability *Applicability `json:"applicability,omitzero"`
	Contract      jsontext.Value `json:"contract"`
	Instructions  jsontext.Value `json:"instructions"`
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
		Scope:          v.Applicability.Name(),
		Goal:           v.Goal,
		Applicability:  NewApplicability(v.Applicability),
		Contract:       v.Contract,
		Instructions:   v.Instructions,
		References:     newReferences(v.References),
		RevisionReason: v.RevisionReason,
		CreatedAt:      v.CreatedAt,
	}
}

type History struct {
	ID            string         `json:"id"`
	CanonicalKey  string         `json:"canonical_key"`
	CreatedAt     time.Time      `json:"created_at"`
	LatestVersion int            `json:"latest_version"`
	Scope         string         `json:"scope"`
	Goal          string         `json:"goal,omitzero"`
	Applicability *Applicability `json:"applicability,omitzero"`
	Origin        *Origin        `json:"origin,omitzero"`
	Versions      []Version      `json:"versions"`
}

func NewHistory(h memory.History) History {
	p := NewProcedure(h.Procedure)
	body := History{
		ID:            p.ID,
		CanonicalKey:  p.CanonicalKey,
		CreatedAt:     p.CreatedAt,
		LatestVersion: p.LatestVersion,
		Scope:         p.Scope,
		Goal:          p.Goal,
		Applicability: p.Applicability,
		Origin:        p.Origin,
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
	Next     string    `json:"next,omitzero"`
}

func NewBindingList(bindings []memory.Binding, next string) BindingList {
	body := BindingList{Bindings: make([]Binding, len(bindings)), Next: next}
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
// present, empty for a leaf. verified_by and selection_evidence are omitted
// without evidence, target_verification without a target (ADR-0018), and
// applicability when the version declares none (ADR-0020).
type GraphNode struct {
	ProcedureID        string              `json:"procedure_id"`
	CanonicalKey       string              `json:"canonical_key"`
	Version            int                 `json:"version"`
	Scope              string              `json:"scope"`
	Applicability      *Applicability      `json:"applicability,omitzero"`
	VerifiedBy         string              `json:"verified_by,omitzero"`
	SelectionEvidence  *SelectionEvidence  `json:"selection_evidence,omitzero"`
	TargetVerification *TargetVerification `json:"target_verification,omitzero"`
	References         []GraphEdge         `json:"references"`
}

// SelectionEvidence is the execution that selected a node, and where it ran:
// the identifier it was recorded with, and its repository_id if registered.
type SelectionEvidence struct {
	ExecutionID  string          `json:"execution_id"`
	Repository   string          `json:"repository"`
	RepositoryID string          `json:"repository_id,omitzero"`
	Commit       string          `json:"commit"`
	Environment  EnvironmentName `json:"environment"`
}

// TargetVerification is a verification-list entry for the node's selected
// combination at the target. latest_execution_id is omitted, and
// execution_ids empty, when nothing has run there.
type TargetVerification struct {
	Combination       Combination `json:"combination"`
	Verified          bool        `json:"verified"`
	LatestExecutionID string      `json:"latest_execution_id,omitzero"`
	ExecutionIDs      []string    `json:"execution_ids"`
	Undecided         []string    `json:"undecided,omitzero"`
}

// GraphEdge is a reference with the node it selected. condition is omitted
// for a required reference, decision without a target or for a required
// reference, and skipped_by unless the parent's evidence skipped it
// (ADR-0029).
type GraphEdge struct {
	Name          string         `json:"name"`
	VersionPolicy VersionPolicy  `json:"version_policy"`
	Condition     *string        `json:"condition,omitzero"`
	SelectedBy    string         `json:"selected_by"`
	SkippedBy     string         `json:"skipped_by,omitzero"`
	Decision      string         `json:"decision,omitzero"`
	Inputs        jsontext.Value `json:"inputs"`
	Node          GraphNode      `json:"node"`
}

func NewGraphNode(n memory.GraphNode) GraphNode {
	body := GraphNode{
		ProcedureID:   n.ProcedureID,
		CanonicalKey:  n.CanonicalKey,
		Version:       n.Version,
		Scope:         n.Applicability.Name(),
		Applicability: NewApplicability(n.Applicability),
		VerifiedBy:    n.VerifiedBy,
		References:    make([]GraphEdge, len(n.Edges)),
	}
	if e := n.Evidence; e != nil {
		body.SelectionEvidence = &SelectionEvidence{ExecutionID: e.ExecutionID, Repository: e.Repository, RepositoryID: e.RepositoryID, Commit: e.Commit, Environment: EnvironmentName{Name: e.Environment}}
	}
	if s := n.Target; s != nil {
		body.TargetVerification = &TargetVerification{
			Combination:       newCombination(s.Combination),
			Verified:          s.Verified,
			LatestExecutionID: s.LatestExecutionID,
			ExecutionIDs:      s.ExecutionIDs,
			Undecided:         s.Undecided,
		}
	}
	for i, e := range n.Edges {
		body.References[i] = GraphEdge{
			Name:          e.Reference.Name,
			VersionPolicy: NewVersionPolicy(e.Reference.VersionPolicy),
			Condition:     e.Reference.Condition,
			SelectedBy:    string(e.SelectedBy),
			SkippedBy:     e.SkippedBy,
			Decision:      string(e.Decision),
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
	Decisions       []Decision     `json:"decisions,omitzero"`
}

// Child links a child execution to the reference it fulfilled.
type Child struct {
	Reference   string `json:"reference"`
	ExecutionID string `json:"execution_id"`
}

// Decision is an execution's applicability decision for one conditional
// reference (ADR-0029). evidence is optional and omitted when absent.
type Decision struct {
	Reference  string         `json:"reference"`
	Applicable *bool          `json:"applicable"`
	Rationale  string         `json:"rationale"`
	Evidence   jsontext.Value `json:"evidence,omitzero"`
}

func decisions(body []Decision) []memory.ApplicabilityDecision {
	if len(body) == 0 {
		return nil
	}
	out := make([]memory.ApplicabilityDecision, len(body))
	for i, d := range body {
		out[i] = memory.ApplicabilityDecision{Reference: d.Reference, Applicable: d.Applicable, Rationale: d.Rationale, Evidence: d.Evidence}
	}
	return out
}

func newDecisions(ds []memory.ApplicabilityDecision) []Decision {
	var out []Decision
	for _, d := range ds {
		out = append(out, Decision{Reference: d.Reference, Applicable: d.Applicable, Rationale: d.Rationale, Evidence: d.Evidence})
	}
	return out
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
		Decisions:       decisions(r.Decisions),
	}
}

// ExecutionSummary is an execution without its inputs and evidence, as
// listed. binding_id and binding_revision are omitted when no binding was used.
// repository is the identifier submitted with the execution; repository_id,
// derived when read, is the repository it is registered to, omitted if it
// is not registered (ADR-0022).
type ExecutionSummary struct {
	ID              string      `json:"id"`
	ProcedureID     string      `json:"procedure_id"`
	Version         int         `json:"version"`
	BindingID       string      `json:"binding_id,omitzero"`
	BindingRevision int         `json:"binding_revision,omitzero"`
	Repository      string      `json:"repository"`
	RepositoryID    string      `json:"repository_id,omitzero"`
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
		RepositoryID:    e.Identity.ID,
		Commit:          e.Commit,
		Environment:     Environment{Name: e.Environment.Name, Attributes: e.Environment.Attributes},
		Outcome:         string(e.Outcome),
		CreatedAt:       e.CreatedAt,
	}
}

type ExecutionList struct {
	Executions []ExecutionSummary `json:"executions"`
	Next       string             `json:"next,omitzero"`
}

func NewExecutionList(executions []memory.Execution, next string) ExecutionList {
	body := ExecutionList{Executions: make([]ExecutionSummary, len(executions)), Next: next}
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
	RepositoryID    string         `json:"repository_id,omitzero"`
	Commit          string         `json:"commit"`
	Environment     Environment    `json:"environment"`
	Inputs          jsontext.Value `json:"inputs"`
	Outcome         string         `json:"outcome"`
	Evidence        jsontext.Value `json:"evidence"`
	// Children and Decisions are omitted when there are none, so executions
	// recorded before they existed are served unchanged.
	Children  []Child    `json:"children,omitzero"`
	Decisions []Decision `json:"decisions,omitzero"`
	CreatedAt time.Time  `json:"created_at"`
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
		RepositoryID:    s.RepositoryID,
		Commit:          s.Commit,
		Environment:     s.Environment,
		Inputs:          e.Inputs,
		Outcome:         s.Outcome,
		Evidence:        e.Evidence,
		Children:        children,
		Decisions:       newDecisions(e.Decisions),
		CreatedAt:       s.CreatedAt,
	}
}

// Combination is the context an execution verifies. repository is the
// canonical identifier of a registered repository, with repository_id, or
// else the unregistered identifier (ADR-0022). children is omitted when the
// version has no linked children, at any level.
type Combination struct {
	Repository   string          `json:"repository"`
	RepositoryID string          `json:"repository_id,omitzero"`
	Commit       string          `json:"commit"`
	Environment  EnvironmentName `json:"environment"`
	Inputs       jsontext.Value  `json:"inputs"`
	Children     []ChildVersion  `json:"children,omitzero"`
}

// ChildVersion is a linked child's reference and version with its own
// children, or a conditional reference decided not applicable:
// {"reference", "skipped": true} without a version (ADR-0029).
type ChildVersion struct {
	Reference string         `json:"reference"`
	Version   int            `json:"version,omitzero"`
	Skipped   bool           `json:"skipped,omitzero"`
	Children  []ChildVersion `json:"children,omitzero"`
}

func newCombination(c memory.Combination) Combination {
	return Combination{
		Repository:   c.Repository,
		RepositoryID: c.RepositoryID,
		Commit:       c.Commit,
		Environment:  EnvironmentName{Name: c.Environment},
		Inputs:       c.Inputs,
		Children:     newChildVersions(c.Children),
	}
}

func newChildVersions(children []memory.ChildVersion) []ChildVersion {
	var out []ChildVersion
	for _, c := range children {
		out = append(out, ChildVersion{Reference: c.Reference, Version: c.Version, Skipped: c.Skipped, Children: newChildVersions(c.Children)})
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
// context, subject, repository and execution_id are optional.
type FeedbackRecord struct {
	Kind        string         `json:"kind"`
	Summary     string         `json:"summary"`
	Details     string         `json:"details"`
	Reporter    string         `json:"reporter"`
	Context     jsontext.Value `json:"context,omitzero"`
	Subject     *Subject       `json:"subject,omitzero"`
	Repository  string         `json:"repository,omitzero"`
	ExecutionID string         `json:"execution_id,omitzero"`
}

// Subject is what a report is about (ADR-0021): type, and the members of
// that type.
type Subject struct {
	Type         string `json:"type"`
	RepositoryID string `json:"repository_id,omitzero"`
	ProcedureID  string `json:"procedure_id,omitzero"`
	Version      int    `json:"version,omitzero"`
	BindingID    string `json:"binding_id,omitzero"`
	Revision     int    `json:"revision,omitzero"`
	ExecutionID  string `json:"execution_id,omitzero"`
}

func (s *Subject) domain() *memory.Subject {
	if s == nil {
		return nil
	}
	return &memory.Subject{Type: memory.SubjectType(s.Type), RepositoryID: s.RepositoryID, ProcedureID: s.ProcedureID,
		Version: s.Version, BindingID: s.BindingID, Revision: s.Revision, ExecutionID: s.ExecutionID}
}

func newSubject(s *memory.Subject) *Subject {
	if s == nil {
		return nil
	}
	return &Subject{Type: string(s.Type), RepositoryID: s.RepositoryID, ProcedureID: s.ProcedureID,
		Version: s.Version, BindingID: s.BindingID, Revision: s.Revision, ExecutionID: s.ExecutionID}
}

// Domain returns r in domain form.
func (r FeedbackRecord) Domain() memory.FeedbackRecord {
	return memory.FeedbackRecord{
		Kind:        memory.FeedbackKind(r.Kind),
		Summary:     r.Summary,
		Details:     r.Details,
		Reporter:    r.Reporter,
		Context:     r.Context,
		Subject:     r.Subject.domain(),
		Repository:  r.Repository,
		ExecutionID: r.ExecutionID,
	}
}

// Feedback is a stored report. context is always present, {} when none was
// given; subject, repository and execution_id are omitted when absent.
type Feedback struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Summary     string         `json:"summary"`
	Details     string         `json:"details"`
	Reporter    string         `json:"reporter"`
	Context     jsontext.Value `json:"context"`
	Subject     *Subject       `json:"subject,omitzero"`
	Repository  string         `json:"repository,omitzero"`
	ExecutionID string         `json:"execution_id,omitzero"`
	CreatedAt   time.Time      `json:"created_at"`
}

func NewFeedback(f memory.Feedback) Feedback {
	return Feedback{
		ID:          f.ID,
		Kind:        string(f.Kind),
		Summary:     f.Summary,
		Details:     f.Details,
		Reporter:    f.Reporter,
		Context:     f.Context,
		Subject:     newSubject(f.Subject),
		Repository:  f.Repository,
		ExecutionID: f.ExecutionID,
		CreatedAt:   f.CreatedAt,
	}
}

type FeedbackList struct {
	Feedback []Feedback `json:"feedback"`
	Next     string     `json:"next,omitzero"`
}

func NewFeedbackList(reports []memory.Feedback, next string) FeedbackList {
	body := FeedbackList{Feedback: make([]Feedback, len(reports)), Next: next}
	for i, f := range reports {
		body.Feedback[i] = NewFeedback(f)
	}
	return body
}

// NewRepository is a request to register a repository (ADR-0019).
type NewRepository struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
}

// Domain returns r in domain form.
func (r NewRepository) Domain() memory.NewRepository {
	return memory.NewRepository{Identifier: r.Identifier, Name: r.Name}
}

// NewAlias is a request to add an alias to a repository.
type NewAlias struct {
	Identifier string `json:"identifier"`
	Reason     string `json:"reason"`
}

// Domain returns a in domain form.
func (a NewAlias) Domain() memory.NewAlias {
	return memory.NewAlias{Identifier: a.Identifier, Reason: a.Reason}
}

// Repository is a registered repository. aliases is always present, oldest
// first.
type Repository struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Identifier string            `json:"identifier"`
	Aliases    []RepositoryAlias `json:"aliases"`
	CreatedAt  time.Time         `json:"created_at"`
}

type RepositoryAlias struct {
	Identifier string    `json:"identifier"`
	Reason     string    `json:"reason"`
	CreatedAt  time.Time `json:"created_at"`
}

func NewRepositoryBody(r memory.Repository) Repository {
	body := Repository{ID: r.ID, Name: r.Name, Identifier: r.Identifier, Aliases: make([]RepositoryAlias, len(r.Aliases)), CreatedAt: r.CreatedAt}
	for i, a := range r.Aliases {
		body.Aliases[i] = RepositoryAlias{Identifier: a.Identifier, Reason: a.Reason, CreatedAt: a.CreatedAt}
	}
	return body
}

type RepositoryList struct {
	Repositories []Repository `json:"repositories"`
	Next         string       `json:"next,omitzero"`
}

func NewRepositoryList(repos []memory.Repository, next string) RepositoryList {
	body := RepositoryList{Repositories: make([]Repository, len(repos)), Next: next}
	for i, r := range repos {
		body.Repositories[i] = NewRepositoryBody(r)
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
	case errors.Is(err, memory.ErrIdentifierExists):
		return http.StatusConflict, ErrorDetail{Code: "repository_identifier_exists", Message: err.Error()}, true
	case errors.Is(err, memory.ErrOriginExists):
		return http.StatusConflict, ErrorDetail{Code: "origin_exists", Message: err.Error()}, true
	case errors.Is(err, memory.ErrNotFound):
		return http.StatusNotFound, ErrorDetail{Code: "not_found", Message: err.Error()}, true
	default:
		return http.StatusInternalServerError, Internal, false
	}
}
