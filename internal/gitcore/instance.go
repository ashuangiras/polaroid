package gitcore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// instanceName holds this repository's instance identity. It is a local
// file, never part of a ref or object, so no push or fetch carries it, and a
// repository rebuilt from a remote gets an identity of its own.
const instanceName = "polaroid-instance"

var instancePattern = regexp.MustCompile(`^pi-[0-9a-f]{32}$`)

// loadInstance reads the identity, creating it once, durably, under the
// writer lock.
func (r *Repository) loadInstance() error {
	path := filepath.Join(r.dir, instanceName)
	read := func() (bool, error) {
		b, err := os.ReadFile(path) //nolint:gosec // inside the repository directory
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		id := strings.TrimSpace(string(b))
		if !instancePattern.MatchString(id) {
			return false, fmt.Errorf("%w: %s holds %q", ErrCorrupt, instanceName, id[:min(len(id), 64)])
		}
		r.instance = id
		return true, nil
	}
	if ok, err := read(); ok || err != nil {
		return err
	}
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if ok, err := read(); ok || err != nil {
		return err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	id := "pi-" + hex.EncodeToString(b[:])
	if err := r.replaceFile(path, []byte(id+"\n"), 0o600, "instance-temp"); err != nil {
		return err
	}
	if err := syncDir(r.dir); err != nil {
		return err
	}
	r.instance = id
	return nil
}

// Instance identifies this repository among the instances that exchange
// knowledge. It names where a publication was made; it authenticates
// nothing.
func (r *Repository) Instance() string { return r.instance }

// Imported reports whether a published snapshot arrived through an
// activation rather than a publication made here. It is derived from the
// activation journal alone, which records every activation before the
// active ref moves; commit authors and messages are never consulted, so a
// commit claiming to be local is classified by how it arrived.
func (r *Repository) Imported(ctx context.Context, s Snapshot) (bool, error) {
	if err := r.published(ctx, s); err != nil {
		return false, err
	}
	set, err := r.importedSet(ctx)
	if err != nil {
		return false, err
	}
	return set[s], nil
}

// importedSet is every commit that an activation introduced: those from
// each Activated snapshot back along first parents to its Previous one.
func (r *Repository) importedSet(ctx context.Context) (map[Snapshot]bool, error) {
	head, err := r.readRef(journalRef)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	cached, ok := r.imported[head]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}
	set := map[Snapshot]bool{}
	for h := plumbing.NewHash(string(head)); head != "" && !h.IsZero(); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, err := object.GetCommit(r.st, h)
		if err != nil {
			return nil, fmt.Errorf("%w: activation %s: %w", ErrCorrupt, h, err)
		}
		target, prev, err := journalMove(c)
		if err != nil {
			return nil, err
		}
		for s := target; s != prev && s != ""; {
			if set[s] {
				break
			}
			set[s] = true
			cm, err := object.GetCommit(r.st, plumbing.NewHash(string(s)))
			if err != nil {
				return nil, fmt.Errorf("%w: activated snapshot %s: %w", ErrCorrupt, s, err)
			}
			s = ""
			if len(cm.ParentHashes) > 0 {
				s = Snapshot(cm.ParentHashes[0].String())
			}
		}
		h = plumbing.ZeroHash
		if len(c.ParentHashes) > 0 {
			h = c.ParentHashes[0]
		}
	}
	r.mu.Lock()
	r.imported = map[Snapshot]map[Snapshot]bool{head: set}
	r.mu.Unlock()
	return set, nil
}

// journalMove reads an activation entry's Activated and Previous trailers.
func journalMove(c *object.Commit) (Snapshot, Snapshot, error) {
	_, _, trailers := parseMessage(c.Message)
	var target, prev Snapshot
	var seen int
	for _, t := range trailers {
		switch t.Key {
		case "Activated":
			target, seen = Snapshot(t.Value), seen+1
		case "Previous":
			if t.Value != "none" {
				prev = Snapshot(t.Value)
			}
			seen++
		}
	}
	if seen != 2 || !target.valid() || (prev != "" && !prev.valid()) {
		return "", "", fmt.Errorf("%w: activation %s does not name its move", ErrCorrupt, c.Hash)
	}
	return target, prev, nil
}
