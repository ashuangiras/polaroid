package http

import (
	"encoding/json/jsontext"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// Request bodies. Server-assigned fields (IDs, version numbers, timestamps)
// are deliberately absent, so a client that sends them is rejected.

type definitionBody struct {
	Philosophy     string         `json:"philosophy"`
	Method         string         `json:"method"`
	Contract       jsontext.Value `json:"contract"`
	Instructions   jsontext.Value `json:"instructions"`
	RevisionReason string         `json:"revision_reason"`
}

func (d definitionBody) definition() memory.Definition {
	return memory.Definition{
		Philosophy:     d.Philosophy,
		Method:         d.Method,
		Contract:       d.Contract,
		Instructions:   d.Instructions,
		RevisionReason: d.RevisionReason,
	}
}

type createProcedureBody struct {
	CanonicalKey string         `json:"canonical_key"`
	Version      definitionBody `json:"version"`
}

type reviseProcedureBody struct {
	BaseVersion int            `json:"base_version"`
	Version     definitionBody `json:"version"`
}

// versionPolicyBody is {"pin": N} or {"contextual": {}}. It is used both
// ways, so a response returns a policy exactly as it was accepted.
type versionPolicyBody struct {
	Pin        *int                  `json:"pin,omitzero"`
	Contextual *contextualPolicyBody `json:"contextual,omitzero"`
}

// contextualPolicyBody has no members yet; any member is rejected.
type contextualPolicyBody struct{}

// policy maps the wire form to the domain form. Anything other than exactly
// one member becomes a zero policy, which validation rejects.
func (p versionPolicyBody) policy() memory.VersionPolicy {
	switch {
	case p.Pin != nil && p.Contextual == nil:
		return memory.VersionPolicy{Kind: memory.PolicyPin, Pin: *p.Pin}
	case p.Contextual != nil && p.Pin == nil:
		return memory.VersionPolicy{Kind: memory.PolicyContextual}
	default:
		return memory.VersionPolicy{}
	}
}

func newVersionPolicyBody(p memory.VersionPolicy) versionPolicyBody {
	if p.Kind == memory.PolicyPin {
		pin := p.Pin
		return versionPolicyBody{Pin: &pin}
	}
	return versionPolicyBody{Contextual: &contextualPolicyBody{}}
}

type bindingConfigBody struct {
	Inputs         jsontext.Value    `json:"inputs"`
	VersionPolicy  versionPolicyBody `json:"version_policy"`
	RevisionReason string            `json:"revision_reason"`
}

func (c bindingConfigBody) config() memory.BindingConfig {
	return memory.BindingConfig{
		Inputs:         c.Inputs,
		VersionPolicy:  c.VersionPolicy.policy(),
		RevisionReason: c.RevisionReason,
	}
}

type createBindingBody struct {
	Repository  string            `json:"repository"`
	Name        string            `json:"name"`
	ProcedureID string            `json:"procedure_id"`
	Revision    bindingConfigBody `json:"revision"`
}

type reviseBindingBody struct {
	BaseRevision int               `json:"base_revision"`
	Revision     bindingConfigBody `json:"revision"`
}

// Response bodies.

type procedureBody struct {
	ID            string    `json:"id"`
	CanonicalKey  string    `json:"canonical_key"`
	CreatedAt     time.Time `json:"created_at"`
	LatestVersion int       `json:"latest_version"`
}

func newProcedureBody(p memory.Procedure) procedureBody {
	return procedureBody{ID: p.ID, CanonicalKey: p.CanonicalKey, CreatedAt: p.CreatedAt, LatestVersion: p.LatestVersion}
}

type versionBody struct {
	ProcedureID    string         `json:"procedure_id"`
	Version        int            `json:"version"`
	Philosophy     string         `json:"philosophy"`
	Method         string         `json:"method"`
	Contract       jsontext.Value `json:"contract"`
	Instructions   jsontext.Value `json:"instructions"`
	RevisionReason string         `json:"revision_reason"`
	CreatedAt      time.Time      `json:"created_at"`
}

func newVersionBody(v memory.Version) versionBody {
	return versionBody{
		ProcedureID:    v.ProcedureID,
		Version:        v.Number,
		Philosophy:     v.Philosophy,
		Method:         v.Method,
		Contract:       v.Contract,
		Instructions:   v.Instructions,
		RevisionReason: v.RevisionReason,
		CreatedAt:      v.CreatedAt,
	}
}

type historyBody struct {
	ID            string        `json:"id"`
	CanonicalKey  string        `json:"canonical_key"`
	CreatedAt     time.Time     `json:"created_at"`
	LatestVersion int           `json:"latest_version"`
	Versions      []versionBody `json:"versions"`
}

func newHistoryBody(h memory.History) historyBody {
	body := historyBody{
		ID:            h.Procedure.ID,
		CanonicalKey:  h.Procedure.CanonicalKey,
		CreatedAt:     h.Procedure.CreatedAt,
		LatestVersion: h.Procedure.LatestVersion,
		Versions:      make([]versionBody, len(h.Versions)),
	}
	for i, v := range h.Versions {
		body.Versions[i] = newVersionBody(v)
	}
	return body
}

type procedureListBody struct {
	Procedures []procedureBody `json:"procedures"`
}

type bindingBody struct {
	ID             string    `json:"id"`
	Repository     string    `json:"repository"`
	Name           string    `json:"name"`
	ProcedureID    string    `json:"procedure_id"`
	CreatedAt      time.Time `json:"created_at"`
	LatestRevision int       `json:"latest_revision"`
}

func newBindingBody(b memory.Binding) bindingBody {
	return bindingBody{
		ID:             b.ID,
		Repository:     b.Repository,
		Name:           b.Name,
		ProcedureID:    b.ProcedureID,
		CreatedAt:      b.CreatedAt,
		LatestRevision: b.LatestRevision,
	}
}

type bindingRevisionBody struct {
	BindingID      string            `json:"binding_id"`
	Revision       int               `json:"revision"`
	Inputs         jsontext.Value    `json:"inputs"`
	VersionPolicy  versionPolicyBody `json:"version_policy"`
	RevisionReason string            `json:"revision_reason"`
	CreatedAt      time.Time         `json:"created_at"`
}

func newBindingRevisionBody(r memory.BindingRevision) bindingRevisionBody {
	return bindingRevisionBody{
		BindingID:      r.BindingID,
		Revision:       r.Number,
		Inputs:         r.Inputs,
		VersionPolicy:  newVersionPolicyBody(r.VersionPolicy),
		RevisionReason: r.RevisionReason,
		CreatedAt:      r.CreatedAt,
	}
}

type bindingHistoryBody struct {
	ID             string                `json:"id"`
	Repository     string                `json:"repository"`
	Name           string                `json:"name"`
	ProcedureID    string                `json:"procedure_id"`
	CreatedAt      time.Time             `json:"created_at"`
	LatestRevision int                   `json:"latest_revision"`
	Revisions      []bindingRevisionBody `json:"revisions"`
}

func newBindingHistoryBody(h memory.BindingHistory) bindingHistoryBody {
	body := bindingHistoryBody{
		ID:             h.Binding.ID,
		Repository:     h.Binding.Repository,
		Name:           h.Binding.Name,
		ProcedureID:    h.Binding.ProcedureID,
		CreatedAt:      h.Binding.CreatedAt,
		LatestRevision: h.Binding.LatestRevision,
		Revisions:      make([]bindingRevisionBody, len(h.Revisions)),
	}
	for i, r := range h.Revisions {
		body.Revisions[i] = newBindingRevisionBody(r)
	}
	return body
}

type bindingListBody struct {
	Bindings []bindingBody `json:"bindings"`
}

type healthBody struct {
	Status string `json:"status"`
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Fields lists invalid input fields for invalid_request errors.
	Fields []fieldProblem `json:"fields,omitzero"`
	// LatestVersion is the procedure's latest version for version_conflict
	// errors, so the client can re-read it and revise again.
	LatestVersion int `json:"latest_version,omitzero"`
	// LatestRevision is the binding's latest revision for revision_conflict
	// errors.
	LatestRevision int `json:"latest_revision,omitzero"`
}

type fieldProblem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}
