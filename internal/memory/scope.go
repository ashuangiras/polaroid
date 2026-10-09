package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ScopeKind says where a version's contract is intended to be used
// (ADR-0020).
type ScopeKind string

const (
	// ScopeUnspecified is the absence of a declaration: every version stored
	// before applicability existed, and any version that omits it.
	ScopeUnspecified ScopeKind = ""
	ScopeShared      ScopeKind = "shared"
	ScopeLocal       ScopeKind = "local"
	// scopeInvalid marks a malformed declaration, which validation rejects.
	scopeInvalid ScopeKind = "invalid"
)

// Applicability is a version's declared scope. RepositoryID is set only for
// ScopeLocal.
type Applicability struct {
	Kind         ScopeKind
	RepositoryID string
}

// InvalidApplicability is a malformed declaration, which validation rejects.
var InvalidApplicability = Applicability{Kind: scopeInvalid}

// Name is the scope's name in responses: shared, local or unspecified.
func (a Applicability) Name() string {
	if a.Kind == ScopeUnspecified {
		return "unspecified"
	}
	return string(a.Kind)
}

// ApplicableIn reports whether a version with applicability a may be used in
// the repository with ID repositoryID ("" for an unregistered identifier).
func (a Applicability) ApplicableIn(repositoryID string) bool {
	return a.Kind != ScopeLocal || (repositoryID != "" && a.RepositoryID == repositoryID)
}

// Admits reports whether a version with applicability a may compose a child
// version with applicability child: a local version admits what is
// applicable in its repository, anything else admits no local version, so a
// shared parent never presents a local dependency as available everywhere.
func (a Applicability) Admits(child Applicability) bool {
	if a.Kind == ScopeLocal {
		return child.ApplicableIn(a.RepositoryID)
	}
	return child.Kind != ScopeLocal
}

func (a Applicability) describe() string {
	if a.Kind == ScopeLocal {
		return fmt.Sprintf("local to repository %q", a.RepositoryID)
	}
	return a.Name()
}

func checkApplicability(p *problems, field string, a Applicability) {
	switch a.Kind {
	case ScopeUnspecified, ScopeShared:
	case ScopeLocal:
		if a.RepositoryID == "" {
			p.add(field+".repository", "is required")
		}
	default:
		p.add(field, `must be {"shared": {}} or {"repository": "<repository id>"}`)
	}
}

// Origin is where and why a procedure was first created (ADR-0020). It is
// provenance and never affects applicability.
type Origin struct {
	RepositoryID string
	Reason       string
	CreatedAt    time.Time
}

func checkOrigin(p *problems, field string, o Origin) {
	if o.RepositoryID == "" {
		p.add(field+".repository_id", "is required")
	}
	checkText(p, field+".reason", o.Reason)
}

// ErrOriginExists reports that a procedure's origin is already recorded.
var ErrOriginExists = errors.New("origin is already recorded")

// RecordOrigin records where and why a procedure was first created, once.
func (s *Service) RecordOrigin(ctx context.Context, procedureID string, o Origin) (History, error) {
	var p problems
	checkOrigin(&p, "origin", o)
	if err := p.err(); err != nil {
		return History{}, err
	}
	o.CreatedAt = timestamp()
	if err := s.store.SetOrigin(ctx, procedureID, o); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			return History{}, fmt.Errorf("procedure %q: %w", procedureID, ErrNotFound)
		case errors.Is(err, ErrOriginExists):
			return History{}, fmt.Errorf("procedure %q: %w", procedureID, ErrOriginExists)
		}
		var invalid *ValidationError
		if errors.As(err, &invalid) {
			return History{}, invalid
		}
		return History{}, fmt.Errorf("record origin: %w", err)
	}
	return s.Procedure(ctx, procedureID)
}

// CheckReferenceScopes checks, on write, that version v admits each of its
// references' targets (ADR-0020): a pinned version must be admitted, and a
// contextual target must have at least one admitted version. r reads the
// versions as the write transaction sees them.
func CheckReferenceScopes(ctx context.Context, r GraphReader, v Version) error {
	var p problems
	for i, ref := range v.References {
		field := fmt.Sprintf("version.references[%d].procedure_id", i)
		if ref.VersionPolicy.Kind == PolicyPin {
			node, err := r.VersionNode(ctx, ref.ProcedureID, ref.VersionPolicy.Pin)
			if err != nil {
				return fmt.Errorf("read reference %q target: %w", ref.Name, err)
			}
			if !v.Applicability.Admits(node.Applicability) {
				p.add(field, fmt.Sprintf("version %d of %q is %s, which a %s version cannot reference",
					ref.VersionPolicy.Pin, ref.ProcedureID, node.Applicability.describe(), v.Applicability.describe()))
			}
			continue
		}
		version, err := latestAdmitted(ctx, r, ref.ProcedureID, v.Applicability.Admits)
		if err != nil {
			return fmt.Errorf("read reference %q target: %w", ref.Name, err)
		}
		if version == 0 {
			p.add(field, fmt.Sprintf("procedure %q has no version that a %s version can reference", ref.ProcedureID, v.Applicability.describe()))
		}
	}
	return p.err()
}

// latestAdmitted returns the highest version of procedureID whose
// applicability admit accepts, or 0 if there is none.
func latestAdmitted(ctx context.Context, r GraphReader, procedureID string, admit func(Applicability) bool) (int, error) {
	latest, err := r.LatestVersion(ctx, procedureID)
	if err != nil {
		return 0, err
	}
	for v := latest; v >= 1; v-- {
		node, err := r.VersionNode(ctx, procedureID, v)
		if err != nil {
			return 0, err
		}
		if admit(node.Applicability) {
			return v, nil
		}
	}
	return 0, nil
}
