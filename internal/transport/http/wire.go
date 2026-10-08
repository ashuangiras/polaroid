package http

import (
	"encoding/json/jsontext"
	"time"

	"example.com/polaroid/internal/memory"
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
}

type fieldProblem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}
