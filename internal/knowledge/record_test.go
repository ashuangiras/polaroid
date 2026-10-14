package knowledge_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
)

// acceptAll is a record validator for kernel tests: the kernel's own checks
// are under test here; the semantic adapter has its own tests with the real
// libraries.
type acceptAll struct{ calls int }

func (a *acceptAll) ValidateRecords(context.Context, []knowledge.RecordDocument) error {
	a.calls++
	return nil
}

func corpusKernel(t *testing.T) (*knowledge.Kernel, *knowledgetest.MemoryStore, knowledge.Snapshot) {
	t.Helper()
	store := knowledgetest.NewMemoryStore()
	k := knowledge.New(store, knowledge.WithRecordValidator(&acceptAll{}))
	s, err := k.Publish(context.Background(), knowledge.Proposal{
		Changes: knowledgetest.CorpusProcedures(), Records: knowledgetest.CorpusRecordChanges(), Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	return k, store, s
}

func TestRecordsAreCanonical(t *testing.T) {
	r := &knowledge.Record{Type: knowledge.TypeConcept, Key: "a.b", Label: "A", Broader: []string{"z", "c", "c"}}
	doc, err := knowledge.EncodeRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"@context":"urn:polaroid:context:v1","id":"k:a.b","type":"Concept","label":"A","broader":["k:c","k:z"]}` + "\n"
	if string(doc) != want {
		t.Fatalf("encoding %s", doc)
	}
	if back, err := knowledge.DecodeRecord(doc); err != nil || back.Key != "a.b" || len(back.Broader) != 2 {
		t.Fatalf("decode %+v %v", back, err)
	}
	for name, bad := range map[string]string{
		"unsorted set":   strings.Replace(want, `["k:c","k:z"]`, `["k:z","k:c"]`, 1),
		"unknown member": strings.Replace(want, `"label"`, `"colour":"red","label"`, 1),
		"other context":  strings.Replace(want, "urn:polaroid:context:v1", "https://example.com/context", 1),
		"bare key":       strings.Replace(want, `"k:a.b"`, `"a.b"`, 1),
		"reordered":      `{"id":"k:a.b","@context":"urn:polaroid:context:v1","type":"Concept","label":"A"}` + "\n",
	} {
		if _, err := knowledge.DecodeRecord([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRecordCatalogChecks(t *testing.T) {
	k, _, s := corpusKernel(t)
	for name, tc := range map[string]struct {
		r    *knowledge.Record
		want string
	}{
		"missing target":     {&knowledge.Record{Type: knowledge.TypeConcept, Key: "x", Label: "x", Broader: []string{"nope"}}, "names nope, which is not a record"},
		"wrong target type":  {&knowledge.Record{Type: knowledge.TypeConcept, Key: "x", Label: "x", ClassifiedAs: []string{"check.build"}}, "must name one of [Domain]"},
		"inadmissible":       {&knowledge.Record{Type: knowledge.TypeConcept, Key: "x", Label: "x", Program: "p"}, "program: is not admitted for a Concept"},
		"required":           {&knowledge.Record{Type: knowledge.TypeRule, Key: "x", Label: "x", Statement: "s", Revision: 1}, "program: is required for a Rule"},
		"self":               {&knowledge.Record{Type: knowledge.TypeConcept, Key: "x", Label: "x", Broader: []string{"x"}}, "cannot relate to itself"},
		"procedure missing":  {&knowledge.Record{Type: knowledge.TypeProcedure, Key: "x", Label: "x"}, "x is not one"},
		"procedure key used": {&knowledge.Record{Type: knowledge.TypeConcept, Key: "go.verify", Label: "x"}, "only a Procedure record may share it"},
		"cycle": {&knowledge.Record{Type: knowledge.TypeProcedure, Key: "change.verify", Label: "x", Specializes: []string{"go.verify.concurrent"}},
			"specializes: cycle: change.verify -> go.verify.concurrent -> go.verify -> change.verify"},
		"two rules excepted": {&knowledge.Record{Type: knowledge.TypeException, Key: "x", Label: "x", Excepts: []string{"rule.go.vet", "rule.go.race"}}, "names exactly one rule"},
		"domain approval without a path": {&knowledge.Record{Type: knowledge.TypeException, Key: "x", Label: "x", Excepts: []string{"rule.go.vet"},
			Authority: &knowledge.Authority{Level: knowledge.AuthorityDomain, Domain: "domain.software"}}, "authority.path: must list the domains the approval covers"},
		"domain path ends elsewhere": {&knowledge.Record{Type: knowledge.TypeException, Key: "x", Label: "x", Excepts: []string{"rule.go.vet"},
			Authority: &knowledge.Authority{Level: knowledge.AuthorityDomain, Domain: "domain.software", Path: []string{"domain.software.go"}}},
			"authority.path: must list the domains the approval covers"},
		"domain path repeats": {&knowledge.Record{Type: knowledge.TypeException, Key: "x", Label: "x", Excepts: []string{"rule.go.vet"},
			Authority: &knowledge.Authority{Level: knowledge.AuthorityDomain, Domain: "domain.software", Path: []string{"domain.software", "domain.software.go", "domain.software"}}},
			"authority.path: must not repeat a domain"},
		"path for corpus authority": {&knowledge.Record{Type: knowledge.TypeException, Key: "x", Label: "x", Excepts: []string{"rule.go.vet"},
			Authority: &knowledge.Authority{Level: knowledge.AuthorityCorpus, Path: []string{"domain.software"}}}, "authority.path: is admitted only for domain authority"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := k.Publish(context.Background(), knowledge.Proposal{Base: s, Records: []knowledge.RecordChange{{Key: tc.r.Key, Record: tc.r}}, Provenance: prov()})
			if !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSpecializationApplicability(t *testing.T) {
	k, _, s := corpusKernel(t)
	local := knowledgetest.SharedProc("team.verify", "Verify, the team way.")
	local.Applicability = knowledge.Applicability{Scope: knowledge.ScopeLocal, Repository: "github.com/team/app"}
	child := knowledgetest.SharedProc("team.verify.strict", "Verify strictly.")
	ctx := context.Background()
	recs := []knowledge.RecordChange{
		{Key: "team.verify", Record: &knowledge.Record{Type: knowledge.TypeProcedure, Key: "team.verify", Label: "t"}},
		{Key: "team.verify.strict", Record: &knowledge.Record{Type: knowledge.TypeProcedure, Key: "team.verify.strict", Label: "t", Specializes: []string{"team.verify"}}},
	}
	_, err := k.Publish(ctx, knowledge.Proposal{Base: s, Changes: []knowledge.Change{put(local), put(child)}, Records: recs, Provenance: prov()})
	if !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "team.verify.strict applies where its parent team.verify does not") {
		t.Fatalf("shared child of a local parent: %v", err)
	}
	child.Applicability = local.Applicability
	if _, err := k.Publish(ctx, knowledge.Proposal{Base: s, Changes: []knowledge.Change{put(local), put(child)}, Records: recs, Provenance: prov()}); err != nil {
		t.Fatalf("local child of a local parent: %v", err)
	}
}

func TestManifestGuardsFormat(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		changes []knowledge.FileChange
		want    string
	}{
		"future format":      {[]knowledge.FileChange{{Path: knowledge.ManifestPath, Content: []byte(`{"format":3,"capabilities":[]}` + "\n")}}, "format 3 is not supported"},
		"unknown capability": {[]knowledge.FileChange{{Path: knowledge.ManifestPath, Content: []byte(`{"format":2,"capabilities":["records/1","proofs/1"]}` + "\n")}}, `capability "proofs/1" is not supported`},
		"no manifest":        {[]knowledge.FileChange{{Path: knowledge.ManifestPath, Delete: true}}, "corpus.json: is required"},
		"stray file":         {[]knowledge.FileChange{{Path: "notes/readme.md", Content: []byte("hi\n")}}, "is not a procedure document, a record document or the manifest"},
	} {
		t.Run(name, func(t *testing.T) {
			_, store, s := corpusKernel(t)
			bad, err := store.Commit(ctx, knowledge.Commit{Expected: s, Changes: tc.changes, Author: "raw", Subject: name, Body: "raw write"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = knowledge.New(store, knowledge.WithRecordValidator(&acceptAll{})).At(ctx, bad)
			if !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestFormatOneIsUnchanged(t *testing.T) {
	store := knowledgetest.NewMemoryStore()
	k := knowledge.New(store, knowledge.WithRecordValidator(&acceptAll{}))
	s, err := publish(t, k, "", knowledgetest.Initial()...)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := store.Files(context.Background(), s)
	for _, p := range files.Paths() {
		if !strings.HasPrefix(p, "procedures/") {
			t.Fatalf("format-1 publication wrote %s", p)
		}
	}
	cat, err := knowledge.New(store).At(context.Background(), s)
	if err != nil || cat.Format() != 1 {
		t.Fatalf("a kernel without a validator reads format 1: %v", err)
	}
}

func TestRecordHistoryAndRollback(t *testing.T) {
	k, _, s1 := corpusKernel(t)
	ctx := context.Background()
	cat, _ := k.At(ctx, s1)
	r, _ := cat.Record("check.race")
	r.Label = "The race detector reports nothing"
	s2, err := k.Publish(ctx, knowledge.Proposal{Base: s1, Records: []knowledge.RecordChange{{Key: r.Key, Record: r}, {Key: "check.review"}}, Provenance: prov()})
	if err != nil {
		t.Fatal(err)
	}
	h, err := k.History(ctx, s2, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := h[0].Changed
	if len(got) != 2 || got[0] != (knowledge.KeyChange{Key: "check.race", Action: "modified", Record: true}) ||
		got[1] != (knowledge.KeyChange{Key: "check.review", Action: "removed", Record: true}) {
		t.Fatalf("changed %+v", got)
	}
	s3, err := k.Rollback(ctx, s2, s1, prov())
	if err != nil {
		t.Fatal(err)
	}
	back, _ := k.At(ctx, s3)
	if r, err := back.Record("check.race"); err != nil || r.Label != "The race detector passes" {
		t.Fatalf("rollback: %+v %v", r, err)
	}
	if _, err := back.Record("check.review"); err != nil {
		t.Fatalf("rollback did not restore check.review: %v", err)
	}
}
