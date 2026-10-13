package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-billy/v5/osfs"

	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge"
)

// LocalScheme is an in-process, pure-Go transport for repositories on this
// machine (go-git's file:// transport runs the git-upload-pack binary).
// Networked instances would use go-git's https or ssh transports instead;
// everything after the fetch is the same.
const LocalScheme = "pgit"

func init() {
	client.InstallProtocol(LocalScheme, server.NewClient(server.NewFilesystemLoader(osfs.New("/"))))
}

// LocalURL addresses another store's repository through LocalScheme.
func LocalURL(dir string) string {
	abs, _ := filepath.Abs(dir)
	return LocalScheme + "://" + abs
}

var (
	ErrUntrusted = errors.New("remote is not trusted for publication")
	ErrNotNewer  = errors.New("incoming knowledge is already contained in the active knowledge")
)

// DivergedError: local and incoming knowledge both changed since Base.
// Neither is chosen; an operator resolves it with Reconcile or not at all.
type DivergedError struct{ Local, Incoming, Base string }

func (e *DivergedError) Error() string {
	return fmt.Sprintf("knowledge diverged: active %s and incoming %s share base %s", short(e.Local), short(e.Incoming), short(e.Base))
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// Incoming is fetched knowledge: present in the object store, invisible to
// readers, and not active until Accept succeeds.
type Incoming struct {
	Remote   string
	Snapshot string
	Bytes    int64 // pack bytes received by this fetch
	Objects  int   // objects received, when the server sent a pack
}

// MaxFetchBytes bounds what one fetch may add to the object store.
var MaxFetchBytes int64 = 256 << 20

// Fetch retrieves the remote's active knowledge into refs/polaroid/incoming/<remote>.
// Only objects this store lacks are transferred. Fetching approves nothing.
func (s *Store) Fetch(ctx context.Context, remote, url string) (Incoming, error) {
	if !knowledge.ValidKey("remote." + remote) {
		return Incoming{}, fmt.Errorf("remote name %q is invalid", remote)
	}
	before, err := s.packs()
	if err != nil {
		return Incoming{}, err
	}
	ref := "refs/polaroid/incoming/" + remote
	seen := "refs/polaroid/seen/" + remote
	fs := &fetchStorer{Storage: s.st}
	if h, err := s.st.Reference(plumbing.ReferenceName(seen)); err == nil {
		fs.refs = append(fs.refs, h)
	}
	r := git.NewRemote(fs, &config.RemoteConfig{Name: remote, URLs: []string{url}})
	err = r.FetchContext(ctx, &git.FetchOptions{RefSpecs: []config.RefSpec{config.RefSpec("+" + activeRef + ":" + ref)}, Tags: git.NoTags})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return Incoming{}, fmt.Errorf("fetch %s: %w", remote, err)
	}
	after, err := s.packs()
	if err != nil {
		return Incoming{}, err
	}
	in := Incoming{Remote: remote}
	var added []string
	for p, size := range after {
		if _, ok := before[p]; !ok && strings.HasSuffix(p, ".pack") {
			in.Bytes += size
			added = append(added, p)
		}
	}
	if in.Bytes > MaxFetchBytes {
		_ = s.st.RemoveReference(plumbing.ReferenceName(ref))
		for _, p := range added {
			os.Remove(p)
			os.Remove(strings.TrimSuffix(p, ".pack") + ".idx")
		}
		return Incoming{}, fmt.Errorf("fetch %s: %d bytes exceed %d", remote, in.Bytes, MaxFetchBytes)
	}
	h, err := s.st.Reference(plumbing.ReferenceName(ref))
	if err != nil {
		return Incoming{}, fmt.Errorf("fetch %s: no active knowledge: %w", remote, err)
	}
	in.Snapshot = h.Hash().String()
	if err := s.st.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(seen), h.Hash())); err != nil {
		return Incoming{}, err
	}
	for _, p := range added {
		in.Objects += packObjects(p)
	}
	return in, nil
}

// fetchStorer advertises, as the commits this store already has, only the
// remote's last fetched state, which the remote is known to hold. go-git's
// fetch would otherwise offer every local ref, and go-git's own upload-pack
// server fails with "object not found" on a commit it lacks (a standard Git
// server ignores it).
type fetchStorer struct {
	*filesystem.Storage
	refs []*plumbing.Reference
}

func (f *fetchStorer) IterReferences() (storer.ReferenceIter, error) {
	return storer.NewReferenceSliceIter(f.refs), nil
}

func (s *Store) packs() (map[string]int64, error) {
	dir := filepath.Join(s.dir, "objects", "pack")
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, e := range ents {
		if info, err := e.Info(); err == nil {
			out[filepath.Join(dir, e.Name())] = info.Size()
		}
	}
	return out, nil
}

// packObjects reads the object count from a pack header.
func packObjects(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	var h [12]byte
	if _, err := f.ReadAt(h[:], 0); err != nil {
		return 0
	}
	return int(h[8])<<24 | int(h[9])<<16 | int(h[10])<<8 | int(h[11])
}

// Accept validates incoming knowledge and, if this store's policy trusts
// its remote and it extends the active knowledge, makes it active. Every
// new snapshot is validated, not only the newest, because each becomes
// readable by snapshot once active. Divergence is reported, never merged.
func (s *Store) Accept(ctx context.Context, in Incoming) (string, error) {
	if !s.trust[in.Remote] {
		return "", fmt.Errorf("%s: %w", in.Remote, ErrUntrusted)
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
	if !hexPattern.MatchString(in.Snapshot) {
		return "", fmt.Errorf("snapshot %q: %w", in.Snapshot, knowledge.ErrNotFound)
	}
	inc, err := object.GetCommit(s.st, plumbing.NewHash(in.Snapshot))
	if err != nil {
		return "", fmt.Errorf("incoming %s: %w", in.Snapshot, err)
	}
	if head != "" {
		cur, err := object.GetCommit(s.st, plumbing.NewHash(head))
		if err != nil {
			return "", err
		}
		if head == in.Snapshot {
			return head, ErrNotNewer
		}
		if ok, err := inc.IsAncestor(cur); err != nil || ok {
			if err == nil {
				err = ErrNotNewer
			}
			return head, err
		}
		ok, err := cur.IsAncestor(inc)
		if err != nil {
			return "", err
		}
		if !ok {
			base := ""
			if bases, err := cur.MergeBase(inc); err == nil && len(bases) > 0 {
				base = bases[0].Hash.String()
			}
			return "", &DivergedError{Local: head, Incoming: in.Snapshot, Base: base}
		}
	}
	if err := s.validateRange(head, inc); err != nil {
		return "", err
	}
	if err := s.syncPacks(); err != nil {
		return "", err
	}
	if err := s.writeRef(activeRef, in.Snapshot); err != nil {
		return "", err
	}
	if err := s.markReachable(in.Snapshot); err != nil {
		return "", err
	}
	return in.Snapshot, nil
}

// MaxImportCommits bounds how much new history one Accept validates.
var MaxImportCommits = 10000

func (s *Store) validateRange(head string, tip *object.Commit) error {
	todo := []*object.Commit{tip}
	seen := map[plumbing.Hash]bool{}
	load := func(_, h string) (*knowledge.Procedure, error) {
		c, err := s.blob(h)
		return c.Procedure, err
	}
	for len(todo) > 0 {
		c := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[c.Hash] || c.Hash.String() == head || s.isReachable(c.Hash.String()) {
			continue
		}
		seen[c.Hash] = true
		if len(seen) > MaxImportCommits {
			return fmt.Errorf("more than %d new snapshots", MaxImportCommits)
		}
		m, err := s.manifest(c.Hash.String())
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", short(c.Hash.String()), err)
		}
		if err := knowledge.Validate(&knowledge.Overlay{Base: m, Load: load}, s.pol); err != nil {
			return fmt.Errorf("snapshot %s: %w", short(c.Hash.String()), err)
		}
		for _, p := range c.ParentHashes {
			pc, err := object.GetCommit(s.st, p)
			if err != nil {
				return err
			}
			todo = append(todo, pc)
		}
	}
	return nil
}

func (s *Store) isReachable(h string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reachable[h]
}

func (s *Store) syncPacks() error {
	all, err := s.packs()
	if err != nil {
		return err
	}
	for p := range all {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	if len(all) > 0 {
		return syncDir(filepath.Join(s.dir, "objects", "pack"))
	}
	return nil
}

// Discard forgets fetched knowledge. Its objects stay until garbage collection.
func (s *Store) Discard(remote string) error {
	return s.st.RemoveReference(plumbing.ReferenceName("refs/polaroid/incoming/" + remote))
}

// ReadIncoming reads a document of fetched knowledge for review, without
// making it readable to agents.
func (s *Store) ReadIncoming(in Incoming, key string) (*knowledge.Procedure, error) {
	m, err := s.manifest(in.Snapshot)
	if err != nil {
		return nil, err
	}
	h, ok := m[key]
	if !ok {
		return nil, knowledge.ErrNotFound
	}
	c, err := s.blob(h)
	return c.Procedure, err
}
