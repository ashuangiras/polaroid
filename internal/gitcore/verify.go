package gitcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Report is the result of Verify. Problems make the repository unusable as
// it is; warnings do not.
type Report struct {
	Active    Snapshot
	Snapshots int
	Objects   int
	Problems  []string
	Warnings  []string
}

func (rep *Report) problem(format string, args ...any) {
	rep.Problems = append(rep.Problems, fmt.Sprintf(format, args...))
}

// Verify checks the active ref and every object reachable from it: each
// object exists, its content hashes to its id, and every tree holds only
// safe paths and regular files. It also compares the reflog with the ref.
func (r *Repository) Verify(ctx context.Context) (Report, error) {
	var rep Report
	active, err := r.Active(ctx)
	if errors.Is(err, ErrCorrupt) {
		rep.problem("%v", err)
		return rep, nil
	}
	if err != nil {
		return rep, err
	}
	rep.Active = active
	if active != "" {
		seen := map[plumbing.Hash]bool{}
		todo := []plumbing.Hash{plumbing.NewHash(string(active))}
		for len(todo) > 0 && len(rep.Problems) < 50 {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			h := todo[len(todo)-1]
			todo = todo[:len(todo)-1]
			if seen[h] {
				continue
			}
			seen[h] = true
			rep.Snapshots++
			c, err := r.checkSnapshot(h, seen, &rep.Objects)
			if err != nil {
				rep.problem("snapshot %s: %v", h, err)
				continue
			}
			todo = append(todo, c.ParentHashes...)
		}
	}
	entries, partial, err := r.readReflog()
	if err != nil {
		return rep, err
	}
	switch {
	case partial:
		rep.Warnings = append(rep.Warnings, "the reflog ends with an incomplete line")
	case active != "" && (len(entries) == 0 || entries[len(entries)-1].to != active):
		rep.Warnings = append(rep.Warnings, "the reflog's last entry is not the active snapshot (a crash after the ref moved)")
	}
	if ents, err := os.ReadDir(filepath.Join(r.dir, tmpName)); err == nil && len(ents) > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d temp files left by an interrupted writer (removed by the next publication)", len(ents)))
	}
	return rep, nil
}

// checkSnapshot verifies one commit and its whole tree, counting each
// object it has not seen.
func (r *Repository) checkSnapshot(h plumbing.Hash, seen map[plumbing.Hash]bool, count *int) (*object.Commit, error) {
	if err := r.checkObject(h, plumbing.CommitObject); err != nil {
		return nil, err
	}
	*count++
	c, err := object.GetCommit(r.st, h)
	if err != nil {
		return nil, err
	}
	return c, r.checkTree(c.TreeHash, "", 0, seen, count)
}

func (r *Repository) checkTree(h plumbing.Hash, prefix string, depth int, seen map[plumbing.Hash]bool, count *int) error {
	if seen[h] {
		return nil
	}
	if depth >= r.lim.MaxPathDepth {
		return fmt.Errorf("tree %q deeper than %d", prefix, r.lim.MaxPathDepth)
	}
	if err := r.checkObject(h, plumbing.TreeObject); err != nil {
		return fmt.Errorf("tree %q: %w", prefix, err)
	}
	seen[h] = true
	*count++
	t, err := object.GetTree(r.st, h)
	if err != nil {
		return err
	}
	for _, e := range t.Entries {
		p := prefix + e.Name
		if !safeName(e.Name) {
			return fmt.Errorf("unsafe path %q", p)
		}
		switch e.Mode {
		case filemode.Dir:
			if err := r.checkTree(e.Hash, p+"/", depth+1, seen, count); err != nil {
				return err
			}
		case filemode.Regular:
			if !seen[e.Hash] {
				if err := r.checkObject(e.Hash, plumbing.BlobObject); err != nil {
					return fmt.Errorf("file %q: %w", p, err)
				}
				seen[e.Hash] = true
				*count++
			}
		default:
			return fmt.Errorf("%q has mode %s", p, e.Mode)
		}
	}
	return nil
}

// checkObject reads an object in full and recomputes its id.
func (r *Repository) checkObject(h plumbing.Hash, t plumbing.ObjectType) error {
	o, err := r.st.EncodedObject(t, h)
	if err != nil {
		return fmt.Errorf("object %s: %w", h, err)
	}
	rd, err := o.Reader()
	if err != nil {
		return fmt.Errorf("object %s: %w", h, err)
	}
	defer func() { _ = rd.Close() }()
	b, err := io.ReadAll(rd)
	if err != nil {
		return fmt.Errorf("object %s: %w", h, err)
	}
	if got := plumbing.ComputeHash(t, b); got != h {
		return fmt.Errorf("object %s: content hashes to %s", h, got)
	}
	return nil
}

type reflogEntry struct{ from, to Snapshot }

func (r *Repository) readReflog() ([]reflogEntry, bool, error) {
	b, err := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(reflogName)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	s := string(b)
	partial := s != "" && !strings.HasSuffix(s, "\n")
	var out []reflogEntry
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && Snapshot(f[1]).valid() {
			out = append(out, reflogEntry{from: Snapshot(f[0]), to: Snapshot(f[1])})
		}
	}
	if partial && len(out) > 0 {
		out = out[:len(out)-1]
	}
	return out, partial, nil
}

// Recover makes the repository usable again after damage to the active ref
// or to the objects of the active snapshot: the newest snapshot recorded in
// the reflog whose commit and tree verify completely becomes active, and
// the move is logged. A healthy active snapshot is left as it is. Recovery
// to an older snapshot loses the newer ones from the active state; their
// surviving objects stay in the store.
func (r *Repository) Recover(ctx context.Context, author string) (Snapshot, error) {
	if author == "" || badAuthor.MatchString(author) {
		return "", invalid("author %q is empty or has <, > or control characters", author)
	}
	unlock, err := r.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	active, err := r.Active(ctx)
	if err == nil && active != "" {
		if _, err = r.checkSnapshot(plumbing.NewHash(string(active)), map[plumbing.Hash]bool{}, new(int)); err == nil {
			return active, nil
		}
	}
	if err == nil && active == "" {
		return "", nil
	}
	reason := err
	entries, _, err := r.readReflog()
	if err != nil {
		return "", err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		cand := entries[i].to
		if cand == active {
			continue
		}
		if _, err := r.checkSnapshot(plumbing.NewHash(string(cand)), map[plumbing.Hash]bool{}, new(int)); err != nil {
			continue
		}
		if err := r.setActive(cand); err != nil {
			return "", err
		}
		r.mu.Lock()
		clear(r.reachable)
		clear(r.indexes)
		r.mu.Unlock()
		sig := object.Signature{Name: author, Email: author + "@polaroid.invalid", When: time.Now().UTC().Truncate(time.Second)}
		if err := r.appendReflog(active, cand, sig, fmt.Sprintf("recover: %v", reason)); err != nil {
			return cand, err
		}
		return cand, nil
	}
	return "", fmt.Errorf("%w: no snapshot in the reflog verifies (%w)", ErrCorrupt, reason)
}
