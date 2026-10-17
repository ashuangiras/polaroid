package knowledge_test

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

func TestCompletenessGapsEmpty(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore())
	cat, err := k.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := cat.CompletenessGaps()
	if len(g.MissingDefinition) != 0 || len(g.MissingObligation) != 0 {
		t.Fatalf("gaps of an empty catalog = %+v, want none", g)
	}
	if len(cat.CheckCompleteness()) != 0 {
		t.Fatalf("CheckCompleteness of an empty catalog found problems")
	}
}

// manifestBytes is corpus.json's canonical bytes, built the same way
// encodeManifest (unexported) does, for a raw, bypass-Kernel.Publish
// snapshot (rawSnapshot): both CompletenessGaps/CheckCompleteness's own
// computation and the ADR-0057 activation (#164) need a catalog that
// does not meet the bar, which Kernel.Publish itself now refuses to
// produce.
func manifestBytes() []byte {
	b, _ := json.Marshal(struct {
		Format       int      `json:"format"`
		Capabilities []string `json:"capabilities"`
	}{knowledge.FormatRecords, knowledge.Capabilities})
	return append(b, '\n')
}

// rawSnapshot commits files directly to store, bypassing Kernel.Publish
// and every check it runs (including, since #164, completeness): exactly
// how a snapshot already on disk before that activation looks today, and
// the only way left to construct an incomplete catalog to test
// CompletenessGaps/CheckCompleteness's own computation against.
func rawSnapshot(t *testing.T, store *knowledgetest.MemoryStore, procs []*knowledge.Procedure, records []*knowledge.Record) knowledge.Snapshot {
	t.Helper()
	var files []knowledge.FileChange
	for _, p := range procs {
		doc, err := knowledge.Encode(p)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, knowledge.FileChange{Path: knowledge.Path(p.Key), Content: doc})
	}
	for _, r := range records {
		doc, err := knowledge.EncodeRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, knowledge.FileChange{Path: knowledge.RecordPath(r.Type, r.Key), Content: doc})
	}
	if len(records) > 0 {
		files = append(files, knowledge.FileChange{Path: knowledge.ManifestPath, Content: manifestBytes()})
	}
	s, err := store.Commit(context.Background(), knowledge.Commit{Changes: files, Author: "seed", Subject: "seed"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCompletenessGaps(t *testing.T) {
	store := knowledgetest.NewMemoryStore()

	// Complete: a Domain and a Concept with definitions, and a Procedure
	// with both a definition and its own Requires.
	domain := &knowledge.Record{Type: knowledge.TypeDomain, Key: "domain.complete", Label: "Complete", Definition: "A domain with a definition."}
	concept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "concept.complete", Label: "Complete", Definition: "A concept with a definition."}
	completeProc := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "proc.complete", Label: "Complete", Definition: "A complete procedure.", Requires: []string{"concept.complete"}}

	// Gaps: a Domain and a Concept with no definition.
	gapDomain := &knowledge.Record{Type: knowledge.TypeDomain, Key: "domain.gap", Label: "Gap"}
	gapConcept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "concept.gap", Label: "Gap"}

	// A Procedure with a definition but no obligation source of its own,
	// and no specializes parent: a gap.
	noObligation := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "proc.no-obligation", Label: "No obligation", Definition: "Has a definition, nothing else."}

	// A Procedure that inherits its obligation via specializes: not a gap,
	// even though it has no Requires/Preconditions/Postconditions itself.
	inherits := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "proc.inherits", Label: "Inherits", Definition: "Inherits its obligation.",
		Specializes: []string{"proc.complete"}}

	// A Procedure with a Postconditions-only obligation: not a gap.
	postOnly := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "proc.post-only", Label: "Post only", Definition: "Has a postcondition.",
		Postconditions: []string{"concept.complete"}}

	all := []*knowledge.Record{domain, concept, completeProc, gapDomain, gapConcept, noObligation, inherits, postOnly}
	// Every typed Procedure record needs a matching legacy procedure
	// document, sharing its key (checkRecords): minimal shared stubs.
	var procs []*knowledge.Procedure
	for _, key := range []string{"proc.complete", "proc.no-obligation", "proc.inherits", "proc.post-only"} {
		procs = append(procs, knowledgetest.SharedProc(key, key))
	}
	s := rawSnapshot(t, store, procs, all)
	k := knowledge.New(store, knowledge.WithRecordValidator(&acceptAll{}))
	cat, err := k.At(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}

	g := cat.CompletenessGaps()
	wantMissingDefinition := []string{"concept.gap", "domain.gap"}
	if !equalStrings(g.MissingDefinition, wantMissingDefinition) {
		t.Errorf("MissingDefinition = %v, want %v", g.MissingDefinition, wantMissingDefinition)
	}
	wantMissingObligation := []string{"proc.no-obligation"}
	if !equalStrings(g.MissingObligation, wantMissingObligation) {
		t.Errorf("MissingObligation = %v, want %v", g.MissingObligation, wantMissingObligation)
	}

	problems := cat.CheckCompleteness()
	if len(problems) != len(wantMissingDefinition)+len(wantMissingObligation) {
		t.Fatalf("CheckCompleteness = %+v, want %d problems", problems, len(wantMissingDefinition)+len(wantMissingObligation))
	}
}

// TestCompletenessIsEnforced is ADR-0057's acceptance criterion 2 (#164):
// the rule is activated, so a new publish missing a definition or an
// obligation is refused, naming every offending record and field, and
// nothing is stored.
func TestCompletenessIsEnforced(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(&acceptAll{}))
	incomplete := &knowledge.Record{Type: knowledge.TypeDomain, Key: "domain.undefined", Label: "Undefined"}
	s, err := k.Publish(context.Background(), knowledge.Proposal{
		Records: []knowledge.RecordChange{{Key: incomplete.Key, Record: incomplete}}, Provenance: prov(),
	})
	var ve *knowledge.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "records[domain.undefined].definition: is required for a Domain") {
		t.Fatalf("a record missing definition must now be refused, got snapshot %q err %v", s, err)
	}
	if active, _ := k.Active(context.Background()); active.Snapshot() != "" {
		t.Fatalf("a refused publish must not change the active snapshot, got %q", active.Snapshot())
	}

	noObligation := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "proc.undefined", Label: "p", Definition: "has a definition, no obligation"}
	_, err = k.Publish(context.Background(), knowledge.Proposal{
		Changes: []knowledge.Change{put(knowledgetest.SharedProc("proc.undefined", "p"))},
		Records: []knowledge.RecordChange{{Key: noObligation.Key, Record: noObligation}}, Provenance: prov(),
	})
	if !errors.As(err, &ve) || !strings.Contains(err.Error(),
		"records[proc.undefined].requires: at least one of requires, preconditions, postconditions, or an inherited one via specializes, is required for a Procedure") {
		t.Fatalf("a Procedure with no obligation source must now be refused, got %v", err)
	}
}

// TestCompletenessDoesNotAffectReadingHistory is the correctness property
// activation (#164) must not break: Kernel.At/Active read an
// already-committed snapshot through loadCatalog, the same shared
// checkCatalog/checkRecords an incoming publish candidate is checked
// with — but CheckCompleteness is wired only into Kernel.publish's own
// candidate, never into loadCatalog, exactly so that activating this
// rule cannot retroactively make history unreadable. Grounded in a real
// finding, not a hypothetical: the live corpus's own earliest typed
// records, seeded before #152/#158 existed, have no definition, and
// would stop loading if checkCatalog's shared scalars table were
// widened instead (the ADR's own sketch) rather than gating here.
func TestCompletenessDoesNotAffectReadingHistory(t *testing.T) {
	store := knowledgetest.NewMemoryStore()
	gap := &knowledge.Record{Type: knowledge.TypeDomain, Key: "domain.old", Label: "Old"} // no definition: pre-activation-looking
	old := rawSnapshot(t, store, nil, []*knowledge.Record{gap})

	k := knowledge.New(store, knowledge.WithRecordValidator(&acceptAll{}))
	cat, err := k.At(context.Background(), old)
	if err != nil {
		t.Fatalf("reading an already-committed, incomplete snapshot must still succeed after activation: %v", err)
	}
	if len(cat.CompletenessGaps().MissingDefinition) != 1 {
		t.Fatalf("the old gap must still be visible to CompletenessGaps, unaffected by activation")
	}

	// The whole catalog is re-validated on the next publish, however
	// small and unrelated (ADR-0057's own documented consequence): even
	// a new, unrelated, complete record is refused while domain.old's own
	// gap is still part of the resulting catalog.
	unrelated := &knowledge.Record{Type: knowledge.TypeDomain, Key: "domain.new", Label: "New", Definition: "d"}
	_, err = k.Publish(context.Background(), knowledge.Proposal{
		Base: old, Records: []knowledge.RecordChange{{Key: unrelated.Key, Record: unrelated}}, Provenance: prov(),
	})
	var ve *knowledge.ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "domain.old") {
		t.Fatalf("a small, unrelated publish on top of an incomplete base must still be refused for the pre-existing gap: %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
