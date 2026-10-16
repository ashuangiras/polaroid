// Package knowledgeops implements Polaroid's Git-native knowledge
// operations (ADR-0048): the agent-facing interface (discover, read,
// feedback) and the operator-facing interface (status, publish, fetch,
// validate, activate, push, activations, reconcile), both built only on
// internal/knowledge, internal/knowledgesync, internal/gitcore and
// internal/memory (for feedback). It adds no new persistence or versioning
// of its own.
package knowledgeops

import (
	"errors"
	"net/http"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledgesync"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// classify maps a Git-core, knowledge-kernel or synchronization error to a
// wire.KnowledgeError, so a transport that only knows wire.Classify can map
// it without depending on any of those packages. Anything unrecognised,
// including storage corruption, is returned unchanged for the caller to log
// and report as an internal error.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var invalid *knowledge.ValidationError
	switch {
	case errors.As(err, &invalid):
		detail := wire.ErrorDetail{Code: "invalid_request", Message: invalid.Error()}
		for _, p := range invalid.Problems {
			detail.Fields = append(detail.Fields, wire.FieldProblem{Field: p.Field, Message: p.Message})
		}
		return wire.NewKnowledgeError(http.StatusBadRequest, detail)
	case errors.Is(err, knowledge.ErrInvalid):
		return wire.NewKnowledgeError(http.StatusBadRequest, wire.ErrorDetail{Code: "invalid_request", Message: err.Error()})
	case errors.Is(err, knowledge.ErrNotApplicable):
		return wire.NewKnowledgeError(http.StatusNotFound, wire.ErrorDetail{Code: "not_applicable", Message: err.Error()})
	case errors.Is(err, knowledge.ErrNotFound), errors.Is(err, gitcore.ErrNotFound), errors.Is(err, memory.ErrNotFound):
		return wire.NewKnowledgeError(http.StatusNotFound, wire.ErrorDetail{Code: "not_found", Message: err.Error()})
	case errors.Is(err, memory.ErrInvalidOutcome):
		return wire.NewKnowledgeError(http.StatusBadRequest, wire.ErrorDetail{Code: "invalid_request", Message: err.Error()})
	case errors.Is(err, knowledge.ErrStale):
		return wire.NewKnowledgeError(http.StatusConflict, wire.ErrorDetail{Code: "stale_snapshot", Message: err.Error()})
	case errors.Is(err, knowledge.ErrDiverged), errors.Is(err, gitcore.ErrDiverged):
		return wire.NewKnowledgeError(http.StatusConflict, wire.ErrorDetail{Code: "diverged", Message: err.Error()})
	case errors.Is(err, gitcore.ErrUnauthorized):
		return wire.NewKnowledgeError(http.StatusUnauthorized, wire.ErrorDetail{Code: "unauthorized", Message: err.Error()})
	case errors.Is(err, gitcore.ErrTooLarge):
		return wire.NewKnowledgeError(http.StatusRequestEntityTooLarge, wire.ErrorDetail{Code: "too_large", Message: err.Error()})
	case errors.Is(err, gitcore.ErrBehind):
		return wire.NewKnowledgeError(http.StatusConflict, wire.ErrorDetail{Code: "behind", Message: err.Error()})
	case errors.Is(err, knowledgesync.ErrUnknownSource):
		return wire.NewKnowledgeError(http.StatusNotFound, wire.ErrorDetail{Code: "not_found", Message: err.Error()})
	case errors.Is(err, knowledgesync.ErrNotPermitted):
		return wire.NewKnowledgeError(http.StatusForbidden, wire.ErrorDetail{Code: "forbidden", Message: err.Error()})
	case errors.Is(err, knowledgesync.ErrNotEligible):
		return wire.NewKnowledgeError(http.StatusConflict, wire.ErrorDetail{Code: "not_eligible", Message: err.Error()})
	default:
		return err
	}
}

// requiredField is an invalid_request error naming a required field.
func requiredField(field string) error {
	return classify(&knowledge.ValidationError{Problems: []knowledge.Problem{{Field: field, Message: "is required"}}})
}

// invalidField is an invalid_request error naming field's problem.
func invalidField(field, message string) error {
	return classify(&knowledge.ValidationError{Problems: []knowledge.Problem{{Field: field, Message: message}}})
}
