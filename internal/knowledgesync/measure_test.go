package knowledgesync_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledgesync"
)

func dirBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err == nil {
			n += info.Size()
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func timed(t *testing.T, f func() error) time.Duration {
	t.Helper()
	start := time.Now()
	if err := f(); err != nil {
		t.Fatal(err)
	}
	return time.Since(start)
}

// TestMeasureExchange reports transfer sizes, latencies and storage growth.
// It runs only with POLAROID_SYNC_MEASURE=1; it asserts correctness, not
// speed.
func TestMeasureExchange(t *testing.T) {
	if os.Getenv("POLAROID_SYNC_MEASURE") != "1" {
		t.Skip("set POLAROID_SYNC_MEASURE=1 to measure")
	}
	ctx := context.Background()
	for _, n := range []int{100, 1000} {
		remote := bareRemote(t)
		a := newInstance(t, team(remote, "alice"))
		b := newInstance(t, team(remote, "alice"))
		var procs []*knowledge.Procedure
		for i := range n {
			procs = append(procs, knowledgetest.Proc(fmt.Sprintf("bench.p%04d", i), fmt.Sprintf("Benchmark procedure number %d", i)))
		}
		publishDur := timed(t, func() error { a.publish(t, "alice", "Add the benchmark procedures", procs...); return nil })
		var push gitcore.PushResult
		pushDur := timed(t, func() (err error) { push, err = a.sync.Publish(ctx, "team", none); return err })
		bBefore := dirBytes(t, b.dir)
		var c *knowledgesync.Candidate
		fetchDur := timed(t, func() (err error) { c, err = b.sync.Fetch(ctx, "team", none); return err })
		validateDur := timed(t, func() error { return b.sync.Validate(ctx, c) })
		activateDur := timed(t, func() error { return b.sync.Activate(ctx, c, decision("operator.b")) })
		bInitial := dirBytes(t, b.dir)
		remoteInitial := dirBytes(t, remote)
		t.Logf("n=%d initial: publish %v; push %d objects %d bytes in %v; fetch %d objects %d bytes in %v; validate %v; activate %v; B store %d -> %d bytes; remote %d bytes",
			n, publishDur.Round(time.Millisecond), push.Objects, push.Bytes, pushDur.Round(time.Millisecond), c.Objects, c.Bytes, fetchDur.Round(time.Millisecond),
			validateDur.Round(time.Millisecond), activateDur.Round(time.Millisecond), bBefore, bInitial, remoteInitial)

		changed := knowledgetest.Proc("bench.p0000", "Benchmark procedure number 0, improved")
		a.publish(t, "alice", "Improve one procedure", changed)
		pushDur = timed(t, func() (err error) { push, err = a.sync.Publish(ctx, "team", none); return err })
		fetchDur = timed(t, func() (err error) { c, err = b.sync.Fetch(ctx, "team", none); return err })
		validateDur = timed(t, func() error { return b.sync.Validate(ctx, c) })
		activateDur = timed(t, func() error { return b.sync.Activate(ctx, c, decision("operator.b")) })
		t.Logf("n=%d incremental: push %d objects %d bytes in %v; fetch %d objects %d bytes in %v; validate %v; activate %v; B store +%d bytes; remote +%d bytes",
			n, push.Objects, push.Bytes, pushDur.Round(time.Millisecond), c.Objects, c.Bytes, fetchDur.Round(time.Millisecond),
			validateDur.Round(time.Millisecond), activateDur.Round(time.Millisecond), dirBytes(t, b.dir)-bInitial, dirBytes(t, remote)-remoteInitial)
	}

	// Concurrent publication: six instances on the same snapshot race to
	// send their own improvement.
	remote := bareRemote(t)
	seed := newInstance(t, team(remote, "alice"))
	seed.publish(t, "alice", "Seed", knowledgetest.Proc("go.module.build", "Build"))
	seed.sync.Publish(ctx, "team", none) //nolint:errcheck // checked by the racers' outcome below
	var racers []*instance
	for i := range 6 {
		r := newInstance(t, team(remote, "alice"))
		if err := r.sync.Activate(ctx, r.fetchValidated(t, "team"), decision("operator")); err != nil {
			t.Fatal(err)
		}
		r.publish(t, "alice", fmt.Sprintf("Racer %d", i), knowledgetest.Proc("go.module.build", fmt.Sprintf("Build, racer %d", i)))
		racers = append(racers, r)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(racers))
	for i, r := range racers {
		wg.Go(func() { _, errs[i] = r.sync.Publish(ctx, "team", none) })
	}
	wg.Wait()
	won, stale, behind := 0, 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, gitcore.ErrStale):
			stale++
		case errors.Is(err, gitcore.ErrBehind):
			behind++
		default:
			t.Fatalf("racer: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d racers won", won)
	}
	t.Logf("concurrent publication: 6 racers, %d won, %d refused by the remote's compare-and-swap, %d refused before sending (behind)", won, stale, behind)
}

// TestGitHubSmoke exercises an explicitly supplied HTTPS remote, such as a
// private GitHub test repository: see docs/development/git-remote-smoke.md.
// It runs only when POLAROID_SMOKE_URL, POLAROID_SMOKE_BRANCH and
// POLAROID_SMOKE_TOKEN are set, and never deletes or rewrites anything.
func TestGitHubSmoke(t *testing.T) {
	url, branch, token := os.Getenv("POLAROID_SMOKE_URL"), os.Getenv("POLAROID_SMOKE_BRANCH"), os.Getenv("POLAROID_SMOKE_TOKEN")
	if url == "" || branch == "" || token == "" {
		t.Skip("set POLAROID_SMOKE_URL, POLAROID_SMOKE_BRANCH and POLAROID_SMOKE_TOKEN to run")
	}
	ctx := context.Background()
	cred := gitcore.Credentials{Username: os.Getenv("POLAROID_SMOKE_USER"), Password: token}
	policy := knowledgesync.Policy{Sources: []knowledgesync.Source{{Name: "smoke", URL: url, Branch: branch, Publishers: []string{"polaroid.smoke"}, Publish: true}}}
	a, b := newInstance(t, policy), newInstance(t, policy)
	a.publish(t, "polaroid.smoke", "Add the smoke-test procedures", knowledgetest.Proc("smoke.build", "Build"),
		knowledgetest.Proc("smoke.verify", "Verify", "build=smoke.build"))
	if _, err := a.sync.Publish(ctx, "smoke", cred); err != nil {
		t.Fatalf("push (the branch must be new or hold only this test's history): %v", err)
	}
	for round := range 2 {
		start := time.Now()
		c, err := b.sync.Fetch(ctx, "smoke", cred)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.sync.Validate(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := b.sync.Activate(ctx, c, decision("operator.smoke")); err != nil {
			t.Fatal(err)
		}
		ac, _ := a.k.Active(ctx)
		bc, _ := b.k.Active(ctx)
		if knowledgetest.Fingerprint(t, ac) != knowledgetest.Fingerprint(t, bc) {
			t.Fatal("B reads different knowledge than A sent")
		}
		t.Logf("round %d: %d objects, %d bytes, fetch+validate+activate %v", round, c.Objects, c.Bytes, time.Since(start).Round(time.Millisecond))
		if round == 0 {
			a.publish(t, "polaroid.smoke", "Improve the build", knowledgetest.Proc("smoke.build", "Build, improved"))
			if _, err := a.sync.Publish(ctx, "smoke", cred); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := b.sync.Fetch(ctx, "smoke", gitcore.Credentials{Username: cred.Username, Password: "not-the-token"}); !errors.Is(err, gitcore.ErrUnauthorized) {
		t.Fatalf("fetch with a wrong token: %v", err)
	}
}
