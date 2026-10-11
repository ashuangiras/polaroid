package wire

import (
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// InvalidationRequest asks to invalidate an execution as evidence
// (ADR-0035).
type InvalidationRequest struct {
	Reason string `json:"reason" jsonschema:"why the execution must no longer be used as evidence: what it claims and why that is wrong"`
	Actor  string `json:"actor" jsonschema:"who invalidates it, in canonical-key form: lowercase letters and digits joined by single '.', '-' or '_', for example copilot.vscode"`
}

// Domain returns r in domain form.
func (r InvalidationRequest) Domain() memory.InvalidationRequest {
	return memory.InvalidationRequest{Reason: r.Reason, Actor: r.Actor}
}

// Invalidation is a stored invalidation.
type Invalidation struct {
	Reason    string    `json:"reason"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

func newInvalidation(inv *memory.Invalidation) *Invalidation {
	if inv == nil {
		return nil
	}
	return &Invalidation{Reason: inv.Reason, Actor: inv.Actor, CreatedAt: inv.CreatedAt}
}

// InvalidationState is an execution's evidence status, with its
// invalidation, omitted while it is valid.
type InvalidationState struct {
	ExecutionID    string        `json:"execution_id"`
	EvidenceStatus string        `json:"evidence_status"`
	Invalidation   *Invalidation `json:"invalidation,omitzero"`
}

func NewInvalidationState(s memory.InvalidationState) InvalidationState {
	return InvalidationState{ExecutionID: s.ExecutionID, EvidenceStatus: string(s.Status()), Invalidation: newInvalidation(s.Invalidation)}
}

// LifecycleChange asks to retire or reactivate a procedure.
type LifecycleChange struct {
	BaseEvent int    `json:"base_event" jsonschema:"the latest lifecycle event you read (lifecycle_event of the procedure, 0 if it has none); a stale base is a lifecycle_conflict"`
	Reason    string `json:"reason" jsonschema:"why the procedure's state changes"`
	Actor     string `json:"actor" jsonschema:"who changes it, in canonical-key form: lowercase letters and digits joined by single '.', '-' or '_', for example copilot.vscode"`
}

// Domain returns c in domain form.
func (c LifecycleChange) Domain() memory.LifecycleChange {
	return memory.LifecycleChange{BaseEvent: c.BaseEvent, Reason: c.Reason, Actor: c.Actor}
}

// LifecycleEvent is one change of a procedure's lifecycle state.
type LifecycleEvent struct {
	Event     int       `json:"event"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

func newLifecycleEvent(e *memory.LifecycleEvent) *LifecycleEvent {
	if e == nil {
		return nil
	}
	return &LifecycleEvent{Event: e.Event, Status: string(e.Status), Reason: e.Reason, Actor: e.Actor, CreatedAt: e.CreatedAt}
}

// Lifecycle is a procedure's lifecycle state and history. events is always
// present, [] for a procedure that was never retired.
type Lifecycle struct {
	ProcedureID string           `json:"procedure_id"`
	Status      string           `json:"status"`
	LatestEvent int              `json:"latest_event"`
	Events      []LifecycleEvent `json:"events"`
}

func NewLifecycle(l memory.Lifecycle) Lifecycle {
	body := Lifecycle{ProcedureID: l.ProcedureID, Status: string(l.Status()), LatestEvent: l.LatestEvent(), Events: make([]LifecycleEvent, len(l.Events))}
	for i := range l.Events {
		body.Events[i] = *newLifecycleEvent(&l.Events[i])
	}
	return body
}
