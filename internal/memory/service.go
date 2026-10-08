package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"
)

// Store persists procedures and their versions.
//
// Implementations must make every write atomic, must never modify or delete a
// stored procedure or version, and must enforce canonical-key uniqueness.
type Store interface {
	// CreateProcedure stores p together with its first version. It returns an
	// error wrapping ErrCanonicalKeyExists if p.CanonicalKey is already used.
	CreateProcedure(ctx context.Context, p Procedure, first Version) error

	// AppendVersion stores next if, and only if, baseVersion is the latest
	// version of next.ProcedureID; the check and the write are one atomic
	// step. Callers set next.Number to baseVersion+1. It returns a
	// *VersionConflictError when baseVersion is not the latest version and an
	// error wrapping ErrNotFound when the procedure does not exist.
	AppendVersion(ctx context.Context, baseVersion int, next Version) error

	// ListProcedures returns every procedure ordered by canonical key.
	ListProcedures(ctx context.Context) ([]Procedure, error)

	// History and HistoryByKey return a consistent snapshot of a procedure and
	// all of its versions, or an error wrapping ErrNotFound.
	History(ctx context.Context, id string) (History, error)
	HistoryByKey(ctx context.Context, canonicalKey string) (History, error)

	// Version returns one version, or an error wrapping ErrNotFound.
	Version(ctx context.Context, procedureID string, number int) (Version, error)

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
		if errors.Is(err, ErrNotFound) {
			return Version{}, fmt.Errorf("procedure %q: %w", procedureID, ErrNotFound)
		}
		var conflict *VersionConflictError
		if errors.As(err, &conflict) {
			return Version{}, conflict
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
