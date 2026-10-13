package gitcore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestMain doubles as the child process of the cross-process tests: with
// GITCORE_CHILD set it publishes once and exits with a code the parent reads.
func TestMain(m *testing.M) {
	if os.Getenv("GITCORE_CHILD") == "publish" {
		os.Exit(childPublish())
	}
	os.Exit(m.Run())
}

const (
	exitPublished = 0
	exitStale     = 3
	exitCrashed   = 17
)

func childPublish() int {
	if stage := os.Getenv("GITCORE_CRASH"); stage != "" {
		testHook = func(s string) {
			if s == stage {
				os.Exit(exitCrashed)
			}
		}
	}
	r, err := Open(os.Getenv("GITCORE_DIR"), Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, err = r.Publish(context.Background(), Publication{Expected: Snapshot(os.Getenv("GITCORE_EXPECTED")), Author: "child",
		Subject: "child publication", Changes: []Change{put(os.Getenv("GITCORE_PATH"), os.Getenv("GITCORE_CONTENT"))}})
	switch {
	case errors.Is(err, ErrStale):
		return exitStale
	case err != nil:
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return exitPublished
}

func child(t *testing.T, dir string, expected Snapshot, path, content, crash string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "GITCORE_CHILD=publish", "GITCORE_DIR="+dir, "GITCORE_EXPECTED="+string(expected),
		"GITCORE_PATH="+path, "GITCORE_CONTENT="+content, "GITCORE_CRASH="+crash)
	cmd.Stderr = os.Stderr
	return cmd
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func put(path, content string) Change { return Change{Path: path, Content: []byte(content)} }

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, dir string) *Repository {
	t.Helper()
	r, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func publish(t *testing.T, r *Repository, expected Snapshot, changes ...Change) Snapshot {
	t.Helper()
	s, err := r.Publish(context.Background(), Publication{Expected: expected, Author: "test.agent", Subject: "change", Changes: changes})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func active(t *testing.T, r *Repository) Snapshot {
	t.Helper()
	s, err := r.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func read(t *testing.T, r *Repository, s Snapshot, path string) string {
	t.Helper()
	tr, err := r.Tree(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := tr.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func verifyClean(t *testing.T, r *Repository) Report {
	t.Helper()
	rep, err := r.Verify(context.Background())
	if err != nil || len(rep.Problems) > 0 {
		t.Fatalf("verify: %v %v", rep.Problems, err)
	}
	return rep
}

func TestPublishReadHistoryAndDiff(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r := open(t, dir)
	if s := active(t, r); s != "" {
		t.Fatalf("new repository active = %q", s)
	}
	s1 := publish(t, r, "", put("procedures/go/module/verify.json", "v1"), put("procedures/go/module/build.json", "b1"))
	s2, err := r.Publish(ctx, Publication{Expected: s1, Author: "copilot.agent", Subject: "build: add -trimpath",
		Body: "Builds were not reproducible.", Trailers: []Trailer{{"Polaroid-Reason", "reproducible builds"}, {"Polaroid-Finding", "01a1-feedback"}},
		Changes: []Change{put("procedures/go/module/build.json", "b2"), put("procedures/go/module/checks.json", "c1")}})
	if err != nil {
		t.Fatal(err)
	}
	s3 := publish(t, r, s2, Change{Path: "procedures/go/module/checks.json", Delete: true})

	if active(t, r) != s3 || read(t, r, s3, "procedures/go/module/build.json") != "b2" {
		t.Fatal("active snapshot")
	}
	if read(t, r, s1, "procedures/go/module/build.json") != "b1" {
		t.Fatal("an old snapshot changed")
	}
	old, _ := r.Tree(ctx, s2)
	if !slices.Equal(old.Paths(), []string{"procedures/go/module/build.json", "procedures/go/module/checks.json", "procedures/go/module/verify.json"}) {
		t.Fatalf("paths at s2 = %v", old.Paths())
	}
	cur, _ := r.Tree(ctx, s3)
	if _, err := cur.Read("procedures/go/module/checks.json"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted file: %v", err)
	}

	h, err := r.History(ctx, s3, 10)
	if err != nil || len(h) != 3 {
		t.Fatalf("history: %d, %v", len(h), err)
	}
	c := h[1]
	if c.Snapshot != s2 || c.Author != "copilot.agent" || c.Subject != "build: add -trimpath" || c.Body != "Builds were not reproducible." ||
		!slices.Equal(c.Trailers, []Trailer{{"Polaroid-Reason", "reproducible builds"}, {"Polaroid-Finding", "01a1-feedback"}}) ||
		!slices.Equal(c.Parents, []Snapshot{s1}) {
		t.Fatalf("history entry = %+v", c)
	}
	if !slices.Equal(c.Changed, []PathChange{{"procedures/go/module/build.json", Modified}, {"procedures/go/module/checks.json", Added}}) {
		t.Fatalf("changed = %v", c.Changed)
	}
	if len(h[2].Changed) != 2 || h[2].Parents != nil {
		t.Fatalf("first publication = %+v", h[2])
	}
	d, err := r.Diff(ctx, s1, s3)
	if err != nil || !slices.Equal(d, []PathChange{{"procedures/go/module/build.json", Modified}}) {
		t.Fatalf("diff = %v, %v", d, err)
	}
	if rep := verifyClean(t, r); rep.Snapshots != 3 || len(rep.Warnings) > 0 {
		t.Fatalf("verify = %+v", rep)
	}

	// A fresh handle (a restart) sees the same state.
	r2 := open(t, dir)
	if active(t, r2) != s3 || read(t, r2, s1, "procedures/go/module/verify.json") != "v1" {
		t.Fatal("state after reopening")
	}
}

func TestStaleAndInvalidPublicationsChangeNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r := open(t, dir)
	s1 := publish(t, r, "", put("a/x.json", "1"))
	s2 := publish(t, r, s1, put("a/x.json", "2"))
	objects := countObjects(t, dir)

	if _, err := r.Publish(ctx, Publication{Expected: s1, Author: "a", Subject: "stale", Changes: []Change{put("a/x.json", "3")}}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale: %v", err)
	}
	if _, err := r.Publish(ctx, Publication{Expected: "", Author: "a", Subject: "stale", Changes: []Change{put("a/x.json", "3")}}); !errors.Is(err, ErrStale) {
		t.Fatalf("stale empty: %v", err)
	}
	big := strings.Repeat("x", int(DefaultLimits.MaxFileBytes)+1)
	cases := map[string]Publication{
		"no changes":       {},
		"parent path":      {Changes: []Change{put("../x.json", "1")}},
		"absolute path":    {Changes: []Change{put("/x.json", "1")}},
		"empty segment":    {Changes: []Change{put("a//x.json", "1")}},
		"dot segment":      {Changes: []Change{put("a/./x.json", "1")}},
		"hidden":           {Changes: []Change{put(".git/config", "1")}},
		"lock name":        {Changes: []Change{put("a/x.lock", "1")}},
		"backslash":        {Changes: []Change{put(`a\x.json`, "1")}},
		"too deep":         {Changes: []Change{put(strings.Repeat("d/", 16)+"x", "1")}},
		"twice":            {Changes: []Change{put("a/y.json", "1"), put("a/y.json", "2")}},
		"oversize":         {Changes: []Change{put("a/y.json", big)}},
		"delete missing":   {Changes: []Change{{Path: "a/nope.json", Delete: true}}},
		"file and dir":     {Changes: []Change{put("a/x.json/y", "1")}},
		"case collision":   {Changes: []Change{put("a/X.json", "1")}},
		"changes nothing":  {Changes: []Change{put("a/x.json", "2")}},
		"bad author":       {Author: "a <b>", Changes: []Change{put("a/y.json", "1")}},
		"two-line subject": {Subject: "a\nb", Changes: []Change{put("a/y.json", "1")}},
		"bad trailer":      {Trailers: []Trailer{{"reason", "x"}}, Changes: []Change{put("a/y.json", "1")}},
		"trailer newline":  {Trailers: []Trailer{{"Reason", "x\ny"}}, Changes: []Change{put("a/y.json", "1")}},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			p.Expected = s2
			if p.Author == "" {
				p.Author = "a"
			}
			if p.Subject == "" {
				p.Subject = name
			}
			_, err := r.Publish(ctx, p)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("published: %v", err)
			}
			t.Log(err)
		})
	}
	rejected := errors.New("procedure rules refuse it")
	var seen []string
	_, err := r.Publish(ctx, Publication{Expected: s2, Author: "a", Subject: "x", Changes: []Change{put("a/y.json", "new")},
		Validate: func(tr *Tree) error {
			seen = tr.Paths()
			if b, err := tr.Read("a/y.json"); err != nil || string(b) != "new" || tr.Snapshot() != "" {
				return fmt.Errorf("proposed tree read: %q %w", b, err)
			}
			if b, err := tr.Read("a/x.json"); err != nil || string(b) != "2" {
				return fmt.Errorf("base file read: %q %w", b, err)
			}
			return rejected
		}})
	if !errors.Is(err, rejected) || !slices.Equal(seen, []string{"a/x.json", "a/y.json"}) {
		t.Fatalf("validate: %v, saw %v", err, seen)
	}
	if active(t, r) != s2 || countObjects(t, dir) != objects {
		t.Fatal("a refused publication changed the repository")
	}
}

func countObjects(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	check(t, filepath.WalkDir(filepath.Join(dir, "objects"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	}))
	return n
}

// Racing writers, through one handle and through two handles on the same
// directory, as two processes would: exactly one wins from a snapshot.
func TestConcurrentPublishersInOneProcess(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r1, r2 := open(t, dir), open(t, dir)
	s1 := publish(t, r1, "", put("a/x.json", "0"))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := r1
			if i%2 == 1 {
				r = r2
			}
			_, err := r.Publish(ctx, Publication{Expected: s1, Author: "w", Subject: "race", Changes: []Change{put("a/x.json", fmt.Sprint(i+1))}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	won := 0
	for err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ErrStale):
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("%d writers won", won)
	}
	verifyClean(t, r2)
}

func TestConcurrentPublishersAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	r := open(t, dir)
	s1 := publish(t, r, "", put("a/x.json", "0"))
	var cmds []*exec.Cmd
	for i := range 6 {
		cmd := child(t, dir, s1, "a/x.json", fmt.Sprint("process ", i), "")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	codes := map[int]int{}
	for _, cmd := range cmds {
		codes[exitCode(cmd.Wait())]++
	}
	if codes[exitPublished] != 1 || codes[exitStale] != 5 {
		t.Fatalf("exit codes %v, want one publication and five stale", codes)
	}
	s := active(t, r)
	h, err := r.History(context.Background(), s, 10)
	if err != nil || len(h) != 2 || h[1].Snapshot != s1 {
		t.Fatalf("history after the race: %v", err)
	}
	verifyClean(t, r)
}

// A real process is killed at each stage of a publication. Before the ref
// moves, the previous snapshot stays active; after, the new one is active;
// either way the repository verifies and the next publication succeeds.
func TestInterruptedPublication(t *testing.T) {
	for _, tc := range []struct {
		stage     string
		published bool
		warning   string
	}{
		{"before-objects", false, ""},
		{"object-temp", false, "temp files"},
		{"objects-durable", false, ""},
		{"ref-temp", false, "temp files"},
		{"ref-moved", true, "reflog"},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			dir := t.TempDir()
			r := open(t, dir)
			s1 := publish(t, r, "", put("a/x.json", "old"))
			if code := exitCode(child(t, dir, s1, "a/x.json", "new", tc.stage).Run()); code != exitCrashed {
				t.Fatalf("child exited %d, want the crash", code)
			}
			r = open(t, dir)
			s := active(t, r)
			if got := read(t, r, s, "a/x.json"); (s != s1) != tc.published || (got == "new") != tc.published {
				t.Fatalf("after a crash at %s: active %s holds %q", tc.stage, s, got)
			}
			rep := verifyClean(t, r)
			if w := strings.Join(rep.Warnings, "; "); tc.warning != "" && !strings.Contains(w, tc.warning) || tc.warning == "" && w != "" {
				t.Fatalf("warnings %q, want %q", w, tc.warning)
			}
			s2 := publish(t, r, s, put("a/x.json", "after"))
			if rep := verifyClean(t, r); len(rep.Warnings) > 0 || read(t, r, s2, "a/x.json") != "after" {
				t.Fatalf("after the next publication: %+v", rep)
			}
		})
	}
}

func TestReadsDuringPublication(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w, rd := open(t, dir), open(t, dir)
	s1 := publish(t, w, "", put("a/x.json", "0"), put("a/y.json", "y"))
	done := make(chan struct{})
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				tr, err := rd.Tree(ctx, s1)
				if err == nil {
					var b []byte
					if b, err = tr.Read("a/x.json"); err == nil && string(b) != "0" {
						err = fmt.Errorf("old snapshot reads %q", b)
					}
				}
				if err == nil {
					var s Snapshot
					if s, err = rd.Active(ctx); err == nil {
						if tr, err = rd.Tree(ctx, s); err == nil {
							_, err = tr.Read("a/y.json")
						}
					}
				}
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	s := s1
	for i := range 20 {
		s = publish(t, w, s, put("a/x.json", fmt.Sprint(i+1)))
	}
	close(done)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestUnpublishedCommitIsNotReadable(t *testing.T) {
	ctx := context.Background()
	r := open(t, t.TempDir())
	s1 := publish(t, r, "", put("a/x.json", "1"))
	c, _ := object.GetCommit(r.st, plumbing.NewHash(string(s1)))
	orphan := &object.Commit{Author: c.Author, Committer: c.Committer, Message: "never published", TreeHash: c.TreeHash, ParentHashes: []plumbing.Hash{c.Hash}}
	o := &plumbing.MemoryObject{}
	if err := orphan.Encode(o); err != nil {
		t.Fatal(err)
	}
	if err := r.writeObjects([]*plumbing.MemoryObject{o}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Tree(ctx, Snapshot(o.Hash().String())); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpublished commit: %v", err)
	}
	for _, s := range []Snapshot{"", "nope", Snapshot(strings.Repeat("0", 40))} {
		if _, err := r.Tree(ctx, s); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%q: %v", s, err)
		}
	}
}

func looseObject(dir string, h plumbing.Hash) string {
	return filepath.Join(dir, "objects", h.String()[:2], h.String()[2:])
}

func blobOf(t *testing.T, r *Repository, s Snapshot, path string) plumbing.Hash {
	t.Helper()
	ix, err := r.indexOf(s)
	if err != nil {
		t.Fatal(err)
	}
	return ix.files[path]
}

// Damage is detected, never read as content, and Recover returns to the
// newest snapshot in the reflog that verifies completely.
func TestDamageIsDetectedAndRecovered(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (string, *Repository, Snapshot, Snapshot) {
		dir := t.TempDir()
		r := open(t, dir)
		s1 := publish(t, r, "", put("a/x.json", "one"), put("a/y.json", "shared"))
		s2 := publish(t, r, s1, put("a/x.json", "two"))
		return dir, r, s1, s2
	}

	t.Run("damaged blob", func(t *testing.T) {
		dir, r, s1, s2 := setup(t)
		p := looseObject(dir, blobOf(t, r, s2, "a/x.json"))
		b, _ := os.ReadFile(p)
		b[len(b)/2] ^= 0xff
		check(t, os.Chmod(p, 0o600))
		check(t, os.WriteFile(p, b, 0o600))
		r = open(t, dir)
		if _, err := r.Tree(ctx, s2); err == nil {
			tr, _ := r.Tree(ctx, s2)
			if got, err := tr.Read("a/x.json"); err == nil {
				t.Fatalf("damaged blob read as %q", got)
			}
		}
		if rep, _ := r.Verify(ctx); len(rep.Problems) == 0 {
			t.Fatal("verify missed a damaged blob")
		}
		got, err := r.Recover(ctx, "operator")
		if err != nil || got != s1 || active(t, r) != s1 || read(t, r, s1, "a/x.json") != "one" {
			t.Fatalf("recover = %s, %v", got, err)
		}
		verifyClean(t, r)
		publish(t, r, s1, put("a/x.json", "three"))
	})

	t.Run("substituted blob", func(t *testing.T) {
		dir, r, s1, s2 := setup(t)
		// A well-formed object file whose content is another object's.
		target := looseObject(dir, blobOf(t, r, s2, "a/x.json"))
		other, _ := os.ReadFile(looseObject(dir, blobOf(t, r, s1, "a/x.json")))
		check(t, os.Chmod(target, 0o600))
		if err := os.WriteFile(target, other, 0o600); err != nil {
			t.Fatal(err)
		}
		r = open(t, dir)
		tr, err := r.Tree(ctx, s2)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := tr.Read("a/x.json"); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("substituted blob read as %q, %v", got, err)
		}
		if rep, _ := r.Verify(ctx); len(rep.Problems) == 0 || !strings.Contains(rep.Problems[0], "hashes to") {
			t.Fatalf("verify = %v", rep.Problems)
		}
	})

	t.Run("missing commit", func(t *testing.T) {
		dir, _, s1, s2 := setup(t)
		check(t, os.Remove(looseObject(dir, plumbing.NewHash(string(s2)))))
		r := open(t, dir)
		if _, err := r.Tree(ctx, s2); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("missing commit: %v", err)
		}
		if got, err := r.Recover(ctx, "operator"); err != nil || got != s1 {
			t.Fatalf("recover = %s, %v", got, err)
		}
	})

	t.Run("damaged ref", func(t *testing.T) {
		dir, _, _, s2 := setup(t)
		check(t, os.WriteFile(filepath.Join(dir, "refs", "heads", "active"), []byte("garbage\n"), 0o600))
		r := open(t, dir)
		if _, err := r.Active(ctx); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("damaged ref: %v", err)
		}
		if _, err := r.Publish(ctx, Publication{Expected: s2, Author: "a", Subject: "x", Changes: []Change{put("a/z.json", "1")}}); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("publish over a damaged ref: %v", err)
		}
		if rep, _ := r.Verify(ctx); len(rep.Problems) == 0 {
			t.Fatal("verify missed a damaged ref")
		}
		if got, err := r.Recover(ctx, "operator"); err != nil || got != s2 {
			t.Fatalf("recover = %s, %v", got, err)
		}
		verifyClean(t, r)
	})

	t.Run("healthy", func(t *testing.T) {
		_, r, _, s2 := setup(t)
		if got, err := r.Recover(ctx, "operator"); err != nil || got != s2 {
			t.Fatalf("recover of a healthy repository = %s, %v", got, err)
		}
	})

	t.Run("nothing verifies", func(t *testing.T) {
		dir, _, s1, s2 := setup(t)
		check(t, os.Remove(looseObject(dir, plumbing.NewHash(string(s2)))))
		check(t, os.Remove(looseObject(dir, plumbing.NewHash(string(s1)))))
		r := open(t, dir)
		if _, err := r.Recover(ctx, "operator"); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("recover with nothing intact: %v", err)
		}
	})
}

// The repository is ordinary Git: the git CLI reads, verifies and repacks
// it, and Polaroid keeps working on what the CLI produced (packed objects,
// packed refs), as with a mirror clone or an archived copy.
func TestGitCompatibility(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, gitBin, append([]string{"--git-dir=" + dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME="+t.TempDir())
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	r := open(t, dir)
	s1 := publish(t, r, "", put("procedures/a.json", "1"))
	s2, err := r.Publish(ctx, Publication{Expected: s1, Author: "copilot.agent", Subject: "a: improve", Trailers: []Trailer{{"Polaroid-Reason", "clearer"}},
		Changes: []Change{put("procedures/a.json", "2"), put("procedures/b.json", "b")}})
	if err != nil {
		t.Fatal(err)
	}
	git("fsck", "--strict", "--no-dangling")
	if got := git("log", "--format=%H", "active"); got != string(s2)+"\n"+string(s1) {
		t.Fatalf("git log = %q", got)
	}
	if git("show", "active:procedures/a.json") != "2" || git("symbolic-ref", "HEAD") != "refs/heads/active" ||
		git("config", "core.bare") != "true" || !strings.Contains(git("log", "-1", "--format=%B"), "Polaroid-Reason: clearer") {
		t.Fatal("git reads the repository differently")
	}
	if !strings.Contains(git("reflog", "show", "active"), "publish: a: improve") {
		t.Fatal("git does not read the reflog")
	}

	read(t, r, s2, "procedures/b.json") // load pack indexes before the CLI repacks
	git("pack-refs", "--all")
	git("gc", "--prune=now", "--quiet")
	if _, err := os.Stat(filepath.Join(dir, "refs", "heads", "active")); err == nil {
		t.Fatal("expected git to pack the active ref")
	}
	if active(t, r) != s2 || read(t, r, s1, "procedures/a.json") != "1" || read(t, open(t, dir), s2, "procedures/b.json") != "b" {
		t.Fatal("reading packed refs and objects")
	}
	s3 := publish(t, r, s2, put("procedures/a.json", "3"))
	verifyClean(t, r)
	git("fsck", "--strict", "--no-dangling")
	if git("rev-parse", "active") != string(s3) {
		t.Fatal("git sees a different active snapshot")
	}
}

func TestOpenCreatesBareRepository(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "knowledge")
	r := open(t, dir)
	head, _ := os.ReadFile(filepath.Join(dir, "HEAD"))
	cfg, _ := os.ReadFile(filepath.Join(dir, "config"))
	if !bytes.Equal(head, []byte("ref: refs/heads/active\n")) || !bytes.Contains(cfg, []byte("bare = true")) {
		t.Fatalf("HEAD %q, config %q", head, cfg)
	}
	if rep := verifyClean(t, r); rep.Active != "" || rep.Snapshots != 0 {
		t.Fatalf("empty verify = %+v", rep)
	}
	if got, err := r.Recover(context.Background(), "operator"); err != nil || got != "" {
		t.Fatalf("recover of an empty repository = %q, %v", got, err)
	}
	// Opening again does not re-initialise.
	publish(t, r, "", put("a/x.json", "1"))
	if active(t, open(t, dir)) == "" {
		t.Fatal("reopening lost the snapshot")
	}
}
