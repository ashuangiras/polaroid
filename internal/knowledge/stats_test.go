package knowledge_test

import (
	"context"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

func TestCatalogStatsEmpty(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore())
	cat, err := k.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st := cat.Stats("")
	for _, rt := range knowledge.RecordTypes {
		if n := st.ByType[string(rt)]; n != 0 {
			t.Errorf("ByType[%s] = %d, want 0", rt, n)
		}
	}
	if len(st.ByDomain) != 0 {
		t.Errorf("ByDomain = %v, want empty", st.ByDomain)
	}
	if st.Procedures.Total != 0 || len(st.Procedures.ByRepository) != 0 {
		t.Errorf("Procedures = %+v, want zero", st.Procedures)
	}
}

func TestCatalogStats(t *testing.T) {
	k := knowledge.New(knowledgetest.NewMemoryStore(), knowledge.WithRecordValidator(&acceptAll{}))

	shared := knowledgetest.SharedProc("go.verify", "Verify a Go change.")
	local := knowledgetest.SharedProc("team.deploy", "Deploy, the team way.")
	local.Applicability = knowledge.Applicability{Scope: knowledge.ScopeLocal, Repository: "github.com/team/app"}
	unspecified := knowledgetest.SharedProc("undeclared.task", "A task with no declared scope.")
	unspecified.Applicability = knowledge.Applicability{}

	domain := &knowledge.Record{Type: knowledge.TypeDomain, Key: "domain.software", Label: "Software"}
	concept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "check.build", Label: "Builds", ClassifiedAs: []string{"domain.software"}}
	unclassifiedConcept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "check.orphan", Label: "Orphan"}
	typedProc := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "go.verify", Label: "go.verify", ClassifiedAs: []string{"domain.software"}}

	s, err := k.Publish(context.Background(), knowledge.Proposal{
		Changes: []knowledge.Change{put(shared), put(local), put(unspecified)},
		Records: []knowledge.RecordChange{{Key: domain.Key, Record: domain}, {Key: concept.Key, Record: concept},
			{Key: unclassifiedConcept.Key, Record: unclassifiedConcept}, {Key: typedProc.Key, Record: typedProc}},
		Provenance: prov(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := k.At(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}

	st := cat.Stats("")
	wantByType := map[string]int{"Domain": 1, "Concept": 2, "Claim": 0, "Rule": 0, "Procedure": 1, "Evidence": 0, "Exception": 0}
	for rt, want := range wantByType {
		if got := st.ByType[rt]; got != want {
			t.Errorf("ByType[%s] = %d, want %d", rt, got, want)
		}
	}
	// typed Procedure records are a subset of legacy procedure documents:
	// one typed record (go.verify), three legacy documents.
	if st.ByType["Procedure"] != 1 || st.Procedures.Total != 3 {
		t.Fatalf("typed Procedure count %d must differ from legacy Procedures.Total %d", st.ByType["Procedure"], st.Procedures.Total)
	}
	wantByDomain := map[string]int{"domain.software": 2, "unclassified": 2}
	if len(st.ByDomain) != len(wantByDomain) {
		t.Fatalf("ByDomain = %v, want %v", st.ByDomain, wantByDomain)
	}
	for d, want := range wantByDomain {
		if got := st.ByDomain[d]; got != want {
			t.Errorf("ByDomain[%s] = %d, want %d", d, got, want)
		}
	}
	wantProcedures := map[string]int{"shared": 1, "unspecified": 1, "github.com/team/app": 1}
	if st.Procedures.Total != 3 || len(st.Procedures.ByRepository) != len(wantProcedures) {
		t.Fatalf("Procedures = %+v, want total 3 and %v", st.Procedures, wantProcedures)
	}
	for r, want := range wantProcedures {
		if got := st.Procedures.ByRepository[r]; got != want {
			t.Errorf("Procedures.ByRepository[%s] = %d, want %d", r, got, want)
		}
	}
	// Every typed record here lacks a definition (domain, concept,
	// unclassifiedConcept, typedProc); typedProc also has no obligation
	// source (no requires/preconditions/postconditions/specializes).
	if st.Completeness.MissingDefinition != 4 {
		t.Errorf("Completeness.MissingDefinition = %d, want 4", st.Completeness.MissingDefinition)
	}
	if st.Completeness.MissingObligation != 1 {
		t.Errorf("Completeness.MissingObligation = %d, want 1", st.Completeness.MissingObligation)
	}

	// Scoped to the local procedure's own repository: shared and
	// unspecified still apply there, the other repository does not.
	scoped := cat.Stats("github.com/team/app")
	if scoped.Procedures.Total != 3 {
		t.Fatalf("scoped Procedures.Total = %d, want 3 (shared + unspecified + local)", scoped.Procedures.Total)
	}
	scopedElsewhere := cat.Stats("github.com/other/app")
	if scopedElsewhere.Procedures.Total != 2 {
		t.Fatalf("scoped Procedures.Total elsewhere = %d, want 2 (shared + unspecified, not the other repository's local)", scopedElsewhere.Procedures.Total)
	}
	// ByType and ByDomain carry no repository applicability: unaffected by
	// the repository argument.
	if scopedElsewhere.ByType["Domain"] != st.ByType["Domain"] || len(scopedElsewhere.ByDomain) != len(st.ByDomain) {
		t.Fatalf("ByType/ByDomain must not be scoped by repository: got %v/%v, want %v/%v", scopedElsewhere.ByType, scopedElsewhere.ByDomain, st.ByType, st.ByDomain)
	}
}
