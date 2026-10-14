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

// TestCompileRunsManyFactSets: Compile analyzes a program once; Run then
// evaluates it against any number of independent fact sets, each giving
// the same result Evaluate would for the same program and facts (#144).
func TestCompileRunsManyFactSets(t *testing.T) {
	p := program(`obligation(P, "r") :- record(P, "Procedure"), context("env", "ci").`)
	compiled, err := ev.Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		extra reasoning.Fact
		want  string
	}{
		{"matching context", reasoning.Fact{Predicate: "context", Args: []string{"env", "ci"}}, "[[p r rule.0 p] [q r rule.0 p]]"},
		{"non-matching context", reasoning.Fact{Predicate: "context", Args: []string{"env", "dev"}}, "[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := append([]reasoning.Fact{tc.extra}, p.Facts...)
			got, err := compiled.Run(context.Background(), facts, p.Query, p.Limits)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got["obligation_of"]) != tc.want {
				t.Fatalf("obligation_of = %v, want %s", got["obligation_of"], tc.want)
			}
			want, err := ev.Evaluate(context.Background(), reasoning.Program{System: p.System, Rules: p.Rules, Heads: p.Heads,
				EDB: p.EDB, Facts: facts, Query: p.Query, Limits: p.Limits})
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("Run and Evaluate disagree:\nRun      %v\nEvaluate %v", got, want)
			}
		})
	}
	// Compile checks Rules and ProgramBytes; Run checks Facts and Derived,
	// against the limits it is given, independently of what Compile saw.
	if _, err := compiled.Run(context.Background(), p.Facts, p.Query, reasoning.Limits{Rules: 10, ProgramBytes: 1 << 20, Facts: 0, Derived: 1 << 20}); !errors.Is(err, datalog.ErrBudget) {
		t.Fatalf("facts budget not enforced by Run: %v", err)
	}
	for name, l := range map[string]reasoning.Limits{
		"rules": {Rules: 0, ProgramBytes: 1 << 20, Facts: 1 << 20, Derived: 1 << 20},
		"bytes": {Rules: 10, ProgramBytes: 10, Facts: 1 << 20, Derived: 1 << 20},
	} {
		q := program(`obligation(P, "r") :- record(P, "Procedure").`)
		q.Limits = l
		if _, err := ev.Compile(q); !errors.Is(err, datalog.ErrBudget) {
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
