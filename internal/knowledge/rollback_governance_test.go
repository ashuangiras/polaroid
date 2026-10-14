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
	// new revision.
	relabelled := unchanged.Clone()
	relabelled.Label = "Race detector, relabelled"
	s2, err := k.Publish(ctx, knowledge.Proposal{Base: s1, Records: []knowledge.RecordChange{{Key: vet2.Key, Record: vet2}, {Key: tests2.Key, Record: tests2},
		{Key: relabelled.Key, Record: relabelled}}, Provenance: prov()})
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
	if _, err := k.Rollback(ctx, s3, "", prov()); !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "to: is required") {
		t.Fatalf("rollback to nothing: %v", err)
	}
}

// A published rule is never removed or retyped, by a publication or a
// rollback, so its revision only rises: no history can bring back a
// (revision, digest) pair an approval was cancelled for (ADR-0050). A
// rule restored from outside the active history takes a new revision.
func TestPublishedRulesAreNeverRemoved(t *testing.T) {
	ctx := context.Background()
	m := knowledgetest.NewMemoryStore()
	k := knowledge.New(m, knowledge.WithRecordValidator(&acceptAll{}))
	s1, err := k.Publish(ctx, knowledge.Proposal{Changes: knowledgetest.CorpusProcedures(), Records: knowledgetest.CorpusRecordChanges(), Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := k.At(ctx, s1)
	vet, _ := cat.Record("rule.go.vet")
	retyped := &knowledge.Record{Type: knowledge.TypeConcept, Key: vet.Key, Label: "no longer a rule"}
	for name, change := range map[string]knowledge.RecordChange{"removed": {Key: vet.Key}, "retyped": {Key: vet.Key, Record: retyped}} {
		_, err := k.Publish(ctx, knowledge.Proposal{Base: s1, Records: []knowledge.RecordChange{change}, Provenance: prov()})
		if !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "is a published rule and cannot be removed or retyped") {
			t.Errorf("%s: %v", name, err)
		}
	}

	// An activation checks only its whole move, so an intermediate snapshot
	// it brought in can hold a rule the active knowledge lacks.
	extra := vet.Clone()
	extra.Key, extra.Revision = "rule.go.extra", 4
	doc, err := knowledge.EncodeRecord(extra)
	if err != nil {
		t.Fatal(err)
	}
	path := knowledge.RecordPath(extra.Type, extra.Key)
	with, err := m.Stage(s1, knowledge.Commit{Changes: []knowledge.FileChange{{Path: path, Content: doc}}, Author: "mallory", Subject: "add", Body: "because"})
	if err != nil {
		t.Fatal(err)
	}
	without, err := m.Stage(with, knowledge.Commit{Changes: []knowledge.FileChange{{Path: path, Delete: true}}, Author: "mallory", Subject: "remove", Body: "because"})
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Activate(ctx, knowledge.Activation{Base: s1, Target: without, Provenance: decision()}); err != nil {
		t.Fatal(err)
	}
	s2, err := k.Rollback(ctx, without, with, prov())
	if err != nil {
		t.Fatalf("rollback to the intermediate snapshot: %v", err)
	}
	cat, _ = k.At(ctx, s2)
	if got, err := cat.Record(extra.Key); err != nil || got.Revision != 5 || knowledge.RuleDigest(got) != knowledge.RuleDigest(extra) {
		t.Fatalf("restored %+v %v", got, err)
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
