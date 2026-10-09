package http

import "github.com/ashuangiras/polaroid/internal/transport/wire"

// Request envelopes of the HTTP API. Server-assigned fields (IDs, version
// numbers, timestamps) are deliberately absent, so a client that sends them
// is rejected.

type createProcedureBody struct {
	CanonicalKey string          `json:"canonical_key"`
	Origin       *wire.NewOrigin `json:"origin,omitzero"`
	Version      wire.Definition `json:"version"`
}

type reviseProcedureBody struct {
	BaseVersion int             `json:"base_version"`
	Version     wire.Definition `json:"version"`
}

type createBindingBody struct {
	Repository  string             `json:"repository"`
	Name        string             `json:"name"`
	ProcedureID string             `json:"procedure_id"`
	Revision    wire.BindingConfig `json:"revision"`
}

type reviseBindingBody struct {
	BaseRevision int                `json:"base_revision"`
	Revision     wire.BindingConfig `json:"revision"`
}

type healthBody struct {
	Status string `json:"status"`
}

// The error body is shared with the MCP transport; these names keep the
// HTTP-only protocol errors short.
type (
	errorDetail  = wire.ErrorDetail
	fieldProblem = wire.FieldProblem
)
