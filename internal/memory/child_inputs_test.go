package memory

import (
	"context"
	"encoding/json/jsontext"
	"reflect"
	"testing"
)

// childInputsWorld: root@1 and root@2 reference leaf, mapping module and a
// literal; ship@1 references root, passing module and working_tree. At
// commitA, root1 (root@1) linked a leaf run with the mapped inputs; root2
// (root@2) is a stored, mismatched link written before ADR-0024, and ship
// links root2.
func childInputsWorld() fakeWorld {
	leafRef := mapped(latest("leaf", "leaf"), `{"module":{"input":"module"},"strict":{"value":true}}`)
	w := newFakeWorld(fakeGraph{
		"leaf": {1: nil},
		"root": {1: {leafRef}, 2: {leafRef}},
		"ship": {1: {mapped(latest("root", "root"), `{"module":{"input":"module"},"working_tree":{"input":"working_tree"}}`)}},
	})
	w.add("leaf-ok", "leaf", 1, OutcomeSucceeded, at(commitA, leafInputs))
	w.add("leaf-wrong", "leaf", 1, OutcomeSucceeded, at(commitA, `{"module":"m"}`))
	w.add("root1", "root", 1, OutcomeSucceeded, at(commitA, rootInputs), ChildExecution{"leaf", "leaf-ok"})
	w.add("root2", "root", 2, OutcomeSucceeded, at(commitA, rootInputs), ChildExecution{"leaf", "leaf-wrong"})
	w.add("ship", "ship", 1, OutcomeSucceeded, at(commitA, rootInputs), ChildExecution{"root", "root2"})
	return w
}

// Each derived read is checked on its own, so removing the check from any
// one of them fails here.
func TestMismatchedChildInputsNeverVerify(t *testing.T) {
	ctx := context.Background()
	w := childInputsWorld()

	if v, err := VerifyExecution(ctx, w, "root1"); err != nil || !v.Verified {
		t.Errorf("root1 links the mapped inputs: %+v, %v", v, err)
	}
	v, err := VerifyExecution(ctx, w, "root2")
	if want := []VerificationProblem{{Code: ProblemChildInputsMismatch, Reference: "leaf", ExecutionID: "leaf-wrong"}}; err != nil || v.Verified || !reflect.DeepEqual(v.Problems, want) {
		t.Errorf("root2 = %+v, %v; want problems %+v", v, err, want)
	}
	if got := w.runs["root2"]; string(got.Inputs) != rootInputs || got.Children[0].ExecutionID != "leaf-wrong" {
		t.Errorf("the stored record changed: %+v", got)
	}

	// Recursively: ship's own link is correct, but its child is unverified.
	g, err := VerifyExecution(ctx, w, "ship")
	if want := []VerificationProblem{{Code: ProblemChildNotVerified, Reference: "root", ExecutionID: "root2"}}; err != nil || g.Verified || !reflect.DeepEqual(g.Problems, want) {
		t.Errorf("ship = %+v, %v; want problems %+v", g, err, want)
	}

	list, err := ListCombinations(ctx, w, VerificationFilter{ProcedureID: "root", Version: 2})
	if err != nil || len(list) != 1 || list[0].Verified {
		t.Errorf("root@2 combinations = %+v, %v", list, err)
	}

	// Contextual selection skips root@2: root@1 is the highest verified.
	if r := resolve(t, w, "root", followLatest, linux); r.Graph.Version != 1 || r.Graph.VerifiedBy != "root1" {
		t.Errorf("contextual root = version %d verified by %q, want 1 by root1", r.Graph.Version, r.Graph.VerifiedBy)
	}

	c := linux
	c.Target = &Target{Commit: commitA, Inputs: jsontext.Value(rootInputs)}
	if s := resolve(t, w, "root", pinned(2), c).Graph.Target; s == nil || s.Verified || s.LatestExecutionID != "root2" {
		t.Errorf("target verification of root@2 = %+v", s)
	}
	// ship's run is no evidence (it is unverified), so its root resolves to
	// root@1, and ship@1 is unverified at the target.
	shipAt := resolve(t, w, "ship", pinned(1), c).Graph
	if s := shipAt.Target; s == nil || s.Verified || shipAt.Edges[0].Node.Version != 1 {
		t.Errorf("target verification of ship@1 = %+v, root version %d", s, shipAt.Edges[0].Node.Version)
	}
}

// The expected and recorded inputs compare canonically, as combinations do.
func TestChildInputsCompareCanonically(t *testing.T) {
	for name, tc := range map[string]struct {
		mapping, parent, child string
		match                  bool
	}{
		"member order":         {`{"b":{"value":2},"a":{"input":"a"}}`, `{"a":1}`, `{"a":1,"b":2}`, true},
		"1.0 is 1":             {`{"n":{"value":1.0}}`, `{}`, `{"n":1}`, true},
		"exponent":             {`{"n":{"value":1e2}}`, `{}`, `{"n":100}`, true},
		"distinct large ints":  {`{"n":{"value":9007199254740993}}`, `{}`, `{"n":9007199254740992}`, false},
		"equal large ints":     {`{"n":{"input":"n"}}`, `{"n":9007199254740993}`, `{"n":9007199254740993}`, true},
		"omitted member":       {`{"a":{"input":"a"}}`, `{}`, `{}`, true},
		"omitted, yet present": {`{"a":{"input":"a"}}`, `{}`, `{"a":null}`, false},
		"empty mapping":        {`{}`, `{"a":1}`, `{}`, true},
		"no subset":            {`{"a":{"input":"a"}}`, `{"a":1}`, `{"a":1,"b":2}`, false},
		"no override":          {`{"a":{"value":1}}`, `{"a":2}`, `{"a":2}`, false},
	} {
		w := newFakeWorld(fakeGraph{"leaf": {1: nil}, "root": {1: {mapped(latest("leaf", "leaf"), tc.mapping)}}})
		w.add("leaf", "leaf", 1, OutcomeSucceeded, at(commitA, tc.child))
		w.add("root", "root", 1, OutcomeSucceeded, at(commitA, tc.parent), ChildExecution{"leaf", "leaf"})
		v, err := VerifyExecution(context.Background(), w, "root")
		if err != nil || v.Verified != tc.match {
			t.Errorf("%s: verified = %v (%+v, %v), want %v", name, v.Verified, v.Problems, err, tc.match)
		}
	}
}
