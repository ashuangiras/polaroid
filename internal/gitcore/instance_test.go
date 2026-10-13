package gitcore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Each repository has one identity, created once (also by racing opens),
// kept across reopening, and never carried to a remote.
func TestInstanceIdentity(t *testing.T) {
	dir := t.TempDir()
	ids := make([]string, 4)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() { ids[i] = open(t, dir).Instance() })
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] || !instancePattern.MatchString(id) {
			t.Fatalf("identities %v", ids)
		}
	}
	if open(t, dir).Instance() != ids[0] || open(t, t.TempDir()).Instance() == ids[0] {
		t.Fatal("identity not stable per repository, or shared between repositories")
	}

	remote := bareRemote(t)
	r := open(t, dir)
	publish(t, r, "", put("p/a.json", "1"))
	push(t, r, Remote{Name: "team", URL: remote})
	if _, err := os.Stat(filepath.Join(remote, instanceName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the identity reached the remote")
	}
	if strings.Contains(runGit(t, "--git-dir="+remote, "log", "--all", "--format=%B"), ids[0]) {
		t.Fatal("the identity is in the remote's history")
	}

	check(t, os.WriteFile(filepath.Join(dir, instanceName), []byte("someone-else\n"), 0o600))
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("open with a damaged identity: %v", err)
	}
}

// The journal is written before the active ref moves: an activation that
// stops in between leaves the old snapshot active, its target recorded but
// unpublished, and Verify warning; nothing local is ever classified as
// imported, nor anything imported as local.
func TestInterruptedActivation(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	rm := Remote{Name: "team", URL: remote}
	a := open(t, t.TempDir())
	s1 := publish(t, a, "", put("p/a.json", "1"))
	s2 := publish(t, a, s1, put("p/a.json", "2"))
	push(t, a, rm)

	dir := t.TempDir()
	b := open(t, dir)
	accept(t, b, rm)
	testHook = func(stage string) {
		if stage == "journal-durable" {
			panic("interrupted")
		}
	}
	func() {
		defer func() { _ = recover() }()
		_ = advance(b, "", s2)
	}()
	testHook = nil
	b = open(t, dir)
	if active(t, b) != "" {
		t.Fatal("an interrupted activation moved the active ref")
	}
	if _, err := b.Imported(ctx, s2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unpublished target is classified: %v", err)
	}
	rep, err := b.Verify(ctx)
	if err != nil || len(rep.Problems) > 0 || !slices.ContainsFunc(rep.Warnings, func(w string) bool { return strings.Contains(w, "did not complete") }) {
		t.Fatalf("verify = %+v, %v", rep, err)
	}
	check(t, advance(b, "", s2))
	local := publish(t, b, s2, put("p/b.json", "local"))
	for s, want := range map[Snapshot]bool{s1: true, s2: true, local: false} {
		if imp, err := b.Imported(ctx, s); err != nil || imp != want {
			t.Fatalf("%s imported %v, %v; want %v", s, imp, err, want)
		}
	}
	if rep := verifyClean(t, b); len(rep.Warnings) > 0 {
		t.Fatalf("warnings after the completed activation: %v", rep.Warnings)
	}
	if !errors.Is(b.Advance(ctx, Advance{Expected: local, Target: s2, Author: "operator", Subject: "x", Trailers: []Trailer{{"Activated", string(s1)}}}), ErrInvalid) {
		t.Fatal("a caller set the journal's own trailer")
	}
}
