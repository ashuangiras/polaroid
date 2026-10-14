package datalog_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"codeberg.org/TauCeti/mangle-go/analysis"
	"codeberg.org/TauCeti/mangle-go/ast"
	"codeberg.org/TauCeti/mangle-go/engine"
	"codeberg.org/TauCeti/mangle-go/factstore"
	"codeberg.org/TauCeti/mangle-go/parse"

	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
)

var ev = datalog.Evaluator{}

func program(rules ...string) reasoning.Program {
	p := reasoning.Program{System: reasoning.System, Heads: reasoning.Heads, EDB: reasoning.EDB, Limits: reasoning.DefaultLimits,
		Query: []string{"obligation_of", "derived_by"},
		Facts: []reasoning.Fact{{Predicate: "record", Args: []string{"p", "Procedure"}}, {Predicate: "specializes", Args: []string{"q", "p"}}}}
	for i, r := range rules {
		p.Rules = append(p.Rules, reasoning.Rule{Key: fmt.Sprintf("rule.%d", i), Text: r})
	}
	return p
}

func TestProvenance(t *testing.T) {
	res, err := ev.Evaluate(context.Background(), program(`obligation(P, "r") :- record(P, "Procedure").`))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"p", "r", "rule.0", "p"}, {"q", "r", "rule.0", "p"}}
	if fmt.Sprint(res["obligation_of"]) != fmt.Sprint(want) {
		t.Fatalf("obligation_of %v", res["obligation_of"])
	}
}

func TestSafeSubset(t *testing.T) {
	for name, tc := range map[string]struct{ rule, want string }{
		"negation":    {`obligation(P, "r") :- record(P, "Procedure"), !specializes(P, "x").`, "negation is not admitted"},
		"aggregation": {`obligation(P, N) :- record(P, T) |> do fn:group_by(P), let N = fn:count().`, "transforms and aggregation"},
		"function":    {`obligation(P, R) :- record(P, T), R = fn:list(T).`, "only variables and string constants"},
		"other head":  {`helper(P) :- record(P, "Procedure").`, "may only define [derived/3 obligation/2], not helper/1"},
		"reserved":    {`obligation(P, R) :- own_obligation(P, R, S).`, "predicate own_obligation is reserved"},
		"builtin":     {`obligation(P, "r") :- record(P, T), :lt(1, 2).`, "built-in predicate"},
		"number":      {`obligation(P, "r") :- record(P, 3).`, "is not a string"},
		"fact":        {`obligation("p", "r").`, "facts are not admitted"},
		"declaration": {`Decl helper(X). obligation(P, "r") :- record(P, "Procedure").`, "declarations and packages"},
		"package":     {`Package x! obligation(P, "r") :- record(P, "Procedure").`, "declarations and packages"},
		"unbound":     {`obligation(P, R) :- record(P, "Procedure").`, "analysis"},
		"unknown":     {`obligation(P, "r") :- nothing(P).`, "analysis"},
		"syntax":      {`obligation(P, "r") :- record(P`, "does not parse"},
		"temporal":    {`obligation(P, "r")@[2024-01-01, 2025-01-01] :- record(P, "Procedure").`, "temporal"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ev.Evaluate(context.Background(), program(tc.rule))
			var re *datalog.RuleError
			if !errors.As(err, &re) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBudgets(t *testing.T) {
	p := program(`obligation(P, R) :- record(P, "Procedure"), record(R, "Procedure").`)
	for i := range 200 {
		p.Facts = append(p.Facts, reasoning.Fact{Predicate: "record", Args: []string{fmt.Sprint("x", i), "Procedure"}})
	}
	p.Limits.Derived = 1000
	if _, err := ev.Evaluate(context.Background(), p); !errors.Is(err, datalog.ErrBudget) {
		t.Fatalf("derived: %v", err)
	}
	for name, l := range map[string]reasoning.Limits{
		"rules": {Rules: 0, ProgramBytes: 1 << 20, Facts: 1 << 20, Derived: 1 << 20},
		"bytes": {Rules: 10, ProgramBytes: 10, Facts: 1 << 20, Derived: 1 << 20},
		"facts": {Rules: 10, ProgramBytes: 1 << 20, Facts: 1, Derived: 1 << 20},
	} {
		p := program(`obligation(P, "r") :- record(P, "Procedure").`)
		p.Limits = l
		if _, err := ev.Evaluate(context.Background(), p); !errors.Is(err, datalog.ErrBudget) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCancellationDiscardsResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ev.Evaluate(ctx, program(`obligation(P, "r") :- record(P, "Procedure").`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// Mangle v0.5.0 drops fact-store lookup errors (engine.premiseAtom); the
// adapter compensates. If a later version propagates them, this fails and
// the compensation can be revisited.
func TestMangleStillIgnoresLookupErrors(t *testing.T) {
	unit, err := parse.Unit(strings.NewReader(`e("a", "b"). p(X) :- e(X, Y).`))
	if err != nil {
		t.Fatal(err)
	}
	info, err := analysis.AnalyzeOneUnit(unit, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := failing{factstore.NewSimpleInMemoryStore()}
	if err := engine.EvalProgram(info, s); err != nil {
		t.Fatalf("Mangle now propagates lookup errors (%v); revisit the sticky store", err)
	}
}

type failing struct{ factstore.SimpleInMemoryStore }

func (failing) GetFacts(ast.Atom, func(ast.Atom) error) error { return errors.New("lookup failed") }

func TestLiterals(t *testing.T) {
	rules := []reasoning.Rule{{Key: "r", Text: `obligation(P, "x") :- record(P, T), context("ci", "true"), context("os", V), context("mode", M), M = "ci", context(N, "y").`}}
	got, err := ev.Literals(rules, "context", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{r [{ci true []} {true true []}]} {r [{os true []} { false []}]} {r [{mode true []} { false [ci]}]} {r [{ false []} {y true []}]}]`
	if fmt.Sprint(got) != want {
		t.Fatalf("body literals\n got %v\nwant %s", got, want)
	}
	heads, err := ev.Literals(rules, "obligation", 2, true)
	if err != nil || fmt.Sprint(heads) != `[{r [{ false []} {x true []}]}]` {
		t.Fatalf("head literals %v %v", heads, err)
	}
}
