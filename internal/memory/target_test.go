package memory

import (
	"context"
	"encoding/json/jsontext"
	"slices"
	"strconv"
	"testing"
)

// ADR-0018: selection evidence (any commit) versus target verification
// (one exact commit and inputs). fakeRuns records at commitA by default.
const (
	commitA = "0123456789abcdef0123456789abcdef01234567"
	commitB = "89abcdef0123456789abcdef0123456789abcdef"
)

func mapped(r Reference, mapping string) Reference {
	r.Inputs = jsontext.Value(mapping)
	return r
}

func at(commit, inputs string) func(*Execution) {
	return func(e *Execution) {
		e.Commit = commit
		e.Inputs = jsontext.Value(inputs)
	}
}

// targetOf resolves id at target in c and returns each node, depth first, as
// "procedure@version verified=<bool> latest=<id>".
func targetOf(t *testing.T, w fakeWorld, id string, c ResolutionContext, commit, inputs string) []string {
	t.Helper()
	c.Target = &Target{Commit: commit, Inputs: jsontext.Value(inputs)}
	res := resolve(t, w, id, pinned(1), c)
	var out []string
	var walk func(n *GraphNode)
	walk = func(n *GraphNode) {
		if n.Target == nil {
			t.Fatalf("%s@%d has no target verification", n.ProcedureID, n.Version)
		}
		if n.Target.Combination.Commit != commit {
			t.Fatalf("%s@%d is judged at %s, want %s", n.ProcedureID, n.Version, n.Target.Combination.Commit, commit)
		}
		out = append(out, n.ProcedureID+"@"+strconv.Itoa(n.Version)+" verified="+strconv.FormatBool(n.Target.Verified)+" latest="+n.Target.LatestExecutionID)
		for i := range n.Edges {
			walk(&n.Edges[i].Node)
		}
	}
	walk(&res.Graph)
	return out
}

const (
	rootInputs = `{"module":"m","working_tree":"clean"}`
	leafInputs = `{"module":"m","strict":true}` // what the root's mapping gives the leaf
	oldInputs  = `{}`
)

// targetWorld: root@1 references leaf contextually, mapping module and a
// literal, and pins leaf@1 as "old". Everything succeeded once at commitA.
func targetWorld() fakeWorld {
	w := newFakeWorld(fakeGraph{
		"leaf": {1: nil, 2: nil},
		"root": {1: {mapped(latest("leaf", "leaf"), `{"module":{"input":"module"},"strict":{"value":true}}`), pin("old", "leaf", 1)}},
	})
	w.add("leaf2-a", "leaf", 2, OutcomeSucceeded, at(commitA, leafInputs))
	w.add("old1-a", "leaf", 1, OutcomeSucceeded, at(commitA, oldInputs))
	w.add("root-a", "root", 1, OutcomeSucceeded, at(commitA, rootInputs), ChildExecution{"leaf", "leaf2-a"}, ChildExecution{"old", "old1-a"})
	return w
}

func TestTargetVerificationIsCommitSpecific(t *testing.T) {
	w := targetWorld()

	// Exact match: the combination verified at A is verified for target A.
	got := targetOf(t, w, "root", linux, commitA, rootInputs)
	if want := []string{"root@1 verified=true latest=root-a", "leaf@2 verified=true latest=leaf2-a", "leaf@1 verified=true latest=old1-a"}; !slices.Equal(got, want) {
		t.Fatalf("at A:\n%q\nwant\n%q", got, want)
	}

	// Unseen commit: A's evidence still selects the same candidates for B,
	// and says where it ran, but nothing is verified at B.
	c := linux
	c.Target = &Target{Commit: commitB, Inputs: jsontext.Value(rootInputs)}
	res := resolve(t, w, "root", pinned(1), c)
	if ev := res.Graph.Evidence; res.Graph.VerifiedBy != "root-a" || ev == nil || *ev != (SelectionEvidence{"root-a", "github.com/o/r", commitA, "ci.linux"}) {
		t.Fatalf("selection evidence at B = %+v", ev)
	}
	if e := edge(res.Graph.Edges[0]); e != "leaf=2/evidence/leaf2-a" {
		t.Fatalf("selection at B = %s, want A's candidate", e)
	}
	if s := res.Graph.Target; s.Verified || len(s.ExecutionIDs) != 0 || s.LatestExecutionID != "" || s.Combination.Commit != commitB {
		t.Fatalf("root at B = %+v, want unverified with no execution", s)
	}
	if got := targetOf(t, w, "root", linux, commitB, rootInputs); !slices.Equal(got, []string{"root@1 verified=false latest=", "leaf@2 verified=false latest=", "leaf@1 verified=false latest="}) {
		t.Fatalf("at B before any run: %q", got)
	}

	// The target combination names the exact selection: effective inputs
	// through the mappings, and the child-version tree including the pin.
	want := Combination{Repository: "github.com/o/r", Commit: commitB, Environment: "ci.linux", Inputs: jsontext.Value(rootInputs),
		Children: []ChildVersion{{Reference: "leaf", Version: 2}, {Reference: "old", Version: 1}}}
	if got := res.Graph.Target.Combination; got.key() != want.key() {
		t.Fatalf("root target combination = %+v, want %+v", got, want)
	}
	if got := string(res.Graph.Edges[0].Node.Target.Combination.Inputs); got != leafInputs {
		t.Fatalf("leaf effective inputs = %s, want %s", got, leafInputs)
	}
	if got := string(res.Graph.Edges[1].Node.Target.Combination.Inputs); got != oldInputs {
		t.Fatalf("pinned leaf effective inputs = %s, want %s", got, oldInputs)
	}

	// New execution at B verifies B; A's executions are unchanged.
	w.add("leaf2-b", "leaf", 2, OutcomeSucceeded, at(commitB, leafInputs))
	w.add("old1-b", "leaf", 1, OutcomeSucceeded, at(commitB, oldInputs))
	if got := targetOf(t, w, "root", linux, commitB, rootInputs); got[0] != "root@1 verified=false latest=" || got[1] != "leaf@2 verified=true latest=leaf2-b" {
		t.Fatalf("children alone must not verify the parent at B: %q", got)
	}
	w.add("root-b", "root", 1, OutcomeSucceeded, at(commitB, rootInputs), ChildExecution{"leaf", "leaf2-b"}, ChildExecution{"old", "old1-b"})
	if got := targetOf(t, w, "root", linux, commitB, rootInputs); got[0] != "root@1 verified=true latest=root-b" {
		t.Fatalf("after recording at B: %q", got)
	}
	if v, err := VerifyExecution(context.Background(), w, "root-a"); err != nil || !v.Verified || v.Combination.Commit != commitA {
		t.Fatalf("A's execution changed: %+v, %v", v, err)
	}

	// No target: selection and its evidence, but no target verification.
	plain := resolve(t, w, "root", pinned(1), linux)
	if plain.Graph.Target != nil || plain.Graph.Edges[0].Node.Target != nil || plain.Graph.Evidence == nil {
		t.Fatalf("without a target: %+v", plain.Graph)
	}
	if none := resolve(t, w, "root", pinned(1), ResolutionContext{}); none.Graph.Evidence != nil || none.Graph.Target != nil {
		t.Fatalf("without a context: %+v", none.Graph)
	}
}

func TestTargetVerificationNeedsTheExactScope(t *testing.T) {
	w := targetWorld()
	verified := func(c ResolutionContext, commit, inputs string) bool {
		t.Helper()
		return targetOf(t, w, "root", c, commit, inputs)[0] == "root@1 verified=true latest=root-a"
	}
	if !verified(linux, commitA, `{ "working_tree" : "clean", "module" : "m" }`) {
		t.Fatal("reordered input members are the same inputs")
	}
	for name, tc := range map[string]struct {
		c      ResolutionContext
		inputs string
	}{
		"modified working tree": {linux, `{"module":"m","working_tree":"modified:3f2a"}`},
		"other inputs":          {linux, `{"module":"other","working_tree":"clean"}`},
		"other environment":     {ResolutionContext{Repository: "github.com/o/r", Environment: "ci.macos"}, rootInputs},
		"other repository":      {ResolutionContext{Repository: "github.com/o/other", Environment: "ci.linux"}, rootInputs},
	} {
		if verified(tc.c, commitA, tc.inputs) {
			t.Errorf("%s: verified by evidence for another scope", name)
		}
	}
}

func TestTargetVerificationFollowsTheSelectedCombination(t *testing.T) {
	w := newFakeWorld(fakeGraph{"leaf": {1: nil, 2: nil}, "root": {1: {latest("leaf", "leaf")}}})
	w.add("leaf1", "leaf", 1, OutcomeSucceeded, at(commitB, `{}`))
	w.add("root-leaf1", "root", 1, OutcomeSucceeded, at(commitB, `{}`), ChildExecution{"leaf", "leaf1"})
	w.add("leaf2", "leaf", 2, OutcomeSucceeded, at(commitB, `{}`))
	if got := targetOf(t, w, "root", linux, commitB, `{}`); got[0] != "root@1 verified=true latest=root-leaf1" || got[1] != "leaf@1 verified=true latest=leaf1" {
		t.Fatalf("the verified parent keeps leaf@1: %q", got)
	}

	// A failure elsewhere withdraws the parent's evidence, so leaf@2 is
	// selected: a combination never run as a whole.
	w.add("root-other", "root", 1, OutcomeFailed, at(commitA, `{}`))
	if got := targetOf(t, w, "root", linux, commitB, `{}`); got[0] != "root@1 verified=false latest=" || got[1] != "leaf@2 verified=true latest=leaf2" {
		t.Fatalf("a changed child combination needs its own parent evidence: %q", got)
	}
	w.add("leaf2-again", "leaf", 2, OutcomeSucceeded, at(commitB, `{}`))
	w.add("root-leaf2", "root", 1, OutcomeSucceeded, at(commitB, `{}`), ChildExecution{"leaf", "leaf2-again"})
	if got := targetOf(t, w, "root", linux, commitB, `{}`); got[0] != "root@1 verified=true latest=root-leaf2" {
		t.Fatalf("after the parent ran with leaf@2: %q", got)
	}

	// A later failure of that combination withdraws its target verification;
	// the earlier success stays verified as an execution.
	w.add("leaf2-third", "leaf", 2, OutcomeSucceeded, at(commitB, `{}`))
	w.add("root-regressed", "root", 1, OutcomeFailed, at(commitB, `{}`), ChildExecution{"leaf", "leaf2-third"})
	c := linux
	c.Target = &Target{Commit: commitB, Inputs: jsontext.Value(`{}`)}
	got := resolve(t, w, "root", pinned(1), c).Graph
	if s := got.Target; s.Verified || s.LatestExecutionID != "root-regressed" || !slices.Equal(s.ExecutionIDs, []string{"root-leaf2", "root-regressed"}) {
		t.Fatalf("after a later failure: %+v", s)
	}
	if v, _ := VerifyExecution(context.Background(), w, "root-leaf2"); !v.Verified {
		t.Fatal("the earlier success must stay verified")
	}
}

func TestMapInputs(t *testing.T) {
	got, err := mapInputs(jsontext.Value(`{"a":{"input":"x"},"b":{"value":null},"c":{"input":"absent"},"d":{"value":{"z":1}}}`), jsontext.Value(`{"x":[1,2],"y":3}`))
	if err != nil || string(got) != `{"a":[1,2],"b":null,"d":{"z":1}}` {
		t.Fatalf("mapInputs = %s, %v", got, err)
	}
}

func TestTargetIsChecked(t *testing.T) {
	for _, tc := range []struct {
		target Target
		fields []string
	}{
		{Target{Commit: commitA, Inputs: jsontext.Value(`{"a":1}`)}, nil},
		{Target{Commit: commitA}, []string{"inputs"}},
		{Target{Inputs: jsontext.Value(`{}`)}, []string{"commit"}},
		{Target{Commit: "0123456", Inputs: jsontext.Value(`[]`)}, []string{"commit", "inputs"}},
		{Target{Commit: commitA, Inputs: jsontext.Value(`{"a":1,"a":2}`)}, []string{"inputs"}},
	} {
		var p problems
		tc.target.check(&p)
		var got []string
		for _, fp := range p {
			got = append(got, fp.Field)
		}
		if !slices.Equal(got, tc.fields) {
			t.Errorf("%+v: fields %v, want %v", tc.target, got, tc.fields)
		}
	}
	var p problems
	ResolutionContext{Target: &Target{Commit: commitA, Inputs: jsontext.Value(`{}`)}}.check(&p, true)
	if len(p) != 2 || p[0].Field != "repository" || p[1].Field != "environment" {
		t.Fatalf("a target without a context: %+v", p)
	}
}
