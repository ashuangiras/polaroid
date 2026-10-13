// Package gitstore implements the knowledge Store on Git-core: snapshots
// are Git commits, the active knowledge is Git-core's active ref.
package gitstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
)

type Store struct{ repo *gitcore.Repository }

var _ knowledge.Store = (*Store)(nil)

// New uses an open Git-core repository; the caller closes it.
func New(repo *gitcore.Repository) *Store { return &Store{repo: repo} }

// mapErr translates Git-core errors to the knowledge contract's.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gitcore.ErrStale):
		return fmt.Errorf("%w: %v", knowledge.ErrStale, err) //nolint:errorlint // Git-core errors stay text: callers match knowledge errors only
	case errors.Is(err, gitcore.ErrNotFound):
		return fmt.Errorf("%w: %v", knowledge.ErrNotFound, err) //nolint:errorlint // Git-core errors stay text: callers match knowledge errors only
	case errors.Is(err, gitcore.ErrDiverged):
		return fmt.Errorf("%w: %v", knowledge.ErrDiverged, err) //nolint:errorlint // Git-core errors stay text: callers match knowledge errors only
	case errors.Is(err, gitcore.ErrCorrupt):
		return fmt.Errorf("%w: %v", knowledge.ErrCorrupt, err) //nolint:errorlint // Git-core errors stay text: callers match knowledge errors only
	case errors.Is(err, gitcore.ErrInvalid):
		return &knowledge.ValidationError{Problems: []knowledge.Problem{{Field: "storage", Message: err.Error()}}}
	}
	return err
}

func (s *Store) Active(ctx context.Context) (knowledge.Snapshot, error) {
	a, err := s.repo.Active(ctx)
	return knowledge.Snapshot(a), mapErr(err)
}

func (s *Store) Files(ctx context.Context, snap knowledge.Snapshot) (knowledge.Files, error) {
	t, err := s.repo.Tree(ctx, gitcore.Snapshot(snap))
	if err != nil {
		return nil, mapErr(err)
	}
	return files{t}, nil
}

type files struct{ t *gitcore.Tree }

func (f files) Paths() []string { return f.t.Paths() }

func (f files) Read(path string) ([]byte, error) {
	b, err := f.t.Read(path)
	return b, mapErr(err)
}

// errCheck carries the kernel's own Check error through Git-core unchanged.
type errCheck struct{ err error }

func (e errCheck) Error() string { return e.err.Error() }
func (e errCheck) Unwrap() error { return e.err }

func (s *Store) Commit(ctx context.Context, c knowledge.Commit) (knowledge.Snapshot, error) {
	p := gitcore.Publication{Expected: gitcore.Snapshot(c.Expected), Author: c.Author, Subject: c.Subject, Body: c.Body}
	for _, ch := range c.Changes {
		p.Changes = append(p.Changes, gitcore.Change{Path: ch.Path, Content: ch.Content, Delete: ch.Delete})
	}
	for _, t := range c.Trailers {
		p.Trailers = append(p.Trailers, gitcore.Trailer{Key: t.Key, Value: t.Value})
	}
	if c.Check != nil {
		p.Validate = func(t *gitcore.Tree) error {
			if err := c.Check(files{t}); err != nil {
				return errCheck{err}
			}
			return nil
		}
	}
	snap, err := s.repo.Publish(ctx, p)
	var ec errCheck
	if errors.As(err, &ec) {
		return "", ec.err
	}
	return knowledge.Snapshot(snap), mapErr(err)
}

func (s *Store) Log(ctx context.Context, from knowledge.Snapshot, limit int) ([]knowledge.LogEntry, error) {
	commits, err := s.repo.History(ctx, gitcore.Snapshot(from), limit)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]knowledge.LogEntry, len(commits))
	for i, c := range commits {
		e := knowledge.LogEntry{Snapshot: knowledge.Snapshot(c.Snapshot), Author: c.Author, When: c.When, Subject: c.Subject, Body: c.Body}
		for _, p := range c.Parents {
			e.Parents = append(e.Parents, knowledge.Snapshot(p))
		}
		for _, t := range c.Trailers {
			e.Trailers = append(e.Trailers, knowledge.Trailer{Key: t.Key, Value: t.Value})
		}
		for _, ch := range c.Changed {
			e.Changed = append(e.Changed, knowledge.PathChange{Path: ch.Path, Action: string(ch.Action)})
		}
		out[i] = e
	}
	return out, nil
}

// Files is the knowledge view of a Git-core tree, such as a fetched
// candidate's, for knowledge.Load.
func Files(t *gitcore.Tree) knowledge.Files { return files{t} }

func (s *Store) Advance(ctx context.Context, a knowledge.Advance) error {
	adv := gitcore.Advance{Expected: gitcore.Snapshot(a.Expected), Target: gitcore.Snapshot(a.Target), Author: a.Author, Subject: a.Subject, Body: a.Body}
	for _, t := range a.Trailers {
		adv.Trailers = append(adv.Trailers, gitcore.Trailer{Key: t.Key, Value: t.Value})
	}
	if a.Check != nil {
		adv.Validate = func(t *gitcore.Tree) error {
			if err := a.Check(files{t}); err != nil {
				return errCheck{err}
			}
			return nil
		}
	}
	err := s.repo.Advance(ctx, adv)
	var ec errCheck
	if errors.As(err, &ec) {
		return ec.err
	}
	return mapErr(err)
}

// Git-core records each activation's target and previous snapshot as the
// last two trailers of its journal entry.
const (
	trailerActivated = "Activated"
	trailerPrevious  = "Previous"
)

func (s *Store) Activations(ctx context.Context, limit int) ([]knowledge.ActivationEntry, error) {
	commits, err := s.repo.Activations(ctx, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]knowledge.ActivationEntry, len(commits))
	for i, c := range commits {
		e := knowledge.ActivationEntry{Author: c.Author, When: c.When, Subject: c.Subject, Body: c.Body}
		for _, t := range c.Trailers {
			switch t.Key {
			case trailerActivated:
				e.Target = knowledge.Snapshot(t.Value)
			case trailerPrevious:
				if t.Value != "none" {
					e.Previous = knowledge.Snapshot(t.Value)
				}
			default:
				e.Trailers = append(e.Trailers, knowledge.Trailer{Key: t.Key, Value: t.Value})
			}
		}
		out[i] = e
	}
	return out, nil
}
