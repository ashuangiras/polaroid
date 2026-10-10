package memory

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// ADR-0029: conditional references and recorded applicability decisions.

func conditional(r Reference, condition string) Reference {
	r.Condition = &condition
	return r
}

func decide(reference string, applicable bool, rationale string) ApplicabilityDecision {
	return ApplicabilityDecision{Reference: reference, Applicable: &applicable, Rationale: rationale}
}

func withDecisions(ds ...ApplicabilityDecision) func(*Execution) {
	return func(e *Execution) { e.Decisions = ds }
}

// changeWorld: verify@1 requires "checks" and runs "integration" only when
// the change can affect what the integration suite observes.
func changeWorld() fakeWorld {
	return newFakeWorld(fakeGraph{
		"checks":      {1: nil},
		"integration": {1: nil, 2: nil},
		"verify": {1: {
			pin("checks", "checks", 1),
			conditional(latest("integration", "integration"), "The change can affect behaviour the integration suite observes."),
		}},
	})
}

func problemCodes(v Verification) []string {
	var out []string
	for _, p := range v.Problems {
		out = append(out, string(p.Code)+":"+p.Reference)
	}
	return out
}

func mustVerify(t *testing.T, r VerificationReader, id string) Verification {
	t.Helper()
	v, err := VerifyExecution(context.Background(), r, id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestConditionalReferenceVerification(t *testing.T) {
	w := changeWorld()
	w.add("checks-1", "checks", 1, OutcomeSucceeded, nil)
	w.add("integration-1", "integration", 1, OutcomeSucceeded, nil)
	w.add("code", "verify", 1, OutcomeSucceeded, withDecisions(decide("integration", true, "internal/ changed")),
		ChildExecution{"checks", "checks-1"}, ChildExecution{"integration", "integration-1"})
	w.add("checks-2", "checks", 1, OutcomeSucceeded, nil)
	w.add("docs", "verify", 1, OutcomeSucceeded, withDecisions(decide("integration", false, "only docs/guide.md changed")),
		ChildExecution{"checks", "checks-2"})

	code, docs := mustVerify(t, w, "code"), mustVerify(t, w, "docs")
	if !code.Verified || !reflect.DeepEqual(code.Combination.Children, []ChildVersion{{Reference: "checks", Version: 1}, {Reference: "integration", Version: 1}}) {
		t.Fatalf("applicable and executed: %+v", code)
	}
	if !docs.Verified || !slices.EqualFunc(docs.Combination.Children, []ChildVersion{{Reference: "checks", Version: 1}, {Reference: "integration", Skipped: true}},
		func(a, b ChildVersion) bool {
			return a.Reference == b.Reference && a.Version == b.Version && a.Skipped == b.Skipped
		}) {
		t.Fatalf("not applicable and skipped: %+v", docs)
	}
	if code.Combination.key() == docs.Combination.key() {
		t.Fatal("an executed and a skipped reference must be different combinations")
	}

	for name, tc := range map[string]struct {
		edit     func(*Execution)
		children []ChildExecution
		want     []string
	}{
		"missing decision, no child": {nil, []ChildExecution{{"checks", "checks-3"}}, []string{"missing_decision:integration"}},
		"missing decision, with child": {nil, []ChildExecution{{"checks", "checks-3"}, {"integration", "integration-3"}},
			[]string{"missing_decision:integration"}},
		"applicable without its child": {withDecisions(decide("integration", true, "code changed")), []ChildExecution{{"checks", "checks-3"}},
			[]string{"missing_child:integration"}},
		"applicable with an unverified child": {withDecisions(decide("integration", true, "code changed")),
			[]ChildExecution{{"checks", "checks-3"}, {"integration", "integration-failed"}}, []string{"child_not_verified:integration"}},
		"a skip never excuses a required reference": {withDecisions(decide("integration", false, "docs only")), nil,
			[]string{"missing_child:checks"}},
	} {
		t.Run(name, func(t *testing.T) {
			w.add("checks-3", "checks", 1, OutcomeSucceeded, nil)
			w.add("integration-3", "integration", 1, OutcomeSucceeded, nil)
			w.add("integration-failed", "integration", 1, OutcomeFailed, nil)
			w.add("run", "verify", 1, OutcomeSucceeded, tc.edit, tc.children...)
			v := mustVerify(t, w, "run")
			if v.Verified || !slices.Equal(problemCodes(v), tc.want) {
				t.Fatalf("problems %v, want %v (verified=%v)", problemCodes(v), tc.want, v.Verified)
			}
		})
	}

	// An executed conditional child must have run with the mapped inputs.
	w.add("integration-other", "integration", 1, OutcomeSucceeded, func(e *Execution) { e.Inputs = jsontext.Value(`{"module":"other"}`) })
	w.add("checks-4", "checks", 1, OutcomeSucceeded, nil)
	w.add("mismatch", "verify", 1, OutcomeSucceeded, withDecisions(decide("integration", true, "code changed")),
		ChildExecution{"checks", "checks-4"}, ChildExecution{"integration", "integration-other"})
	if v := mustVerify(t, w, "mismatch"); v.Verified || !slices.Equal(problemCodes(v), []string{"child_inputs_mismatch:integration"}) {
		t.Fatalf("mapped inputs of an executed conditional child: %v", problemCodes(v))
	}
}

func TestSkipRationaleIsNotPartOfTheCombination(t *testing.T) {
	w := changeWorld()
	for i, rationale := range []string{"only README.md changed", "Documentation only: README.md."} {
		id := []string{"a", "b"}[i]
		w.add("checks-"+id, "checks", 1, OutcomeSucceeded, nil)
		w.add("docs-"+id, "verify", 1, OutcomeSucceeded, withDecisions(decide("integration", false, rationale)), ChildExecution{"checks", "checks-" + id})
	}
	statuses, err := ListCombinations(context.Background(), w, VerificationFilter{ProcedureID: "verify", Version: 1})
	if err != nil || len(statuses) != 1 || !slices.Equal(statuses[0].ExecutionIDs, []string{"docs-a", "docs-b"}) || !statuses[0].Verified {
		t.Fatalf("two skips with different wording = %+v, %v; want one verified combination", statuses, err)
	}
}

func TestNestedConditionalReferenceVerifiesRecursively(t *testing.T) {
	w := newFakeWorld(fakeGraph{
		"leaf": {1: nil},
		"mid":  {1: {conditional(pin("extra", "leaf", 1), "Only for schema changes.")}},
		"root": {1: {pin("mid", "mid", 1)}},
	})
	w.add("mid-skipped", "mid", 1, OutcomeSucceeded, withDecisions(decide("extra", false, "no schema change")))
	w.add("root-1", "root", 1, OutcomeSucceeded, nil, ChildExecution{"mid", "mid-skipped"})
	v := mustVerify(t, w, "root-1")
	if !v.Verified || len(v.Combination.Children) != 1 || !v.Combination.Children[0].Children[0].Skipped {
		t.Fatalf("a nested skip: %+v", v)
	}
	w.add("mid-undecided", "mid", 1, OutcomeSucceeded, nil)
	w.add("root-2", "root", 1, OutcomeSucceeded, nil, ChildExecution{"mid", "mid-undecided"})
	if v := mustVerify(t, w, "root-2"); v.Verified || !slices.Equal(problemCodes(v), []string{"child_not_verified:mid"}) {
		t.Fatalf("an undecided nested reference: %v", problemCodes(v))
	}
}

func TestDecisionsAreChecked(t *testing.T) {
	fields := func(ds ...ApplicabilityDecision) []string {
		t.Helper()
		return executionFields(t, func(r *ExecutionRecord) { r.Decisions = ds })
	}
	if got := fields(decide("integration", false, "docs only")); got != nil {
		t.Fatalf("a valid decision: %v", got)
	}
	cases := map[string]struct {
		decisions []ApplicabilityDecision
		want      []string
	}{
		"duplicate":           {[]ApplicabilityDecision{decide("a", true, "r"), decide("a", false, "r")}, []string{"decisions[1].reference"}},
		"no applicable":       {[]ApplicabilityDecision{{Reference: "a", Rationale: "r"}}, []string{"decisions[0].applicable"}},
		"blank rationale":     {[]ApplicabilityDecision{decide("a", false, "  ")}, []string{"decisions[0].rationale"}},
		"bad reference":       {[]ApplicabilityDecision{decide("A B", true, "r")}, []string{"decisions[0].reference"}},
		"evidence not object": {[]ApplicabilityDecision{{Reference: "a", Applicable: decide("a", true, "r").Applicable, Rationale: "r", Evidence: jsontext.Value(`[1]`)}}, []string{"decisions[0].evidence"}},
	}
	for name, tc := range cases {
		if got := fields(tc.decisions...); !slices.Equal(got, tc.want) {
			t.Errorf("%s: fields %v, want %v", name, got, tc.want)
		}
	}

	v := Version{Number: 1, Definition: Definition{References: changeWorld().fakeGraph["verify"][1]}}
	for name, tc := range map[string]struct {
		record ExecutionRecord
		want   []string
	}{
		"unknown reference":             {ExecutionRecord{Decisions: []ApplicabilityDecision{decide("lint", false, "r")}}, []string{"decisions[0].reference"}},
		"required reference":            {ExecutionRecord{Decisions: []ApplicabilityDecision{decide("checks", false, "r")}}, []string{"decisions[0].reference"}},
		"applicable required reference": {ExecutionRecord{Decisions: []ApplicabilityDecision{decide("checks", true, "r")}}, []string{"decisions[0].reference"}},
		"not applicable with a child": {ExecutionRecord{Children: []ChildExecution{{"integration", "x"}}, Decisions: []ApplicabilityDecision{decide("integration", false, "r")}},
			[]string{"decisions[0].applicable"}},
		"applicable with a child":    {ExecutionRecord{Children: []ChildExecution{{"integration", "x"}}, Decisions: []ApplicabilityDecision{decide("integration", true, "r")}}, nil},
		"applicable without a child": {ExecutionRecord{Decisions: []ApplicabilityDecision{decide("integration", true, "r")}}, nil},
		"no decision":                {ExecutionRecord{}, nil},
	} {
		var p problems
		checkDecisionTargets(tc.record, v, &p)
		var got []string
		for _, fp := range p {
			got = append(got, fp.Field)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: fields %v, want %v", name, got, tc.want)
		}
	}
}

func TestBlankConditionIsRejected(t *testing.T) {
	for _, c := range []string{"", "  "} {
		_, err := NewProcedure{CanonicalKey: "p", Definition: definitionWith(conditional(validReference("x"), c))}.validate()
		if got := problemFields(t, err); !slices.Contains(got, "version.references[0].condition") {
			t.Errorf("condition %q: fields %v", c, got)
		}
	}
}

// targetDecisions resolves verify@1 at commitA with decisions and returns the
// root's target status.
func targetDecisions(t *testing.T, w fakeWorld, decisions string) (*CombinationStatus, GraphNode) {
	t.Helper()
	c := linux
	c.Target = &Target{Commit: commitA, Inputs: jsontext.Value(`{"module":"m"}`), Decisions: jsontext.Value(decisions)}
	var p problems
	c.Target.check(&p)
	if err := p.err(); err != nil {
		t.Fatal(err)
	}
	res := resolve(t, w, "verify", pinned(1), c)
	return res.Graph.Target, res.Graph
}

// Regression (#50): a documentation task skipped the integration check; a
// later task at the same commit that needs it stays unverified until it runs.
func TestHistoricalSkipNeverVerifiesATargetThatNeedsTheWork(t *testing.T) {
	w := changeWorld()
	w.add("checks-docs", "checks", 1, OutcomeSucceeded, nil)
	w.add("docs", "verify", 1, OutcomeSucceeded, withDecisions(decide("integration", false, "only docs/guide.md changed")), ChildExecution{"checks", "checks-docs"})

	if s, _ := targetDecisions(t, w, `{"integration":false}`); !s.Verified || s.LatestExecutionID != "docs" {
		t.Fatalf("the documentation target is verified by its own run: %+v", s)
	}
	s, root := targetDecisions(t, w, `{"integration":true}`)
	if s.Verified || len(s.ExecutionIDs) != 0 {
		t.Fatalf("a target that needs integration was verified by a skip: %+v", s)
	}
	if e := root.Edges[1]; e.Decision != DecisionApplicable || e.SkippedBy != "docs" || e.Reference.Condition == nil || e.Node.Target == nil {
		t.Fatalf("integration edge = %+v; want applicable, skipped by the evidence, condition shown, target status", e)
	}

	// Without a decision the target is undecided: nothing is inferred from
	// the historical skip.
	s, root = targetDecisions(t, w, `{}`)
	if s.Verified || !slices.Equal(s.Undecided, []string{"integration"}) || len(s.ExecutionIDs) != 0 || root.Edges[1].Decision != DecisionUndecided {
		t.Fatalf("an undecided target: %+v", s)
	}
	c := linux
	c.Target = &Target{Commit: commitA, Inputs: jsontext.Value(`{"module":"m"}`)}
	if res := resolve(t, w, "verify", pinned(1), c); res.Graph.Target.Verified || len(res.Graph.Target.Undecided) != 1 {
		t.Fatalf("a target without decisions: %+v", res.Graph.Target)
	}

	// Running the integration check and recording it verifies the code task.
	w.add("checks-code", "checks", 1, OutcomeSucceeded, nil)
	w.add("integration-code", "integration", 2, OutcomeSucceeded, nil)
	w.add("code", "verify", 1, OutcomeSucceeded, withDecisions(decide("integration", true, "internal/ changed")),
		ChildExecution{"checks", "checks-code"}, ChildExecution{"integration", "integration-code"})
	if s, _ := targetDecisions(t, w, `{"integration":true}`); !s.Verified || s.LatestExecutionID != "code" {
		t.Fatalf("after running integration: %+v", s)
	}
	if s, _ := targetDecisions(t, w, `{"integration":false}`); !s.Verified || s.LatestExecutionID != "docs" {
		t.Fatalf("the documentation combination is unchanged: %+v", s)
	}
}

func TestSkippedEvidenceDoesNotSelectTheChild(t *testing.T) {
	w := changeWorld()
	w.add("checks-1", "checks", 1, OutcomeSucceeded, nil)
	w.add("docs", "verify", 1, OutcomeSucceeded, withDecisions(decide("integration", false, "docs only")), ChildExecution{"checks", "checks-1"})

	// No integration evidence in the context: the latest version, unverified,
	// and the edge says the parent's evidence skipped it.
	g := resolve(t, w, "verify", pinned(1), linux).Graph
	if got := edge(g.Edges[1]); got != "integration=2/latest/" || g.Edges[1].SkippedBy != "docs" || g.Edges[1].Reference.Condition == nil {
		t.Fatalf("integration edge = %s skipped_by=%q", got, g.Edges[1].SkippedBy)
	}
	if got := edge(g.Edges[0]); got != "checks=1/pin/checks-1" || g.Edges[0].SkippedBy != "" {
		t.Fatalf("checks edge = %s", got)
	}

	// Its own verified run in the context selects it, and says so.
	w.add("integration-1", "integration", 1, OutcomeSucceeded, nil)
	g = resolve(t, w, "verify", pinned(1), linux).Graph
	if got := edge(g.Edges[1]); got != "integration=1/evidence/integration-1" || g.Edges[1].SkippedBy != "docs" {
		t.Fatalf("integration edge = %s", got)
	}

	// Without a context the graph shows every reference and its condition.
	full, err := ExpandGraph(context.Background(), w, "verify", 1)
	if err != nil || len(full.Edges) != 2 || full.Edges[1].Reference.Condition == nil || full.Edges[1].SkippedBy != "" {
		t.Fatalf("full graph = %+v, %v", full, err)
	}
}

func TestTargetDecisionsAreChecked(t *testing.T) {
	for _, tc := range []struct {
		target Target
		fields []string
	}{
		{Target{Commit: commitA, Inputs: jsontext.Value(`{}`), Decisions: jsontext.Value(`{"a":true,"a/b-c":false}`)}, nil},
		{Target{Decisions: jsontext.Value(`{"a":true}`)}, []string{"decisions"}},
		{Target{Commit: commitA, Inputs: jsontext.Value(`{}`), Decisions: jsontext.Value(`{"a":"yes"}`)}, []string{"decisions"}},
		{Target{Commit: commitA, Inputs: jsontext.Value(`{}`), Decisions: jsontext.Value(`[true]`)}, []string{"decisions"}},
		{Target{Commit: commitA, Inputs: jsontext.Value(`{}`), Decisions: jsontext.Value(`{"a//b":true}`)}, []string{"decisions"}},
		{Target{Commit: commitA, Inputs: jsontext.Value(`{}`), Decisions: jsontext.Value(`{"a":true,"a":false}`)}, []string{"decisions"}},
	} {
		var p problems
		tc.target.check(&p)
		var got []string
		for _, fp := range p {
			got = append(got, fp.Field)
		}
		if !slices.Equal(got, tc.fields) {
			t.Errorf("%s: fields %v, want %v", tc.target.Decisions, got, tc.fields)
		}
	}

	w := newFakeWorld(fakeGraph{
		"leaf": {1: nil},
		"mid":  {1: {conditional(pin("extra", "leaf", 1), "schema changes")}},
		"verify": {1: {
			pin("checks", "leaf", 1),
			conditional(pin("mid", "mid", 1), "service changes"),
		}},
	})
	for decisions, wantErr := range map[string]bool{
		`{"mid":true,"mid/extra":false}`: false,
		`{"mid":false}`:                  false,
		`{"mid":false,"mid/extra":true}`: true, // inside a skipped reference
		`{"checks":true}`:                true, // a required reference
		`{"lint":true}`:                  true, // no such reference
		`{"mid/extra":true,"mid":true}`:  false,
	} {
		c := linux
		c.Target = &Target{Commit: commitA, Inputs: jsontext.Value(`{"module":"m"}`), Decisions: jsontext.Value(decisions)}
		var p problems
		c.Target.check(&p)
		_, err := ResolveGraph(context.Background(), w, "verify", pinned(1), c)
		var invalid *ValidationError
		if got := errors.As(err, &invalid); got != wantErr {
			t.Errorf("%s: err %v, want a validation error: %v", decisions, err, wantErr)
		}
	}

	// A node with an undecided reference below it is undecided too; a
	// reference decided not applicable gets no target status below it.
	c := linux
	c.Target = &Target{Commit: commitA, Inputs: jsontext.Value(`{"module":"m"}`), Decisions: jsontext.Value(`{"mid":true}`)}
	var p problems
	c.Target.check(&p)
	g := resolve(t, w, "verify", pinned(1), c).Graph
	if !slices.Equal(g.Target.Undecided, []string{"mid/extra"}) || !slices.Equal(g.Edges[1].Node.Target.Undecided, []string{"mid/extra"}) || g.Edges[0].Node.Target.Undecided != nil {
		t.Fatalf("undecided below mid: root %v, mid %v", g.Target.Undecided, g.Edges[1].Node.Target.Undecided)
	}
	c.Target = &Target{Commit: commitA, Inputs: jsontext.Value(`{"module":"m"}`), Decisions: jsontext.Value(`{"mid":false}`)}
	c.Target.check(&p)
	g = resolve(t, w, "verify", pinned(1), c).Graph
	if g.Edges[1].Decision != DecisionNotApplicable || g.Edges[1].Node.Target != nil || g.Target.Undecided != nil ||
		!g.Target.Combination.Children[1].Skipped {
		t.Fatalf("mid not applicable: %+v", g.Edges[1])
	}
}
