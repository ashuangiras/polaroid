package gitcore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// Action is how a path changed between two snapshots.
type Action string

const (
	Added    Action = "added"
	Modified Action = "modified"
	Deleted  Action = "deleted"
)

type PathChange struct {
	Path   string
	Action Action
}

// Commit is one publication, as the history reports it.
type Commit struct {
	Snapshot Snapshot
	Parents  []Snapshot
	Author   string
	When     time.Time
	Subject  string
	Body     string
	Trailers []Trailer
	Changed  []PathChange // against the first parent
}

// History lists publications from snapshot back along first parents,
// newest first, at most limit of them.
func (r *Repository) History(ctx context.Context, from Snapshot, limit int) ([]Commit, error) {
	if err := r.published(ctx, from); err != nil {
		return nil, err
	}
	var out []Commit
	h := plumbing.NewHash(string(from))
	for len(out) < limit && !h.IsZero() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := object.GetCommit(r.st, h)
		if err != nil {
			return nil, fmt.Errorf("%w: commit %s: %w", ErrCorrupt, h, err)
		}
		e, err := r.describe(ctx, r.st, c)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		h = plumbing.ZeroHash
		if len(c.ParentHashes) > 0 {
			h = c.ParentHashes[0]
		}
	}
	return out, nil
}

// describe reports one commit with what changed against its first parent.
func (r *Repository) describe(ctx context.Context, st storer.EncodedObjectStorer, c *object.Commit) (Commit, error) {
	subject, body, trailers := parseMessage(c.Message)
	e := Commit{Snapshot: Snapshot(c.Hash.String()), Author: c.Author.Name, When: c.Author.When, Subject: subject, Body: body, Trailers: trailers}
	for _, p := range c.ParentHashes {
		e.Parents = append(e.Parents, Snapshot(p.String()))
	}
	var parent *object.Commit
	if len(c.ParentHashes) > 0 {
		var err error
		if parent, err = object.GetCommit(st, c.ParentHashes[0]); err != nil {
			return e, fmt.Errorf("%w: commit %s: %w", ErrCorrupt, c.ParentHashes[0], err)
		}
	}
	var err error
	e.Changed, err = diffCommits(ctx, parent, c)
	return e, err
}

// Diff lists the paths that differ between two published snapshots.
func (r *Repository) Diff(ctx context.Context, from, to Snapshot) ([]PathChange, error) {
	for _, s := range []Snapshot{from, to} {
		if err := r.published(ctx, s); err != nil {
			return nil, err
		}
	}
	a, err := object.GetCommit(r.st, plumbing.NewHash(string(from)))
	if err != nil {
		return nil, fmt.Errorf("%w: commit %s: %w", ErrCorrupt, from, err)
	}
	b, err := object.GetCommit(r.st, plumbing.NewHash(string(to)))
	if err != nil {
		return nil, fmt.Errorf("%w: commit %s: %w", ErrCorrupt, to, err)
	}
	return diffCommits(ctx, a, b)
}

// diffCommits compares two trees; equal subtrees have equal hashes, so only
// what changed is read. A nil from is the empty tree.
func diffCommits(ctx context.Context, from, to *object.Commit) ([]PathChange, error) {
	a := &object.Tree{}
	if from != nil {
		var err error
		if a, err = from.Tree(); err != nil {
			return nil, fmt.Errorf("%w: tree of %s: %w", ErrCorrupt, from.Hash, err)
		}
	}
	b, err := to.Tree()
	if err != nil {
		return nil, fmt.Errorf("%w: tree of %s: %w", ErrCorrupt, to.Hash, err)
	}
	changes, err := object.DiffTreeContext(ctx, a, b)
	if err != nil {
		return nil, fmt.Errorf("%w: diff: %w", ErrCorrupt, err)
	}
	var out []PathChange
	for _, ch := range changes {
		switch f, t := ch.From.Name, ch.To.Name; {
		case f == "":
			out = append(out, PathChange{Path: t, Action: Added})
		case t == "":
			out = append(out, PathChange{Path: f, Action: Deleted})
		case f == t:
			out = append(out, PathChange{Path: t, Action: Modified})
		default:
			out = append(out, PathChange{Path: f, Action: Deleted}, PathChange{Path: t, Action: Added})
		}
	}
	return out, nil
}

// parseMessage splits a commit message into subject, body and trailers: the
// last paragraph, when every line of it is "Key: Value".
func parseMessage(msg string) (string, string, []Trailer) {
	msg = strings.TrimRight(msg, "\n")
	subject, rest, _ := strings.Cut(msg, "\n")
	paras := strings.Split(strings.TrimLeft(rest, "\n"), "\n\n")
	var trailers []Trailer
	if last := paras[len(paras)-1]; last != "" {
		for _, line := range strings.Split(last, "\n") {
			k, v, ok := strings.Cut(line, ": ")
			if !ok || !trailerKey.MatchString(k) {
				trailers = nil
				break
			}
			trailers = append(trailers, Trailer{Key: k, Value: v})
		}
		if trailers != nil {
			paras = paras[:len(paras)-1]
		}
	}
	return subject, strings.TrimSpace(strings.Join(paras, "\n\n")), trailers
}
