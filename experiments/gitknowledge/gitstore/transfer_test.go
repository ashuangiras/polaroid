package gitstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/ashuangiras/polaroid/experiments/gitknowledge/gitstore"
	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge"
	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge/storetest"
)

func note(msg string) knowledge.Note { return knowledge.Note{Author: "test.agent", Message: msg} }

func checks(goal string) []knowledge.Change {
	return []knowledge.Change{{Key: "go.module.checks", Procedure: storetest.Proc("go.module.checks", goal, []string{"gate_commands", "module"},
		storetest.Ref("guidance", storetest.Guidance, nil))}}
}

func head(t *testing.T, s *gitstore.Store) string {
	t.Helper()
	h, err := s.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestTransfer(t *testing.T) {
	ctx := context.Background()
	adir, bdir := t.TempDir(), t.TempDir()
	a, b := open(t, adir, "b"), open(t, bdir, "a")
	defer a.Close()
	defer b.Close()
	s1, err := a.Publish(ctx, "", storetest.Initial(), note("initial"))
	if err != nil {
		t.Fatal(err)
	}

	// Initial transfer: fetched knowledge is invisible until accepted.
	in, err := b.Fetch(ctx, "a", gitstore.LocalURL(adir))
	if err != nil || in.Snapshot != s1 {
		t.Fatalf("fetch: %+v, %v", in, err)
	}
	if head(t, b) != "" {
		t.Fatal("fetch activated knowledge")
	}
	if _, err := b.ReadAt(ctx, "go.module.verify", s1); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("unaccepted snapshot is readable: %v", err)
	}
	if got, err := b.Accept(ctx, in); err != nil || got != s1 || head(t, b) != s1 {
		t.Fatalf("accept: %s, %v", got, err)
	}
	ra, _ := knowledge.Resolve(ctx, a, "go.module.verify", s1)
	rb, err := knowledge.Resolve(ctx, b, "go.module.verify", s1)
	if err != nil || ra.Hash != rb.Hash || rb.Children[2].Node.Hash != ra.Children[2].Node.Hash {
		t.Fatalf("B resolves differently: %v", err)
	}
	initialObjects := in.Objects
	t.Logf("initial transfer: %d objects, %d bytes", in.Objects, in.Bytes)

	// Incremental transfer: only the changed path travels.
	s2, err := a.Publish(ctx, s1, checks("Run gofmt, go vet and staticcheck"), note("checks: staticcheck"))
	if err != nil {
		t.Fatal(err)
	}
	in, err = b.Fetch(ctx, "a", gitstore.LocalURL(adir))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("incremental transfer: %d objects, %d bytes", in.Objects, in.Bytes)
	// blob, trees procedures/go/module, procedures/go, procedures, root, commit
	if in.Objects != 6 || in.Objects >= initialObjects {
		t.Fatalf("incremental fetch sent %d objects (initial %d)", in.Objects, initialObjects)
	}
	if _, err := b.Accept(ctx, in); err != nil || head(t, b) != s2 {
		t.Fatalf("accept incremental: %v", err)
	}
	b1, _ := b.ReadAt(ctx, "go.module.build", s1)
	b2, _ := b.ReadAt(ctx, "go.module.build", s2)
	if b1.Hash != b2.Hash {
		t.Fatal("unchanged knowledge changed")
	}
	if _, err := b.Accept(ctx, in); !errors.Is(err, gitstore.ErrNotNewer) {
		t.Fatalf("re-accept: %v", err)
	}

	// Divergence: both improve from s2; neither side overwrites the other.
	bLocal, err := b.Publish(ctx, s2, []knowledge.Change{{Key: "go.module.build", Procedure: storetest.Proc("go.module.build", "Build with -trimpath", []string{"module"},
		storetest.Ref("guidance", storetest.Guidance, nil))}}, note("b: build"))
	if err != nil {
		t.Fatal(err)
	}
	aLocal, err := a.Publish(ctx, s2, checks("Run gofmt, go vet, staticcheck and govulncheck"), note("a: checks"))
	if err != nil {
		t.Fatal(err)
	}
	in, err = b.Fetch(ctx, "a", gitstore.LocalURL(adir))
	if err != nil {
		t.Fatal(err)
	}
	var div *gitstore.DivergedError
	if _, err := b.Accept(ctx, in); !errors.As(err, &div) || div.Base != s2 || div.Local != bLocal || div.Incoming != aLocal {
		t.Fatalf("divergence on B: %v", err)
	}
	inB, err := a.Fetch(ctx, "b", gitstore.LocalURL(bdir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Accept(ctx, inB); !errors.As(err, &div) {
		t.Fatalf("divergence on A: %v", err)
	}
	if head(t, a) != aLocal || head(t, b) != bLocal {
		t.Fatal("divergence moved an active snapshot")
	}
	// Explicit resolution on B: take A's checks, keep B's build, record both parents.
	theirs, err := b.ReadIncoming(in, "go.module.checks")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := b.Reconcile(ctx, bLocal, aLocal, []knowledge.Change{{Key: "go.module.checks", Procedure: theirs}}, note("b: reconcile with a"))
	if err != nil {
		t.Fatal(err)
	}
	inB, err = a.Fetch(ctx, "b", gitstore.LocalURL(bdir))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.Accept(ctx, inB); err != nil || got != merged {
		t.Fatalf("A fast-forwards to the reconciliation: %s, %v", got, err)
	}
	ac, _ := a.ReadActive(ctx, "go.module.checks")
	ab, _ := a.ReadActive(ctx, "go.module.build")
	if ac.Procedure.Goal != "Run gofmt, go vet, staticcheck and govulncheck" || ab.Procedure.Goal != "Build with -trimpath" {
		t.Fatalf("converged knowledge: %q, %q", ac.Procedure.Goal, ab.Procedure.Goal)
	}
	if _, err := a.ReadAt(ctx, "go.module.checks", aLocal); err != nil {
		t.Fatalf("A's own divergent snapshot is no longer reachable: %v", err)
	}

	// Autonomy: an untrusted instance can fetch but never activate.
	cdir := t.TempDir()
	c := open(t, cdir)
	defer c.Close()
	inC, err := c.Fetch(ctx, "a", gitstore.LocalURL(adir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Accept(ctx, inC); !errors.Is(err, gitstore.ErrUntrusted) || head(t, c) != "" {
		t.Fatalf("untrusted accept: %v", err)
	}
}

// evil writes a commit with an arbitrary tree on top of parent in the bare
// repository at dir and points refs/heads/active at it, as a hostile or
// broken peer could.
type file struct {
	mode    filemode.FileMode
	content string
}

func evil(t *testing.T, dir, parent string, files map[string]file) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := repo.Storer
	put := func(o interface {
		Encode(plumbing.EncodedObject) error
	}) plumbing.Hash {
		obj := st.NewEncodedObject()
		if err := o.Encode(obj); err != nil {
			t.Fatal(err)
		}
		h, err := st.SetEncodedObject(obj)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	blob := func(s string) plumbing.Hash {
		obj := st.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		w, _ := obj.Writer()
		w.Write([]byte(s))
		w.Close()
		h, err := st.SetEncodedObject(obj)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	var build func(prefix string) plumbing.Hash
	build = func(prefix string) plumbing.Hash {
		entries := map[string]object.TreeEntry{}
		for p, f := range files {
			rest, ok := strings.CutPrefix(p, prefix)
			if !ok {
				continue
			}
			if name, _, sub := strings.Cut(rest, "/"); sub {
				entries[name] = object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: build(prefix + name + "/")}
			} else {
				entries[rest] = object.TreeEntry{Name: rest, Mode: f.mode, Hash: blob(f.content)}
			}
		}
		tr := &object.Tree{}
		for _, e := range entries {
			tr.Entries = append(tr.Entries, e)
		}
		sort.Slice(tr.Entries, func(i, j int) bool { return tr.Entries[i].Name < tr.Entries[j].Name })
		return put(tr)
	}
	sig := object.Signature{Name: "mallory", Email: "m@example.invalid", When: time.Now()}
	c := put(&object.Commit{Author: sig, Committer: sig, Message: "evil", TreeHash: build(""), ParentHashes: []plumbing.Hash{plumbing.NewHash(parent)}})
	if err := st.SetReference(plumbing.NewHashReference("refs/heads/active", c)); err != nil {
		t.Fatal(err)
	}
	return c.String()
}

// tree returns the files of a snapshot, as evil takes them.
func tree(t *testing.T, s *gitstore.Store, snapshot string) map[string]file {
	t.Helper()
	keys, err := s.Keys(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]file{}
	for _, k := range keys {
		kn, err := s.ReadAt(context.Background(), k, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := knowledge.Encode(kn.Procedure)
		out["procedures/"+strings.ReplaceAll(k, ".", "/")+".json"] = file{filemode.Regular, string(b)}
	}
	return out
}

func TestRejectedRemoteKnowledge(t *testing.T) {
	ctx := context.Background()
	adir, bdir := t.TempDir(), t.TempDir()
	a, b := open(t, adir), open(t, bdir, "a")
	defer b.Close()
	s1, err := a.Publish(ctx, "", storetest.Initial(), note("initial"))
	if err != nil {
		t.Fatal(err)
	}
	good := tree(t, a, s1)
	a.Close()
	in, err := b.Fetch(ctx, "a", gitstore.LocalURL(adir))
	if err == nil {
		_, err = b.Accept(ctx, in)
	}
	if err != nil {
		t.Fatal(err)
	}
	with := func(extra map[string]file) map[string]file {
		m := map[string]file{}
		for k, v := range good {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	verify := "procedures/go/module/verify.json"
	cases := map[string]map[string]file{
		"missing reference": with(map[string]file{verify: {filemode.Regular, strings.Replace(good[verify].content, `"key":"go.module.build"`, `"key":"go.module.gone"`, 1)}}),
		"non-canonical":     with(map[string]file{verify: {filemode.Regular, " " + good[verify].content}}),
		"unknown member":    with(map[string]file{verify: {filemode.Regular, strings.Replace(good[verify].content, `{"key"`, `{"run":"rm -rf /","key"`, 1)}}),
		"symlink":           with(map[string]file{"procedures/go/module/link.json": {filemode.Symlink, "/etc/passwd"}}),
		"executable":        with(map[string]file{"procedures/go/module/x.json": {filemode.Executable, good[verify].content}}),
		"extra root file":   with(map[string]file{"README": {filemode.Regular, "hi"}}),
		"dot-dot path":      with(map[string]file{"procedures/../x.json": {filemode.Regular, "{}"}}),
		"oversize blob":     with(map[string]file{verify: {filemode.Regular, strings.Repeat("a", 1<<20)}}),
		"wrong identity":    with(map[string]file{"procedures/go/module/copy.json": good[verify]}),
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			bad := evil(t, adir, s1, files)
			in, err := b.Fetch(ctx, "a", gitstore.LocalURL(adir))
			if err != nil || in.Snapshot != bad {
				t.Fatalf("fetch: %+v, %v", in, err)
			}
			_, err = b.Accept(ctx, in)
			if !errors.Is(err, knowledge.ErrInvalid) {
				t.Fatalf("accepted or wrong error: %v", err)
			}
			t.Log(err)
			if head(t, b) != s1 {
				t.Fatal("B's active knowledge moved")
			}
			if _, err := b.ReadAt(ctx, "go.module.verify", bad); !errors.Is(err, knowledge.ErrNotFound) {
				t.Fatalf("rejected snapshot is readable: %v", err)
			}
			if err := b.Discard("a"); err != nil {
				t.Fatal(err)
			}
		})
	}
	// An invalid intermediate snapshot taints a valid tip: once active, it would be readable.
	mid := evil(t, adir, s1, cases["missing reference"])
	tip := evil(t, adir, mid, good)
	in, err = b.Fetch(ctx, "a", gitstore.LocalURL(adir))
	if err != nil || in.Snapshot != tip {
		t.Fatal(err)
	}
	if _, err := b.Accept(ctx, in); !errors.Is(err, knowledge.ErrInvalid) || head(t, b) != s1 {
		t.Fatalf("invalid history accepted: %v", err)
	}
	// A fetch over the size limit is refused before validation and leaves no incoming ref.
	defer func(old int64) { gitstore.MaxFetchBytes = old }(gitstore.MaxFetchBytes)
	gitstore.MaxFetchBytes = 10
	evil(t, adir, s1, with(map[string]file{"procedures/go/module/new.json": {filemode.Regular, strings.Repeat("b", 5000)}}))
	if _, err := b.Fetch(ctx, "a", gitstore.LocalURL(adir)); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("oversize fetch: %v", err)
	}
	if head(t, b) != s1 {
		t.Fatal("head moved")
	}
}

// Backup is a copy of the directory; a damaged object is detected on read,
// and a replica restores the knowledge without any database.
func TestBackupCorruptionAndRestore(t *testing.T) {
	ctx := context.Background()
	adir := t.TempDir()
	a := open(t, adir)
	s1, err := a.Publish(ctx, "", storetest.Initial(), note("initial"))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := a.Publish(ctx, s1, checks("Run gofmt, go vet and staticcheck"), note("checks"))
	if err != nil {
		t.Fatal(err)
	}
	old, _ := a.ReadAt(ctx, "go.module.checks", s1)
	a.Close()

	backup := filepath.Join(t.TempDir(), "backup")
	if err := os.CopyFS(backup, os.DirFS(adir)); err != nil {
		t.Fatal(err)
	}
	// Damage the blob of the old checks document in A.
	repo, _ := git.PlainOpen(adir)
	c, _ := repo.CommitObject(plumbing.NewHash(s1))
	tr, _ := c.Tree()
	e, err := tr.FindEntry("procedures/go/module/checks.json")
	if err != nil {
		t.Fatal(err)
	}
	obj := filepath.Join(adir, "objects", e.Hash.String()[:2], e.Hash.String()[2:])
	raw, _ := os.ReadFile(obj)
	raw[len(raw)/2] ^= 0xff
	os.Chmod(obj, 0o600)
	if err := os.WriteFile(obj, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	a = open(t, adir)
	if kn, err := a.ReadAt(ctx, "go.module.checks", s1); err == nil && kn.Hash == old.Hash {
		t.Fatal("damaged object read back as the original")
	} else {
		t.Logf("damaged object: %v", err)
	}
	if _, err := a.ReadActive(ctx, "go.module.verify"); err != nil {
		t.Fatalf("undamaged knowledge unreadable: %v", err)
	}
	a.Close()

	// Restore: open the backup copy, or rebuild an empty store from it.
	r := open(t, backup)
	if head(t, r) != s2 {
		t.Fatal("backup head")
	}
	if kn, err := r.ReadAt(ctx, "go.module.checks", s1); err != nil || kn.Hash != old.Hash {
		t.Fatalf("backup read: %v", err)
	}
	r.Close()
	fresh := open(t, t.TempDir(), "backup")
	defer fresh.Close()
	in, err := fresh.Fetch(ctx, "backup", gitstore.LocalURL(backup))
	if err == nil {
		_, err = fresh.Accept(ctx, in)
	}
	if err != nil || head(t, fresh) != s2 {
		t.Fatalf("restore from mirror: %v", err)
	}
	if kn, err := fresh.ReadAt(ctx, "go.module.checks", s1); err != nil || kn.Hash != old.Hash {
		t.Fatalf("restored history: %v", err)
	}
}
