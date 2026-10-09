package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"
)

// Repository is a registered repository (ADR-0019). Its ID, name and
// identifiers never change; aliases are only ever added.
type Repository struct {
	ID         string
	Name       string
	Identifier string
	Aliases    []RepositoryAlias
	CreatedAt  time.Time
}

// RepositoryAlias is an identifier registered as another name of a
// repository, with the reason it was registered.
type RepositoryAlias struct {
	Identifier string
	Reason     string
	CreatedAt  time.Time
}

// Identifiers returns the canonical identifier followed by the aliases.
func (r Repository) Identifiers() []string {
	ids := []string{r.Identifier}
	for _, a := range r.Aliases {
		ids = append(ids, a.Identifier)
	}
	return ids
}

// RepositoryIdentity is the registered repository an identifier belongs to:
// its ID and canonical identifier (ADR-0022). The zero value means the
// identifier is not registered, and is its own identity.
type RepositoryIdentity struct {
	ID         string
	Identifier string
}

// identity returns r's identity, or the zero value for nil.
func (r *Repository) identity() RepositoryIdentity {
	if r == nil {
		return RepositoryIdentity{}
	}
	return RepositoryIdentity{ID: r.ID, Identifier: r.Identifier}
}

// NewRepository asks to register a repository under its canonical identifier.
type NewRepository struct {
	Identifier string
	Name       string
}

// NewAlias asks to register identifier as another name of a repository.
type NewAlias struct {
	Identifier string
	Reason     string
}

// ErrIdentifierExists reports that a repository identifier is already
// registered, as a canonical identifier or an alias.
var ErrIdentifierExists = errors.New("repository identifier is already registered")

// BindingNameConflictError reports that adding an alias would give a
// repository two bindings with the same local name.
type BindingNameConflictError struct {
	Name string
}

func (e *BindingNameConflictError) Error() string {
	return fmt.Sprintf("the repository already has a binding named %q under another identifier", e.Name)
}

// RegisterRepository registers a repository with a new stable ID.
func (s *Service) RegisterRepository(ctx context.Context, in NewRepository) (Repository, error) {
	var p problems
	checkRepository(&p, "identifier", in.Identifier)
	checkLine(&p, "name", in.Name)
	if err := p.err(); err != nil {
		return Repository{}, err
	}
	r := Repository{ID: uuid.NewV7().String(), Name: in.Name, Identifier: in.Identifier, CreatedAt: timestamp()}
	if err := s.store.CreateRepository(ctx, &r); err != nil {
		if errors.Is(err, ErrIdentifierExists) {
			return Repository{}, fmt.Errorf("identifier %q: %w", in.Identifier, ErrIdentifierExists)
		}
		return Repository{}, fmt.Errorf("register repository: %w", err)
	}
	return r, nil
}

// AddRepositoryAlias registers in.Identifier as another name of a repository.
func (s *Service) AddRepositoryAlias(ctx context.Context, repositoryID string, in NewAlias) (Repository, error) {
	var p problems
	checkRepository(&p, "identifier", in.Identifier)
	checkText(&p, "reason", in.Reason)
	if err := p.err(); err != nil {
		return Repository{}, err
	}
	a := RepositoryAlias{Identifier: in.Identifier, Reason: in.Reason, CreatedAt: timestamp()}
	if err := s.store.AddRepositoryAlias(ctx, repositoryID, a); err != nil {
		var conflict *BindingNameConflictError
		switch {
		case errors.Is(err, ErrNotFound):
			return Repository{}, fmt.Errorf("repository %q: %w", repositoryID, ErrNotFound)
		case errors.Is(err, ErrIdentifierExists):
			return Repository{}, fmt.Errorf("identifier %q: %w", in.Identifier, ErrIdentifierExists)
		case errors.As(err, &conflict):
			return Repository{}, fmt.Errorf("alias %q: %s: %w", in.Identifier, conflict.Error(), ErrBindingExists)
		}
		return Repository{}, fmt.Errorf("add repository alias: %w", err)
	}
	return s.Repository(ctx, repositoryID)
}

// Repository returns a registered repository by ID.
func (s *Service) Repository(ctx context.Context, id string) (Repository, error) {
	r, err := s.store.Repository(ctx, id)
	if err != nil {
		return Repository{}, describeLookup(err, fmt.Sprintf("repository %q", id))
	}
	return r, nil
}

// RepositoryByIdentifier returns the repository that identifier, canonical
// or alias, is registered to.
func (s *Service) RepositoryByIdentifier(ctx context.Context, identifier string) (Repository, error) {
	var p problems
	checkRepository(&p, "identifier", identifier)
	if err := p.err(); err != nil {
		return Repository{}, err
	}
	r, err := s.store.RepositoryByIdentifier(ctx, identifier)
	if err != nil {
		return Repository{}, describeLookup(err, fmt.Sprintf("repository with identifier %q", identifier))
	}
	return r, nil
}

// ListRepositories returns registered repositories, oldest first.
func (s *Service) ListRepositories(ctx context.Context, page Page) ([]Repository, string, error) {
	var p problems
	g := newPager("repositories", true, page)
	after := g.start(&p)
	if err := p.err(); err != nil {
		return nil, "", err
	}
	repos, err := s.store.ListRepositories(ctx, after, page.Limit)
	if err != nil {
		return nil, "", fmt.Errorf("list repositories: %w", err)
	}
	repos, next := trim(page, repos, func(r Repository) string { return g.next("", r.CreatedAt, r.ID) })
	return repos, next, nil
}

// identity returns the repository identifier is registered to, or nil if it
// is not registered. Reads never register anything (ADR-0019).
func (s *Service) identity(ctx context.Context, identifier string) (*Repository, error) {
	r, err := s.store.RepositoryByIdentifier(ctx, identifier)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read repository identity: %w", err)
	}
	return &r, nil
}

// identityID returns the ID of the repository identifier is registered to,
// or "" if it is not registered.
func (s *Service) identityID(ctx context.Context, identifier string) (string, error) {
	r, err := s.identity(ctx, identifier)
	if r == nil || err != nil {
		return "", err
	}
	return r.ID, nil
}

// sameRepository returns every identifier of the repository identifier is
// registered to, or identifier alone if it is not registered.
func (s *Service) sameRepository(ctx context.Context, identifier string) ([]string, string, error) {
	r, err := s.identity(ctx, identifier)
	if err != nil {
		return nil, "", err
	}
	if r == nil {
		return []string{identifier}, "", nil
	}
	return r.Identifiers(), r.ID, nil
}

// checkLine accepts required, single-line, non-blank text.
func checkLine(p *problems, field, s string) {
	checkText(p, field, s)
	if strings.TrimSpace(s) != "" && strings.ContainsAny(s, lineBreaks) {
		p.add(field, "must be a single line")
	}
}
