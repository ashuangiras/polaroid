package memory

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
)

// fakeWorld is an in-memory ResolutionReader over versions and executions.
type fakeWorld struct {
	fakeGraph
	*fakeRuns
}

func newFakeWorld(g fakeGraph) fakeWorld {
	return fakeWorld{fakeGraph: g, fakeRuns: newFakeRuns(nil)}
}

func (w fakeWorld) ReferenceMappings(_ context.Context, procedureID string, version int) ([]ReferenceMapping, error) {
	var refs []ReferenceMapping
	for _, r := range w.fakeGraph[procedureID][version] {
		refs = append(refs, ReferenceMapping{Name: r.Name, Inputs: r.Inputs, Conditional: r.Conditional()})
	}
	return refs, nil
}

func (w fakeWorld) LatestRuns(_ context.Context, procedureID string, c ResolutionContext) ([]VersionRun, error) {
	latest := map[int]string{}
	for _, id := range w.order {
		e := w.runs[id]
		if e.ProcedureID == procedureID && e.Repository == c.Repository && e.Environment.Name == c.Environment {
			latest[e.Version] = id
		}
	}
	var runs []VersionRun
	for v, id := range latest {
		runs = append(runs, VersionRun{Version: v, ExecutionID: id})
	}
	slices.SortFunc(runs, func(a, b VersionRun) int { return b.Version - a.Version })
	return runs, nil
}

// Identity registers nothing: the domain tests match identifiers exactly;
// identity matching is tested against SQLite.
func (fakeWorld) Identity(context.Context, string) (RepositoryIdentity, error) {
	return RepositoryIdentity{}, nil
}

var linux = ResolutionContext{Repository: "github.com/o/r", Environment: "ci.linux"}

func resolve(t *testing.T, w fakeWorld, id string, policy VersionPolicy, c ResolutionContext) Resolution {
	t.Helper()
	res, err := ResolveGraph(context.Background(), w, id, policy, c)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func pinned(n int) VersionPolicy { return VersionPolicy{Kind: PolicyPin, Pin: n} }

var followLatest = VersionPolicy{Kind: PolicyContextual}

// edge describes one edge as "name=version/selected_by/verified_by".
func edge(e GraphEdge) string {
	return e.Reference.Name + "=" + strconv.Itoa(e.Node.Version) + "/" + string(e.SelectedBy) + "/" + e.Node.VerifiedBy
}

func TestContextualReferenceSelectsHighestVerifiedVersion(t *testing.T) {
	w := newFakeWorld(fakeGraph{"leaf": {1: nil, 2: nil, 3: nil}, "mid": {1: {latest("leaf", "leaf")}}})
	w.add("l1", "leaf", 1, OutcomeSucceeded, nil)
	w.add("l2", "leaf", 2, OutcomeSucceeded, nil)
	w.add("l3", "leaf", 3, OutcomeFailed, nil)
	w.add("l3-mac", "leaf", 3, OutcomeSucceeded, func(e *Execution) { e.Environment.Name = "ci.macos" })
	w.add("l3-other", "leaf", 3, OutcomeSucceeded, func(e *Execution) { e.Repository = "github.com/o/other" })

	got := resolve(t, w, "mid", pinned(1), linux)
	if e := edge(got.Graph.Edges[0]); e != "leaf=2/evidence/l2" {
		t.Fatalf("edge = %s, want the highest version verified in this repository and environment", e)
	}
	if got.SelectedBy != SelectedByPin || got.Graph.VerifiedBy != "" {
		t.Fatalf("root = %+v", got)
	}
	if e := edge(resolve(t, w, "mid", pinned(1), ResolutionContext{}).Graph.Edges[0]); e != "leaf=3/latest/" {
		t.Fatalf("without a context: %s", e)
	}
	if e := edge(resolve(t, w, "mid", pinned(1), ResolutionContext{Repository: "github.com/o/r", Environment: "ci.macos"}).Graph.Edges[0]); e != "leaf=3/evidence/l3-mac" {
		t.Fatalf("in ci.macos: %s", e)
	}

	// The latest execution of a version decides: a newer failure withdraws it.
	w.add("l2-regressed", "leaf", 2, OutcomeFailed, nil)
	if e := edge(resolve(t, w, "mid", pinned(1), linux).Graph.Edges[0]); e != "leaf=1/evidence/l1" {
		t.Fatalf("after version 2 regressed: %s", e)
	}
	w.add("l1-regressed", "leaf", 1, OutcomeFailed, nil)
	if e := edge(resolve(t, w, "mid", pinned(1), linux).Graph.Edges[0]); e != "leaf=3/latest/" {
		t.Fatalf("with nothing verified: %s", e)
	}

	// A contextual root, as a binding resolves it.
	w.add("l1-fixed", "leaf", 1, OutcomeSucceeded, nil)
	if root := resolve(t, w, "leaf", followLatest, linux); root.SelectedBy != SelectedByEvidence || root.Graph.Version != 1 || root.Graph.VerifiedBy != "l1-fixed" {
		t.Fatalf("contextual root = %+v", root)
	}
	if root := resolve(t, w, "leaf", followLatest, ResolutionContext{Repository: "scratch", Environment: "ci.linux"}); root.SelectedBy != SelectedByLatest || root.Graph.Version != 3 {
		t.Fatalf("contextual root without evidence = %+v", root)
	}
}

func TestVerifiedParentFixesItsChildVersions(t *testing.T) {
	w := newFakeWorld(fakeGraph{
		"leaf": {1: nil, 2: nil},
		"mid":  {1: {latest("leaf", "leaf")}},
		"root": {1: {latest("mid", "mid"), pin("old", "leaf", 1)}},
	})
	w.add("leaf1-for-mid", "leaf", 1, OutcomeSucceeded, nil)
	w.add("mid1", "mid", 1, OutcomeSucceeded, nil, ChildExecution{"leaf", "leaf1-for-mid"})
	w.add("leaf1-for-root", "leaf", 1, OutcomeSucceeded, nil)
	w.add("root1", "root", 1, OutcomeSucceeded, nil, ChildExecution{"old", "leaf1-for-root"}, ChildExecution{"mid", "mid1"})
	// A higher leaf version is verified on its own, but never together with mid.
	w.add("leaf2", "leaf", 2, OutcomeSucceeded, nil)

	got := resolve(t, w, "root", pinned(1), linux)
	if got.Graph.VerifiedBy != "root1" {
		t.Fatalf("root evidence = %q", got.Graph.VerifiedBy)
	}
	mid := got.Graph.Edges[0]
	edges := []string{edge(mid), edge(mid.Node.Edges[0]), edge(got.Graph.Edges[1])}
	if want := []string{"mid=1/evidence/mid1", "leaf=1/evidence/leaf1-for-mid", "old=1/pin/leaf1-for-root"}; !slices.Equal(edges, want) {
		t.Fatalf("edges = %q, want %q", edges, want)
	}

	// mid on its own also follows its verified combination.
	if e := edge(resolve(t, w, "mid", pinned(1), linux).Graph.Edges[0]); e != "leaf=1/evidence/leaf1-for-mid" {
		t.Fatalf("mid alone: %s", e)
	}

	// Without the parent's evidence, each reference resolves on its own.
	w.add("mid1-failed", "mid", 1, OutcomeFailed, nil)
	if e := edge(resolve(t, w, "mid", pinned(1), linux).Graph.Edges[0]); e != "leaf=2/evidence/leaf2" {
		t.Fatalf("mid without evidence: %s", e)
	}
}

func TestPinnedReferencesCarryTheirOwnEvidence(t *testing.T) {
	w := newFakeWorld(fakeGraph{"leaf": {1: nil, 2: nil}, "root": {1: {pin("old", "leaf", 1), pin("new", "leaf", 2)}}})
	w.add("leaf1", "leaf", 1, OutcomeSucceeded, nil)
	w.add("leaf2", "leaf", 2, OutcomeFailed, nil)
	got := resolve(t, w, "root", pinned(1), linux)
	edges := []string{edge(got.Graph.Edges[0]), edge(got.Graph.Edges[1])}
	if want := []string{"old=1/pin/leaf1", "new=2/pin/"}; !slices.Equal(edges, want) {
		t.Fatalf("edges = %q, want %q", edges, want)
	}
}

func TestEvidenceCannotLeadIntoACycle(t *testing.T) {
	// b@1 used a; a@2 then started using b, which is acyclic only while b's
	// latest version (2) is selected.
	w := newFakeWorld(fakeGraph{
		"a": {1: nil, 2: {latest("b", "b")}},
		"b": {1: {latest("a", "a")}, 2: nil},
	})
	w.add("a1", "a", 1, OutcomeSucceeded, nil)
	w.add("b1", "b", 1, OutcomeSucceeded, nil, ChildExecution{"a", "a1"})

	if e := edge(resolve(t, w, "a", pinned(2), ResolutionContext{}).Graph.Edges[0]); e != "b=2/latest/" {
		t.Fatalf("without a context: %s", e)
	}
	_, err := ResolveGraph(context.Background(), w, "a", pinned(2), linux)
	var cycle *ReferenceCycleError
	if !errors.As(err, &cycle) || len(cycle.Cycle) != 3 || cycle.Cycle[1] != (CycleStep{ProcedureID: "b", Version: 1, Reference: "a"}) {
		t.Fatalf("err = %v, want the cycle a@2 -> b@1 -> a@1", err)
	}
}

func TestResolutionContextIsChecked(t *testing.T) {
	for _, tc := range []struct {
		c      ResolutionContext
		fields []string
	}{
		{ResolutionContext{}, nil},
		{linux, nil},
		{ResolutionContext{Repository: "Not/Canonical", Environment: "CI Linux"}, []string{"repository", "environment"}},
	} {
		var p problems
		tc.c.check(&p, true)
		var got []string
		for _, fp := range p {
			got = append(got, fp.Field)
		}
		if !slices.Equal(got, tc.fields) {
			t.Errorf("%+v: fields %v, want %v", tc.c, got, tc.fields)
		}
	}
}
