package gitstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/ashuangiras/polaroid/experiments/gitknowledge/gitstore"
	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge"
	"github.com/ashuangiras/polaroid/experiments/gitknowledge/knowledge/storetest"
)

func open(t *testing.T, dir string, trusted ...string) *gitstore.Store {
	t.Helper()
	s, err := gitstore.Open(dir, gitstore.Options{Policy: knowledge.DefaultPolicy, TrustedRemotes: trusted})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBehaviour(t *testing.T) {
	storetest.Run(t, func(t *testing.T, dir string) knowledge.Store { return open(t, dir) })
}

// A publish interrupted after writing objects but before moving the ref
// leaves unreachable objects and a temp file; the active knowledge is the
// previous one. A ref damaged outside Polaroid is an error, never "empty".
func TestInterruptedPublishAndDamagedRef(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir)
	s1, err := s.Publish(ctx, "", storetest.Initial(), note("initial"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, _ := obj.Writer()
	w.Write([]byte("orphan"))
	w.Close()
	if _, err := repo.Storer.SetEncodedObject(obj); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "refs", "heads", ".ref-interrupted"), []byte("0000"), 0o600); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	if head(t, s) != s1 {
		t.Fatal("interrupted publish changed the active knowledge")
	}
	if _, err := s.Publish(ctx, s1, checks("After the crash"), note("after")); err != nil {
		t.Fatalf("publish after an interrupted one: %v", err)
	}
	s.Close()

	if err := os.WriteFile(filepath.Join(dir, "refs", "heads", "active"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	defer s.Close()
	if _, err := s.Head(ctx); err == nil {
		t.Fatal("an empty active ref read as an empty store")
	}
	if _, err := s.Publish(ctx, "", storetest.Initial(), note("over a damaged ref")); err == nil {
		t.Fatal("published over a damaged ref")
	}
}
