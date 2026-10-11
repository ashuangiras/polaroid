package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"
)

// Store persists procedures, bindings and their histories.
//
// Implementations must make every write atomic, must never modify or delete a
// stored record, and must enforce canonical-key and binding-name uniqueness.
type Store interface {
	// CreateProcedure stores p together with its first version, and p.Origin
	// if set. It returns an error wrapping ErrCanonicalKeyExists if
	// p.CanonicalKey is already used, a *MissingTargetsError if a reference's
	// target or pinned version does not exist, a *ValidationError if a named
	// repository is not registered or CheckReferenceScopes fails, and the
	// error of ExpandGraph if the stored version's graph has a cycle or
	// exceeds the limits; then nothing is stored.
	CreateProcedure(ctx context.Context, p Procedure, first Version) error

	// AppendVersion stores next if, and only if, baseVersion is the latest
	// version of next.ProcedureID; the check and the write are one atomic
	// step. Callers set next.Number to baseVersion+1. It returns a
	// *VersionConflictError when baseVersion is not the latest version, an
	// error wrapping ErrNotFound when the procedure does not exist, and the
	// reference errors of CreateProcedure.
	AppendVersion(ctx context.Context, baseVersion int, next Version) error

	// ListProcedures returns the procedures f selects ordered by canonical
	// key, with one extra item when f.Page.Limit is set and more exist. With
	// f.Snapshot it reads only what the boundary includes, and returns
	// ErrInvalidSnapshot for a boundary ProcedureSnapshot did not issue.
	ListProcedures(ctx context.Context, f ProcedureFilter) ([]Procedure, error)

	// ProcedureSnapshot returns the current boundary of the procedure list.
	ProcedureSnapshot(ctx context.Context) (Snapshot, error)

	// LatestVersions returns the latest version of every procedure, ordered
	// by canonical key, read in one statement. A non-empty repository keeps
	// only versions applicable there, as ProcedureFilter.Repository does.
	LatestVersions(ctx context.Context, repository string) ([]LatestVersion, error)

	// SetOrigin records a procedure's origin. It returns an error wrapping
	// ErrNotFound if the procedure does not exist, ErrOriginExists if an
	// origin is already recorded, and a *ValidationError if the repository
	// is not registered.
	SetOrigin(ctx context.Context, procedureID string, o Origin) error

	// CreateRepository stores r with its canonical identifier, setting
	// r.CreatedAt strictly after every stored repository's. It returns
	// ErrIdentifierExists if the identifier is registered.
	CreateRepository(ctx context.Context, r *Repository) error

	// AddRepositoryAlias stores a as an alias of a repository. It returns an
	// error wrapping ErrNotFound if the repository does not exist,
	// ErrIdentifierExists if the identifier is registered, and a
	// *BindingNameConflictError if a binding name would be duplicated.
	AddRepositoryAlias(ctx context.Context, repositoryID string, a RepositoryAlias) error

	// Repository and RepositoryByIdentifier return a repository with its
	// aliases, oldest first, or an error wrapping ErrNotFound.
	Repository(ctx context.Context, id string) (Repository, error)
	RepositoryByIdentifier(ctx context.Context, identifier string) (Repository, error)

	// ListRepositories returns repositories after the position, oldest
	// first: all of them without a limit, else at most limit+1.
	ListRepositories(ctx context.Context, after *Position, limit int) ([]Repository, error)

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

	// ListBindings returns the bindings in f.Repository, by identity, ordered
	// by name and ID, after the position: all of them without a limit, else
	// at most limit+1. With f.Snapshot it reads only what the boundary
	// includes, and returns ErrInvalidSnapshot for a boundary BindingSnapshot
	// did not issue.
	ListBindings(ctx context.Context, f BindingFilter) ([]Binding, error)

	// BindingSnapshot returns the current boundary of the binding list.
	BindingSnapshot(ctx context.Context) (Snapshot, error)

	// BindingHistory returns a consistent snapshot of a binding and all of its
	// revisions, or an error wrapping ErrNotFound.
	BindingHistory(ctx context.Context, id string) (BindingHistory, error)

	// BindingRevision returns one revision, or an error wrapping ErrNotFound.
	BindingRevision(ctx context.Context, bindingID string, number int) (BindingRevision, error)

	// CreateExecution stores e and its child links, setting e.CreatedAt
	// strictly after every stored execution's. The caller has checked that
	// its version, binding revision and children exist and match; the store
	// returns a *LinkedChildError if a child already has a parent, and never
	// modifies or deletes an execution.
	CreateExecution(ctx context.Context, e *Execution) error

	// Execution returns one execution, or an error wrapping ErrNotFound.
	Execution(ctx context.Context, id string) (Execution, error)

	// ListExecutions returns the executions f selects, oldest first, without
	// their Inputs and Evidence, with one extra item when f.Page.Limit is set
	// and more exist.
	ListExecutions(ctx context.Context, f ExecutionFilter) ([]Execution, error)

	// ExecutionVerification returns VerifyExecution of one execution, read
	// from a single consistent snapshot.
	ExecutionVerification(ctx context.Context, id string) (Verification, error)

	// Verifications returns ListCombinations for f, read from a single
	// consistent snapshot.
	Verifications(ctx context.Context, f VerificationFilter) ([]CombinationStatus, error)

	// CreateFeedback stores f, setting f.CreatedAt strictly after every
	// stored report's, and never modifies or deletes a report.
	CreateFeedback(ctx context.Context, f *Feedback) error

	// Feedback returns one report, or an error wrapping ErrNotFound.
	Feedback(ctx context.Context, id string) (Feedback, error)

	// ListFeedback returns the reports f selects, oldest first, with one
	// extra item when f.Page.Limit is set and more exist.
	ListFeedback(ctx context.Context, f FeedbackFilter) ([]Feedback, error)

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
	p := Procedure{ID: uuid.NewV7().String(), CanonicalKey: in.CanonicalKey, CreatedAt: now, LatestVersion: 1,
		Goal: def.Goal, Applicability: def.Applicability}
	if in.Origin != nil {
		o := *in.Origin
		o.CreatedAt = now
		p.Origin = &o
	}
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

// ProcedureFilter selects procedures for discovery (ADR-0021). Repository
// keeps those whose latest version is applicable in that repository; Scope
// (shared, local or unspecified) those whose latest version declares it;
// Query those whose canonical key or latest goal contains it, ignoring ASCII
// case. After and Snapshot are set by the service for the store: with a
// Snapshot, "latest" and registration are as of its boundary (ADR-0023).
type ProcedureFilter struct {
	Repository string
	Scope      string
	Query      string
	Page       Page
	After      *Position
	Snapshot   Snapshot
}

// ListProcedures returns the procedures f selects, ordered by canonical key,
// and the cursor of the next page if there is one.
func (s *Service) ListProcedures(ctx context.Context, f ProcedureFilter) ([]Procedure, string, error) {
	var p problems
	if f.Repository != "" {
		checkRepository(&p, "repository", f.Repository)
	}
	switch f.Scope {
	case "", "shared", "local", "unspecified":
	default:
		p.add("scope", `must be "shared", "local" or "unspecified"`)
	}
	if f.Query != "" && strings.TrimSpace(f.Query) == "" {
		p.add("q", "must not be blank")
	}
	f.Query = strings.ToLower(f.Query)
	g := newPager("procedures", false, f.Page, f.Repository, f.Scope, f.Query)
	f.After = g.start(&p)
	if err := p.err(); err != nil {
		return nil, "", err
	}
	if f.Page.Snapshot && f.After == nil {
		var err error
		if g.Snapshot, err = s.store.ProcedureSnapshot(ctx); err != nil {
			return nil, "", fmt.Errorf("read snapshot boundary: %w", err)
		}
	}
	f.Snapshot = g.Snapshot
	procedures, err := s.store.ListProcedures(ctx, f)
	if err != nil {
		return nil, "", listError("procedures", err)
	}
	procedures, next := trim(f.Page, procedures, func(p Procedure) string { return g.next(p.CanonicalKey, time.Time{}, p.ID) })
	return procedures, next, nil
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
