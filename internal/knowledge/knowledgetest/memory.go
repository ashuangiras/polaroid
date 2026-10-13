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
	mu     sync.Mutex
	active knowledge.Snapshot
	snaps  map[knowledge.Snapshot]memSnap
}

type memSnap struct {
	files map[string][]byte
	entry knowledge.LogEntry
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
	if !ok {
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
	files := map[string][]byte{}
	if base, ok := m.snaps[m.active]; ok {
		files = maps.Clone(base.files)
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
	if m.active != "" {
		e.Parents = []knowledge.Snapshot{m.active}
	}
	m.snaps[s] = memSnap{files: files, entry: e}
	m.active = s
	return s, nil
}

func (m *MemoryStore) Log(_ context.Context, from knowledge.Snapshot, limit int) ([]knowledge.LogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.snaps[from]; !ok {
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
