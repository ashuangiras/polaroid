package knowledge_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

// A rollback is a new publication that restores earlier semantics under
// new revisions: never below the active revision, never with an
// exemption, never lifting a protection, and never changing history
// (ADR-0050).
func TestRollbackRestoresUnderNewRevisions(t *testing.T) {
	ctx := context.Background()
	m := knowledgetest.NewMemoryStore()
	k := knowledge.New(m, knowledge.WithRecordValidator(&acceptAll{}))
	s0, err := k.Publish(ctx, knowledge.Proposal{Changes: knowledgetest.CorpusProcedures(), Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	s1, err := k.Publish(ctx, knowledge.Proposal{Base: s0, Records: knowledgetest.CorpusRecordChanges(), Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	rule := func(s knowledge.Snapshot, key string) *knowledge.Record {
		t.Helper()
		c, err := k.At(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.Record(key)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	vet, tests := rule(s1, "rule.go.vet"), rule(s1, "rule.software.tests")
	vet2, tests2 := vet.Clone(), tests.Clone()
	vet2.Program, vet2.Revision = `obligation(P, "check.vet") :- record(P, "Procedure").`, 2
	tests2.Statement, tests2.Revision = "Every software procedure requires passing tests, always.", 2
	unchanged := rule(s1, "rule.go.race")
	// A label is not part of a rule's semantics, so it changes without a
	// new revision; an unprotected rule can be removed.
	relabelled := unchanged.Clone()
	relabelled.Label = "Race detector, relabelled"
	removed := rule(s1, "rule.audio.phase.method")
	s2, err := k.Publish(ctx, knowledge.Proposal{Base: s1, Records: []knowledge.RecordChange{{Key: vet2.Key, Record: vet2}, {Key: tests2.Key, Record: tests2},
		{Key: relabelled.Key, Record: relabelled}, {Key: removed.Key}}, Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}

	s3, err := k.Rollback(ctx, s2, s1, prov())
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	for _, want := range []*knowledge.Record{vet, tests} {
		got := rule(s3, want.Key)
		if knowledge.RuleDigest(got) != knowledge.RuleDigest(want) || got.Revision != 3 || got.Protected != want.Protected {
			t.Errorf("%s restored as revision %d, protected %v, semantics restored %v", want.Key, got.Revision, got.Protected,
				knowledge.RuleDigest(got) == knowledge.RuleDigest(want))
		}
	}
	if got := rule(s3, unchanged.Key); got.Revision != unchanged.Revision || got.Label != unchanged.Label {
		t.Errorf("a rule with unchanged semantics moved to revision %d (label %q)", got.Revision, got.Label)
	}
	// A rule the rollback adds back takes a revision above its restored one.
	if got := rule(s3, removed.Key); got.Revision != removed.Revision+1 || knowledge.RuleDigest(got) != knowledge.RuleDigest(removed) {
		t.Errorf("re-added rule at revision %d", got.Revision)
	}
	h, err := k.History(ctx, s3, 1)
	if err != nil || len(h) != 1 || h[0].RollbackTo != s1 {
		t.Fatalf("rollback provenance %+v, %v", h, err)
	}
	if got := rule(s1, vet.Key); got.Revision != 1 || got.Program != vet.Program {
		t.Fatal("the rollback changed the historical snapshot")
	}
	// A rollback never lifts a protection: returning to a snapshot from
	// before the protected rule existed would remove it.
	if _, err := k.Rollback(ctx, s3, s0, prov()); !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "is a protected invariant and cannot be removed") {
		t.Fatalf("rollback removing a protected invariant: %v", err)
	}
}

// Activation checks only the move from the active knowledge to its target,
// so an intermediate snapshot that arrived with it was never governed
// itself. Restoring it is an ordinary governed publication: its semantics
// come back under a revision above the active one.
func TestRollbackToAnIntermediateSnapshot(t *testing.T) {
	ctx := context.Background()
	m := knowledgetest.NewMemoryStore()
	k := knowledge.New(m, knowledge.WithRecordValidator(&acceptAll{}))
	s1, err := k.Publish(ctx, knowledge.Proposal{Changes: knowledgetest.CorpusProcedures(), Records: knowledgetest.CorpusRecordChanges(), Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := k.At(ctx, s1)
	if err != nil {
		t.Fatal(err)
	}
	stage := func(base knowledge.Snapshot, statement string, revision int) knowledge.Snapshot {
		r, err := cat.Record("rule.software.tests")
		if err != nil {
			t.Fatal(err)
		}
		r.Statement, r.Revision = statement, revision
		doc, err := knowledge.EncodeRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		s, err := m.Stage(base, knowledge.Commit{Changes: []knowledge.FileChange{{Path: knowledge.RecordPath(r.Type, r.Key), Content: doc}},
			Author: "mallory", Subject: "from elsewhere", Body: "because"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	original, _ := cat.Record("rule.software.tests")
	weakened := stage(s1, "Only some software needs tests.", 1)
	restored := stage(weakened, original.Statement, 2)
	if err := k.Activate(ctx, knowledge.Activation{Base: s1, Target: restored, Provenance: decision()}); err != nil {
		t.Fatal(err)
	}
	back, err := k.Rollback(ctx, restored, weakened, prov())
	if err != nil {
		t.Fatalf("rollback to an intermediate snapshot: %v", err)
	}
	c, err := k.At(ctx, back)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := c.Record("rule.software.tests"); r.Revision != 3 || r.Statement != "Only some software needs tests." || !r.Protected {
		t.Fatalf("restored %+v", r)
	}
}
