package gitcore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

const quarantineName = "polaroid-quarantine"

// Candidate is a remote's snapshot as fetched: its new objects stay in a
// quarantine of their own, outside the repository's object store, until
// Import. Its snapshots are readable only through Candidate.Tree.
type Candidate struct {
	Remote   Remote
	Snapshot Snapshot // the remote branch's snapshot; "" when it holds none
	Previous Snapshot // the tracking ref when fetched
	Bytes    int64    // pack bytes received
	Objects  int      // objects received

	r        *Repository
	lim      TransferLimits
	q        *quarantine // nil when nothing was received
	verified bool
	indexes  map[Snapshot]*index
}

type quarantine struct {
	dir  string
	lock *os.File
	st   *filesystem.Storage
}

// newQuarantine creates a private directory for one fetch, held by a lock
// so that abandoned ones (from a crash) can be told apart and removed.
func (r *Repository) newQuarantine() (*quarantine, error) {
	base := filepath.Join(r.dir, quarantineName)
	if err := os.MkdirAll(base, 0o700); err != nil { //nolint:gosec // inside the repository directory
		return nil, err
	}
	r.removeAbandonedQuarantines(base)
	dir, err := os.MkdirTemp(base, "q-")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // inside the quarantine
	if err == nil && !tryFlock(f) {
		_ = f.Close()
		err = errors.New("gitcore: cannot lock a new quarantine")
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	st := filesystem.NewStorage(osfs.New(dir), cache.NewObjectLRU(8<<20))
	return &quarantine{dir: dir, lock: f, st: st}, nil
}

func (r *Repository) removeAbandonedQuarantines(base string) {
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range ents {
		dir := filepath.Join(base, e.Name())
		f, err := os.Open(filepath.Join(dir, "lock")) //nolint:gosec // inside the quarantine directory
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				_ = os.RemoveAll(dir) // crashed before taking its lock
			}
			continue
		}
		if tryFlock(f) {
			_ = os.RemoveAll(dir)
		}
		_ = f.Close()
	}
}

func (q *quarantine) close() error {
	err := q.st.Close()
	_ = q.lock.Close()
	return errors.Join(err, os.RemoveAll(q.dir))
}

// receive spools the pack, bounded by MaxPackBytes, checks its headers
// against the limits, and only then indexes it.
func (q *quarantine) receive(rd io.Reader, lim TransferLimits) (int64, int, error) {
	f, err := os.OpenFile(filepath.Join(q.dir, "incoming.pack"), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600) //nolint:gosec // inside the quarantine
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	n, err := io.Copy(f, io.LimitReader(rd, lim.MaxPackBytes+1))
	if err != nil {
		return n, 0, fmt.Errorf("transfer interrupted after %d bytes: %w", n, err)
	}
	if n > lim.MaxPackBytes {
		return n, 0, fmt.Errorf("%w: the pack exceeds %d bytes", ErrTooLarge, lim.MaxPackBytes)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return n, 0, err
	}
	objects, err := scanPack(f, lim)
	if err != nil {
		return n, objects, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return n, objects, err
	}
	if err := packfile.UpdateObjectStorage(q.st, f); err != nil {
		return n, objects, fmt.Errorf("%w: pack: %w", ErrRejected, err)
	}
	return n, objects, nil
}

// scanPack reads every object header, and every delta's result size, so
// that no object or delta is expanded beyond the limits when indexing. It
// also checks the pack's checksum.
func scanPack(r io.Reader, lim TransferLimits) (int, error) {
	s := packfile.NewScanner(r)
	_, count, err := s.Header()
	if err != nil {
		return 0, fmt.Errorf("%w: pack header: %w", ErrRejected, err)
	}
	if int64(count) > int64(lim.MaxObjects) {
		return int(count), fmt.Errorf("%w: %d objects exceed %d", ErrTooLarge, count, lim.MaxObjects)
	}
	var total int64
	for range count {
		h, err := s.NextObjectHeader()
		if err != nil {
			return int(count), fmt.Errorf("%w: pack: %w", ErrRejected, err)
		}
		if h.Length > lim.MaxObjectBytes {
			return int(count), fmt.Errorf("%w: an object of %d bytes exceeds %d", ErrTooLarge, h.Length, lim.MaxObjectBytes)
		}
		total += h.Length
		if h.Type == plumbing.OFSDeltaObject || h.Type == plumbing.REFDeltaObject {
			var b bytes.Buffer
			if _, _, err := s.NextObject(&b); err != nil {
				return int(count), fmt.Errorf("%w: pack: %w", ErrRejected, err)
			}
			size, ok := deltaResultSize(b.Bytes())
			if !ok {
				return int(count), fmt.Errorf("%w: malformed delta", ErrRejected)
			}
			if size > lim.MaxObjectBytes {
				return int(count), fmt.Errorf("%w: a delta's result of %d bytes exceeds %d", ErrTooLarge, size, lim.MaxObjectBytes)
			}
			total += size
		}
		if total > lim.MaxInflatedBytes {
			return int(count), fmt.Errorf("%w: objects expand to more than %d bytes", ErrTooLarge, lim.MaxInflatedBytes)
		}
	}
	if _, err := s.Checksum(); err != nil {
		return int(count), fmt.Errorf("%w: pack: %w", ErrRejected, err)
	}
	return int(count), nil
}

// deltaResultSize reads a Git delta's header: the base size, then the
// result size, each a little-endian base-128 number.
func deltaResultSize(b []byte) (int64, bool) {
	read := func() (int64, bool) {
		var v int64
		for shift := uint(0); len(b) > 0 && shift < 63; shift += 7 {
			c := b[0]
			b = b[1:]
			v |= int64(c&0x7f) << shift
			if c&0x80 == 0 {
				return v, true
			}
		}
		return 0, false
	}
	if _, ok := read(); !ok {
		return 0, false
	}
	return read()
}

// view reads the candidate's objects, then the repository's.
func (c *Candidate) view() storer.EncodedObjectStorer {
	u := unionStorer{main: c.r.st}
	if c.q != nil {
		u.q = c.q.st
	}
	return u
}

func (c *Candidate) received(h plumbing.Hash) bool {
	return c.q != nil && c.q.st.HasEncodedObject(h) == nil
}

// Verify checks everything received: each object hashes to its id; each
// new snapshot has at most one parent and a well-formed message, and its
// tree holds only safe, sorted, unique paths of regular files within the
// repository's limits; the history connects to a stored snapshot or to a
// root; and the pack holds nothing else. It returns the new snapshots,
// oldest first. Verify reads no content as knowledge.
func (c *Candidate) Verify(ctx context.Context) ([]Commit, error) {
	c.indexes = map[Snapshot]*index{}
	if c.Snapshot == "" {
		c.verified = true
		return nil, nil
	}
	if !c.Snapshot.valid() {
		return nil, fmt.Errorf("%w: snapshot %q", ErrRejected, c.Snapshot)
	}
	view := c.view()
	checked := map[plumbing.Hash]bool{}
	var chain []*object.Commit
	for h := plumbing.NewHash(string(c.Snapshot)); !h.IsZero(); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !c.received(h) {
			if c.r.st.HasEncodedObject(h) != nil {
				return nil, fmt.Errorf("%w: snapshot %s was neither received nor stored", ErrRejected, h)
			}
			break
		}
		if len(chain) >= c.lim.MaxCommits {
			return nil, fmt.Errorf("%w: more than %d new snapshots", ErrTooLarge, c.lim.MaxCommits)
		}
		if err := c.checkReceived(h, plumbing.CommitObject, int64(c.r.lim.MaxMessageBytes)+4096); err != nil {
			return nil, err
		}
		checked[h] = true
		cm, err := object.GetCommit(view, h)
		if err != nil {
			return nil, fmt.Errorf("%w: snapshot %s: %w", ErrRejected, h, err)
		}
		if err := c.checkCommit(cm); err != nil {
			return nil, err
		}
		chain = append(chain, cm)
		h = plumbing.ZeroHash
		if len(cm.ParentHashes) == 1 {
			h = cm.ParentHashes[0]
		}
	}
	for _, cm := range chain {
		ix := &index{files: map[string]plumbing.Hash{}}
		if err := c.walk(view, cm.TreeHash, "", 0, ix, checked); err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", cm.Hash, err)
		}
		if _, err := c.r.newIndex(ix.files); err != nil {
			return nil, fmt.Errorf("%w: snapshot %s: %w", ErrRejected, cm.Hash, err)
		}
		sort.Strings(ix.paths)
		c.indexes[Snapshot(cm.Hash.String())] = ix
	}
	if c.q != nil {
		extra := 0
		it, err := c.q.st.IterEncodedObjects(plumbing.AnyObject)
		if err != nil {
			return nil, err
		}
		if err := it.ForEach(func(o plumbing.EncodedObject) error {
			if !checked[o.Hash()] {
				extra++
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrRejected, err)
		}
		if extra > 0 {
			return nil, fmt.Errorf("%w: %d received objects belong to no received snapshot", ErrRejected, extra)
		}
	}
	out := make([]Commit, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		e, err := c.r.describe(ctx, view, chain[i])
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	c.verified = true
	return out, nil
}

// WalkHistory visits every commit from the candidate's snapshot back to, but
// excluding, until, oldest first. An empty until walks to the root. Unlike
// Verify, it includes commits already stored in the repository.
func (c *Candidate) WalkHistory(ctx context.Context, until Snapshot, visit func(Commit) error) error {
	if !c.verified {
		return errors.New("gitcore: the candidate is not verified")
	}
	if visit == nil {
		return errors.New("gitcore: history visitor is nil")
	}
	if until != "" && !until.valid() {
		return fmt.Errorf("%w: snapshot %q", ErrNotFound, until)
	}
	view := c.view()
	var hashes []plumbing.Hash
	h := plumbing.NewHash(string(c.Snapshot))
	reached := c.Snapshot == until
	for !h.IsZero() && !reached {
		if err := ctx.Err(); err != nil {
			return err
		}
		if Snapshot(h.String()) == until {
			reached = true
			break
		}
		cm, err := object.GetCommit(view, h)
		if err != nil {
			return fmt.Errorf("%w: commit %s: %w", ErrRejected, h, err)
		}
		if err := c.checkCommit(cm); err != nil {
			return err
		}
		hashes = append(hashes, h)
		h = plumbing.ZeroHash
		if len(cm.ParentHashes) == 1 {
			h = cm.ParentHashes[0]
		}
	}
	if until != "" && !reached {
		return fmt.Errorf("%w: snapshot %s is not an ancestor of candidate %s", ErrNotFound, until, c.Snapshot)
	}
	for i := len(hashes) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		cm, err := object.GetCommit(view, hashes[i])
		if err != nil {
			return fmt.Errorf("%w: commit %s: %w", ErrRejected, hashes[i], err)
		}
		entry, err := c.r.describe(ctx, view, cm)
		if err != nil {
			return err
		}
		if err := visit(entry); err != nil {
			return err
		}
	}
	return nil
}

func (c *Candidate) checkCommit(cm *object.Commit) error {
	switch {
	case len(cm.ParentHashes) > 1:
		return fmt.Errorf("%w: snapshot %s merges %d histories; knowledge history is linear", ErrRejected, cm.Hash, len(cm.ParentHashes))
	case len(cm.Message) > c.r.lim.MaxMessageBytes || strings.Contains(cm.Message, "\x00"):
		return fmt.Errorf("%w: snapshot %s: message over %d bytes or holding NUL", ErrRejected, cm.Hash, c.r.lim.MaxMessageBytes)
	case badAuthor.MatchString(cm.Author.Name) || badAuthor.MatchString(cm.Committer.Name) || strings.TrimSpace(cm.Author.Name) == "":
		return fmt.Errorf("%w: snapshot %s: malformed author", ErrRejected, cm.Hash)
	}
	return nil
}

// checkReceived reads a received object in full, within max bytes, and
// recomputes its id.
func (c *Candidate) checkReceived(h plumbing.Hash, t plumbing.ObjectType, maxBytes int64) error {
	o, err := c.q.st.EncodedObject(t, h)
	if err != nil {
		return fmt.Errorf("%w: object %s: %w", ErrRejected, h, err)
	}
	if o.Size() > maxBytes {
		return fmt.Errorf("%w: object %s of %d bytes exceeds %d", ErrRejected, h, o.Size(), maxBytes)
	}
	rd, err := o.Reader()
	if err != nil {
		return fmt.Errorf("%w: object %s: %w", ErrRejected, h, err)
	}
	defer func() { _ = rd.Close() }()
	b, err := io.ReadAll(io.LimitReader(rd, maxBytes+1))
	if err != nil {
		return fmt.Errorf("%w: object %s: %w", ErrRejected, h, err)
	}
	if got := plumbing.ComputeHash(t, b); got != h {
		return fmt.Errorf("%w: object %s hashes to %s", ErrRejected, h, got)
	}
	return nil
}

// walk is the strict form of Repository.walk for received trees: entries
// must also be in Git's order without duplicates, and every received
// object is checked once.
func (c *Candidate) walk(view storer.EncodedObjectStorer, h plumbing.Hash, prefix string, depth int, ix *index, checked map[plumbing.Hash]bool) error {
	lim := c.r.lim
	if depth >= lim.MaxPathDepth {
		return fmt.Errorf("%w: tree deeper than %d", ErrRejected, lim.MaxPathDepth)
	}
	if c.received(h) && !checked[h] {
		if err := c.checkReceived(h, plumbing.TreeObject, c.lim.MaxObjectBytes); err != nil {
			return err
		}
		checked[h] = true
	}
	t, err := object.GetTree(view, h)
	if err != nil {
		return fmt.Errorf("%w: tree %q: %w", ErrRejected, prefix, err)
	}
	prev := ""
	for _, e := range t.Entries {
		p := prefix + e.Name
		if name := gitName(e); name <= prev {
			return fmt.Errorf("%w: tree %q is not sorted or repeats %q", ErrRejected, prefix, e.Name)
		} else {
			prev = name
		}
		if !safeName(e.Name) || len(p) > lim.MaxPathBytes {
			return fmt.Errorf("%w: tree entry %q is not a safe path", ErrRejected, p)
		}
		switch e.Mode {
		case filemode.Dir:
			if err := c.walk(view, e.Hash, p+"/", depth+1, ix, checked); err != nil {
				return err
			}
		case filemode.Regular:
			if len(ix.files) >= lim.MaxFiles {
				return fmt.Errorf("%w: more than %d files", ErrRejected, lim.MaxFiles)
			}
			switch {
			case checked[e.Hash]:
			case c.received(e.Hash):
				if err := c.checkReceived(e.Hash, plumbing.BlobObject, lim.MaxFileBytes); err != nil {
					return fmt.Errorf("file %q: %w", p, err)
				}
				checked[e.Hash] = true
			case c.r.st.HasEncodedObject(e.Hash) != nil:
				return fmt.Errorf("%w: file %q: object %s is missing", ErrRejected, p, e.Hash)
			}
			ix.files[p] = e.Hash
			ix.paths = append(ix.paths, p)
		default:
			return fmt.Errorf("%w: tree entry %q has mode %s; only regular files are stored", ErrRejected, p, e.Mode)
		}
	}
	return nil
}

// Tree is the read view of one of the candidate's new snapshots, after
// Verify. Reads recompute each file's id.
func (c *Candidate) Tree(_ context.Context, s Snapshot) (*Tree, error) {
	ix, ok := c.indexes[s]
	if !c.verified || !ok {
		return nil, fmt.Errorf("candidate snapshot %s: %w", s, ErrNotFound)
	}
	return &Tree{r: c.r, snap: s, ix: ix, cand: c}, nil
}

func (c *Candidate) readBlob(h plumbing.Hash) ([]byte, error) {
	if !c.received(h) {
		return c.r.readBlob(h)
	}
	o, err := c.q.st.EncodedObject(plumbing.BlobObject, h)
	if err != nil {
		return nil, fmt.Errorf("%w: blob %s: %w", ErrRejected, h, err)
	}
	rd, err := o.Reader()
	if err != nil {
		return nil, fmt.Errorf("%w: blob %s: %w", ErrRejected, h, err)
	}
	defer func() { _ = rd.Close() }()
	b, err := io.ReadAll(io.LimitReader(rd, c.r.lim.MaxFileBytes+1))
	if err != nil || int64(len(b)) > c.r.lim.MaxFileBytes || plumbing.ComputeHash(plumbing.BlobObject, b) != h {
		return nil, fmt.Errorf("%w: blob %s does not verify", ErrRejected, h)
	}
	return b, nil
}

// Relation says how the candidate's snapshot relates to a local one.
type Relation string

const (
	Same     Relation = "same"
	Behind   Relation = "behind"   // the candidate descends from the local snapshot
	Ahead    Relation = "ahead"    // the local snapshot descends from the candidate
	Diverged Relation = "diverged" // neither descends from the other
)

// Relation compares the verified candidate with local, along first parents.
func (c *Candidate) Relation(_ context.Context, local Snapshot) (Relation, error) {
	if !c.verified {
		return "", errors.New("gitcore: the candidate is not verified")
	}
	switch {
	case c.Snapshot == local:
		return Same, nil
	case c.Snapshot == "":
		return Ahead, nil
	case local == "":
		return Behind, nil
	}
	if ok, err := c.r.descends(c.view(), c.Snapshot, local); err != nil || ok {
		return Behind, err
	}
	if !c.received(plumbing.NewHash(string(c.Snapshot))) {
		if ok, err := c.r.descends(c.r.st, local, c.Snapshot); err != nil || ok {
			return Ahead, err
		}
	}
	return Diverged, nil
}

// descends reports whether anc is from or one of its first-parent ancestors.
func (r *Repository) descends(st storer.EncodedObjectStorer, from, anc Snapshot) (bool, error) {
	for h := plumbing.NewHash(string(from)); !h.IsZero(); {
		if Snapshot(h.String()) == anc {
			return true, nil
		}
		cm, err := object.GetCommit(st, h)
		if err != nil {
			return false, fmt.Errorf("%w: commit %s: %w", ErrCorrupt, h, err)
		}
		h = plumbing.ZeroHash
		if len(cm.ParentHashes) > 0 {
			h = cm.ParentHashes[0]
		}
	}
	return false, nil
}

// Close discards whatever the candidate still holds in quarantine.
func (c *Candidate) Close() error {
	if c.q == nil {
		return nil
	}
	q := c.q
	c.q = nil
	return q.close()
}

// Import stores a verified candidate: its objects join the repository's
// object store (as the received pack, synced before it is moved in), and
// the tracking ref records the remote's snapshot. The active snapshot does
// not change. A tracking ref that moved since the fetch is ErrStale.
func (r *Repository) Import(_ context.Context, c *Candidate) error {
	if !c.verified || c.r != r {
		return errors.New("gitcore: only a verified candidate of this repository can be imported")
	}
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := r.readRef(c.Remote.trackingRef())
	if err != nil {
		return err
	}
	if cur != c.Previous {
		return ErrStale
	}
	if c.q != nil {
		if err := r.adoptPacks(c.q); err != nil {
			return err
		}
	}
	if c.Snapshot != "" && c.Snapshot != cur {
		if err := r.writeRef(c.Remote.trackingRef(), c.Snapshot); err != nil {
			return err
		}
	}
	return c.Close()
}

// adoptPacks moves a quarantine's packs into the object store: each file is
// synced, packs are moved before their indexes (Git finds a pack through
// its index), and the directory is synced.
func (r *Repository) adoptPacks(q *quarantine) error {
	if err := q.st.Close(); err != nil {
		return err
	}
	src := filepath.Join(q.dir, "objects", "pack")
	dst := filepath.Join(r.dir, "objects", "pack")
	ents, err := os.ReadDir(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil { //nolint:gosec // inside the repository directory
		return err
	}
	var names []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "pack-") {
			names = append(names, e.Name())
		}
	}
	rank := func(n string) int {
		if strings.HasSuffix(n, ".idx") {
			return 1
		}
		return 0
	}
	slices.SortFunc(names, func(a, b string) int { return rank(a) - rank(b) })
	for _, n := range names {
		f, err := os.Open(filepath.Join(src, n)) //nolint:gosec // inside the quarantine
		if err != nil {
			return err
		}
		err = f.Sync()
		_ = f.Close()
		if err == nil {
			err = os.Rename(filepath.Join(src, n), filepath.Join(dst, n)) //nolint:gosec // both inside the repository
		}
		if err != nil {
			return err
		}
	}
	if err := syncDir(dst); err != nil {
		return err
	}
	r.st.reindex()
	return nil
}

// trackingRefs lists the snapshots of every refs/remotes ref.
func (r *Repository) trackingRefs() ([]Snapshot, error) {
	seen := map[string]bool{}
	var out []Snapshot
	root := filepath.Join(r.dir, "refs", "remotes")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(r.dir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		s, err := r.readRef(name)
		if err != nil {
			return err
		}
		seen[name] = true
		out = append(out, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	packed, err := r.packedRefs()
	if err != nil {
		return nil, err
	}
	for name, v := range packed {
		if strings.HasPrefix(name, "refs/remotes/") && !seen[name] {
			s, err := parseRef(name, v)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// unionStorer reads a candidate's received objects, then the repository's.
type unionStorer struct {
	q    *filesystem.Storage
	main *lockedStorage
}

var errReadOnly = errors.New("gitcore: a candidate view is read-only")

func (u unionStorer) has(h plumbing.Hash) bool { return u.q != nil && u.q.HasEncodedObject(h) == nil }

func (u unionStorer) NewEncodedObject() plumbing.EncodedObject { return &plumbing.MemoryObject{} }
func (u unionStorer) SetEncodedObject(plumbing.EncodedObject) (plumbing.Hash, error) {
	return plumbing.ZeroHash, errReadOnly
}
func (u unionStorer) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	if u.has(h) {
		return u.q.EncodedObject(t, h)
	}
	return u.main.EncodedObject(t, h)
}
func (u unionStorer) IterEncodedObjects(plumbing.ObjectType) (storer.EncodedObjectIter, error) {
	return nil, errReadOnly
}
func (u unionStorer) HasEncodedObject(h plumbing.Hash) error {
	if u.has(h) {
		return nil
	}
	return u.main.HasEncodedObject(h)
}
func (u unionStorer) EncodedObjectSize(h plumbing.Hash) (int64, error) {
	if u.has(h) {
		return u.q.EncodedObjectSize(h)
	}
	return u.main.EncodedObjectSize(h)
}
func (u unionStorer) AddAlternate(string) error { return errReadOnly }
