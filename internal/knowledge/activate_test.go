package knowledge_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

// stage stores procedures on top of base without activating them, as
// knowledge fetched from another instance arrives.
func stage(t *testing.T, m *knowledgetest.MemoryStore, base knowledge.Snapshot, procs ...*knowledge.Procedure) knowledge.Snapshot {
	t.Helper()
	var changes []knowledge.FileChange
	for _, p := range procs {
		changes = append(changes, knowledge.FileChange{Path: knowledge.Path(p.Key), Content: mustEncode(t, p)})
	}
	s, err := m.Stage(base, knowledge.Commit{Changes: changes, Author: "alice", Subject: "from elsewhere", Body: "because"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func decision() knowledge.Provenance {
	return knowledge.Provenance{Author: "operator", Summary: "accept team knowledge", Rationale: "reviewed", Validation: "fetched and validated"}
}

func TestActivateIsAValidatedFastForward(t *testing.T) {
	ctx := context.Background()
	m := knowledgetest.NewMemoryStore()
	k := knowledge.New(m)
	s1, err := k.Publish(ctx, knowledge.Proposal{Changes: knowledgetest.Initial(), Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	improved := stage(t, m, s1, knowledgetest.Proc("go.module.checks", "Run gofmt, go vet and staticcheck"))
	broken := stage(t, m, s1, knowledgetest.Proc("go.module.verify", "Verify", "b=go.module.gone"))
	if _, err := k.At(ctx, improved); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("a staged snapshot is readable before activation: %v", err)
	}
	activate := func(base, target knowledge.Snapshot, pv knowledge.Provenance) error {
		return k.Activate(ctx, knowledge.Activation{Base: base, Target: target, Provenance: pv, Origin: []knowledge.Trailer{{Key: "Polaroid-Remote", Value: "team /srv/team.git"}}})
	}
	if err := activate(s1, broken, decision()); !errors.Is(err, knowledge.ErrInvalid) {
		t.Fatalf("activating an invalid catalog: %v", err)
	}
	if err := activate(s1, improved, knowledge.Provenance{Author: "operator", Summary: "no rationale"}); !errors.Is(err, knowledge.ErrInvalid) {
		t.Fatalf("activation without rationale: %v", err)
	}
	if err := activate("mem-0", improved, decision()); !errors.Is(err, knowledge.ErrStale) {
		t.Fatalf("stale activation: %v", err)
	}
	if err := activate(s1, "mem-404", decision()); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatalf("unknown target: %v", err)
	}
	if s, _ := m.Active(ctx); s != s1 {
		t.Fatalf("refused activations moved the active snapshot to %s", s)
	}

	if err := activate(s1, improved, decision()); err != nil {
		t.Fatal(err)
	}
	c, err := k.Active(ctx)
	if err != nil || c.Snapshot() != improved {
		t.Fatalf("active = %v, %v", c, err)
	}
	p, _ := c.Get("go.module.checks")
	if p.Goal != "Run gofmt, go vet and staticcheck" {
		t.Fatalf("activated goal = %q", p.Goal)
	}
	h, err := k.History(ctx, improved, 5)
	if err != nil || len(h) != 2 || h[0].Provenance.Author != "alice" || h[0].Provenance.Rationale != "because" {
		t.Fatalf("history keeps the publisher's provenance: %+v, %v", h, err)
	}
	recs, err := k.Activations(ctx, 5)
	if err != nil || len(recs) != 1 {
		t.Fatalf("activations = %+v, %v", recs, err)
	}
	r := recs[0]
	if r.Target != improved || r.Previous != s1 || r.Provenance != decision() || r.Accepted == "" ||
		!slices.Equal(r.Origin, []knowledge.Trailer{{Key: "Polaroid-Remote", Value: "team /srv/team.git"}}) {
		t.Fatalf("activation record = %+v", r)
	}

	// A local publication since the fetch: activation would discard it.
	elsewhere := stage(t, m, improved, knowledgetest.Proc("go.module.build", "Build it elsewhere"))
	local, err := k.Publish(ctx, knowledge.Proposal{Base: improved, Changes: []knowledge.Change{put(knowledgetest.Proc("go.module.build", "Build it here"))}, Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	if err := activate(local, elsewhere, decision()); !errors.Is(err, knowledge.ErrDiverged) {
		t.Fatalf("diverged activation: %v", err)
	}
	if s, _ := m.Active(ctx); s != local {
		t.Fatal("a diverged activation lost the local publication")
	}
}
