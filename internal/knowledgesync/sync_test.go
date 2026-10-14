package knowledgesync_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledgesync"
)

var none gitcore.Credentials

func bareRemote(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	dir := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.CommandContext(context.Background(), bin, "init", "--quiet", "--bare", "--initial-branch=main", dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

// instance is one independent Polaroid: its own Git-core store, kernel and
// trust policy.
type instance struct {
	dir  string
	repo *gitcore.Repository
	sync *knowledgesync.Syncer
	k    *knowledge.Kernel
}

func newInstance(t *testing.T, p knowledgesync.Policy) *instance {
	t.Helper()
	return newInstanceAt(t, t.TempDir(), p)
}

// newInstanceAt opens the instance in dir, as a restarted daemon would.
func newInstanceAt(t *testing.T, dir string, p knowledgesync.Policy) *instance {
	t.Helper()
	repo, err := gitcore.Open(dir, gitcore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	s, err := knowledgesync.New(repo, p)
	if err != nil {
		t.Fatal(err)
	}
	return &instance{dir: dir, repo: repo, sync: s, k: s.Kernel()}
}

func team(remote string, publishers ...string) knowledgesync.Policy {
	return knowledgesync.Policy{Sources: []knowledgesync.Source{{Name: "team", URL: remote, Publishers: publishers, Publish: true}}}
}

func (in *instance) active(t *testing.T) knowledge.Snapshot {
	t.Helper()
	c, err := in.k.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c.Snapshot()
}

func (in *instance) publish(t *testing.T, author, summary string, procs ...*knowledge.Procedure) knowledge.Snapshot {
	t.Helper()
	var changes []knowledge.Change
	for _, p := range procs {
		changes = append(changes, knowledge.Change{Key: p.Key, Procedure: p})
	}
	s, err := in.k.Publish(context.Background(), knowledge.Proposal{Base: in.active(t), Changes: changes,
		Provenance: knowledge.Provenance{Author: author, Summary: summary, Rationale: "Because " + summary + ".", Validation: "go test ./..."}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (in *instance) push(t *testing.T) gitcore.PushResult {
	t.Helper()
	res, err := in.sync.Publish(context.Background(), "team", none)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// fetchValidated fetches and validates from source, failing on any error.
func (in *instance) fetchValidated(t *testing.T, source string) *knowledgesync.Candidate {
	t.Helper()
	c, err := in.sync.Fetch(context.Background(), source, none)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.sync.Validate(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c
}

func decision(who string) knowledge.Provenance {
	return knowledge.Provenance{Author: who, Summary: "Accept the team's knowledge", Rationale: "The publications are from authorised publishers and validate.", Validation: "fetched, verified and validated"}
}

func resolve(t *testing.T, c *knowledge.Catalog) string {
	t.Helper()
	n, err := c.Resolve("go.module.verify", "github.com/example/service")
	if err != nil {
		t.Fatal(err)
	}
	return knowledgetest.Describe(t, n)
}

// Two independent instances exchange knowledge through an ordinary bare
// repository: publish, send, fetch, validate, activate, improve, diverge,
// reject, and rebuild a third instance from the remote alone.
func TestExchangeBetweenInstances(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a := newInstance(t, team(remote, "alice", "bob"))
	b := newInstance(t, team(remote, "alice", "bob"))

	// 1–2. A publishes three related procedures and sends them.
	s1 := a.publish(t, "alice", "Add the Go module procedures", knowledgetest.Proc("go.module.build", "Build the Go module"),
		knowledgetest.Proc("go.module.checks", "Run gofmt and go vet on the module"),
		knowledgetest.Proc("go.module.verify", "Verify a change to a Go module", "build=go.module.build", "checks=go.module.checks"))
	if res := a.push(t); res.After != gitcore.Snapshot(s1) {
		t.Fatalf("push = %+v", res)
	}

	// 3. B fetches: nothing is active or readable yet.
	c, err := b.sync.Fetch(ctx, "team", none)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != knowledgesync.Fetched || c.Remote != s1 || b.active(t) != "" {
		t.Fatalf("fetched candidate %+v", c)
	}
	if _, err := b.k.At(ctx, s1); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("fetched knowledge readable before activation: %v", err)
	}
	if err := b.sync.Activate(ctx, c, decision("operator.b")); !errors.Is(err, knowledgesync.ErrNotEligible) {
		t.Fatalf("activation before validation: %v", err)
	}
	// 4. B validates the complete catalog.
	if err := b.sync.Validate(ctx, c); err != nil {
		t.Fatal(err)
	}
	if c.Status != knowledgesync.FastForward || len(c.Incoming) != 1 || c.Incoming[0].Publisher != "alice" || c.Incoming[0].Procedures != 3 {
		t.Fatalf("validated candidate %+v", c)
	}
	if b.active(t) != "" {
		t.Fatal("validation activated knowledge")
	}
	// 5. B activates it explicitly.
	if err := b.sync.Activate(ctx, c, decision("operator.b")); err != nil {
		t.Fatal(err)
	}
	// 6. B discovers and reads it.
	cat, err := b.k.Active(ctx)
	if err != nil || cat.Snapshot() != s1 {
		t.Fatalf("B active = %v, %v", cat, err)
	}
	if found := cat.Discover("verify a change to my go module", "github.com/example/service", 3); len(found) != 3 || found[0].Key != "go.module.verify" {
		t.Fatalf("discover on B = %+v", found)
	}
	original := resolve(t, cat)
	aCat, _ := a.k.Active(ctx)
	if knowledgetest.Fingerprint(t, cat) != knowledgetest.Fingerprint(t, aCat) || resolve(t, aCat) != original {
		t.Fatal("B reads different knowledge than A published")
	}
	recs, err := b.k.Activations(ctx, 5)
	if err != nil || len(recs) != 1 || recs[0].Target != s1 || recs[0].Provenance != decision("operator.b") || recs[0].Accepted == "" {
		t.Fatalf("activation record = %+v, %v", recs, err)
	}
	if !strings.Contains(trailer(recs[0].Origin, knowledgesync.TrailerSource), remote) || trailer(recs[0].Origin, knowledgesync.TrailerPublishers) != "alice" ||
		trailer(recs[0].Origin, knowledgesync.TrailerRemote) != string(s1) {
		t.Fatalf("activation origin = %+v", recs[0].Origin)
	}
	h, err := b.k.History(ctx, s1, 5)
	if err != nil || len(h) != 1 || h[0].Snapshot != s1 || h[0].Provenance.Author != "alice" || h[0].Provenance.Rationale != "Because Add the Go module procedures." ||
		h[0].Provenance.Validation != "go test ./..." || !strings.HasPrefix(h[0].Validated, "catalog of 3 procedures") {
		t.Fatalf("B's history of A's publication = %+v, %v", h, err)
	}

	// 7–8. A improves a referenced procedure; B transfers and activates it.
	s2 := a.publish(t, "alice", "Add staticcheck", knowledgetest.Proc("go.module.checks", "Run gofmt, go vet and staticcheck on the module"))
	a.push(t)
	c2 := b.fetchValidated(t, "team")
	if c2.Status != knowledgesync.FastForward || c2.Local != s1 || len(c2.Incoming) != 1 || c2.Bytes >= c.Bytes {
		t.Fatalf("incremental candidate %+v (initial %d bytes)", c2, c.Bytes)
	}
	if err := b.sync.Activate(ctx, c2, decision("operator.b")); err != nil {
		t.Fatal(err)
	}
	cat2, _ := b.k.Active(ctx)
	if cat2.Snapshot() != s2 || !strings.Contains(resolve(t, cat2), "staticcheck") {
		t.Fatalf("B after the improvement: %s", resolve(t, cat2))
	}
	// 9. B still reads its previous snapshot.
	old, err := b.k.At(ctx, s1)
	if err != nil || resolve(t, old) != original {
		t.Fatalf("B's previous snapshot: %v", err)
	}

	// 14. A fresh instance rebuilds the knowledge from the remote alone.
	fresh := newInstance(t, team(remote, "alice"))
	cf := fresh.fetchValidated(t, "team")
	if cf.Status != knowledgesync.FastForward || len(cf.Incoming) != 2 {
		t.Fatalf("fresh candidate %+v", cf)
	}
	if err := fresh.sync.Activate(ctx, cf, decision("operator.c")); err != nil {
		t.Fatal(err)
	}
	fcat, _ := fresh.k.Active(ctx)
	if knowledgetest.Fingerprint(t, fcat) != knowledgetest.Fingerprint(t, cat2) {
		t.Fatal("the rebuilt instance reads different knowledge")
	}
	if fh, _ := fresh.k.History(ctx, s2, 5); len(fh) != 2 || fh[1].Provenance.Author != "alice" {
		t.Fatalf("rebuilt history = %+v", fh)
	}

	// 10. A and B publish conflicting improvements independently.
	sa := a.publish(t, "alice", "Build with -trimpath", knowledgetest.Proc("go.module.build", "Build the Go module with -trimpath"))
	sb := b.publish(t, "bob", "Build with -race", knowledgetest.Proc("go.module.build", "Build the Go module with -race"))
	a.push(t)
	if _, err := b.sync.Publish(ctx, "team", none); !errors.Is(err, gitcore.ErrBehind) {
		t.Fatalf("B's push over unfetched knowledge: %v", err)
	}
	// 11. Synchronisation detects the divergence.
	cd := b.fetchValidated(t, "team")
	if cd.Status != knowledgesync.Diverged {
		t.Fatalf("status = %s", cd.Status)
	}
	if err := b.sync.Activate(ctx, cd, decision("operator.b")); !errors.Is(err, knowledgesync.ErrNotEligible) || !errors.Is(err, knowledge.ErrDiverged) {
		t.Fatalf("activating a diverged candidate: %v", err)
	}
	if _, err := b.sync.Publish(ctx, "team", none); !errors.Is(err, gitcore.ErrDiverged) {
		t.Fatalf("B's push of a diverged history: %v", err)
	}
	// 12. Neither instance loses its knowledge; both histories are kept.
	if b.active(t) != sb || a.active(t) != sa {
		t.Fatal("divergence changed an instance's active knowledge")
	}
	if tr, _ := b.repo.Tracking(ctx, gitcore.Remote{Name: "team"}); tr != gitcore.Snapshot(sa) {
		t.Fatalf("B does not keep A's diverged history: tracking %s", tr)
	}
	bcat, _ := b.k.Active(ctx)
	if p, _ := bcat.Get("go.module.build"); p.Goal != "Build the Go module with -race" {
		t.Fatalf("B's own improvement: %q", p.Goal)
	}
	if ca := a.fetchValidated(t, "team"); ca.Status != knowledgesync.Identical {
		t.Fatalf("A's view of the remote: %s", ca.Status)
	}
}

func trailer(ts []knowledge.Trailer, key string) string {
	for _, t := range ts {
		if t.Key == key {
			return t.Value
		}
	}
	return ""
}

// 13. Knowledge that is invalid, or from an unauthorised publisher or
// source, is rejected and changes nothing.
func TestUntrustedKnowledgeIsRejected(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a := newInstance(t, team(remote, "alice", "mallory"))
	b := newInstance(t, team(remote, "alice"))
	s1 := a.publish(t, "alice", "Add the build procedure", knowledgetest.Proc("go.module.build", "Build the Go module"))
	a.push(t)
	if err := b.sync.Activate(ctx, b.fetchValidated(t, "team"), decision("operator.b")); err != nil {
		t.Fatal(err)
	}

	rejected := func(t *testing.T, want string) {
		t.Helper()
		c, err := b.sync.Fetch(ctx, "team", none)
		if err != nil {
			t.Fatal(err)
		}
		err = b.sync.Validate(ctx, c)
		if !errors.Is(err, knowledgesync.ErrRejected) || c.Status != knowledgesync.Rejected || !strings.Contains(err.Error(), want) {
			t.Fatalf("validate: %v", err)
		}
		t.Log(err)
		if b.active(t) != s1 {
			t.Fatal("a rejected candidate changed the active knowledge")
		}
		if tr, _ := b.repo.Tracking(ctx, gitcore.Remote{Name: "team"}); tr != gitcore.Snapshot(s1) {
			t.Fatalf("a rejected candidate was imported: tracking %s", tr)
		}
		if err := b.sync.Activate(ctx, c, decision("operator.b")); !errors.Is(err, knowledgesync.ErrNotEligible) {
			t.Fatalf("activating a rejected candidate: %v", err)
		}
	}

	// An unauthorised publisher, through the kernel (a valid catalog).
	s2 := a.publish(t, "mallory", "Weaken the build", knowledgetest.Proc("go.module.build", "Skip the build"))
	a.push(t)
	rejected(t, `publisher "mallory" is not authorised`)

	// An invalid catalog written behind the kernel's back, by an authorised
	// name: a reference to a procedure that does not exist.
	doc, err := knowledge.Encode(knowledgetest.Proc("go.module.verify", "Verify", "build=go.module.gone"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.repo.Publish(ctx, gitcore.Publication{Expected: gitcore.Snapshot(s2), Author: "alice", Subject: "broken", Body: "no kernel",
		Changes: []gitcore.Change{{Path: knowledge.Path("go.module.verify"), Content: doc}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.repo.Push(ctx, gitcore.Remote{Name: "team", URL: remote}, none); err != nil {
		t.Fatal(err)
	}
	rejected(t, "go.module.gone, which is not in the catalog")

	// A publication without a rationale, written behind the kernel's back.
	s3, _ := a.repo.Active(ctx)
	if _, err := a.repo.Publish(ctx, gitcore.Publication{Expected: s3, Author: "alice", Subject: "unexplained",
		Changes: []gitcore.Change{{Path: knowledge.Path("go.module.verify"), Delete: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.repo.Push(ctx, gitcore.Remote{Name: "team", URL: remote}, none); err != nil {
		t.Fatal(err)
	}
	rejected(t, "no rationale")

	// A source the policy does not list is never contacted.
	if _, err := b.sync.Fetch(ctx, "elsewhere", none); !errors.Is(err, knowledgesync.ErrUnknownSource) {
		t.Fatalf("fetch from an unknown source: %v", err)
	}
	ro := newInstance(t, knowledgesync.Policy{Sources: []knowledgesync.Source{{Name: "team", URL: remote, Publishers: []string{"alice"}}}})
	if _, err := ro.sync.Publish(ctx, "team", none); !errors.Is(err, knowledgesync.ErrNotPermitted) {
		t.Fatalf("publish to a fetch-only source: %v", err)
	}
	if _, err := knowledgesync.New(b.repo, knowledgesync.Policy{Sources: []knowledgesync.Source{{Name: "team", URL: remote}}}); err == nil {
		t.Fatal("a source without publishers was accepted")
	}
}

type acceptRecords struct{}

func (acceptRecords) ValidateRecords(context.Context, []knowledge.RecordDocument) error { return nil }

// A fetched publication that changes a rule's semantics under the same
// revision, written behind the publisher's kernel, is rejected at
// validation (ADR-0050): rule governance holds across instances.
func TestFetchedRuleGovernance(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	open := func() *instance {
		repo, err := gitcore.Open(t.TempDir(), gitcore.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Close() })
		s, err := knowledgesync.New(repo, team(remote, "alice"), knowledge.WithRecordValidator(acceptRecords{}))
		if err != nil {
			t.Fatal(err)
		}
		return &instance{repo: repo, sync: s, k: s.Kernel()}
	}
	a, b := open(), open()
	s0, err := a.k.Publish(ctx, knowledge.Proposal{Changes: knowledgetest.CorpusProcedures(),
		Provenance: knowledge.Provenance{Author: "alice", Summary: "procedures", Rationale: "Because."}})
	if err != nil {
		t.Fatal(err)
	}
	s1, err := a.k.Publish(ctx, knowledge.Proposal{Base: s0, Records: knowledgetest.CorpusRecordChanges(),
		Provenance: knowledge.Provenance{Author: "alice", Summary: "corpus", Rationale: "Because."}})
	if err != nil {
		t.Fatal(err)
	}
	a.push(t)
	if err := b.sync.Activate(ctx, b.fetchValidated(t, "team"), decision("operator.b")); err != nil {
		t.Fatal(err)
	}
	cat, err := a.k.At(ctx, s1)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := cat.Record("rule.software.tests")
	if err != nil {
		t.Fatal(err)
	}
	rule.Statement = "Only some software needs tests."
	doc, err := knowledge.EncodeRecord(rule)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.repo.Publish(ctx, gitcore.Publication{Expected: gitcore.Snapshot(s1), Author: "alice", Subject: "weaken", Body: "no kernel",
		Changes: []gitcore.Change{{Path: knowledge.RecordPath(rule.Type, rule.Key), Content: doc}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.repo.Push(ctx, gitcore.Remote{Name: "team", URL: remote}, none); err != nil {
		t.Fatal(err)
	}
	c, err := b.sync.Fetch(ctx, "team", none)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.sync.Validate(ctx, c); !errors.Is(err, knowledgesync.ErrRejected) || !strings.Contains(err.Error(), "its revision must rise above 1") {
		t.Fatalf("validate: %v", err)
	}
	if b.active(t) != s1 {
		t.Fatal("a rejected candidate changed the active knowledge")
	}

	// Rollback trailers in fetched history are the remote's claims and grant
	// no exemption: "restoring" the original rule under the same revision
	// is refused, and so is removing the protected rule.
	weakened, _ := a.repo.Active(ctx)
	orig, err := cat.Record("rule.software.tests")
	if err != nil {
		t.Fatal(err)
	}
	origDoc, err := knowledge.EncodeRecord(orig)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := a.repo.Publish(ctx, gitcore.Publication{Expected: weakened, Author: "alice", Subject: "restore", Body: "no kernel",
		Trailers: []gitcore.Trailer{{Key: knowledge.TrailerRollback, Value: string(s1)}},
		Changes:  []gitcore.Change{{Path: knowledge.RecordPath(orig.Type, orig.Key), Content: origDoc}}})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := a.repo.Publish(ctx, gitcore.Publication{Expected: restored, Author: "alice", Subject: "remove", Body: "no kernel",
		Trailers: []gitcore.Trailer{{Key: knowledge.TrailerRollback, Value: string(s0)}},
		Changes:  []gitcore.Change{{Path: knowledge.RecordPath(orig.Type, orig.Key), Delete: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.repo.Push(ctx, gitcore.Remote{Name: "team", URL: remote}, none); err != nil {
		t.Fatal(err)
	}
	c, err = b.sync.Fetch(ctx, "team", none)
	if err != nil {
		t.Fatal(err)
	}
	err = b.sync.Validate(ctx, c)
	for _, want := range []string{
		"snapshot " + string(restored) + ": invalid knowledge: records[rule.software.tests].revision: the rule's semantics changed",
		"snapshot " + string(removed) + ": invalid knowledge: records[rule.software.tests]: is a protected invariant and cannot be removed",
	} {
		if !errors.Is(err, knowledgesync.ErrRejected) || !strings.Contains(err.Error(), want) {
			t.Fatalf("validate with forged rollback trailers: %v\nwant %s", err, want)
		}
	}
}

// A candidate validated against a snapshot that is no longer active cannot
// be activated; whichever of the activation and a local publication comes
// first wins, and the other is refused.
func TestActivationIsOptimistic(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a := newInstance(t, team(remote, "alice"))
	s1 := a.publish(t, "alice", "Add the build procedure", knowledgetest.Proc("go.module.build", "Build the Go module"))
	a.push(t)

	b := newInstance(t, team(remote, "alice"))
	c := b.fetchValidated(t, "team")
	local := b.publish(t, "bob", "Add a local procedure", knowledgetest.Proc("team.local", "Something local"))
	if err := b.sync.Activate(ctx, c, decision("operator.b")); !errors.Is(err, knowledge.ErrStale) {
		t.Fatalf("stale activation: %v", err)
	}
	if b.active(t) != local {
		t.Fatal("a stale activation changed the active knowledge")
	}

	for range 5 {
		r := newInstance(t, team(remote, "alice"))
		c := r.fetchValidated(t, "team")
		var wg sync.WaitGroup
		var actErr, pubErr error
		wg.Go(func() { actErr = r.sync.Activate(ctx, c, decision("operator.r")) })
		wg.Go(func() {
			_, pubErr = r.k.Publish(ctx, knowledge.Proposal{Changes: []knowledge.Change{{Key: "team.local", Procedure: knowledgetest.Proc("team.local", "Local")}},
				Provenance: knowledge.Provenance{Author: "bob", Summary: "local", Rationale: "racing"}})
		})
		wg.Wait()
		if (actErr == nil) == (pubErr == nil) || !errors.Is(errors.Join(actErr, pubErr), knowledge.ErrStale) {
			t.Fatalf("activation %v, publication %v", actErr, pubErr)
		}
		if want := s1; actErr == nil && r.active(t) != want {
			t.Fatalf("active = %s", r.active(t))
		}
	}
}

// An oversized candidate stops at the transfer limits and leaves nothing.
func TestOversizedCandidate(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a := newInstance(t, team(remote, "alice"))
	var procs []*knowledge.Procedure
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		procs = append(procs, knowledgetest.Proc("bulk."+k, strings.Repeat("Large goal ", 150)))
	}
	a.publish(t, "alice", "Add bulk procedures", procs...)
	a.push(t)
	p := team(remote, "alice")
	p.Limits = gitcore.TransferLimits{MaxPackBytes: 1 << 20, MaxObjects: 1000, MaxObjectBytes: 64 << 10, MaxInflatedBytes: 4 << 10, MaxCommits: 100}
	b := newInstance(t, p)
	if _, err := b.sync.Fetch(ctx, "team", none); !errors.Is(err, gitcore.ErrTooLarge) {
		t.Fatalf("oversized fetch: %v", err)
	}
	if b.active(t) != "" {
		t.Fatal("an oversized candidate changed the active knowledge")
	}
	if ents, _ := os.ReadDir(filepath.Join(b.dir, "polaroid-quarantine")); len(ents) != 0 {
		t.Fatalf("quarantine left behind: %v", ents)
	}
}
