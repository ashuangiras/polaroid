// Package gitstore keeps living knowledge in a bare Git repository through
// go-git: documents are blobs under procedures/, a snapshot is a commit,
// and refs/heads/active names the active snapshot. There is no second
// database: every index here is a rebuildable in-memory cache.
package gitstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge"
)

const (
	activeRef = "refs/heads/active"
	root      = "procedures"
)

var hexPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Store struct {
	dir   string
	st    *filesystem.Storage
	repo  *git.Repository
	pol   knowledge.Policy
	trust map[string]bool
	cache *knowledge.Cache

	wmu       sync.Mutex // one in-process writer; flock covers other processes
	mu        sync.Mutex // guards the caches below
	manif     map[string]map[string]string // snapshot -> key -> blob hash
	reachable map[string]bool              // snapshots at or below active
}

type Options struct {
	Policy         knowledge.Policy
	TrustedRemotes []string // remote names whose knowledge may be accepted
}

func Open(dir string, opt Options) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st := filesystem.NewStorageWithOptions(osfs.New(dir), cache.NewObjectLRU(32<<20), filesystem.Options{})
	repo, err := git.Open(st, nil)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		if repo, err = git.Init(st, nil); err == nil {
			err = st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, activeRef))
		}
	}
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, st: st, repo: repo, pol: opt.Policy, trust: map[string]bool{}, cache: knowledge.NewCache(1 << 16),
		manif: map[string]map[string]string{}, reachable: map[string]bool{}}
	for _, r := range opt.TrustedRemotes {
		s.trust[r] = true
	}
	return s, nil
}

func (s *Store) Close() error { return s.st.Close() }

// Head reads the active ref file, which only Polaroid writes, by rename.
func (s *Store) Head(context.Context) (string, error) {
	return s.readRef(activeRef)
}

func (s *Store) readRef(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(name)))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	h := strings.TrimSpace(string(b))
	if !hexPattern.MatchString(h) {
		return "", fmt.Errorf("ref %s is corrupt: %q", name, h)
	}
	return h, nil
}

func (s *Store) ReadActive(ctx context.Context, key string) (knowledge.Knowledge, error) {
	head, err := s.Head(ctx)
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	if head == "" {
		return knowledge.Knowledge{}, fmt.Errorf("%s: %w", key, knowledge.ErrNotFound)
	}
	return s.ReadAt(ctx, key, head)
}

// ReadAt serves only snapshots at or below the active one: fetched but
// unaccepted commits exist in the object store and must stay invisible.
func (s *Store) ReadAt(ctx context.Context, key, snapshot string) (knowledge.Knowledge, error) {
	if !knowledge.ValidKey(key) {
		return knowledge.Knowledge{}, fmt.Errorf("%s: %w", key, knowledge.ErrNotFound)
	}
	if err := s.checkReachable(ctx, snapshot); err != nil {
		return knowledge.Knowledge{}, err
	}
	m, err := s.manifest(snapshot)
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	h, ok := m[key]
	if !ok {
		return knowledge.Knowledge{}, fmt.Errorf("%s at %s: %w", key, snapshot, knowledge.ErrNotFound)
	}
	c, err := s.blob(h)
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	return knowledge.Knowledge{Key: key, Snapshot: snapshot, Hash: c.Hash, Procedure: c.Procedure}, nil
}

func (s *Store) checkReachable(ctx context.Context, snapshot string) error {
	s.mu.Lock()
	ok := s.reachable[snapshot]
	s.mu.Unlock()
	if ok {
		return nil
	}
	head, err := s.Head(ctx)
	if err != nil {
		return err
	}
	if head != "" {
		if err := s.markReachable(head); err != nil {
			return err
		}
	}
	s.mu.Lock()
	ok = s.reachable[snapshot]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("snapshot %s: %w", snapshot, knowledge.ErrNotFound)
	}
	return nil
}

func (s *Store) markReachable(from string) error {
	todo := []plumbing.Hash{plumbing.NewHash(from)}
	for len(todo) > 0 {
		h := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		s.mu.Lock()
		seen := s.reachable[h.String()]
		s.reachable[h.String()] = true
		s.mu.Unlock()
		if seen {
			continue
		}
		c, err := object.GetCommit(s.st, h)
		if err != nil {
			return err
		}
		todo = append(todo, c.ParentHashes...)
	}
	return nil
}

func (s *Store) blob(h string) (knowledge.Cached, error) {
	if c, ok := s.cache.Get(h); ok {
		return c, nil
	}
	o, err := s.st.EncodedObject(plumbing.BlobObject, plumbing.NewHash(h))
	if err != nil {
		return knowledge.Cached{}, err
	}
	if max := s.pol.MaxDocBytes; max > 0 && o.Size() > int64(max) {
		return knowledge.Cached{}, fmt.Errorf("blob %s of %d bytes exceeds %d", h, o.Size(), max)
	}
	r, err := o.Reader()
	if err != nil {
		return knowledge.Cached{}, err
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		return knowledge.Cached{}, err
	}
	p, err := knowledge.Decode(b, s.pol.MaxDocBytes)
	if err != nil {
		return knowledge.Cached{}, fmt.Errorf("blob %s: %w", h, err)
	}
	c := knowledge.Cached{Procedure: p, Hash: knowledge.Hash(b)}
	s.cache.Put(h, c)
	return c, nil
}

// manifest maps every key of a snapshot to its blob, and is also the
// structural check of a tree: only procedures/<segments>.json regular files,
// each path the image of a valid key. Anything else makes the tree invalid.
func (s *Store) manifest(snapshot string) (map[string]string, error) {
	s.mu.Lock()
	m, ok := s.manif[snapshot]
	s.mu.Unlock()
	if ok {
		return m, nil
	}
	if !hexPattern.MatchString(snapshot) {
		return nil, fmt.Errorf("snapshot %q: %w", snapshot, knowledge.ErrNotFound)
	}
	c, err := object.GetCommit(s.st, plumbing.NewHash(snapshot))
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return nil, fmt.Errorf("snapshot %s: %w", snapshot, knowledge.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	t, err := c.Tree()
	if err != nil {
		return nil, err
	}
	m = map[string]string{}
	for _, e := range t.Entries {
		if e.Name != root || e.Mode != filemode.Dir {
			return nil, invalid("tree entry %q is not allowed at the root", e.Name)
		}
	}
	w := object.NewTreeWalker(t, true, nil)
	defer w.Close()
	for {
		name, e, err := w.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, invalid("tree: %v", err)
		}
		if e.Mode == filemode.Dir {
			continue
		}
		key, ok := pathKey(name)
		if !ok || e.Mode != filemode.Regular {
			return nil, invalid("tree entry %q (mode %s) is not a procedure document", name, e.Mode)
		}
		if s.pol.MaxProcedures > 0 && len(m) >= s.pol.MaxProcedures {
			return nil, invalid("more than %d procedures", s.pol.MaxProcedures)
		}
		m[key] = e.Hash.String()
	}
	s.mu.Lock()
	if len(s.manif) > 8 {
		clear(s.manif)
	}
	s.manif[snapshot] = m
	s.mu.Unlock()
	return m, nil
}

func invalid(format string, args ...any) error {
	return &knowledge.ValidationError{Problems: []string{fmt.Sprintf(format, args...)}}
}

func keyPath(key string) string { return root + "/" + strings.ReplaceAll(key, ".", "/") + ".json" }

func pathKey(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, root+"/")
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, ".json")
	key := strings.ReplaceAll(rest, "/", ".")
	return key, ok && !strings.Contains(rest, ".") && knowledge.ValidKey(key)
}

func (s *Store) Keys(_ context.Context, snapshot string) ([]string, error) {
	if err := s.checkReachable(context.Background(), snapshot); err != nil {
		return nil, err
	}
	m, err := s.manifest(snapshot)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *Store) Publish(ctx context.Context, expected string, changes []knowledge.Change, note knowledge.Note) (string, error) {
	return s.commit(ctx, expected, "", changes, note)
}

// Reconcile publishes an explicit resolution of a divergence: the tree of
// expected with changes applied, recording other as a second parent so that
// both histories stay reachable and the other side can fast-forward to it.
func (s *Store) Reconcile(ctx context.Context, expected, other string, changes []knowledge.Change, note knowledge.Note) (string, error) {
	if !hexPattern.MatchString(other) {
		return "", fmt.Errorf("snapshot %q: %w", other, knowledge.ErrNotFound)
	}
	if _, err := object.GetCommit(s.st, plumbing.NewHash(other)); err != nil {
		return "", fmt.Errorf("snapshot %s: %w", other, knowledge.ErrNotFound)
	}
	return s.commit(ctx, expected, other, changes, note)
}

func (s *Store) commit(ctx context.Context, expected, other string, changes []knowledge.Change, note knowledge.Note) (string, error) {
	bodies, err := knowledge.CheckChanges(changes, s.pol)
	if err != nil {
		return "", err
	}
	unlock, err := s.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	head, err := s.Head(ctx)
	if err != nil {
		return "", err
	}
	if head != expected {
		return "", knowledge.ErrConflict
	}
	base := map[string]string{}
	var baseTree plumbing.Hash
	var parents []plumbing.Hash
	if head != "" {
		if base, err = s.manifest(head); err != nil {
			return "", err
		}
		c, err := object.GetCommit(s.st, plumbing.NewHash(head))
		if err != nil {
			return "", err
		}
		baseTree, parents = c.TreeHash, []plumbing.Hash{c.Hash}
	}
	if other != "" {
		parents = append(parents, plumbing.NewHash(other))
	}
	load := func(_, h string) (*knowledge.Procedure, error) {
		c, err := s.blob(h)
		return c.Procedure, err
	}
	if err := knowledge.Validate(&knowledge.Overlay{Base: base, Load: load, Changes: changes}, s.pol); err != nil {
		return "", err
	}

	w := &writer{st: s.st}
	edits := map[string]*plumbing.Hash{}
	for i, c := range changes {
		if c.Procedure == nil {
			edits[keyPath(c.Key)] = nil
			continue
		}
		h, err := w.blob(bodies[i])
		if err != nil {
			return "", err
		}
		edits[keyPath(c.Key)] = &h
	}
	tree, empty, err := w.edit(baseTree, edits)
	if err == nil && empty {
		tree, err = w.put(&object.Tree{})
	}
	if err != nil {
		return "", err
	}
	if note.Time.IsZero() {
		note.Time = time.Now().UTC()
	}
	sig := object.Signature{Name: note.Author, Email: note.Author + "@polaroid.invalid", When: note.Time}
	commit, err := w.put(&object.Commit{Author: sig, Committer: sig, Message: note.Message, TreeHash: tree, ParentHashes: parents})
	if err != nil {
		return "", err
	}
	if err := w.sync(s.dir); err != nil {
		return "", err
	}
	if err := s.writeRef(activeRef, commit.String()); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.reachable[commit.String()] = true
	s.mu.Unlock()
	if other != "" {
		if err := s.markReachable(other); err != nil {
			return "", err
		}
	}
	return commit.String(), nil
}

// lock serialises writers: a mutex in this process, flock across processes.
// The kernel drops the flock when a process dies, so no stale lock survives a crash.
func (s *Store) lock() (func(), error) {
	s.wmu.Lock()
	f, err := os.OpenFile(filepath.Join(s.dir, "polaroid.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	}
	if err != nil {
		if f != nil {
			f.Close()
		}
		s.wmu.Unlock()
		return nil, err
	}
	return func() { f.Close(); s.wmu.Unlock() }, nil
}

// writeRef replaces a ref atomically and durably: temp file, fsync, rename,
// fsync of the directory. go-git's own ref update truncates and rewrites the
// file in place without fsync, which a crash can leave empty.
func (s *Store) writeRef(name, hash string) error {
	path := filepath.Join(s.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ref-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(hash + "\n"); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writer writes objects and remembers them, so they can be fsynced before
// the ref that makes them reachable moves: go-git writes loose objects by
// temp file and rename, without fsync.
type writer struct {
	st      *filesystem.Storage
	written []plumbing.Hash
}

type encoder interface {
	Encode(plumbing.EncodedObject) error
}

func (w *writer) put(o encoder) (plumbing.Hash, error) {
	obj := w.st.NewEncodedObject()
	if err := o.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return w.store(obj)
}

func (w *writer) store(obj plumbing.EncodedObject) (plumbing.Hash, error) {
	if w.st.HasEncodedObject(obj.Hash()) == nil {
		return obj.Hash(), nil
	}
	h, err := w.st.SetEncodedObject(obj)
	if err == nil {
		w.written = append(w.written, h)
	}
	return h, err
}

func (w *writer) blob(b []byte) (plumbing.Hash, error) {
	obj := w.st.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	wr, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := wr.Write(b); err != nil {
		return plumbing.ZeroHash, err
	}
	if err := wr.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return w.store(obj)
}

// edit applies edits (path -> blob, nil to delete) to the tree base and
// returns the new tree, rewriting only the trees on the edited paths.
func (w *writer) edit(base plumbing.Hash, edits map[string]*plumbing.Hash) (plumbing.Hash, bool, error) {
	entries := map[string]object.TreeEntry{}
	if !base.IsZero() {
		t, err := object.GetTree(w.st, base)
		if err != nil {
			return plumbing.ZeroHash, false, err
		}
		for _, e := range t.Entries {
			entries[e.Name] = e
		}
	}
	sub := map[string]map[string]*plumbing.Hash{}
	for p, h := range edits {
		if dir, rest, ok := strings.Cut(p, "/"); ok {
			if sub[dir] == nil {
				sub[dir] = map[string]*plumbing.Hash{}
			}
			sub[dir][rest] = h
		} else if h == nil {
			delete(entries, p)
		} else {
			entries[p] = object.TreeEntry{Name: p, Mode: filemode.Regular, Hash: *h}
		}
	}
	for dir, e := range sub {
		var child plumbing.Hash
		if cur, ok := entries[dir]; ok && cur.Mode == filemode.Dir {
			child = cur.Hash
		}
		h, empty, err := w.edit(child, e)
		if err != nil {
			return plumbing.ZeroHash, false, err
		}
		if empty {
			delete(entries, dir)
		} else {
			entries[dir] = object.TreeEntry{Name: dir, Mode: filemode.Dir, Hash: h}
		}
	}
	if len(entries) == 0 {
		return plumbing.ZeroHash, true, nil
	}
	t := &object.Tree{}
	for _, e := range entries {
		t.Entries = append(t.Entries, e)
	}
	sort.Slice(t.Entries, func(i, j int) bool { return gitName(t.Entries[i]) < gitName(t.Entries[j]) })
	h, err := w.put(t)
	return h, false, err
}

// gitName is the name Git sorts a tree entry by: directories sort as name/.
func gitName(e object.TreeEntry) string {
	if e.Mode == filemode.Dir {
		return e.Name + "/"
	}
	return e.Name
}

func (w *writer) sync(dir string) error {
	dirs := map[string]bool{}
	for _, h := range w.written {
		hs := h.String()
		p := filepath.Join(dir, "objects", hs[:2], hs[2:])
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
		dirs[filepath.Dir(p)] = true
	}
	for d := range dirs {
		if err := syncDir(d); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) History(ctx context.Context, limit int) ([]knowledge.Entry, error) {
	head, err := s.Head(ctx)
	if err != nil || head == "" {
		return nil, err
	}
	var out []knowledge.Entry
	h := plumbing.NewHash(head)
	for len(out) < limit && !h.IsZero() {
		c, err := object.GetCommit(s.st, h)
		if err != nil {
			return nil, err
		}
		e := knowledge.Entry{Snapshot: c.Hash.String(), Note: knowledge.Note{Author: c.Author.Name, Message: c.Message, Time: c.Author.When}}
		for _, p := range c.ParentHashes {
			e.Parents = append(e.Parents, p.String())
		}
		if e.Changed, err = s.changed(c); err != nil {
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

// changed lists the keys a commit changed against its first parent. Equal
// subtrees have equal hashes, so the diff only descends into what changed.
func (s *Store) changed(c *object.Commit) ([]string, error) {
	to, err := c.Tree()
	if err != nil {
		return nil, err
	}
	from := &object.Tree{}
	if len(c.ParentHashes) > 0 {
		p, err := object.GetCommit(s.st, c.ParentHashes[0])
		if err != nil {
			return nil, err
		}
		if from, err = p.Tree(); err != nil {
			return nil, err
		}
	}
	diff, err := object.DiffTree(from, to)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, ch := range diff {
		name := ch.To.Name
		if name == "" {
			name = ch.From.Name
		}
		if k, ok := pathKey(name); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
