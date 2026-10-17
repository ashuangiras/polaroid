package gitstore_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/gitstore"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

func open(t *testing.T, dir string) *gitstore.Store {
	t.Helper()
	r, err := gitcore.Open(dir, gitcore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return gitstore.New(r)
}

// The domain scenario against real Git-core: each open is a separate
// repository handle on the same directory.
func TestScenarioOnGitCore(t *testing.T) {
	knowledgetest.Run(t, func(t *testing.T, dir string) knowledge.Store { return open(t, dir) })
}

type observed struct {
	Active      knowledge.Snapshot
	Fingerprint string
	Resolution  string
	Original    string
	History     []knowledge.Publication
}

func observe(t *testing.T, k *knowledge.Kernel, original knowledge.Snapshot) observed {
	t.Helper()
	ctx := context.Background()
	c, err := k.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, err := c.Resolve("go.module.verify", "github.com/example/service")
	if err != nil {
		t.Fatal(err)
	}
	old, err := k.At(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	h, err := k.History(ctx, c.Snapshot(), 10)
	if err != nil {
		t.Fatal(err)
	}
	return observed{c.Snapshot(), knowledgetest.Fingerprint(t, c), knowledgetest.Describe(t, n), knowledgetest.Fingerprint(t, old), h}
}

// TestRecoverInFreshProcess's child: it gets only the repository directory.
func TestFreshProcessChild(t *testing.T) {
	dir, out := os.Getenv("KNOWLEDGE_CHILD_DIR"), os.Getenv("KNOWLEDGE_CHILD_OUT")
	if dir == "" {
		t.Skip("run by TestRecoverInFreshProcess")
	}
	k := knowledge.New(open(t, dir))
	b, err := json.Marshal(observe(t, k, knowledge.Snapshot(os.Getenv("KNOWLEDGE_CHILD_ORIGINAL"))))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A new process given nothing but the Git repository reconstructs the same
// catalog, compositions, earlier snapshots and provenance.
func TestRecoverInFreshProcess(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	k := knowledge.New(open(t, dir))
	s1, err := k.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{Changes: knowledgetest.Initial(),
		Provenance: knowledge.Provenance{Author: "alice", Summary: "Add the Go module procedures", Rationale: "Every module change is verified the same way."}}))
	if err != nil {
		t.Fatal(err)
	}
	checks := knowledgetest.Proc("go.module.checks", "Run gofmt, go vet and staticcheck on the module")
	if _, err := k.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{Base: s1, Changes: []knowledge.Change{{Key: checks.Key, Procedure: checks}},
		Provenance: knowledge.Provenance{Author: "bob", Summary: "Add staticcheck", Rationale: "vet misses unused results.", Finding: "fb-42", Validation: "go test ./..."}})); err != nil {
		t.Fatal(err)
	}
	want := observe(t, k, s1)

	out := filepath.Join(t.TempDir(), "observed.json")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFreshProcessChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "KNOWLEDGE_CHILD_DIR="+dir, "KNOWLEDGE_CHILD_OUT="+out, "KNOWLEDGE_CHILD_ORIGINAL="+string(s1))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got observed
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Active != want.Active || got.Fingerprint != want.Fingerprint || got.Resolution != want.Resolution || got.Original != want.Original {
		t.Fatalf("fresh process sees\n%+v\nwant\n%+v", got, want)
	}
	if len(got.History) != 2 || got.History[0].Provenance != want.History[0].Provenance || got.History[1].Provenance.Author != "alice" ||
		got.History[0].Provenance.Finding != "fb-42" || !got.History[0].When.Equal(want.History[0].When) {
		t.Fatalf("history = %+v", got.History)
	}
	t.Logf("fresh process: active %s, %s", got.Active, got.Resolution)
}

// Git-core stores any file; the kernel refuses a tree that is not a valid
// catalog, however it got there.
func TestForeignTreesAreRefused(t *testing.T) {
	ctx := context.Background()
	doc, _ := knowledge.Encode(knowledgetest.Proc("go.module.build", "Build"))
	for name, change := range map[string]gitcore.Change{
		"stray file":      {Path: "notes/todo.txt", Content: []byte("x")},
		"conflicting key": {Path: knowledge.Path("go.module.copy"), Content: doc},
		"not a document":  {Path: knowledge.Path("go.module.build"), Content: []byte(`{"key":"go.module.build"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			r, err := gitcore.Open(dir, gitcore.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			if _, err := r.Publish(ctx, gitcore.Publication{Changes: []gitcore.Change{change}, Author: "someone", Subject: name}); err != nil {
				t.Fatal(err)
			}
			k := knowledge.New(gitstore.New(r))
			if _, err := k.Active(ctx); !errors.Is(err, knowledge.ErrInvalid) {
				t.Fatalf("served: %v", err)
			}
			if _, err := k.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{Changes: knowledgetest.Initial(),
				Provenance: knowledge.Provenance{Author: "a", Summary: "s", Rationale: "r"}})); err == nil {
				t.Fatal("published on top of an invalid tree")
			}
		})
	}
}

// A damaged or substituted document is reported as corruption, never read
// as knowledge.
func TestDamagedDocumentIsCorrupt(t *testing.T) {
	ctx := context.Background()
	for _, how := range []string{"flipped byte", "substituted"} {
		t.Run(how, func(t *testing.T) {
			dir := t.TempDir()
			k := knowledge.New(open(t, dir))
			s, err := k.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{Changes: knowledgetest.Initial(),
				Provenance: knowledge.Provenance{Author: "a", Summary: "s", Rationale: "r"}}))
			if err != nil {
				t.Fatal(err)
			}
			c, _ := k.At(ctx, s)
			build, _ := c.Get("go.module.build")
			checks, _ := c.Get("go.module.checks")
			target := looseBlob(t, dir, build)
			if err := os.Chmod(target, 0o600); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(target)
			if how == "substituted" {
				b, _ = os.ReadFile(looseBlob(t, dir, checks))
			} else {
				b[len(b)/2] ^= 0xff
			}
			if err := os.WriteFile(target, b, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = knowledge.New(open(t, dir)).At(ctx, s)
			if !errors.Is(err, knowledge.ErrCorrupt) {
				t.Fatalf("read damaged knowledge: %v", err)
			}
			t.Log(err)
		})
	}
}

// looseBlob is the object file of a procedure's document.
func looseBlob(t *testing.T, dir string, p *knowledge.Procedure) string {
	t.Helper()
	doc, err := knowledge.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %s\x00", strconv.Itoa(len(doc)))
	_, _ = h.Write(doc)
	sum := hex.EncodeToString(h.Sum(nil))
	return filepath.Join(dir, "objects", sum[:2], sum[2:])
}
