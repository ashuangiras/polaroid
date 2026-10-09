package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"
)

// Store persists procedures, bindings and their histories.
//
// Implementations must make every write atomic, must never modify or delete a
// stored record, and must enforce canonical-key and binding-name uniqueness.
type Store interface {
	// CreateProcedure stores p together with its first version. It returns an
	// error wrapping ErrCanonicalKeyExists if p.CanonicalKey is already used,
	// a *MissingTargetsError if a reference's target or pinned version does
	// not exist, and the error of ExpandGraph if the stored version's graph
	// has a cycle or exceeds the limits; then nothing is stored.
	CreateProcedure(ctx context.Context, p Procedure, first Version) error

	// AppendVersion stores next if, and only if, baseVersion is the latest
	// version of next.ProcedureID; the check and the write are one atomic
	// step. Callers set next.Number to baseVersion+1. It returns a
	// *VersionConflictError when baseVersion is not the latest version, an
	// error wrapping ErrNotFound when the procedure does not exist, and the
	// reference errors of CreateProcedure.
	AppendVersion(ctx context.Context, baseVersion int, next Version) error

	// ListProcedures returns every procedure ordered by canonical key.
	ListProcedures(ctx context.Context) ([]Procedure, error)

	// History and HistoryByKey return a consistent snapshot of a procedure and
	// all of its versions, or an error wrapping ErrNotFound.
	History(ctx context.Context, id string) (History, error)
	HistoryByKey(ctx context.Context, canonicalKey string) (History, error)

	// Version returns one version, or an error wrapping ErrNotFound.
	Version(ctx context.Context, procedureID string, number int) (Version, error)

	// Resolve returns ResolveGraph for policy in c, read from a single
	// consistent snapshot.
	Resolve(ctx context.Context, procedureID string, policy VersionPolicy, c ResolutionContext) (Resolution, error)

	// CreateBinding stores b together with its first revision. It returns an
	// error wrapping ErrNotFound if b.ProcedureID does not exist,
	// ErrPinnedVersionNotFound if first pins a version the procedure does not
	// have, and ErrBindingExists if b.Repository already has a binding named
	// b.Name.
	CreateBinding(ctx context.Context, b Binding, first BindingRevision) error

	// AppendBindingRevision stores next if, and only if, baseRevision is the
	// latest revision of next.BindingID; the check and the write are one
	// atomic step. Callers set next.Number to baseRevision+1. It returns a
	// *RevisionConflictError when baseRevision is not the latest revision, an
	// error wrapping ErrNotFound when the binding does not exist, and
	// ErrPinnedVersionNotFound when next pins a version the bound procedure
	// does not have.
	AppendBindingRevision(ctx context.Context, baseRevision int, next BindingRevision) error

	// ListBindings returns the bindings of one repository ordered by name.
	ListBindings(ctx context.Context, repository string) ([]Binding, error)

	// BindingHistory returns a consistent snapshot of a binding and all of its
	// revisions, or an error wrapping ErrNotFound.
	BindingHistory(ctx context.Context, id string) (BindingHistory, error)

	// BindingRevision returns one revision, or an error wrapping ErrNotFound.
	BindingRevision(ctx context.Context, bindingID string, number int) (BindingRevision, error)

	// CreateExecution stores e and its child links. The caller has checked
	// that its version, binding revision and children exist and match; the
	// store returns a *LinkedChildError if a child already has a parent, and
	// never modifies or deletes an execution.
	CreateExecution(ctx context.Context, e Execution) error

	// Execution returns one execution, or an error wrapping ErrNotFound.
	Execution(ctx context.Context, id string) (Execution, error)

	// ListExecutions returns the executions f selects, oldest first, without
	// their Inputs and Evidence.
	ListExecutions(ctx context.Context, f ExecutionFilter) ([]Execution, error)

	// ExecutionVerification returns VerifyExecution of one execution, read
	// from a single consistent snapshot.
	ExecutionVerification(ctx context.Context, id string) (Verification, error)

	// Verifications returns ListCombinations for f, read from a single
	// consistent snapshot.
	Verifications(ctx context.Context, f VerificationFilter) ([]CombinationStatus, error)

	// CreateFeedback stores f, and never modifies or deletes a report.
	CreateFeedback(ctx context.Context, f Feedback) error

	// Feedback returns one report, or an error wrapping ErrNotFound.
	Feedback(ctx context.Context, id string) (Feedback, error)

	// ListFeedback returns every report, or only those of kind if it is not
	// empty, oldest first.
	ListFeedback(ctx context.Context, kind FeedbackKind) ([]Feedback, error)

	// Ping reports whether the store can serve requests.
	Ping(ctx context.Context) error
}

// Service implements Polaroid's procedure operations on top of a Store.
type Service struct {
	store Store
}

// NewService returns a Service that keeps its records in store.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// CreateProcedure creates a procedure with a new stable ID and stores in.Definition
// as its version 1.
func (s *Service) CreateProcedure(ctx context.Context, in NewProcedure) (History, error) {
	def, err := in.validate()
	if err != nil {
		return History{}, err
	}
	now := timestamp()
	p := Procedure{ID: uuid.NewV7().String(), CanonicalKey: in.CanonicalKey, CreatedAt: now, LatestVersion: 1}
	v := Version{ProcedureID: p.ID, Number: 1, CreatedAt: now, Definition: def}
	if err := s.store.CreateProcedure(ctx, p, v); err != nil {
		if errors.Is(err, ErrCanonicalKeyExists) {
			return History{}, fmt.Errorf("procedure %q: %w", in.CanonicalKey, ErrCanonicalKeyExists)
		}
		var missing *MissingTargetsError
		if errors.As(err, &missing) {
			return History{}, missingTargets(def.References, missing)
		}
		if gerr := graphError(err); gerr != nil {
			return History{}, gerr
		}
		return History{}, fmt.Errorf("create procedure: %w", err)
	}
	return History{Procedure: p, Versions: []Version{v}}, nil
}

// ReviseProcedure appends in.Definition as the next version of the procedure.
// It fails with a *VersionConflictError, leaving history untouched, unless
// in.BaseVersion is the procedure's latest version.
func (s *Service) ReviseProcedure(ctx context.Context, procedureID string, in Revision) (Version, error) {
	def, err := in.validate()
	if err != nil {
		return Version{}, err
	}
	v := Version{ProcedureID: procedureID, Number: in.BaseVersion + 1, CreatedAt: timestamp(), Definition: def}
	if err := s.store.AppendVersion(ctx, in.BaseVersion, v); err != nil {
		if gerr := graphError(err); gerr != nil {
			return Version{}, gerr
		}
		if errors.Is(err, ErrNotFound) {
			return Version{}, fmt.Errorf("procedure %q: %w", procedureID, ErrNotFound)
		}
		var conflict *VersionConflictError
		if errors.As(err, &conflict) {
			return Version{}, conflict
		}
		var missing *MissingTargetsError
		if errors.As(err, &missing) {
			return Version{}, missingTargets(def.References, missing)
		}
		return Version{}, fmt.Errorf("append version: %w", err)
	}
	return v, nil
}

// ListProcedures returns every procedure ordered by canonical key.
func (s *Service) ListProcedures(ctx context.Context) ([]Procedure, error) {
	procedures, err := s.store.ListProcedures(ctx)
	if err != nil {
		return nil, fmt.Errorf("list procedures: %w", err)
	}
	return procedures, nil
}

// Procedure returns a procedure and its full version history.
func (s *Service) Procedure(ctx context.Context, id string) (History, error) {
	h, err := s.store.History(ctx, id)
	if err != nil {
		return History{}, describeLookup(err, fmt.Sprintf("procedure %q", id))
	}
	return h, nil
}

// ProcedureByKey returns the procedure with the given canonical key and its
// full version history.
func (s *Service) ProcedureByKey(ctx context.Context, canonicalKey string) (History, error) {
	h, err := s.store.HistoryByKey(ctx, canonicalKey)
	if err != nil {
		return History{}, describeLookup(err, fmt.Sprintf("procedure with canonical key %q", canonicalKey))
	}
	return h, nil
}

// Version returns one version of a procedure.
func (s *Service) Version(ctx context.Context, procedureID string, number int) (Version, error) {
	if number < 1 {
		return Version{}, &ValidationError{Problems: []FieldProblem{{Field: "version", Message: "must be a version number of at least 1"}}}
	}
	v, err := s.store.Version(ctx, procedureID, number)
	if err != nil {
		return Version{}, describeLookup(err, fmt.Sprintf("procedure %q version %d", procedureID, number))
	}
	return v, nil
}

// Health reports whether the service can reach its store.
func (s *Service) Health(ctx context.Context) error {
	return s.store.Ping(ctx)
}

// missingTargets turns a store's missing-target report into field errors.
func missingTargets(refs []Reference, e *MissingTargetsError) error {
	var p problems
	for _, m := range e.Missing {
		r := refs[m.Index]
		field := fmt.Sprintf("version.references[%d]", m.Index)
		if m.Procedure {
			p.add(field+".procedure_id", fmt.Sprintf("procedure %q does not exist", r.ProcedureID))
		} else {
			p.add(field+".version_policy.pin", fmt.Sprintf("procedure %q has no version %d", r.ProcedureID, r.VersionPolicy.Pin))
		}
	}
	return p.err()
}

func describeLookup(err error, what string) error {
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return fmt.Errorf("read %s: %w", what, err)
}

// timestamp returns the current time in the form stored and served: UTC,
// without a monotonic clock reading.
func timestamp() time.Time {
	return time.Now().UTC().Round(0)
}
