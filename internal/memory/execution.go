package memory

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"regexp"
	"time"
	"uuid"
)

// Outcome is how a run ended.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
)

// Environment identifies where a run happened. Name is the identity;
// Attributes is a JSON object that is stored but never interpreted.
type Environment struct {
	Name       string
	Attributes jsontext.Value
}

// ExecutionRecord is the client-supplied content of an execution. BindingID
// is empty, and BindingRevision 0, when the run did not use a binding.
// Inputs and Evidence are JSON objects whose members are task knowledge.
type ExecutionRecord struct {
	ProcedureID     string
	Version         int
	BindingID       string
	BindingRevision int
	Repository      string
	Commit          string
	Environment     Environment
	Inputs          jsontext.Value
	Outcome         Outcome
	Evidence        jsontext.Value
}

// Execution is an immutable record of one finished run (ADR-0010).
type Execution struct {
	ID        string
	CreatedAt time.Time
	ExecutionRecord
}

// ExecutionFilter selects the executions of one procedure, optionally only
// of one version (Version > 0) or one repository (Repository != "").
type ExecutionFilter struct {
	ProcedureID string
	Version     int
	Repository  string
}

// A full SHA-1 or SHA-256 commit hash.
var commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// validate checks r and returns it in stored form.
func (r ExecutionRecord) validate() (ExecutionRecord, error) {
	var p problems
	if r.ProcedureID == "" {
		p.add("procedure_id", "is required")
	}
	if r.Version < 1 {
		p.add("version", "must be a version number of at least 1")
	}
	switch {
	case r.BindingID == "" && r.BindingRevision != 0:
		p.add("binding_id", "is required when binding_revision is set")
	case r.BindingID != "" && r.BindingRevision < 1:
		p.add("binding_revision", "must be a revision number of at least 1 when binding_id is set")
	}
	checkRepository(&p, "repository", r.Repository)
	if !commitPattern.MatchString(r.Commit) {
		p.add("commit", "must be a full commit hash: 40 or 64 lowercase hex characters")
	}
	checkKey(&p, "environment.name", r.Environment.Name)
	r.Environment.Attributes = checkObject(&p, "environment.attributes", r.Environment.Attributes)
	r.Inputs = checkObject(&p, "inputs", r.Inputs)
	if r.Outcome != OutcomeSucceeded && r.Outcome != OutcomeFailed {
		p.add("outcome", `must be "succeeded" or "failed"`)
	}
	r.Evidence = checkObject(&p, "evidence", r.Evidence)
	if string(r.Evidence) == "{}" {
		p.add("evidence", "must not be empty")
	}
	return r, p.err()
}

// RecordExecution stores in as a new execution. The procedure version must
// exist, and so must the binding revision if one is named; the binding must
// be for the same procedure and repository, and a pinned revision must pin
// the version that ran.
func (s *Service) RecordExecution(ctx context.Context, in ExecutionRecord) (Execution, error) {
	rec, err := in.validate()
	if err != nil {
		return Execution{}, err
	}
	// Versions and binding revisions are immutable and never deleted, so
	// checking them before the write cannot race with it.
	if err := s.checkExecutionTargets(ctx, rec); err != nil {
		return Execution{}, err
	}
	e := Execution{ID: uuid.NewV7().String(), CreatedAt: timestamp(), ExecutionRecord: rec}
	if err := s.store.CreateExecution(ctx, e); err != nil {
		return Execution{}, fmt.Errorf("record execution: %w", err)
	}
	return e, nil
}

func (s *Service) checkExecutionTargets(ctx context.Context, r ExecutionRecord) error {
	if _, err := s.store.Version(ctx, r.ProcedureID, r.Version); err != nil {
		if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("read version: %w", err)
		}
		// Every procedure has version 1.
		if _, err := s.store.Version(ctx, r.ProcedureID, 1); err != nil {
			return describeLookup(err, fmt.Sprintf("procedure %q", r.ProcedureID))
		}
		return &ValidationError{Problems: []FieldProblem{{Field: "version", Message: fmt.Sprintf("procedure %q has no version %d", r.ProcedureID, r.Version)}}}
	}
	if r.BindingID == "" {
		return nil
	}
	h, err := s.store.BindingHistory(ctx, r.BindingID)
	if err != nil {
		return describeLookup(err, fmt.Sprintf("binding %q", r.BindingID))
	}
	var p problems
	if h.Binding.ProcedureID != r.ProcedureID {
		p.add("binding_id", fmt.Sprintf("binds procedure %q, not %q", h.Binding.ProcedureID, r.ProcedureID))
	}
	if h.Binding.Repository != r.Repository {
		p.add("binding_id", fmt.Sprintf("belongs to repository %q, not %q", h.Binding.Repository, r.Repository))
	}
	switch {
	case r.BindingRevision > h.Binding.LatestRevision:
		p.add("binding_revision", fmt.Sprintf("binding %q has no revision %d", r.BindingID, r.BindingRevision))
	case h.Binding.ProcedureID != r.ProcedureID:
		// A pin of another procedure says nothing about this version.
	default:
		if policy := h.Revisions[r.BindingRevision-1].VersionPolicy; policy.Kind == PolicyPin && policy.Pin != r.Version {
			p.add("version", fmt.Sprintf("binding revision %d pins version %d", r.BindingRevision, policy.Pin))
		}
	}
	return p.err()
}

// Execution returns one execution.
func (s *Service) Execution(ctx context.Context, id string) (Execution, error) {
	e, err := s.store.Execution(ctx, id)
	if err != nil {
		return Execution{}, describeLookup(err, fmt.Sprintf("execution %q", id))
	}
	return e, nil
}

// ListExecutions returns the executions selected by f, oldest first.
func (s *Service) ListExecutions(ctx context.Context, f ExecutionFilter) ([]Execution, error) {
	var p problems
	if f.ProcedureID == "" {
		p.add("procedure_id", "is required")
	}
	if f.Version < 0 {
		p.add("version", "must be a version number of at least 1")
	}
	if f.Repository != "" {
		checkRepository(&p, "repository", f.Repository)
	}
	if err := p.err(); err != nil {
		return nil, err
	}
	executions, err := s.store.ListExecutions(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("list executions: %w", err)
	}
	return executions, nil
}
