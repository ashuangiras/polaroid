package gitcore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// runGit runs the git CLI with no user or system configuration.
func runGit(t *testing.T, args ...string) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	cmd := exec.CommandContext(context.Background(), bin, args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareRemote is an ordinary bare repository made by the git CLI.
func bareRemote(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "init", "--quiet", "--bare", "--initial-branch=main", dir)
	return dir
}

func fetch(t *testing.T, r *Repository, rm Remote) *Candidate {
	t.Helper()
	c, err := r.Fetch(context.Background(), rm, Credentials{}, TransferLimits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// accept fetches, verifies and imports the remote's snapshot.
func accept(t *testing.T, r *Repository, rm Remote) (*Candidate, []Commit) {
	t.Helper()
	c := fetch(t, r, rm)
	commits, err := c.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Import(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c, commits
}

func advance(r *Repository, expected, target Snapshot) error {
	return r.Advance(context.Background(), Advance{Expected: expected, Target: target, Author: "operator", Subject: "activate", Body: "trusted source"})
}

func quarantines(t *testing.T, dir string) []string {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(dir, quarantineName))
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func push(t *testing.T, r *Repository, rm Remote) PushResult {
	t.Helper()
	res, err := r.Push(context.Background(), rm, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A's snapshots reach B through an ordinary bare repository: fetched into
// quarantine, verified, imported without becoming active, then advanced.
func TestExchangeThroughLocalRemote(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	rm := Remote{Name: "team", URL: remote}
	dirA, dirB := t.TempDir(), t.TempDir()
	a, b := open(t, dirA), open(t, dirB)

	s1 := publish(t, a, "", put("p/a.json", "1"), put("p/b.json", "b"))
	if res := push(t, a, rm); res.Before != "" || res.After != s1 || res.Objects != 5 {
		t.Fatalf("first push = %+v", res)
	}
	runGit(t, "--git-dir="+remote, "fsck", "--strict", "--no-dangling")
	if got := runGit(t, "--git-dir="+remote, "rev-parse", "main"); got != string(s1) {
		t.Fatalf("remote main = %s", got)
	}
	if tr, _ := a.Tracking(ctx, rm); tr != s1 {
		t.Fatalf("A's tracking = %s", tr)
	}

	c := fetch(t, b, rm)
	if c.Snapshot != s1 || c.Previous != "" || c.Bytes == 0 || c.Objects != 5 {
		t.Fatalf("candidate = %+v", c)
	}
	if _, err := c.Tree(ctx, s1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read before verification: %v", err)
	}
	commits, err := c.Verify(ctx)
	if err != nil || len(commits) != 1 || commits[0].Snapshot != s1 || commits[0].Author != "test.agent" {
		t.Fatalf("verify = %+v, %v", commits, err)
	}
	tr, err := c.Tree(ctx, s1)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tr.Read("p/a.json"); err != nil || string(got) != "1" {
		t.Fatalf("candidate read = %q, %v", got, err)
	}
	if rel, err := c.Relation(ctx, ""); err != nil || rel != Behind {
		t.Fatalf("relation = %s, %v", rel, err)
	}
	if err := advance(b, "", s1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("advance before import: %v", err)
	}
	if _, err := b.Tree(ctx, s1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fetched snapshot readable before activation: %v", err)
	}
	check(t, b.Import(ctx, c))
	if active(t, b) != "" || quarantines(t, dirB) != nil {
		t.Fatalf("import changed the active snapshot or left a quarantine: %v", quarantines(t, dirB))
	}
	if tr, _ := b.Tracking(ctx, rm); tr != s1 {
		t.Fatalf("B's tracking = %s", tr)
	}
	if _, err := b.Tree(ctx, s1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("imported snapshot readable before activation: %v", err)
	}
	var seen []string
	err = b.Advance(ctx, Advance{Target: s1, Author: "operator", Subject: "activate team", Body: "trusted", Trailers: []Trailer{{"Remote", "team"}},
		Validate: func(tr *Tree) error { seen = tr.Paths(); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if active(t, b) != s1 || read(t, b, s1, "p/b.json") != "b" || !slices.Equal(seen, []string{"p/a.json", "p/b.json"}) {
		t.Fatalf("after advance: active %s, validated %v", active(t, b), seen)
	}
	if h, err := b.History(ctx, s1, 5); err != nil || len(h) != 1 || h[0].Author != "test.agent" {
		t.Fatalf("history = %+v, %v", h, err)
	}
	acts, err := b.Activations(ctx, 5)
	if err != nil || len(acts) != 1 || acts[0].Author != "operator" ||
		!slices.Equal(acts[0].Trailers, []Trailer{{"Remote", "team"}, {"Activated", string(s1)}, {"Previous", "none"}}) {
		t.Fatalf("activations = %+v, %v", acts, err)
	}

	// Incremental: only the changed file's objects travel.
	s2 := publish(t, a, s1, put("p/a.json", "2"))
	if res := push(t, a, rm); res.Before != s1 || res.Objects != 4 {
		t.Fatalf("second push = %+v", res)
	}
	c2, commits := accept(t, b, rm)
	if c2.Objects != 4 || len(commits) != 1 || commits[0].Changed[0] != (PathChange{"p/a.json", Modified}) {
		t.Fatalf("incremental candidate %+v, commits %+v", c2, commits)
	}
	t.Logf("initial transfer %d bytes, incremental %d bytes", c.Bytes, c2.Bytes)
	check(t, advance(b, s1, s2))
	if read(t, b, s2, "p/a.json") != "2" || read(t, b, s1, "p/a.json") != "1" {
		t.Fatal("snapshots read differently after the second activation")
	}

	// Nothing new: no transfer, nothing to verify.
	c3 := fetch(t, b, rm)
	if commits, err := c3.Verify(ctx); err != nil || c3.Bytes != 0 || len(commits) != 0 {
		t.Fatalf("up-to-date fetch: %+v %v", c3, err)
	}
	if rel, _ := c3.Relation(ctx, s2); rel != Same {
		t.Fatalf("relation = %s", rel)
	}
	if res := push(t, b, rm); !res.UpToDate {
		t.Fatalf("push of an identical snapshot = %+v", res)
	}
	verifyClean(t, b)
	runGit(t, "--git-dir="+dirB, "fsck", "--strict", "--no-dangling")
	if got := runGit(t, "--git-dir="+dirB, "log", "--format=%H", "active"); got != string(s2)+"\n"+string(s1) {
		t.Fatalf("git log of B = %q", got)
	}
	// A fresh handle sees the imported pack.
	if read(t, open(t, dirB), s1, "p/b.json") != "b" {
		t.Fatal("reopened")
	}
}

// Advance only fast-forwards, only to imported snapshots, only from the
// expected one; Push never overwrites what it has not fetched.
func TestAdvanceAndPushRefusals(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	rm := Remote{Name: "team", URL: remote}
	a, b := open(t, t.TempDir()), open(t, t.TempDir())
	s1 := publish(t, a, "", put("p/a.json", "1"))
	push(t, a, rm)
	accept(t, b, rm)
	check(t, advance(b, "", s1))

	s2 := publish(t, a, s1, put("p/a.json", "2"))
	push(t, a, rm)
	local := publish(t, b, s1, put("p/a.json", "local"))
	c, _ := accept(t, b, rm)
	if rel, err := c.Relation(ctx, local); err != nil || rel != Diverged {
		t.Fatalf("relation = %s, %v", rel, err)
	}
	if err := advance(b, s1, s2); !errors.Is(err, ErrStale) {
		t.Fatalf("stale advance: %v", err)
	}
	if err := advance(b, local, s2); !errors.Is(err, ErrDiverged) {
		t.Fatalf("diverged advance: %v", err)
	}
	if err := advance(b, local, Snapshot(strings.Repeat("ab", 20))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("advance to an unknown snapshot: %v", err)
	}
	if active(t, b) != local || read(t, b, local, "p/a.json") != "local" {
		t.Fatal("refused advances changed B")
	}
	if _, err := b.Push(ctx, rm, Credentials{}); !errors.Is(err, ErrDiverged) {
		t.Fatalf("diverged push: %v", err)
	}
	if got := runGit(t, "--git-dir="+remote, "rev-parse", "main"); got != string(s2) {
		t.Fatalf("remote main = %s after a refused push", got)
	}

	// C is behind: what it has not fetched is never overwritten.
	cdir := t.TempDir()
	cr := open(t, cdir)
	if _, err := cr.Fetch(ctx, rm, Credentials{}, TransferLimits{}); err != nil {
		t.Fatal(err)
	}
	publish(t, cr, "", put("p/a.json", "unrelated"))
	if _, err := cr.Push(ctx, rm, Credentials{}); !errors.Is(err, ErrBehind) {
		t.Fatalf("push over unfetched snapshots: %v", err)
	}

	// A rejecting Validate leaves everything as it was.
	d := open(t, t.TempDir())
	accept(t, d, rm)
	boom := errors.New("not acceptable")
	if err := d.Advance(ctx, Advance{Target: s2, Author: "operator", Subject: "activate", Validate: func(*Tree) error { return boom }}); !errors.Is(err, boom) {
		t.Fatalf("validate error: %v", err)
	}
	if active(t, d) != "" {
		t.Fatal("refused activation moved the active ref")
	}
	check(t, advance(d, "", s2))
	if h, _ := d.History(ctx, s2, 5); len(h) != 2 {
		t.Fatalf("fast-forward over two snapshots: history %d", len(h))
	}
}

// Two writers racing for the same remote branch: the remote's
// compare-and-swap lets exactly one through.
func TestConcurrentPushesOneWins(t *testing.T) {
	remote := bareRemote(t)
	rm := Remote{Name: "team", URL: remote}
	a, b := open(t, t.TempDir()), open(t, t.TempDir())
	s1 := publish(t, a, "", put("p/a.json", "1"))
	push(t, a, rm)
	accept(t, b, rm)
	check(t, advance(b, "", s1))
	sa := publish(t, a, s1, put("p/a.json", "a"))
	sb := publish(t, b, s1, put("p/a.json", "b"))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, r := range []*Repository{a, b} {
		wg.Go(func() { _, errs[i] = r.Push(context.Background(), rm, Credentials{}) })
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrStale) && !errors.Is(err, ErrBehind):
			t.Fatalf("losing push: %v", err)
		}
	}
	got := Snapshot(runGit(t, "--git-dir="+remote, "rev-parse", "main"))
	if won != 1 || (got != sa && got != sb) || (errs[0] == nil) != (got == sa) {
		t.Fatalf("winners %d, remote %s, errors %v", won, got, errs)
	}
	t.Logf("losing push: %v", errs[slices.IndexFunc(errs, func(e error) bool { return e != nil })])
}

// Each limit stops a fetch before anything is stored, and the quarantine
// is removed.
func TestFetchLimits(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	rm := Remote{Name: "team", URL: remote}
	a := open(t, t.TempDir())
	s1 := publish(t, a, "", put("p/a.json", strings.Repeat("x", 4000)), put("p/b.json", "b"))
	publish(t, a, s1, put("p/b.json", "c"))
	push(t, a, rm)
	for name, lim := range map[string]TransferLimits{
		"pack bytes":     {MaxPackBytes: 100, MaxObjects: 100, MaxObjectBytes: 1 << 20, MaxInflatedBytes: 1 << 20, MaxCommits: 10},
		"objects":        {MaxPackBytes: 1 << 20, MaxObjects: 3, MaxObjectBytes: 1 << 20, MaxInflatedBytes: 1 << 20, MaxCommits: 10},
		"object bytes":   {MaxPackBytes: 1 << 20, MaxObjects: 100, MaxObjectBytes: 1000, MaxInflatedBytes: 1 << 20, MaxCommits: 10},
		"inflated bytes": {MaxPackBytes: 1 << 20, MaxObjects: 100, MaxObjectBytes: 1 << 20, MaxInflatedBytes: 2000, MaxCommits: 10},
		"commits":        {MaxPackBytes: 1 << 20, MaxObjects: 100, MaxObjectBytes: 1 << 20, MaxInflatedBytes: 1 << 20, MaxCommits: 1},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			b := open(t, dir)
			c, err := b.Fetch(ctx, rm, Credentials{}, lim)
			if err == nil {
				_, err = c.Verify(ctx)
				if err == nil {
					err = b.Import(ctx, c)
				}
				_ = c.Close()
			}
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("fetch within %+v: %v", lim, err)
			}
			if tr, _ := b.Tracking(ctx, rm); tr != "" || active(t, b) != "" || quarantines(t, dir) != nil {
				t.Fatalf("refused fetch left tracking %s or quarantine %v", tr, quarantines(t, dir))
			}
			t.Log(err)
		})
	}
}

// writeRemote stores a hand-made snapshot in the remote, as a hostile or
// broken publisher could, and points main at it.
func writeRemote(t *testing.T, remote string, build func(st *filesystem.Storage) plumbing.Hash) Snapshot {
	t.Helper()
	st := filesystem.NewStorage(osfs.New(remote), cache.NewObjectLRUDefault())
	defer func() { _ = st.Close() }()
	h := build(st)
	check(t, st.SetReference(plumbing.NewHashReference("refs/heads/main", h)))
	return Snapshot(h.String())
}

func storeObject(t *testing.T, st *filesystem.Storage, enc interface {
	Encode(plumbing.EncodedObject) error
}) plumbing.Hash {
	t.Helper()
	o := st.NewEncodedObject()
	check(t, enc.Encode(o))
	h, err := st.SetEncodedObject(o)
	check(t, err)
	return h
}

func storeBlob(t *testing.T, st *filesystem.Storage, content string) plumbing.Hash {
	t.Helper()
	return storeRaw(t, st, plumbing.BlobObject, []byte(content))
}

func storeRaw(t *testing.T, st *filesystem.Storage, typ plumbing.ObjectType, content []byte) plumbing.Hash {
	t.Helper()
	o := st.NewEncodedObject()
	o.SetType(typ)
	w, _ := o.Writer()
	_, _ = w.Write(content)
	_ = w.Close()
	h, err := st.SetEncodedObject(o)
	check(t, err)
	return h
}

// rawTree encodes entries in the given order, which go-git refuses to do.
func rawTree(t *testing.T, st *filesystem.Storage, entries ...object.TreeEntry) plumbing.Hash {
	t.Helper()
	var b []byte
	for _, e := range entries {
		b = append(b, []byte(strconv.FormatUint(uint64(e.Mode), 8)+" "+e.Name+"\x00")...)
		b = append(b, e.Hash[:]...)
	}
	return storeRaw(t, st, plumbing.TreeObject, b)
}

func storeCommit(t *testing.T, st *filesystem.Storage, tree plumbing.Hash, parents ...plumbing.Hash) plumbing.Hash {
	t.Helper()
	sig := object.Signature{Name: "mallory", Email: "m@example.invalid"}
	return storeObject(t, st, &object.Commit{Author: sig, Committer: sig, Message: "hand-made\n", TreeHash: tree, ParentHashes: parents})
}

// Received trees that Git accepts but Polaroid must not store are refused
// by Verify, and nothing is imported.
func TestReceivedSnapshotsAreChecked(t *testing.T) {
	ctx := context.Background()
	tree := func(entries ...object.TreeEntry) func(*testing.T, *filesystem.Storage) plumbing.Hash {
		return func(t *testing.T, st *filesystem.Storage) plumbing.Hash {
			return storeCommit(t, st, storeObject(t, st, &object.Tree{Entries: entries}))
		}
	}
	file := func(t *testing.T, st *filesystem.Storage, name string, mode filemode.FileMode) object.TreeEntry {
		return object.TreeEntry{Name: name, Mode: mode, Hash: storeBlob(t, st, "x")}
	}
	cases := map[string]func(*testing.T, *filesystem.Storage) plumbing.Hash{
		"hidden name": func(t *testing.T, st *filesystem.Storage) plumbing.Hash {
			return tree(file(t, st, ".git", filemode.Regular))(t, st)
		},
		"executable": func(t *testing.T, st *filesystem.Storage) plumbing.Hash {
			return tree(file(t, st, "run.sh", filemode.Executable))(t, st)
		},
		"symlink": func(t *testing.T, st *filesystem.Storage) plumbing.Hash {
			return tree(file(t, st, "link", filemode.Symlink))(t, st)
		},
		"submodule": func(t *testing.T, st *filesystem.Storage) plumbing.Hash {
			return tree(object.TreeEntry{Name: "sub", Mode: filemode.Submodule, Hash: plumbing.NewHash(strings.Repeat("cd", 20))})(t, st)
		},
		"unsorted": func(t *testing.T, st *filesystem.Storage) plumbing.Hash {
			return storeCommit(t, st, rawTree(t, st, file(t, st, "b.json", filemode.Regular), file(t, st, "a.json", filemode.Regular)))
		},
		"duplicate": func(t *testing.T, st *filesystem.Storage) plumbing.Hash {
			return tree(file(t, st, "a.json", filemode.Regular), file(t, st, "a.json", filemode.Regular))(t, st)
		},
		"case collision": func(t *testing.T, st *filesystem.Storage) plumbing.Hash {
			return tree(file(t, st, "A.json", filemode.Regular), file(t, st, "a.json", filemode.Regular))(t, st)
		},
		"oversized file": func(t *testing.T, st *filesystem.Storage) plumbing.Hash {
			big := object.TreeEntry{Name: "big.json", Mode: filemode.Regular, Hash: storeBlob(t, st, strings.Repeat("x", 2<<20))}
			return tree(big)(t, st)
		},
		"merge": func(t *testing.T, st *filesystem.Storage) plumbing.Hash {
			t1 := storeObject(t, st, &object.Tree{Entries: []object.TreeEntry{file(t, st, "a.json", filemode.Regular)}})
			return storeCommit(t, st, t1, storeCommit(t, st, t1), storeCommit(t, st, t1, storeCommit(t, st, t1)))
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			remote := bareRemote(t)
			rm := Remote{Name: "team", URL: remote}
			s := writeRemote(t, remote, func(st *filesystem.Storage) plumbing.Hash { return build(t, st) })
			dir := t.TempDir()
			b := open(t, dir)
			c, err := b.Fetch(ctx, rm, Credentials{}, TransferLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if c.Snapshot != s {
				t.Fatalf("candidate %s, want %s", c.Snapshot, s)
			}
			_, err = c.Verify(ctx)
			if !errors.Is(err, ErrRejected) || (name == "merge" && !strings.Contains(err.Error(), "merges 2 histories")) {
				t.Fatalf("verify: %v", err)
			}
			t.Log(err)
			if err := b.Import(ctx, c); err == nil {
				t.Fatal("imported a rejected candidate")
			}
			check(t, c.Close())
			if tr, _ := b.Tracking(ctx, rm); tr != "" || quarantines(t, dir) != nil {
				t.Fatal("a rejected candidate left state behind")
			}
			if _, err := os.Stat(filepath.Join(dir, "objects", "pack")); err == nil {
				if ents, _ := os.ReadDir(filepath.Join(dir, "objects", "pack")); len(ents) > 0 {
					t.Fatalf("objects stored: %v", ents)
				}
			}
		})
	}
}

// A crash leaves a quarantine behind; the next fetch removes it, but never
// one that a live fetch holds.
func TestAbandonedQuarantinesAreRemoved(t *testing.T) {
	dir := t.TempDir()
	r := open(t, dir)
	base := filepath.Join(dir, quarantineName)
	check(t, os.MkdirAll(filepath.Join(base, "q-abandoned", "objects"), 0o700))
	check(t, os.WriteFile(filepath.Join(base, "q-abandoned", "lock"), nil, 0o600))
	live, err := r.newQuarantine()
	check(t, err)
	defer func() { _ = live.close() }()
	remote := bareRemote(t)
	rm := Remote{Name: "team", URL: remote}
	src := open(t, t.TempDir())
	publish(t, src, "", put("p/a.json", "1"))
	push(t, src, rm)
	fetch(t, r, rm)
	if got := quarantines(t, dir); len(got) != 2 || !slices.Contains(got, filepath.Base(live.dir)) || slices.Contains(got, "q-abandoned") {
		t.Fatalf("quarantines = %v", got)
	}
}

func TestRemoteValidation(t *testing.T) {
	r := open(t, t.TempDir())
	for _, rm := range []Remote{
		{Name: "team", URL: "https://user:secret@example.com/k.git"},
		{Name: "team", URL: "https://token@example.com/k.git"},
		{Name: "team", URL: "http://example.com/k.git"},
		{Name: "team", URL: "git://example.com/k.git"},
		{Name: "team", URL: "relative/path"},
		{Name: "../x", URL: "/tmp/x"},
		{Name: "team", URL: "/tmp/x", Branch: "a/../b"},
		{Name: "team", URL: "ssh://git@example.com/k.git"}, // no key or known_hosts
	} {
		_, err := r.Fetch(context.Background(), rm, Credentials{}, TransferLimits{})
		if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "secret") {
			t.Errorf("%+v: %v", rm, err)
		}
	}
	if s := (Credentials{Password: "secret"}).String(); strings.Contains(s, "secret") {
		t.Fatal(s)
	}
}

// A pack that carries objects no received snapshot uses is refused: every
// object that enters the store must have been verified.
func TestUnrequestedObjectsAreRefused(t *testing.T) {
	ctx := context.Background()
	src := open(t, t.TempDir())
	s1 := publish(t, src, "", put("p/a.json", "1"))
	s2 := publish(t, src, s1, put("p/extra.json", "unverified"))
	objs, err := revlist.Objects(src.st, []plumbing.Hash{plumbing.NewHash(string(s2))}, nil)
	check(t, err)
	var pack bytes.Buffer
	_, err = packfile.NewEncoder(&pack, src.st, false).Encode(objs, 0)
	check(t, err)

	dir := t.TempDir()
	r := open(t, dir)
	q, err := r.newQuarantine()
	check(t, err)
	c := &Candidate{Remote: Remote{Name: "team", URL: "/unused"}, Snapshot: s1, r: r, lim: DefaultTransferLimits, q: q}
	defer func() { _ = c.Close() }()
	if _, _, err := q.receive(&pack, DefaultTransferLimits); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Verify(ctx); !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "belong to no received snapshot") {
		t.Fatalf("verify: %v", err)
	}
}

// Two fetches from the same state: the second import finds the tracking
// ref moved and is refused rather than moving it back.
func TestImportIsCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	rm := Remote{Name: "team", URL: remote}
	a, b := open(t, t.TempDir()), open(t, t.TempDir())
	s1 := publish(t, a, "", put("p/a.json", "1"))
	push(t, a, rm)
	first := fetch(t, b, rm)
	publish(t, a, s1, put("p/a.json", "2"))
	push(t, a, rm)
	second := fetch(t, b, rm)
	for _, c := range []*Candidate{first, second} {
		if _, err := c.Verify(ctx); err != nil {
			t.Fatal(err)
		}
	}
	check(t, b.Import(ctx, second))
	if err := b.Import(ctx, first); !errors.Is(err, ErrStale) {
		t.Fatalf("import of an older fetch: %v", err)
	}
	if tr, _ := b.Tracking(ctx, rm); tr != second.Snapshot {
		t.Fatalf("tracking = %s", tr)
	}
}
