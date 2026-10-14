package knowledge_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

// Activation checks only the transition from the active knowledge to its
// target, so an intermediate snapshot that arrived with it was never
// governed itself. A rollback to such imported knowledge therefore gets no
// exemption: it cannot restore a protected rule's ungoverned semantics
// under a lower revision (ADR-0050).
func TestRollbackToImportedKnowledgeIsGoverned(t *testing.T) {
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
	// An ungoverned intermediate step, then the original text at a higher
	// revision: the whole move from s1 is governed, so it activates.
	weakened := stage(s1, "Only some software needs tests.", 1)
	restored := stage(weakened, original.Statement, 2)
	if err := k.Activate(ctx, knowledge.Activation{Base: s1, Target: restored, Provenance: decision()}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Rollback(ctx, restored, weakened, prov()); !errors.Is(err, knowledge.ErrInvalid) ||
		!strings.Contains(err.Error(), "records[rule.software.tests].revision: is 1, below the published revision 2") {
		t.Fatalf("rollback to an ungoverned imported snapshot: %v", err)
	}
	// An activation target was checked as a whole, so undoing a later
	// activation that raised the revision restores it under the exemption.
	raised := stage(restored, "Every software procedure requires passing tests, without exception.", 3)
	if err := k.Activate(ctx, knowledge.Activation{Base: restored, Target: raised, Provenance: decision()}); err != nil {
		t.Fatal(err)
	}
	back, err := k.Rollback(ctx, raised, restored, prov())
	if err != nil {
		t.Fatalf("rollback to an earlier activation target: %v", err)
	}
	// Knowledge published here keeps its exemption.
	if _, err := k.Rollback(ctx, back, s1, prov()); err != nil {
		t.Fatalf("rollback to local knowledge: %v", err)
	}
}
