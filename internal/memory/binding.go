package memory

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"time"
	"uuid"
)

// Binding records that a repository uses a shared procedure under a
// repository-local name. Its ID, repository, name and procedure never change.
type Binding struct {
	ID             string
	Repository     string
	Name           string
	ProcedureID    string
	CreatedAt      time.Time
	LatestRevision int
}

// PolicyKind names how a binding selects the version of its procedure.
type PolicyKind string

const (
	// PolicyPin selects exactly VersionPolicy.Pin.
	PolicyPin PolicyKind = "pin"
	// PolicyContextual asks for contextual resolution, which is stored but
	// not resolved yet.
	PolicyContextual PolicyKind = "contextual"
)

// VersionPolicy selects the version of a binding's procedure. Pin is set only
// for PolicyPin.
type VersionPolicy struct {
	Kind PolicyKind
	Pin  int
}

// BindingConfig is the author-supplied content of one binding revision.
// Inputs is a JSON object whose members are task knowledge.
type BindingConfig struct {
	Inputs         jsontext.Value
	VersionPolicy  VersionPolicy
	RevisionReason string
}

// BindingRevision is one immutable configuration of a binding. Revisions are
// numbered contiguously from 1 within their binding.
type BindingRevision struct {
	BindingID string
	Number    int
	CreatedAt time.Time
	BindingConfig
}

// BindingHistory is a binding together with all of its revisions, oldest
// first.
type BindingHistory struct {
	Binding   Binding
	Revisions []BindingRevision
}

// NewBinding asks to create a binding and its first revision.
type NewBinding struct {
	Repository  string
	Name        string
	ProcedureID string
	Config      BindingConfig
}

// BindingRevise asks to append a revision derived from BaseRevision, which
// must be the binding's latest revision at the time the revision is stored.
type BindingRevise struct {
	BaseRevision int
	Config       BindingConfig
}

var (
	// ErrBindingExists reports that the repository already has a binding with
	// the requested local name.
	ErrBindingExists = errors.New("binding already exists")
	// ErrPinnedVersionNotFound reports that a pinned version does not exist
	// in the bound procedure.
	ErrPinnedVersionNotFound = errors.New("pinned version does not exist")
)

// RevisionConflictError reports that a binding revision's base revision is
// not the binding's latest revision. The caller should re-read the binding
// and revise again.
type RevisionConflictError struct {
	BaseRevision   int
	LatestRevision int
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("base revision %d is not the latest revision (latest is %d)", e.BaseRevision, e.LatestRevision)
}

// CreateBinding creates a binding with a new stable ID and stores in.Config as
// its revision 1. The procedure must exist, and so must a pinned version.
func (s *Service) CreateBinding(ctx context.Context, in NewBinding) (BindingHistory, error) {
	cfg, err := in.validate()
	if err != nil {
		return BindingHistory{}, err
	}
	if err := s.checkBindingScope(ctx, in.ProcedureID, in.Repository, cfg.VersionPolicy, "procedure_id"); err != nil {
		return BindingHistory{}, err
	}
	now := timestamp()
	b := Binding{ID: uuid.NewV7().String(), Repository: in.Repository, Name: in.Name, ProcedureID: in.ProcedureID, CreatedAt: now, LatestRevision: 1}
	r := BindingRevision{BindingID: b.ID, Number: 1, CreatedAt: now, BindingConfig: cfg}
	if err := s.store.CreateBinding(ctx, b, r); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			return BindingHistory{}, fmt.Errorf("procedure %q: %w", in.ProcedureID, ErrNotFound)
		case errors.Is(err, ErrBindingExists):
			return BindingHistory{}, fmt.Errorf("binding %q in repository %q: %w", in.Name, in.Repository, ErrBindingExists)
		case errors.Is(err, ErrPinnedVersionNotFound):
			return BindingHistory{}, pinNotFound(fmt.Sprintf("procedure %q has no version %d", in.ProcedureID, cfg.VersionPolicy.Pin))
		}
		return BindingHistory{}, fmt.Errorf("create binding: %w", err)
	}
	return BindingHistory{Binding: b, Revisions: []BindingRevision{r}}, nil
}

// ReviseBinding appends in.Config as the next revision of the binding. It
// fails with a *RevisionConflictError, leaving history untouched, unless
// in.BaseRevision is the binding's latest revision.
func (s *Service) ReviseBinding(ctx context.Context, bindingID string, in BindingRevise) (BindingRevision, error) {
	cfg, err := in.validate()
	if err != nil {
		return BindingRevision{}, err
	}
	h, err := s.store.BindingHistory(ctx, bindingID)
	if err != nil {
		return BindingRevision{}, describeLookup(err, fmt.Sprintf("binding %q", bindingID))
	}
	if err := s.checkBindingScope(ctx, h.Binding.ProcedureID, h.Binding.Repository, cfg.VersionPolicy, "revision.version_policy"); err != nil {
		return BindingRevision{}, err
	}
	r := BindingRevision{BindingID: bindingID, Number: in.BaseRevision + 1, CreatedAt: timestamp(), BindingConfig: cfg}
	if err := s.store.AppendBindingRevision(ctx, in.BaseRevision, r); err != nil {
		var conflict *RevisionConflictError
		switch {
		case errors.Is(err, ErrNotFound):
			return BindingRevision{}, fmt.Errorf("binding %q: %w", bindingID, ErrNotFound)
		case errors.As(err, &conflict):
			return BindingRevision{}, conflict
		case errors.Is(err, ErrPinnedVersionNotFound):
			return BindingRevision{}, pinNotFound(fmt.Sprintf("the bound procedure has no version %d", cfg.VersionPolicy.Pin))
		}
		return BindingRevision{}, fmt.Errorf("append binding revision: %w", err)
	}
	return r, nil
}

// BindingFilter selects the bindings of one repository, by identity
// (ADR-0019), for the store.
type BindingFilter struct {
	Repository string
	After      *Position
	Limit      int
	Snapshot   Snapshot
}

// ListBindings returns the bindings of one repository ordered by local name,
// and the cursor of the next page if there is one. A registered identifier
// lists the bindings of every identifier of its repository (ADR-0019).
func (s *Service) ListBindings(ctx context.Context, repository string, page Page) ([]Binding, string, error) {
	var p problems
	checkRepository(&p, "repository", repository)
	g := newPager("bindings", false, page, repository)
	after := g.start(&p)
	if err := p.err(); err != nil {
		return nil, "", err
	}
	if page.Snapshot && after == nil {
		var err error
		if g.Snapshot, err = s.store.BindingSnapshot(ctx); err != nil {
			return nil, "", fmt.Errorf("read snapshot boundary: %w", err)
		}
	}
	bindings, err := s.store.ListBindings(ctx, BindingFilter{Repository: repository, After: after, Limit: page.Limit, Snapshot: g.Snapshot})
	if err != nil {
		return nil, "", listError("bindings", err)
	}
	bindings, next := trim(page, bindings, func(b Binding) string { return g.next(b.Name, time.Time{}, b.ID) })
	return bindings, next, nil
}

// checkBindingScope rejects a binding policy that contradicts applicability
// (ADR-0020): a pinned version must be applicable in the repository, and so
// must the latest version for a contextual policy, which follows the
// procedure forward. field names the contextual case. A missing procedure or
// pinned version is left to the store, which reports it as before.
func (s *Service) checkBindingScope(ctx context.Context, procedureID, repository string, policy VersionPolicy, field string) error {
	h, err := s.store.History(ctx, procedureID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read bound procedure: %w", err)
	}
	number := h.Procedure.LatestVersion
	if policy.Kind == PolicyPin {
		if policy.Pin > number {
			return nil
		}
		number, field = policy.Pin, "revision.version_policy.pin"
	}
	repositoryID, err := s.identityID(ctx, repository)
	if err != nil {
		return err
	}
	if a := h.Versions[number-1].Applicability; !a.ApplicableIn(repositoryID) {
		return &ValidationError{Problems: []FieldProblem{{Field: field,
			Message: fmt.Sprintf("version %d of procedure %q is %s, so it does not apply in repository %q", number, procedureID, a.describe(), repository)}}}
	}
	return nil
}

// Binding returns a binding and its full revision history.
func (s *Service) Binding(ctx context.Context, id string) (BindingHistory, error) {
	h, err := s.store.BindingHistory(ctx, id)
	if err != nil {
		return BindingHistory{}, describeLookup(err, fmt.Sprintf("binding %q", id))
	}
	return h, nil
}

// BindingRevision returns one revision of a binding.
func (s *Service) BindingRevision(ctx context.Context, bindingID string, number int) (BindingRevision, error) {
	if number < 1 {
		return BindingRevision{}, &ValidationError{Problems: []FieldProblem{{Field: "revision", Message: "must be a revision number of at least 1"}}}
	}
	r, err := s.store.BindingRevision(ctx, bindingID, number)
	if err != nil {
		return BindingRevision{}, describeLookup(err, fmt.Sprintf("binding %q revision %d", bindingID, number))
	}
	return r, nil
}

func pinNotFound(message string) error {
	return &ValidationError{Problems: []FieldProblem{{Field: "revision.version_policy.pin", Message: message}}}
}
