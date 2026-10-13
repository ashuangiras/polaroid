package gitcore

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Change writes Content at Path, or removes Path when Delete is set.
type Change struct {
	Path    string
	Content []byte
	Delete  bool
}

// Trailer is one "Key: Value" line of structured provenance at the end of
// a commit message, readable by any Git tool.
type Trailer struct{ Key, Value string }

// Publication is one atomic change of the active snapshot.
type Publication struct {
	// Expected must be the active snapshot ("" for the first publication),
	// or the publication is refused with ErrStale.
	Expected Snapshot
	Changes  []Change
	Author   string // who authorised it, e.g. "copilot.agent"
	Subject  string // one line
	Body     string
	Trailers []Trailer
	When     time.Time // zero: now
	// Validate sees the complete proposed tree before anything is written;
	// an error refuses the publication and is returned as is.
	Validate func(*Tree) error
}

var (
	trailerKey = regexp.MustCompile(`^[A-Z][A-Za-z0-9-]{0,63}$`)
	badAuthor  = regexp.MustCompile(`[<>\x00-\x1f\x7f]`)
)

func singleLine(s string) bool { return !strings.ContainsAny(s, "\r\n\x00") }

func (p *Publication) message(lim Limits) (string, error) {
	var probs []string
	if strings.TrimSpace(p.Author) == "" || badAuthor.MatchString(p.Author) || len(p.Author) > 128 {
		probs = append(probs, fmt.Sprintf("author %q is empty, too long or has <, > or control characters", p.Author))
	}
	if strings.TrimSpace(p.Subject) == "" || !singleLine(p.Subject) {
		probs = append(probs, "subject must be one non-empty line")
	}
	if strings.Contains(p.Body, "\x00") {
		probs = append(probs, "body must not contain NUL")
	}
	for _, t := range p.Trailers {
		if !trailerKey.MatchString(t.Key) || t.Value == "" || !singleLine(t.Value) || strings.TrimSpace(t.Value) != t.Value {
			probs = append(probs, fmt.Sprintf("trailer %q must be a capitalised key with one trimmed, non-empty line", t.Key))
		}
	}
	msg := strings.TrimSpace(p.Subject) + "\n"
	if body := strings.TrimSpace(p.Body); body != "" {
		msg += "\n" + body + "\n"
	}
	if len(p.Trailers) > 0 {
		msg += "\n"
		for _, t := range p.Trailers {
			msg += t.Key + ": " + t.Value + "\n"
		}
	}
	if len(msg) > lim.MaxMessageBytes {
		probs = append(probs, fmt.Sprintf("message of %d bytes exceeds %d", len(msg), lim.MaxMessageBytes))
	}
	if len(probs) > 0 {
		return "", &InvalidError{Problems: probs}
	}
	return msg, nil
}

// testHook, when set by tests, runs at each stage of a publication, so that
// a crash can be simulated exactly there.
var testHook func(stage string)

func hook(stage string) {
	if testHook != nil {
		testHook(stage)
	}
}

// Publish makes p's changes the new active snapshot:
//
//  1. take the writer lock (this process and every other one);
//  2. refuse unless Expected is still the active snapshot;
//  3. build the proposed tree and let Validate inspect all of it;
//  4. write every new object and fsync it and its directory;
//  5. replace the active ref by rename, fsync it, and append to the reflog.
//
// A crash before step 5 leaves the previous snapshot active and only
// unreachable objects behind; after it, the new one is active. If only the
// reflog append fails, the new snapshot is returned with that error: it is
// active, and Verify reports the gap.
func (r *Repository) Publish(ctx context.Context, p Publication) (Snapshot, error) {
	msg, err := p.message(r.lim)
	if err != nil {
		return "", err
	}
	if err := r.checkChanges(p.Changes); err != nil {
		return "", err
	}
	unlock, err := r.lock()
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := r.cleanTmp(); err != nil {
		return "", err
	}
	active, err := r.Active(ctx)
	if err != nil {
		return "", err
	}
	if active != p.Expected {
		return "", ErrStale
	}
	files := map[string]plumbing.Hash{}
	var parents []plumbing.Hash
	var baseTree plumbing.Hash
	if active != "" {
		base, err := r.indexOf(active)
		if err != nil {
			return "", err
		}
		files = maps.Clone(base.files)
		c, err := object.GetCommit(r.st, plumbing.NewHash(string(active)))
		if err != nil {
			return "", fmt.Errorf("%w: commit %s: %w", ErrCorrupt, active, err)
		}
		parents, baseTree = []plumbing.Hash{c.Hash}, c.TreeHash
	}
	pending := map[plumbing.Hash][]byte{}
	for _, c := range p.Changes {
		if c.Delete {
			if _, ok := files[c.Path]; !ok {
				return "", invalid("delete of %q, which does not exist", c.Path)
			}
			delete(files, c.Path)
			continue
		}
		h := plumbing.ComputeHash(plumbing.BlobObject, c.Content)
		files[c.Path] = h
		pending[h] = c.Content
	}
	ix, err := r.newIndex(files)
	if err != nil {
		return "", err
	}
	if p.Validate != nil {
		if err := p.Validate(&Tree{r: r, ix: ix, pending: pending}); err != nil {
			return "", err
		}
	}

	objects, tree, err := buildTree(files, pending)
	if err != nil {
		return "", err
	}
	if tree == baseTree && active != "" {
		return "", invalid("publication changes nothing")
	}
	when := p.When
	if when.IsZero() {
		when = time.Now()
	}
	sig := object.Signature{Name: p.Author, Email: p.Author + "@polaroid.invalid", When: when.UTC().Truncate(time.Second)}
	commit := &object.Commit{Author: sig, Committer: sig, Message: msg, TreeHash: tree, ParentHashes: parents}
	co := &plumbing.MemoryObject{}
	if err := commit.Encode(co); err != nil {
		return "", err
	}
	objects = append(objects, co)
	if err := ctx.Err(); err != nil {
		return "", err
	}

	hook("before-objects")
	if err := r.writeObjects(objects); err != nil {
		return "", err
	}
	hook("objects-durable")
	if err := ctx.Err(); err != nil {
		return "", err
	}
	next := Snapshot(co.Hash().String())
	if err := r.setActive(next); err != nil {
		return "", err
	}
	hook("ref-moved")
	if err := r.appendReflog(active, next, sig, "publish: "+strings.TrimSpace(p.Subject)); err != nil {
		return next, fmt.Errorf("published %s, but the reflog was not updated: %w", next, err)
	}
	r.mu.Lock()
	r.reachable[next] = true
	r.mu.Unlock()
	r.cacheIndex(next, ix)
	return next, nil
}

func (r *Repository) checkChanges(changes []Change) error {
	if len(changes) == 0 {
		return invalid("no changes")
	}
	var probs []string
	seen := map[string]bool{}
	for _, c := range changes {
		if err := r.checkPath(c.Path); err != nil {
			var ie *InvalidError
			errors.As(err, &ie)
			probs = append(probs, ie.Problems...)
			continue
		}
		if seen[c.Path] {
			probs = append(probs, fmt.Sprintf("path %q is changed twice", c.Path))
		}
		seen[c.Path] = true
		if c.Delete && c.Content != nil {
			probs = append(probs, fmt.Sprintf("path %q is deleted with content", c.Path))
		}
		if int64(len(c.Content)) > r.lim.MaxFileBytes {
			probs = append(probs, fmt.Sprintf("path %q: %d bytes exceed %d", c.Path, len(c.Content), r.lim.MaxFileBytes))
		}
	}
	if len(probs) > 0 {
		return &InvalidError{Problems: probs}
	}
	return nil
}

// newIndex checks the whole proposed tree: file count, no file that is also
// a directory, and no two paths that differ only in case (a checkout on a
// case-insensitive filesystem would merge them).
func (r *Repository) newIndex(files map[string]plumbing.Hash) (*index, error) {
	if len(files) > r.lim.MaxFiles {
		return nil, invalid("%d files exceed %d", len(files), r.lim.MaxFiles)
	}
	ix := &index{files: files, paths: make([]string, 0, len(files))}
	folded := map[string]string{}
	for p := range files {
		ix.paths = append(ix.paths, p)
		if q, ok := folded[strings.ToLower(p)]; ok {
			return nil, invalid("paths %q and %q differ only in case", p, q)
		}
		folded[strings.ToLower(p)] = p
	}
	sort.Strings(ix.paths)
	for i := 1; i < len(ix.paths); i++ {
		if strings.HasPrefix(ix.paths[i], ix.paths[i-1]+"/") {
			return nil, invalid("%q is both a file and a directory", ix.paths[i-1])
		}
	}
	return ix, nil
}

type dirNode struct {
	files map[string]plumbing.Hash
	dirs  map[string]*dirNode
}

// buildTree encodes the tree of files and returns every object of it that
// may be new (all blobs in pending, all trees) and the root tree's hash.
// Unchanged subtrees encode to the hashes they already have; writeObjects
// skips what is stored.
func buildTree(files map[string]plumbing.Hash, pending map[plumbing.Hash][]byte) ([]*plumbing.MemoryObject, plumbing.Hash, error) {
	root := &dirNode{files: map[string]plumbing.Hash{}, dirs: map[string]*dirNode{}}
	for p, h := range files {
		n := root
		segs := strings.Split(p, "/")
		for _, s := range segs[:len(segs)-1] {
			if n.dirs[s] == nil {
				n.dirs[s] = &dirNode{files: map[string]plumbing.Hash{}, dirs: map[string]*dirNode{}}
			}
			n = n.dirs[s]
		}
		n.files[segs[len(segs)-1]] = h
	}
	var out []*plumbing.MemoryObject
	for h, b := range pending {
		o := &plumbing.MemoryObject{}
		o.SetType(plumbing.BlobObject)
		if _, err := o.Write(b); err != nil {
			return nil, plumbing.ZeroHash, err
		}
		if o.Hash() != h {
			return nil, plumbing.ZeroHash, errors.New("gitcore: blob hash mismatch")
		}
		out = append(out, o)
	}
	var encode func(n *dirNode) (plumbing.Hash, error)
	encode = func(n *dirNode) (plumbing.Hash, error) {
		t := &object.Tree{}
		for name, h := range n.files {
			t.Entries = append(t.Entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: h})
		}
		for name, d := range n.dirs {
			h, err := encode(d)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			t.Entries = append(t.Entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
		}
		sort.Slice(t.Entries, func(i, j int) bool { return gitName(t.Entries[i]) < gitName(t.Entries[j]) })
		o := &plumbing.MemoryObject{}
		if err := t.Encode(o); err != nil {
			return plumbing.ZeroHash, err
		}
		out = append(out, o)
		return o.Hash(), nil
	}
	h, err := encode(root)
	return out, h, err
}

// gitName is the name Git sorts a tree entry by: a directory sorts as name/.
func gitName(e object.TreeEntry) string {
	if e.Mode == filemode.Dir {
		return e.Name + "/"
	}
	return e.Name
}

// writeObjects stores every object that is not stored yet as a loose
// object: compressed into a temp file, fsynced, renamed into place, and its
// directory fsynced. An object found in the store was written the same way
// or arrived in a pack.
func (r *Repository) writeObjects(objs []*plumbing.MemoryObject) error {
	dirs := map[string]bool{}
	newDir := false
	for _, o := range objs {
		h := o.Hash()
		if r.st.HasEncodedObject(h) == nil {
			continue
		}
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		fmt.Fprintf(zw, "%s %d\x00", o.Type(), o.Size())
		rd, err := o.Reader()
		if err != nil {
			return err
		}
		if _, err := io.Copy(zw, rd); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		hs := h.String()
		dir := filepath.Join(r.dir, "objects", hs[:2])
		if _, err := os.Stat(dir); err != nil { //nolint:gosec // an object directory named by a hash
			if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) { //nolint:gosec // same directory
				return err
			}
			newDir = true
		}
		if err := r.replaceFile(filepath.Join(dir, hs[2:]), buf.Bytes(), 0o444, "object-temp"); err != nil {
			return err
		}
		dirs[dir] = true
	}
	for d := range dirs {
		if err := syncDir(d); err != nil {
			return err
		}
	}
	if newDir {
		return syncDir(filepath.Join(r.dir, "objects"))
	}
	return nil
}

// replaceFile writes data to a temp file, fsyncs it and renames it to path.
// The caller fsyncs path's directory.
func (r *Repository) replaceFile(path string, data []byte, perm os.FileMode, stage string) error {
	f, err := os.CreateTemp(filepath.Join(r.dir, tmpName), "w-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }() //nolint:gosec // our temp file; gone after the rename
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync() // F_FULLFSYNC on darwin
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), perm) //nolint:gosec // a temp file this package created
	}
	if err == nil {
		hook(stage)
		err = os.Rename(f.Name(), path) //nolint:gosec // both paths inside the repository
	}
	return err
}

// setActive replaces the active ref atomically and durably.
func (r *Repository) setActive(s Snapshot) error { return r.writeRef(activeRef, s) }

// appendReflog records the move in Git's reflog format, for audit and as
// the source of Recover.
func (r *Repository) appendReflog(from, to Snapshot, sig object.Signature, msg string) error {
	if from == "" {
		from = Snapshot(plumbing.ZeroHash.String())
	}
	path := filepath.Join(r.dir, filepath.FromSlash(reflogName))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil { //nolint:gosec // logs/refs/heads of the repository
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // the reflog inside the repository
	if err != nil {
		return err
	}
	line := fmt.Sprintf("%s %s %s <%s> %d +0000\t%s\n", from, to, sig.Name, sig.Email, sig.When.Unix(), strings.ReplaceAll(msg, "\n", " "))
	_, err = f.WriteString(line)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // a directory of the repository
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// lock serialises writers: a mutex in this process, flock across processes.
func (r *Repository) lock() (func(), error) {
	r.wmu.Lock()
	f, err := os.OpenFile(filepath.Join(r.dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		if err = flock(f); err != nil {
			_ = f.Close()
		}
	}
	if err != nil {
		r.wmu.Unlock()
		return nil, err
	}
	return func() { _ = f.Close(); r.wmu.Unlock() }, nil
}

// cleanTmp removes temp files that crashed writers left. It runs under the
// writer lock, so no live writer owns any of them.
func (r *Repository) cleanTmp() error {
	dir := filepath.Join(r.dir, tmpName)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil { //nolint:gosec // entries of the repository's temp directory
			return err
		}
	}
	return nil
}
