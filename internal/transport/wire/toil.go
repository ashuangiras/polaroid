package wire

import (
	"encoding/json/jsontext"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// ToilContent is the client-supplied content of a toil revision (ADR-0034).
// Optional members are tagged omitzero so that generated schemas do not
// require them; decoding ignores the tag.
type ToilContent struct {
	Activity        string           `json:"activity"`
	Applicability   *Applicability   `json:"applicability"`
	Sources         []ToilSource     `json:"sources"`
	Observations    []ToilEvidence   `json:"observations,omitzero"`
	Assessment      jsontext.Value   `json:"assessment"`
	Proposal        jsontext.Value   `json:"proposal,omitzero"`
	Status          string           `json:"status"`
	StatusRationale string           `json:"status_rationale"`
	Replacement     *ToilReplacement `json:"replacement,omitzero"`
	Validation      *ToilValidation  `json:"validation,omitzero"`
	RevisionReason  string           `json:"revision_reason"`
	Author          string           `json:"author"`
}

// ToilSource is an exact procedure version, with optional declared steps.
type ToilSource struct {
	ProcedureID string   `json:"procedure_id"`
	Version     int      `json:"version"`
	Steps       []string `json:"steps,omitzero"`
}

// ToilEvidence cites an execution and says what it shows.
type ToilEvidence struct {
	ExecutionID string `json:"execution_id"`
	Note        string `json:"note"`
}

// ExternalReference is a declared reference: stored, never fetched.
type ExternalReference struct {
	URI  string `json:"uri"`
	Note string `json:"note,omitzero"`
}

// ToilReplacement identifies what replaces the activity.
type ToilReplacement struct {
	Summary    string              `json:"summary"`
	References []ExternalReference `json:"references"`
}

// ToilValidation is the evidence offered for a replacement: executions,
// declared references, or both.
type ToilValidation struct {
	Rationale  string              `json:"rationale"`
	Executions []ToilEvidence      `json:"executions,omitzero"`
	References []ExternalReference `json:"references,omitzero"`
}

// Domain returns c in domain form.
func (c ToilContent) Domain() memory.ToilContent {
	out := memory.ToilContent{
		Activity:        c.Activity,
		Applicability:   c.Applicability.Domain(),
		Assessment:      c.Assessment,
		Proposal:        c.Proposal,
		Status:          memory.ToilStatus(c.Status),
		StatusRationale: c.StatusRationale,
		RevisionReason:  c.RevisionReason,
		Author:          c.Author,
	}
	for _, s := range c.Sources {
		out.Sources = append(out.Sources, memory.ToilSource(s))
	}
	out.Observations = toilEvidence(c.Observations)
	if c.Replacement != nil {
		out.Replacement = &memory.ToilReplacement{Summary: c.Replacement.Summary, References: externalReferences(c.Replacement.References)}
	}
	if c.Validation != nil {
		out.Validation = &memory.ToilValidation{Rationale: c.Validation.Rationale,
			Executions: toilEvidence(c.Validation.Executions), References: externalReferences(c.Validation.References)}
	}
	return out
}

func toilEvidence(in []ToilEvidence) []memory.ToilEvidence {
	var out []memory.ToilEvidence
	for _, e := range in {
		out = append(out, memory.ToilEvidence(e))
	}
	return out
}

func externalReferences(in []ExternalReference) []memory.ExternalReference {
	var out []memory.ExternalReference
	for _, r := range in {
		out = append(out, memory.ExternalReference(r))
	}
	return out
}

// ToilRevisionBody is a stored toil revision. observations is always
// present; proposal, replacement and validation are omitted when absent,
// and a validation always lists executions and references.
type ToilRevisionBody struct {
	ToilItemID      string                `json:"toil_item_id"`
	Revision        int                   `json:"revision"`
	Activity        string                `json:"activity"`
	Scope           string                `json:"scope"`
	Applicability   *Applicability        `json:"applicability"`
	Sources         []ToilSource          `json:"sources"`
	Observations    []ToilEvidence        `json:"observations"`
	Assessment      jsontext.Value        `json:"assessment"`
	Proposal        jsontext.Value        `json:"proposal,omitzero"`
	Status          string                `json:"status"`
	StatusRationale string                `json:"status_rationale"`
	Replacement     *ToilReplacement      `json:"replacement,omitzero"`
	Validation      *ToilValidationRecord `json:"validation,omitzero"`
	RevisionReason  string                `json:"revision_reason"`
	Author          string                `json:"author"`
	CreatedAt       time.Time             `json:"created_at"`
}

// ToilValidationRecord is a stored validation.
type ToilValidationRecord struct {
	Rationale  string              `json:"rationale"`
	Executions []ToilEvidence      `json:"executions"`
	References []ExternalReference `json:"references"`
}

func NewToilRevision(r memory.ToilRevision) ToilRevisionBody {
	body := ToilRevisionBody{
		ToilItemID:      r.ToilItemID,
		Revision:        r.Number,
		Activity:        r.Activity,
		Scope:           r.Applicability.Name(),
		Applicability:   NewApplicability(r.Applicability),
		Sources:         make([]ToilSource, len(r.Sources)),
		Observations:    newToilEvidence(r.Observations),
		Assessment:      r.Assessment,
		Proposal:        r.Proposal,
		Status:          string(r.Status),
		StatusRationale: r.StatusRationale,
		RevisionReason:  r.RevisionReason,
		Author:          r.Author,
		CreatedAt:       r.CreatedAt,
	}
	for i, s := range r.Sources {
		body.Sources[i] = ToilSource(s)
	}
	if r.Replacement != nil {
		body.Replacement = &ToilReplacement{Summary: r.Replacement.Summary, References: newExternalReferences(r.Replacement.References)}
	}
	if r.Validation != nil {
		body.Validation = &ToilValidationRecord{Rationale: r.Validation.Rationale,
			Executions: newToilEvidence(r.Validation.Executions), References: newExternalReferences(r.Validation.References)}
	}
	return body
}

func newToilEvidence(in []memory.ToilEvidence) []ToilEvidence {
	out := make([]ToilEvidence, len(in))
	for i, e := range in {
		out[i] = ToilEvidence(e)
	}
	return out
}

func newExternalReferences(in []memory.ExternalReference) []ExternalReference {
	out := make([]ExternalReference, len(in))
	for i, r := range in {
		out[i] = ExternalReference(r)
	}
	return out
}

// ToilItem is a toil item's identity with a summary of its latest revision.
type ToilItem struct {
	ID             string         `json:"id"`
	CanonicalKey   string         `json:"canonical_key"`
	CreatedAt      time.Time      `json:"created_at"`
	LatestRevision int            `json:"latest_revision"`
	Activity       string         `json:"activity"`
	Status         string         `json:"status"`
	Scope          string         `json:"scope"`
	Applicability  *Applicability `json:"applicability"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

func NewToilItem(t memory.ToilItem) ToilItem {
	return ToilItem{
		ID:             t.ID,
		CanonicalKey:   t.CanonicalKey,
		CreatedAt:      t.CreatedAt,
		LatestRevision: t.LatestRevision,
		Activity:       t.Activity,
		Status:         string(t.Status),
		Scope:          t.Applicability.Name(),
		Applicability:  NewApplicability(t.Applicability),
		UpdatedAt:      t.UpdatedAt,
	}
}

type ToilItemList struct {
	ToilItems []ToilItem `json:"toil_items"`
	Next      string     `json:"next,omitzero"`
}

func NewToilItemList(items []memory.ToilItem, next string) ToilItemList {
	body := ToilItemList{ToilItems: make([]ToilItem, len(items)), Next: next}
	for i, t := range items {
		body.ToilItems[i] = NewToilItem(t)
	}
	return body
}

// ToilHistory is a toil item's summary with all of its revisions, oldest
// first.
type ToilHistory struct {
	ToilItem
	Revisions []ToilRevisionBody `json:"revisions"`
}

func NewToilHistory(h memory.ToilHistory) ToilHistory {
	body := ToilHistory{ToilItem: NewToilItem(h.Item), Revisions: make([]ToilRevisionBody, len(h.Revisions))}
	for i, r := range h.Revisions {
		body.Revisions[i] = NewToilRevision(r)
	}
	return body
}
