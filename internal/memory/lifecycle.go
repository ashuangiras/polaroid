package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// EvidenceStatus says whether an execution may still be used as evidence
// (ADR-0035). It is separate from the recorded outcome and from structural
// verification.
type EvidenceStatus string

const (
	EvidenceValid       EvidenceStatus = "valid"
	EvidenceInvalidated EvidenceStatus = "invalidated"
)

// Invalidation records that an execution must no longer be used as evidence.
// It is stored apart from the execution, which never changes, and it is
// irreversible.
type Invalidation struct {
	ExecutionID string
	Reason      string
	Actor       string
	CreatedAt   time.Time
}

// InvalidationRequest asks to invalidate an execution: why, and who asks.
type InvalidationRequest struct {
	Reason string
	Actor  string
}

// InvalidationState is an execution's current evidence status, with its
// invalidation if it has one.
type InvalidationState struct {
	ExecutionID  string
	Invalidation *Invalidation
}

// Status is the evidence status the state stands for.
func (s InvalidationState) Status() EvidenceStatus {
	return evidenceStatus(s.Invalidation)
}

func evidenceStatus(inv *Invalidation) EvidenceStatus {
	if inv != nil {
		return EvidenceInvalidated
	}
	return EvidenceValid
}

// EvidenceStatus reports whether e may still be used as evidence.
func (e Execution) EvidenceStatus() EvidenceStatus {
	return evidenceStatus(e.Invalidation)
}

// ErrExecutionInvalidated reports that an execution is already invalidated.
var ErrExecutionInvalidated = errors.New("execution is already invalidated")

// LifecycleStatus is a procedure identity's lifecycle state (ADR-0035).
type LifecycleStatus string

const (
	LifecycleActive  LifecycleStatus = "active"
	LifecycleRetired LifecycleStatus = "retired"
)

// LifecycleEvent is one immutable change of a procedure's lifecycle state.
// Events are numbered from 1 without gaps, alternate, and start with a
// retirement.
type LifecycleEvent struct {
	ProcedureID string
	Event       int
	Status      LifecycleStatus
	Reason      string
	Actor       string
	CreatedAt   time.Time
}

// Lifecycle is a procedure's lifecycle history, oldest first.
type Lifecycle struct {
	ProcedureID string
	Events      []LifecycleEvent
}

// Status is the procedure's current state: its latest event's, or active.
func (l Lifecycle) Status() LifecycleStatus {
	if n := len(l.Events); n > 0 {
		return l.Events[n-1].Status
	}
	return LifecycleActive
}

// LatestEvent is the number of the latest event, 0 without one.
func (l Lifecycle) LatestEvent() int {
	return len(l.Events)
}

// LifecycleChange asks to retire or reactivate a procedure. BaseEvent must
// be the procedure's latest event, 0 if it has none.
type LifecycleChange struct {
	BaseEvent int
	Reason    string
	Actor     string
}

// LifecycleConflictError reports that a lifecycle change's base event is not
// the procedure's latest event. The caller should re-read the lifecycle.
type LifecycleConflictError struct {
	BaseEvent   int
	LatestEvent int
}

func (e *LifecycleConflictError) Error() string {
	return fmt.Sprintf("base event %d is not the latest lifecycle event (latest is %d)", e.BaseEvent, e.LatestEvent)
}

// ErrLifecycleUnchanged reports a retirement of a retired procedure or a
// reactivation of an active one.
var ErrLifecycleUnchanged = errors.New("lifecycle status unchanged")

// RetiredProcedureError reports a write that a retired procedure forbids: a
// version, binding or binding revision of ProcedureID, or a version whose
// References (indexes) target retired procedures. The service fills Fields
// and Message for clients.
type RetiredProcedureError struct {
	ProcedureID string
	References  []int
	Fields      []FieldProblem
	Message     string
}

func (e *RetiredProcedureError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.ProcedureID != "" {
		return fmt.Sprintf("procedure %q is retired", e.ProcedureID)
	}
	return fmt.Sprintf("%d referenced procedures are retired", len(e.References))
}

// lifecycleFilter checks a lifecycle filter value and returns it, active
// when empty.
func lifecycleFilter(p *problems, field, v string) string {
	switch v {
	case "":
		return string(LifecycleActive)
	case string(LifecycleActive), string(LifecycleRetired), "all":
		return v
	}
	p.add(field, `must be "active", "retired" or "all"`)
	return v
}

// matchesLifecycle reports whether status passes a checked lifecycle filter.
func matchesLifecycle(filter string, status LifecycleStatus) bool {
	return filter == "all" || filter == string(status)
}

// InvalidateExecution records that an execution must no longer be used as
// evidence. It never changes the execution, and it is irreversible: a second
// invalidation fails with ErrExecutionInvalidated.
func (s *Service) InvalidateExecution(ctx context.Context, executionID string, in InvalidationRequest) (InvalidationState, error) {
	var p problems
	checkText(&p, "reason", in.Reason)
	checkKey(&p, "actor", in.Actor)
	if err := p.err(); err != nil {
		return InvalidationState{}, err
	}
	inv := Invalidation{ExecutionID: executionID, Reason: in.Reason, Actor: in.Actor, CreatedAt: timestamp()}
	if err := s.store.InvalidateExecution(ctx, inv); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			return InvalidationState{}, fmt.Errorf("execution %q: %w", executionID, ErrNotFound)
		case errors.Is(err, ErrExecutionInvalidated):
			return InvalidationState{}, fmt.Errorf("execution %q: %w; record a new execution to replace it", executionID, ErrExecutionInvalidated)
		}
		return InvalidationState{}, fmt.Errorf("invalidate execution: %w", err)
	}
	return InvalidationState{ExecutionID: executionID, Invalidation: &inv}, nil
}

// ExecutionInvalidation returns an execution's evidence status.
func (s *Service) ExecutionInvalidation(ctx context.Context, executionID string) (InvalidationState, error) {
	e, err := s.Execution(ctx, executionID)
	if err != nil {
		return InvalidationState{}, err
	}
	return InvalidationState{ExecutionID: e.ID, Invalidation: e.Invalidation}, nil
}

// ProcedureLifecycle returns a procedure's lifecycle history.
func (s *Service) ProcedureLifecycle(ctx context.Context, procedureID string) (Lifecycle, error) {
	l, err := s.store.ProcedureLifecycle(ctx, procedureID)
	if err != nil {
		return Lifecycle{}, describeLookup(err, fmt.Sprintf("procedure %q", procedureID))
	}
	return l, nil
}

// RetireProcedure appends a retirement to a procedure's lifecycle.
func (s *Service) RetireProcedure(ctx context.Context, procedureID string, in LifecycleChange) (Lifecycle, error) {
	return s.changeLifecycle(ctx, procedureID, LifecycleRetired, in)
}

// ReactivateProcedure appends a reactivation to a procedure's lifecycle.
func (s *Service) ReactivateProcedure(ctx context.Context, procedureID string, in LifecycleChange) (Lifecycle, error) {
	return s.changeLifecycle(ctx, procedureID, LifecycleActive, in)
}

func (s *Service) changeLifecycle(ctx context.Context, procedureID string, status LifecycleStatus, in LifecycleChange) (Lifecycle, error) {
	var p problems
	if in.BaseEvent < 0 {
		p.add("base_event", "must be the latest lifecycle event number, 0 if there is none")
	}
	checkText(&p, "reason", in.Reason)
	checkKey(&p, "actor", in.Actor)
	if err := p.err(); err != nil {
		return Lifecycle{}, err
	}
	e := LifecycleEvent{ProcedureID: procedureID, Event: in.BaseEvent + 1, Status: status, Reason: in.Reason, Actor: in.Actor, CreatedAt: timestamp()}
	l, err := s.store.AppendLifecycleEvent(ctx, in.BaseEvent, e)
	if err != nil {
		var conflict *LifecycleConflictError
		switch {
		case errors.Is(err, ErrNotFound):
			return Lifecycle{}, fmt.Errorf("procedure %q: %w", procedureID, ErrNotFound)
		case errors.As(err, &conflict):
			return Lifecycle{}, conflict
		case errors.Is(err, ErrLifecycleUnchanged):
			return Lifecycle{}, fmt.Errorf("procedure %q is already %s: %w", procedureID, status, ErrLifecycleUnchanged)
		}
		return Lifecycle{}, fmt.Errorf("change lifecycle: %w", err)
	}
	return l, nil
}

const reactivateFirst = "reactivate it first, or use an active procedure"

// retiredWrite fills a store's *RetiredProcedureError for clients: field
// names the procedure ID field of a binding write, and refs are the
// references of a version write.
func retiredWrite(err *RetiredProcedureError, field string, refs []Reference) error {
	if len(err.References) > 0 {
		for _, i := range err.References {
			err.Fields = append(err.Fields, FieldProblem{Field: fmt.Sprintf("version.references[%d].procedure_id", i),
				Message: fmt.Sprintf("procedure %q is retired: %s", refs[i].ProcedureID, reactivateFirst)})
		}
		err.Message = fmt.Sprintf("a new version cannot reference a retired procedure (%d references): %s", len(err.References), reactivateFirst)
		return err
	}
	err.Message = fmt.Sprintf("procedure %q is retired: %s", err.ProcedureID, reactivateFirst)
	if field != "" {
		err.Fields = []FieldProblem{{Field: field, Message: "is retired"}}
	}
	return err
}
