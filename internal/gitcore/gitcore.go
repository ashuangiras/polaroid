// Package gitcore keeps Polaroid's knowledge snapshots in a bare Git
// repository (ADR-0044). It stores files, snapshots, history and one active
// ref, durably and atomically; it knows nothing about procedures. Callers
// validate content through Publication.Validate.
//
// Git is used as a format, not as a database: go-git reads and decodes
// objects, but this package writes loose objects and the active ref itself,
// fsyncs them, and serialises writers with a lock that also holds across
// processes, because go-git does none of these.
package gitcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

const (
	activeRef  = "refs/heads/active"
	lockName   = "polaroid.lock"
	tmpName    = "polaroid-tmp"
	reflogName = "logs/refs/heads/active"
)

// Snapshot identifies one published state of the whole tree: a commit id.
// The empty Snapshot means that nothing has been published.
type Snapshot string

var hexPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (s Snapshot) valid() bool { return hexPattern.MatchString(string(s)) }

var (
	ErrNotFound = errors.New("not found")
	// ErrStale: the expected snapshot is no longer the active one.
	ErrStale = errors.New("stale snapshot: the active snapshot has changed")
	// ErrCorrupt: the repository holds a damaged ref or object.
	ErrCorrupt = errors.New("repository is damaged")
	ErrInvalid = errors.New("invalid publication")
)

// InvalidError lists why a publication or a stored tree is not acceptable.
type InvalidError struct{ Problems []string }

func (e *InvalidError) Error() string {
	return "invalid publication: " + strings.Join(e.Problems, "; ")
}
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, args ...any) error {
	return &InvalidError{Problems: []string{fmt.Sprintf(format, args...)}}
}

// Limits bound what a repository accepts and reads.
type Limits struct {
	MaxFileBytes    int64
	MaxFiles        int
	MaxPathBytes    int
	MaxPathDepth    int
	MaxMessageBytes int
}

var DefaultLimits = Limits{MaxFileBytes: 1 << 20, MaxFiles: 100_000, MaxPathBytes: 255, MaxPathDepth: 16, MaxMessageBytes: 64 << 10}

type Options struct {
	Limits Limits // zero: DefaultLimits
}

// Repository is one bare repository. It is safe for concurrent use, and
// several processes may open the same directory.
type Repository struct {
	dir string
	st  *lockedStorage
	lim Limits

	wmu       sync.Mutex // one writer in this process; flock covers other processes
	mu        sync.Mutex // guards the caches below
	indexes   map[Snapshot]*index
	reachable map[Snapshot]bool        // snapshots at or below the active one
	blobs     map[plumbing.Hash][]byte // verified content; ids never change meaning
	blobBytes int
}

const blobCacheBytes = 32 << 20

// Open opens the repository in dir, creating an empty bare repository whose
// HEAD names the active ref when dir holds none.
func Open(dir string, opt Options) (*Repository, error) {
	lim := opt.Limits
	if lim == (Limits{}) {
		lim = DefaultLimits
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // the repository directory the caller chose
		return nil, err
	}
	st := &lockedStorage{Storage: filesystem.NewStorage(osfs.New(dir), cache.NewObjectLRUDefault())}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); errors.Is(err, fs.ErrNotExist) { //nolint:gosec // the repository directory the caller chose
		cfg := config.NewConfig()
		cfg.Core.IsBare = true
		if err := st.Init(); err != nil {
			return nil, err
		}
		if err := st.SetConfig(cfg); err != nil {
			return nil, err
		}
		// HEAD last: a repository without it is initialised again.
		if err := st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, activeRef)); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, tmpName), 0o700); err != nil { //nolint:gosec // inside the repository directory
		return nil, err
	}
	return &Repository{dir: dir, st: st, lim: lim, indexes: map[Snapshot]*index{}, reachable: map[Snapshot]bool{}, blobs: map[plumbing.Hash][]byte{}}, nil
}

func (r *Repository) Close() error { return r.st.Close() }

// Active returns the active snapshot, "" when nothing is published.
func (r *Repository) Active(context.Context) (Snapshot, error) {
	b, err := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(activeRef))) //nolint:gosec // inside the repository directory
	if errors.Is(err, fs.ErrNotExist) {
		return r.packedActive()
	}
	if err != nil {
		return "", err
	}
	return parseRef(string(b))
}

func parseRef(s string) (Snapshot, error) {
	v := Snapshot(strings.TrimSpace(s))
	if !v.valid() {
		if len(v) > 64 {
			v = v[:64]
		}
		return "", fmt.Errorf("%w: the active ref holds %q", ErrCorrupt, v)
	}
	return v, nil
}

// packedActive reads the active ref from packed-refs, where standard Git
// tools (clone --mirror, pack-refs, gc) may have moved it.
func (r *Repository) packedActive() (Snapshot, error) {
	b, err := os.ReadFile(filepath.Join(r.dir, "packed-refs")) //nolint:gosec // inside the repository directory
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if h, name, ok := strings.Cut(line, " "); ok && name == activeRef {
			return parseRef(h)
		}
	}
	return "", nil
}

// Tree is the read view of one snapshot. Every read through it sees that
// snapshot, however many publications follow.
type Tree struct {
	r       *Repository
	snap    Snapshot
	ix      *index
	pending map[plumbing.Hash][]byte // content not yet stored (a proposed tree)
}

type index struct {
	files map[string]plumbing.Hash
	paths []string // sorted
}

// Tree returns the view of a published snapshot: the active one or one of
// its ancestors. Commits that exist in the object store but were never
// published are not found.
func (r *Repository) Tree(ctx context.Context, s Snapshot) (*Tree, error) {
	if err := r.published(ctx, s); err != nil {
		return nil, err
	}
	ix, err := r.indexOf(s)
	if err != nil {
		return nil, err
	}
	return &Tree{r: r, snap: s, ix: ix}, nil
}

// Snapshot is "" for a proposed tree that is not published yet.
func (t *Tree) Snapshot() Snapshot { return t.snap }

// Paths lists the files of the tree, sorted.
func (t *Tree) Paths() []string { return slices.Clone(t.ix.paths) }

func (t *Tree) Has(path string) bool {
	_, ok := t.ix.files[path]
	return ok
}

func (t *Tree) Read(path string) ([]byte, error) {
	h, ok := t.ix.files[path]
	if !ok {
		return nil, fmt.Errorf("%s: %w", path, ErrNotFound)
	}
	if b, ok := t.pending[h]; ok {
		return slices.Clone(b), nil
	}
	return t.r.readBlob(h)
}

func (r *Repository) readBlob(h plumbing.Hash) ([]byte, error) {
	r.mu.Lock()
	b, ok := r.blobs[h]
	r.mu.Unlock()
	if ok {
		return slices.Clone(b), nil
	}
	o, err := r.st.EncodedObject(plumbing.BlobObject, h)
	if err != nil {
		return nil, fmt.Errorf("%w: blob %s: %w", ErrCorrupt, h, err)
	}
	if o.Size() > r.lim.MaxFileBytes {
		return nil, invalid("blob %s of %d bytes exceeds %d", h, o.Size(), r.lim.MaxFileBytes)
	}
	rd, err := o.Reader()
	if err != nil {
		return nil, fmt.Errorf("%w: blob %s: %w", ErrCorrupt, h, err)
	}
	defer func() { _ = rd.Close() }()
	b, err = io.ReadAll(rd)
	if err != nil {
		return nil, fmt.Errorf("%w: blob %s: %w", ErrCorrupt, h, err)
	}
	// go-git does not check that content matches its id; a substituted
	// object would otherwise read as valid knowledge.
	if got := plumbing.ComputeHash(plumbing.BlobObject, b); got != h {
		return nil, fmt.Errorf("%w: blob %s holds content hashing to %s", ErrCorrupt, h, got)
	}
	r.mu.Lock()
	if r.blobBytes+len(b) > blobCacheBytes {
		clear(r.blobs)
		r.blobBytes = 0
	}
	r.blobs[h] = b
	r.blobBytes += len(b)
	r.mu.Unlock()
	return slices.Clone(b), nil
}

func (r *Repository) published(ctx context.Context, s Snapshot) error {
	if !s.valid() {
		return fmt.Errorf("snapshot %q: %w", s, ErrNotFound)
	}
	r.mu.Lock()
	ok := r.reachable[s]
	r.mu.Unlock()
	if ok {
		return nil
	}
	active, err := r.Active(ctx)
	if err != nil {
		return err
	}
	if active != "" {
		if err := r.markReachable(active); err != nil {
			return err
		}
	}
	r.mu.Lock()
	ok = r.reachable[s]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("snapshot %s: %w", s, ErrNotFound)
	}
	return nil
}

func (r *Repository) markReachable(from Snapshot) error {
	todo := []plumbing.Hash{plumbing.NewHash(string(from))}
	for len(todo) > 0 {
		h := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		r.mu.Lock()
		seen := r.reachable[Snapshot(h.String())]
		r.mu.Unlock()
		if seen {
			continue
		}
		c, err := object.GetCommit(r.st, h)
		if err != nil {
			return fmt.Errorf("%w: commit %s: %w", ErrCorrupt, h, err)
		}
		r.mu.Lock()
		r.reachable[Snapshot(h.String())] = true
		r.mu.Unlock()
		todo = append(todo, c.ParentHashes...)
	}
	return nil
}

// indexOf maps every file of a snapshot to its blob. It is also the
// structural check of a stored tree: only safe paths, only regular files.
func (r *Repository) indexOf(s Snapshot) (*index, error) {
	r.mu.Lock()
	ix, ok := r.indexes[s]
	r.mu.Unlock()
	if ok {
		return ix, nil
	}
	c, err := object.GetCommit(r.st, plumbing.NewHash(string(s)))
	if err != nil {
		return nil, fmt.Errorf("%w: commit %s: %w", ErrCorrupt, s, err)
	}
	t, err := c.Tree()
	if err != nil {
		return nil, fmt.Errorf("%w: tree of %s: %w", ErrCorrupt, s, err)
	}
	ix = &index{files: map[string]plumbing.Hash{}}
	if err := r.walk(t, "", 0, ix); err != nil {
		return nil, err
	}
	sort.Strings(ix.paths)
	r.cacheIndex(s, ix)
	return ix, nil
}

func (r *Repository) walk(t *object.Tree, prefix string, depth int, ix *index) error {
	if depth >= r.lim.MaxPathDepth {
		return invalid("tree deeper than %d", r.lim.MaxPathDepth)
	}
	for _, e := range t.Entries {
		p := prefix + e.Name
		if !safeName(e.Name) || len(p) > r.lim.MaxPathBytes {
			return invalid("tree entry %q is not a safe path", p)
		}
		switch e.Mode {
		case filemode.Dir:
			sub, err := object.GetTree(r.st, e.Hash)
			if err != nil {
				return fmt.Errorf("%w: tree %s: %w", ErrCorrupt, p, err)
			}
			if err := r.walk(sub, p+"/", depth+1, ix); err != nil {
				return err
			}
		case filemode.Regular:
			if len(ix.files) >= r.lim.MaxFiles {
				return invalid("more than %d files", r.lim.MaxFiles)
			}
			ix.files[p] = e.Hash
			ix.paths = append(ix.paths, p)
		default:
			return invalid("tree entry %q has mode %s; only regular files are stored", p, e.Mode)
		}
	}
	return nil
}

func (r *Repository) cacheIndex(s Snapshot, ix *index) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.indexes) >= 16 {
		clear(r.indexes)
	}
	r.indexes[s] = ix
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// safeName accepts one path segment that no filesystem or Git tool reads
// specially: no "." or "..", no leading dot, no separators, no ".lock" suffix.
func safeName(s string) bool {
	return namePattern.MatchString(s) && !strings.HasSuffix(s, ".lock") && len(s) <= 255
}

// checkPath accepts a slash-separated relative path of safe segments.
func (r *Repository) checkPath(p string) error {
	if p == "" || len(p) > r.lim.MaxPathBytes {
		return invalid("path %q is empty or longer than %d bytes", p, r.lim.MaxPathBytes)
	}
	segs := strings.Split(p, "/")
	if len(segs) > r.lim.MaxPathDepth {
		return invalid("path %q is deeper than %d", p, r.lim.MaxPathDepth)
	}
	for _, s := range segs {
		if !safeName(s) {
			return invalid("path %q has an unsafe segment %q", p, s)
		}
	}
	return nil
}
