package memory

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

// fakeRuns is an in-memory VerificationReader. order is the recording order;
// refs["procedure@version"] lists a version's reference names.
type fakeRuns struct {
	runs  map[string]Execution
	order []string
	refs  map[string][]string
}

func newFakeRuns(refs map[string][]string) *fakeRuns {
	return &fakeRuns{runs: map[string]Execution{}, refs: refs}
}

func (f *fakeRuns) Run(_ context.Context, id string) (Execution, error) {
	e, ok := f.runs[id]
	if !ok {
		return Execution{}, ErrNotFound
	}
	return e, nil
}

func (f *fakeRuns) RunIDs(_ context.Context, flt VerificationFilter) ([]string, error) {
	var ids []string
	for _, id := range f.order {
		e := f.runs[id]
		if e.ProcedureID == flt.ProcedureID && e.Version == flt.Version &&
			(flt.Repository == "" || e.Repository == flt.Repository) &&
			(flt.Commit == "" || e.Commit == flt.Commit) &&
			(flt.Environment == "" || e.Environment.Name == flt.Environment) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (f *fakeRuns) ReferenceNames(_ context.Context, procedureID string, version int) ([]string, error) {
	return f.refs[fmt.Sprintf("%s@%d", procedureID, version)], nil
}

// add records an execution of procedure@version; edit adjusts its defaults.
func (f *fakeRuns) add(id, procedure string, version int, outcome Outcome, edit func(*Execution), children ...ChildExecution) {
	e := Execution{ID: id, ExecutionRecord: ExecutionRecord{
		ProcedureID: procedure,
		Version:     version,
		Repository:  "github.com/o/r",
		Commit:      "0123456789abcdef0123456789abcdef01234567",
		Environment: Environment{Name: "ci.linux", Attributes: jsontext.Value(`{"os":"linux"}`)},
		Inputs:      jsontext.Value(`{"module":"m"}`),
		Outcome:     outcome,
		Evidence:    jsontext.Value(`{"log":"ok"}`),
		Children:    children,
	}}
	if edit != nil {
		edit(&e)
	}
	f.runs[id] = e
	f.order = append(f.order, id)
}

// A leaf "c"; "b" references c; "a" references b and c (as "direct").
func coverageRuns() *fakeRuns {
	f := newFakeRuns(map[string][]string{"a@1": {"b", "direct"}, "b@1": {"c"}, "c@1": nil})
	f.add("c-ok", "c", 1, OutcomeSucceeded, nil)
	f.add("c-ok-2", "c", 1, OutcomeSucceeded, nil)
	f.add("c-failed", "c", 1, OutcomeFailed, nil)
	f.add("b-ok", "b", 1, OutcomeSucceeded, nil, ChildExecution{"c", "c-ok"})
	f.add("b-missing", "b", 1, OutcomeSucceeded, nil)
	f.add("b-bad-child", "b", 1, OutcomeSucceeded, nil, ChildExecution{"c", "c-failed"})
	f.add("a-ok", "a", 1, OutcomeSucceeded, nil, ChildExecution{"direct", "c-ok-2"}, ChildExecution{"b", "b-ok"})
	f.add("a-deep", "a", 1, OutcomeSucceeded, nil, ChildExecution{"b", "b-bad-child"})
	f.add("a-failed", "a", 1, OutcomeFailed, nil, ChildExecution{"b", "b-ok"})
	return f
}

func TestVerificationCoverageRule(t *testing.T) {
	f := coverageRuns()
	cases := []struct {
		id       string
		verified bool
		problems []VerificationProblem
	}{
		{"c-ok", true, nil},
		{"c-failed", false, []VerificationProblem{{Code: ProblemOutcomeFailed}}},
		{"b-ok", true, nil},
		{"b-missing", false, []VerificationProblem{{Code: ProblemMissingChild, Reference: "c"}}},
		{"b-bad-child", false, []VerificationProblem{{Code: ProblemChildNotVerified, Reference: "c", ExecutionID: "c-failed"}}},
		{"a-ok", true, nil},
		// Two levels down, only the direct problem is reported.
		{"a-deep", false, []VerificationProblem{
			{Code: ProblemChildNotVerified, Reference: "b", ExecutionID: "b-bad-child"},
			{Code: ProblemMissingChild, Reference: "direct"},
		}},
		{"a-failed", false, []VerificationProblem{{Code: ProblemOutcomeFailed}, {Code: ProblemMissingChild, Reference: "direct"}}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			v, err := VerifyExecution(context.Background(), f, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if v.Verified != tc.verified || !reflect.DeepEqual(v.Problems, tc.problems) {
				t.Fatalf("verified = %v, problems = %+v; want %v, %+v", v.Verified, v.Problems, tc.verified, tc.problems)
			}
		})
	}
	if _, err := VerifyExecution(context.Background(), f, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown execution: err = %v, want ErrNotFound", err)
	}
}

func TestCombinationChildTreeFollowsReferenceOrder(t *testing.T) {
	v, err := VerifyExecution(context.Background(), coverageRuns(), "a-ok")
	if err != nil {
		t.Fatal(err)
	}
	// Links were listed as direct, b; the tree follows the version: b, direct.
	want := []ChildVersion{
		{Reference: "b", Version: 1, Children: []ChildVersion{{Reference: "c", Version: 1}}},
		{Reference: "direct", Version: 1},
	}
	if !reflect.DeepEqual(v.Combination.Children, want) {
		t.Fatalf("children = %+v, want %+v", v.Combination.Children, want)
	}
	c := v.Combination
	if c.Repository != "github.com/o/r" || c.Environment != "ci.linux" || string(c.Inputs) != `{"module":"m"}` {
		t.Fatalf("combination = %+v", c)
	}
}

func TestCombinationKey(t *testing.T) {
	f := newFakeRuns(nil)
	with := func(inputs, attributes string) func(*Execution) {
		return func(e *Execution) {
			e.Inputs = jsontext.Value(inputs)
			e.Environment.Attributes = jsontext.Value(attributes)
		}
	}
	f.add("base", "p", 1, OutcomeSucceeded, with(`{"b":1,"a":[1.0,"é"]}`, `{"os":"linux"}`))
	f.add("reordered", "p", 1, OutcomeSucceeded, with(`{ "a" : [ 1, "\u00e9" ], "b" : 1e0 }`, `{"os":"darwin"}`))
	f.add("big", "p", 1, OutcomeSucceeded, with(`{"n":12345678901234567891}`, `{}`))
	f.add("big-neighbour", "p", 1, OutcomeSucceeded, with(`{"n":12345678901234567892}`, `{}`))
	f.add("array-reversed", "p", 1, OutcomeSucceeded, with(`{"b":1,"a":["é",1.0]}`, `{"os":"linux"}`))

	key := func(id string) string {
		t.Helper()
		v, err := VerifyExecution(context.Background(), f, id)
		if err != nil {
			t.Fatal(err)
		}
		return v.Combination.key()
	}
	if key("base") != key("reordered") {
		t.Fatal("member order, whitespace, number spelling and string escapes must not split a combination; neither may environment attributes")
	}
	if key("base") == key("array-reversed") {
		t.Fatal("array element order is significant: reordered arrays are different inputs")
	}
	if key("big") == key("big-neighbour") {
		t.Fatal("distinct integers above 2^53 must not be merged")
	}
	v, _ := VerifyExecution(context.Background(), f, "reordered")
	if got := string(v.Combination.Inputs); got != `{"a":[1,"é"],"b":1}` {
		t.Fatalf("canonical inputs = %s", got)
	}
}

func TestListCombinations(t *testing.T) {
	f := newFakeRuns(map[string][]string{"p@1": {"child"}, "q@1": nil, "q@2": nil})
	f.add("q1", "q", 1, OutcomeSucceeded, nil)
	f.add("q2", "q", 2, OutcomeSucceeded, nil)
	f.add("q1-again", "q", 1, OutcomeSucceeded, nil)
	f.add("q1-third", "q", 1, OutcomeSucceeded, nil)
	f.add("q1-fourth", "q", 1, OutcomeSucceeded, nil)
	f.add("p-ok", "p", 1, OutcomeSucceeded, nil, ChildExecution{"child", "q1"})
	f.add("p-v2", "p", 1, OutcomeSucceeded, nil, ChildExecution{"child", "q2"})
	f.add("p-regressed", "p", 1, OutcomeFailed, nil, ChildExecution{"child", "q1-again"})
	f.add("p-fixed", "p", 1, OutcomeSucceeded, nil, ChildExecution{"child", "q1-third"})
	f.add("p-elsewhere", "p", 1, OutcomeSucceeded, func(e *Execution) { e.Environment.Name = "ci.macos" }, ChildExecution{"child", "q1-fourth"})

	list := func(flt VerificationFilter) []CombinationStatus {
		t.Helper()
		flt.ProcedureID, flt.Version = "p", 1
		got, err := ListCombinations(context.Background(), f, flt)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	summary := func(statuses []CombinationStatus) []string {
		var out []string
		for _, s := range statuses {
			out = append(out, fmt.Sprintf("%s q@%d %v %v latest=%s", s.Combination.Environment, s.Combination.Children[0].Version, s.ExecutionIDs, s.Verified, s.LatestExecutionID))
		}
		return out
	}

	got := summary(list(VerificationFilter{}))
	want := []string{
		"ci.linux q@1 [p-ok p-regressed p-fixed] true latest=p-fixed",
		"ci.linux q@2 [p-v2] true latest=p-v2",
		"ci.macos q@1 [p-elsewhere] true latest=p-elsewhere",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("combinations:\n%q\nwant\n%q", got, want)
	}

	// Before the fix, the regression was the latest execution of its combination.
	f.order = slices.DeleteFunc(f.order, func(id string) bool { return id == "p-fixed" })
	if got := list(VerificationFilter{}); got[0].Verified || got[0].LatestExecutionID != "p-regressed" {
		t.Fatalf("a later failure must make the combination unverified: %+v", got[0])
	}

	if got := summary(list(VerificationFilter{Environment: "ci.macos"})); !slices.Equal(got, want[2:]) {
		t.Fatalf("environment filter = %q", got)
	}
	if got := list(VerificationFilter{Repository: "github.com/other/repo"}); len(got) != 0 {
		t.Fatalf("repository filter = %+v", got)
	}
}
