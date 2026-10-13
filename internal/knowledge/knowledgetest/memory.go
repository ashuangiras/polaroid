package knowledgetest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

// MemoryStore is an in-memory knowledge.Store with Git-core's contract
// (linear history, compare-and-publish, published snapshots only), for
// testing the domain without storage.
type MemoryStore struct {
	mu          sync.Mutex
	active      knowledge.Snapshot
	snaps       map[knowledge.Snapshot]memSnap
	activations []knowledge.ActivationEntry
}

type memSnap struct {
	files     map[string][]byte
	entry     knowledge.LogEntry
	published bool // active, or an ancestor of the active snapshot
	imported  bool // made published by an activation
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{snaps: map[knowledge.Snapshot]memSnap{}} }

func (m *MemoryStore) Active(context.Context) (knowledge.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active, nil
}

type memFiles map[string][]byte

func (f memFiles) Paths() []string { return slices.Sorted(maps.Keys(f)) }

func (f memFiles) Read(path string) ([]byte, error) {
	b, ok := f[path]
	if !ok {
		return nil, fmt.Errorf("%s: %w", path, knowledge.ErrNotFound)
	}
	return slices.Clone(b), nil
}

func (m *MemoryStore) Files(_ context.Context, s knowledge.Snapshot) (knowledge.Files, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap, ok := m.snaps[s]
	if !ok || !snap.published {
		return nil, fmt.Errorf("snapshot %q: %w", s, knowledge.ErrNotFound)
	}
	return memFiles(snap.files), nil
}

func (m *MemoryStore) Commit(_ context.Context, c knowledge.Commit) (knowledge.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.Expected != m.active {
		return "", knowledge.ErrStale
	}
	return m.commit(m.active, c, true)
}

// Stage stores a snapshot on top of base without making it active, as
// knowledge fetched from another instance arrives.
func (m *MemoryStore) Stage(base knowledge.Snapshot, c knowledge.Commit) (knowledge.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.commit(base, c, false)
}

func (m *MemoryStore) commit(base knowledge.Snapshot, c knowledge.Commit, activate bool) (knowledge.Snapshot, error) {
	files := map[string][]byte{}
	if b, ok := m.snaps[base]; ok {
		files = maps.Clone(b.files)
	}
	var changed []knowledge.PathChange
	for _, ch := range c.Changes {
		_, existed := files[ch.Path]
		switch {
		case ch.Delete:
			delete(files, ch.Path)
			changed = append(changed, knowledge.PathChange{Path: ch.Path, Action: "deleted"})
		case existed:
			files[ch.Path] = slices.Clone(ch.Content)
			changed = append(changed, knowledge.PathChange{Path: ch.Path, Action: "modified"})
		default:
			files[ch.Path] = slices.Clone(ch.Content)
			changed = append(changed, knowledge.PathChange{Path: ch.Path, Action: "added"})
		}
	}
	if c.Check != nil {
		if err := c.Check(memFiles(files)); err != nil {
			return "", err
		}
	}
	slices.SortFunc(changed, func(a, b knowledge.PathChange) int {
		if a.Path < b.Path {
			return -1
		}
		return 1
	})
	s := knowledge.Snapshot(fmt.Sprintf("mem-%d", len(m.snaps)+1))
	e := knowledge.LogEntry{Snapshot: s, Author: c.Author, When: time.Now().UTC(), Subject: c.Subject, Body: c.Body,
		Trailers: slices.Clone(c.Trailers), Changed: changed}
	if base != "" {
		e.Parents = []knowledge.Snapshot{base}
	}
	m.snaps[s] = memSnap{files: files, entry: e, published: activate}
	if activate {
		m.active = s
	}
	return s, nil
}

func (m *MemoryStore) Advance(_ context.Context, a knowledge.Advance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.Expected != m.active {
		return knowledge.ErrStale
	}
	target, ok := m.snaps[a.Target]
	if !ok {
		return fmt.Errorf("snapshot %q: %w", a.Target, knowledge.ErrNotFound)
	}
	var chain []knowledge.Snapshot
	for s := a.Target; s != m.active; {
		if s == "" {
			return knowledge.ErrDiverged
		}
		chain = append(chain, s)
		s = ""
		if p := m.snaps[chain[len(chain)-1]].entry.Parents; len(p) > 0 {
			s = p[0]
		}
	}
	if len(chain) == 0 {
		return fmt.Errorf("%s is already active: %w", a.Target, knowledge.ErrInvalid)
	}
	if a.Check != nil {
		if err := a.Check(memFiles(target.files)); err != nil {
			return err
		}
	}
	for _, s := range chain {
		sn := m.snaps[s]
		sn.published, sn.imported = true, true
		m.snaps[s] = sn
	}
	m.activations = append(m.activations, knowledge.ActivationEntry{Target: a.Target, Previous: m.active, Author: a.Author,
		When: time.Now().UTC(), Subject: a.Subject, Body: a.Body, Trailers: slices.Clone(a.Trailers)})
	m.active = a.Target
	return nil
}

func (m *MemoryStore) Activations(_ context.Context, limit int) ([]knowledge.ActivationEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []knowledge.ActivationEntry
	for i := len(m.activations) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.activations[i])
	}
	return out, nil
}

func (m *MemoryStore) Log(_ context.Context, from knowledge.Snapshot, limit int) ([]knowledge.LogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sn, ok := m.snaps[from]; !ok || !sn.published {
		return nil, fmt.Errorf("snapshot %q: %w", from, knowledge.ErrNotFound)
	}
	var out []knowledge.LogEntry
	for s := from; s != "" && len(out) < limit; {
		e := m.snaps[s].entry
		out = append(out, e)
		s = ""
		if len(e.Parents) > 0 {
			s = e.Parents[0]
		}
	}
	return out, nil
}

func (m *MemoryStore) Instance(context.Context) (string, error) { return "memory-instance", nil }

func (m *MemoryStore) Imported(_ context.Context, s knowledge.Snapshot) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sn, ok := m.snaps[s]
	if !ok || !sn.published {
		return false, fmt.Errorf("snapshot %q: %w", s, knowledge.ErrNotFound)
	}
	return sn.imported, nil
}
