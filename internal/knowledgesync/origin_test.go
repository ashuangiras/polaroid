package knowledgesync_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ashuangiras/polaroid/internal/gitcore"
	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/gitstore"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

type origins struct {
	Instance string
	Origins  map[knowledge.Snapshot]knowledge.Origin
}

func classify(t *testing.T, k *knowledge.Kernel, snaps ...knowledge.Snapshot) origins {
	t.Helper()
	ctx := context.Background()
	id, err := k.Instance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := origins{Instance: id, Origins: map[knowledge.Snapshot]knowledge.Origin{}}
	for _, s := range snaps {
		o, err := k.Origin(ctx, s)
		if err != nil {
			t.Fatalf("origin of %s: %v", s, err)
		}
		out.Origins[s] = o
	}
	return out
}

// TestOriginChild is TestOriginSurvivesRestartAndRecovery's fresh process:
// it gets only the repository directory and the snapshots to classify.
func TestOriginChild(t *testing.T) {
	dir, out := os.Getenv("ORIGIN_CHILD_DIR"), os.Getenv("ORIGIN_CHILD_OUT")
	if dir == "" {
		t.Skip("run by TestOriginSurvivesRestartAndRecovery")
	}
	var snaps []knowledge.Snapshot
	if err := json.Unmarshal([]byte(os.Getenv("ORIGIN_CHILD_SNAPSHOTS")), &snaps); err != nil {
		t.Fatal(err)
	}
	repo, err := gitcore.Open(dir, gitcore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	b, err := json.Marshal(classify(t, knowledge.New(gitstore.New(repo)), snaps...))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Whether knowledge is local or imported follows how it arrived: by
// activation (imported) or by a publication here (local, rollbacks
// included). A commit that claims this instance's identity, or a local
// operator as its author, is still imported. The classification survives
// recovery from damage and a fresh process.
func TestOriginSurvivesRestartAndRecovery(t *testing.T) {
	ctx := context.Background()
	remote := bareRemote(t)
	a := newInstance(t, team(remote, "alice", "operator.b"))
	b := newInstance(t, team(remote, "alice", "operator.b"))
	bID := b.repo.Instance()

	s1 := a.publish(t, "alice", "Add the procedures", knowledgetest.Initial()[0].Procedure, knowledgetest.Initial()[1].Procedure, knowledgetest.Initial()[2].Procedure)
	// A forged claim: B's identity and B's operator, written on A.
	doc, err := knowledge.Encode(knowledgetest.Proc("go.module.build", "Build, as B would"))
	if err != nil {
		t.Fatal(err)
	}
	forged, err := a.repo.Publish(ctx, gitcore.Publication{Expected: gitcore.Snapshot(s1), Author: "operator.b", Subject: "Improve the build", Body: "Looks local to B.",
		Trailers: []gitcore.Trailer{{Key: "Polaroid-Instance", Value: bID}, {Key: knowledge.TrailerFinding, Value: "01a13000-0000-7000-8000-000000000000"}},
		Changes:  []gitcore.Change{{Path: knowledge.Path("go.module.build"), Content: doc}}})
	if err != nil {
		t.Fatal(err)
	}
	a.push(t)
	if err := b.sync.Activate(ctx, b.fetchValidated(t, "team"), decision("operator.b")); err != nil {
		t.Fatal(err)
	}
	sForged := knowledge.Snapshot(forged)
	local := b.publish(t, "operator.b", "Local improvement", knowledgetest.Proc("go.module.checks", "Checks, improved here"))
	back, err := b.k.Rollback(ctx, local, sForged, knowledge.Provenance{Author: "operator.b", Summary: "Roll back", Rationale: "Testing rollback"})
	if err != nil {
		t.Fatal(err)
	}
	want := origins{Instance: bID, Origins: map[knowledge.Snapshot]knowledge.Origin{
		s1: knowledge.OriginImported, sForged: knowledge.OriginImported, local: knowledge.OriginLocal, back: knowledge.OriginLocal}}
	got := classify(t, b.k, s1, sForged, local, back)
	if !equalOrigins(got, want) {
		t.Fatalf("B classifies %+v, want %+v", got, want)
	}
	if h, _ := b.k.History(ctx, sForged, 1); len(h) != 1 || h[0].Provenance.Author != "operator.b" || h[0].Provenance.Finding == "" {
		t.Fatalf("the forged claim should stay visible as a claim: %+v", h)
	}
	if aID := a.repo.Instance(); aID == bID {
		t.Fatal("instances share an identity")
	}
	if got := classify(t, a.k, s1); got.Origins[s1] != knowledge.OriginLocal {
		t.Fatalf("A's own publication on A: %+v", got)
	}

	// A fresh process sees the same identity and classification.
	snaps, _ := json.Marshal([]knowledge.Snapshot{s1, sForged, local, back})
	out := filepath.Join(t.TempDir(), "origins.json")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOriginChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "ORIGIN_CHILD_DIR="+b.dir, "ORIGIN_CHILD_OUT="+out, "ORIGIN_CHILD_SNAPSHOTS="+string(snaps))
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, o)
	}
	var fresh origins
	raw, err := os.ReadFile(out)
	if err != nil || json.Unmarshal(raw, &fresh) != nil || !equalOrigins(fresh, want) {
		t.Fatalf("fresh process classifies %s, %v", raw, err)
	}

	// Recovery from a damaged local publication keeps the classification.
	obj := filepath.Join(b.dir, "objects", string(back)[:2], string(back)[2:])
	if err := os.Chmod(obj, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obj, []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened := newInstanceAt(t, b.dir, team(remote, "alice", "operator.b"))
	recovered, err := reopened.repo.Recover(ctx, "operator.b")
	if err != nil || knowledge.Snapshot(recovered) != local {
		t.Fatalf("recover = %s, %v", recovered, err)
	}
	if _, err := reopened.k.Origin(ctx, back); err == nil {
		t.Fatal("the damaged snapshot is still classified")
	}
	delete(want.Origins, back)
	if got := classify(t, reopened.k, s1, sForged, local); !equalOrigins(got, want) {
		t.Fatalf("after recovery %+v", got)
	}

	// An instance rebuilt from the remote is a new instance: everything it
	// holds was imported.
	rebuilt := newInstance(t, team(remote, "alice", "operator.b"))
	if err := rebuilt.sync.Activate(ctx, rebuilt.fetchValidated(t, "team"), decision("operator.c")); err != nil {
		t.Fatal(err)
	}
	if got := classify(t, rebuilt.k, s1, sForged); got.Instance == bID || got.Origins[s1] != knowledge.OriginImported || got.Origins[sForged] != knowledge.OriginImported {
		t.Fatalf("rebuilt instance %+v", got)
	}
	if _, err := rebuilt.k.Origin(ctx, local); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("B's unpublished-elsewhere snapshot on the rebuilt instance: %v", err)
	}
}

func equalOrigins(a, b origins) bool {
	if a.Instance != b.Instance || len(a.Origins) != len(b.Origins) {
		return false
	}
	for k, v := range a.Origins {
		if b.Origins[k] != v {
			return false
		}
	}
	return true
}
