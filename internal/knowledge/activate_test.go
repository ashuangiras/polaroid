package knowledge_test

import (
	"context"
	"errors"
	"slices"
	"strings"
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

// Knowledge fetched from elsewhere obeys the same rule governance as a
// local publication (ADR-0050): a rule's semantics never change under the
// same revision, and a protected invariant is never unprotected.
func TestActivationEnforcesRuleGovernance(t *testing.T) {
	ctx := context.Background()
	m := knowledgetest.NewMemoryStore()
	k := knowledge.New(m, knowledge.WithRecordValidator(&acceptAll{}))
	s0, err := k.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{Changes: knowledgetest.CorpusProcedures(), Provenance: prov()}))
	if err != nil {
		t.Fatal(err)
	}
	s1, err := k.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{Base: s0, Records: knowledgetest.CorpusRecordChanges(), Provenance: prov()}))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := k.At(ctx, s1)
	if err != nil {
		t.Fatal(err)
	}
	stageRule := func(edit func(r *knowledge.Record)) knowledge.Snapshot {
		r, err := cat.Record("rule.software.tests")
		if err != nil {
			t.Fatal(err)
		}
		edit(r)
		doc, err := knowledge.EncodeRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		s, err := m.Stage(s1, knowledge.Commit{Changes: []knowledge.FileChange{{Path: knowledge.RecordPath(r.Type, r.Key), Content: doc}},
			Author: "alice", Subject: "from elsewhere", Body: "because"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for name, tc := range map[string]struct {
		edit func(r *knowledge.Record)
		want string
	}{
		"same revision": {func(r *knowledge.Record) { r.Statement = "Only some software needs tests." }, "its revision must rise above 1"},
		"unprotected":   {func(r *knowledge.Record) { r.Protected, r.Revision = false, 2 }, "cannot be unprotected"},
	} {
		err := k.Activate(ctx, knowledge.Activation{Base: s1, Target: stageRule(tc.edit), Provenance: decision()})
		if !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s, _ := m.Active(ctx); s != s1 {
		t.Fatalf("a refused activation moved the active snapshot to %s", s)
	}
	governed := stageRule(func(r *knowledge.Record) {
		r.Statement, r.Revision = "Every software procedure requires passing tests, always.", 2
	})
	if err := k.Activate(ctx, knowledge.Activation{Base: s1, Target: governed, Provenance: decision()}); err != nil {
		t.Fatalf("governed revision: %v", err)
	}
	// A rollback restores the earlier semantics under a new revision.
	restore, err := k.At(ctx, s1)
	if err != nil {
		t.Fatal(err)
	}
	rolled, err := k.Rollback(ctx, governed, s1, prov(), knowledgetest.ReviewCatalog(restore))
	if err != nil {
		t.Fatalf("rollback to the earlier revision: %v", err)
	}
	// But a rollback never lifts a protection: returning to a snapshot
	// from before the protected rule existed would remove it.
	beforeRule, err := k.At(ctx, s0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Rollback(ctx, rolled, s0, prov(), knowledgetest.ReviewCatalog(beforeRule)); !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "is a protected invariant and cannot be removed") {
		t.Fatalf("rollback removing a protected invariant: %v", err)
	}
}

func TestActivateIsAValidatedFastForward(t *testing.T) {
	ctx := context.Background()
	m := knowledgetest.NewMemoryStore()
	k := knowledge.New(m)
	s1, err := k.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{Changes: knowledgetest.Initial(), Provenance: prov()}))
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
	local, err := k.Publish(ctx, knowledgetest.WithFullReview(knowledge.Proposal{Base: improved, Changes: []knowledge.Change{put(knowledgetest.Proc("go.module.build", "Build it here"))}, Provenance: prov()}))
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
