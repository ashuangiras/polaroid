// Package datalog evaluates Corpus reasoning programs with Mangle
// (codeberg.org/TauCeti/mangle-go), the only package that imports it
// (ADR-0050). Untrusted rule programs are restricted to positive Datalog:
// atoms, = and != over variables and string constants. Mangle parses,
// analyses and evaluates; this package adds the subset check, provenance,
// budgets and cancellation Mangle does not provide.
package datalog

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"

	"codeberg.org/TauCeti/mangle-go/analysis"
	"codeberg.org/TauCeti/mangle-go/ast"
	"codeberg.org/TauCeti/mangle-go/engine"
	"codeberg.org/TauCeti/mangle-go/factstore"
	"codeberg.org/TauCeti/mangle-go/parse"

	"github.com/ashuangiras/polaroid/internal/reasoning"
)

// Evaluator implements reasoning.Evaluator on Mangle.
type Evaluator struct{}

var _ reasoning.Evaluator = Evaluator{}

// RuleError says why a rule program is refused.
type RuleError struct {
	Rule   string
	Reason string
}

func (e *RuleError) Error() string { return fmt.Sprintf("rule %s: %s", e.Rule, e.Reason) }

// ErrBudget is reasoning.ErrBudget.
var ErrBudget = reasoning.ErrBudget

// defaultDecls is what the parser declares for a unit that declares
// nothing: the empty package.
var defaultDecls = func() []ast.Decl {
	u, err := parse.Unit(strings.NewReader(""))
	if err != nil {
		panic(err)
	}
	return u.Decls
}()

func parseUnit(name, text string) (parse.SourceUnit, error) {
	unit, err := parse.Unit(strings.NewReader(text))
	if err != nil {
		return parse.SourceUnit{}, &RuleError{Rule: name, Reason: "does not parse: " + firstLine(err.Error())}
	}
	return unit, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func sym(a ast.Atom) string { return fmt.Sprintf("%s/%d", a.Predicate.Symbol, a.Predicate.Arity) }

// checkTerm admits only variables and string constants.
func checkTerm(t ast.BaseTerm) error {
	switch v := t.(type) {
	case ast.Variable:
		return nil
	case ast.Constant:
		if v.Type == ast.StringType {
			return nil
		}
		return fmt.Errorf("constant %s is not a string", v)
	}
	return fmt.Errorf("%s is not a variable or a string constant (functions are not admitted)", t)
}

func checkAtom(a ast.Atom, reserved map[string]bool) error {
	if a.Predicate.IsBuiltin() {
		return fmt.Errorf("built-in predicate %s is not admitted", a.Predicate.Symbol)
	}
	if reserved[a.Predicate.Symbol] {
		return fmt.Errorf("predicate %s is reserved", a.Predicate.Symbol)
	}
	for _, arg := range a.Args {
		if err := checkTerm(arg); err != nil {
			return err
		}
	}
	return nil
}

// restrict checks a rule program against the safe subset and rewrites its
// heads to carry the rule's key.
func restrict(r reasoning.Rule, heads map[string]string) ([]ast.Clause, error) {
	unit, err := parseUnit(r.Key, r.Text)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(unit.Decls, defaultDecls) {
		return nil, &RuleError{Rule: r.Key, Reason: "declarations and packages are not admitted"}
	}
	if len(unit.Clauses) == 0 {
		return nil, &RuleError{Rule: r.Key, Reason: "defines no clause"}
	}
	reserved := map[string]bool{}
	for _, stored := range heads {
		reserved[stored] = true
	}
	var out []ast.Clause
	for i, c := range unit.Clauses {
		fail := func(format string, args ...any) error {
			return &RuleError{Rule: r.Key, Reason: fmt.Sprintf("clause %d: ", i+1) + fmt.Sprintf(format, args...)}
		}
		if c.Transform != nil {
			return nil, fail("transforms and aggregation are not admitted")
		}
		if c.HeadTime != nil {
			return nil, fail("temporal clauses are not admitted")
		}
		stored, ok := heads[sym(c.Head)]
		if !ok {
			return nil, fail("may only define %s, not %s", slices.Sorted(maps.Keys(heads)), sym(c.Head))
		}
		if len(c.Premises) == 0 {
			return nil, fail("facts are not admitted in rules; state them as records")
		}
		if err := checkAtom(c.Head, reserved); err != nil {
			return nil, fail("head: %v", err)
		}
		for _, p := range c.Premises {
			switch v := p.(type) {
			case ast.Atom:
				if err := checkAtom(v, reserved); err != nil {
					return nil, fail("%v", err)
				}
			case ast.Eq:
				if checkTerm(v.Left) != nil || checkTerm(v.Right) != nil {
					return nil, fail("= compares only variables and string constants")
				}
			case ast.Ineq:
				if checkTerm(v.Left) != nil || checkTerm(v.Right) != nil {
					return nil, fail("!= compares only variables and string constants")
				}
			case ast.NegAtom:
				return nil, fail("negation is not admitted: absence of knowledge is not evidence")
			default:
				return nil, fail("%T premises are not admitted", p)
			}
		}
		head := ast.NewAtom(stored, append(slices.Clone(c.Head.Args), ast.String(r.Key))...)
		out = append(out, ast.NewClause(head, c.Premises))
	}
	return out, nil
}

// Literals implements reasoning.Evaluator. A variable argument carries the
// string constants that `=` premises of its clause bind it to.
func (Evaluator) Literals(rules []reasoning.Rule, predicate string, arity int, head bool) (out []reasoning.Literal, err error) {
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	defer contain(&err) // runs first; recover must be called by the deferred function itself
	for _, r := range rules {
		unit, err := parseUnit(r.Key, r.Text)
		if err != nil {
			return nil, err
		}
		for _, c := range unit.Clauses {
			bound := map[ast.Variable][]string{}
			for _, p := range c.Premises {
				eq, ok := p.(ast.Eq)
				if !ok {
					continue
				}
				for _, pair := range [][2]ast.BaseTerm{{eq.Left, eq.Right}, {eq.Right, eq.Left}} {
					v, isVar := pair[0].(ast.Variable)
					k, isConst := pair[1].(ast.Constant)
					if isVar && isConst && k.Type == ast.StringType {
						s, _ := k.StringValue()
						bound[v] = append(bound[v], s)
					}
				}
			}
			atoms := []ast.Atom{c.Head}
			if !head {
				atoms = nil
				for _, p := range c.Premises {
					if a, ok := p.(ast.Atom); ok {
						atoms = append(atoms, a)
					}
				}
			}
			for _, a := range atoms {
				if a.Predicate.Symbol != predicate || a.Predicate.Arity != arity {
					continue
				}
				lit := reasoning.Literal{Rule: r.Key}
				for _, arg := range a.Args {
					switch t := arg.(type) {
					case ast.Constant:
						s, err := t.StringValue()
						lit.Args = append(lit.Args, reasoning.Term{Constant: s, IsConstant: err == nil})
					case ast.Variable:
						lit.Args = append(lit.Args, reasoning.Term{Bound: slices.Clone(bound[t])})
					default:
						lit.Args = append(lit.Args, reasoning.Term{})
					}
				}
				out = append(out, lit)
			}
		}
	}
	return out, nil
}

// store makes evaluation observe a context. Mangle v0.5.0 ignores the error
// of a premise lookup (engine.premiseAtom), so returning ctx.Err() alone
// would silently produce an incomplete fixed point. The store records the
// first error, answers nothing afterwards so evaluation ends quickly, and
// Evaluate discards the result.
type store struct {
	factstore.SimpleInMemoryStore
	ctx     context.Context
	stopped error
}

func (s *store) GetFacts(a ast.Atom, fn func(ast.Atom) error) error {
	if s.stopped == nil {
		s.stopped = s.ctx.Err()
	}
	if s.stopped != nil {
		return s.stopped
	}
	return s.SimpleInMemoryStore.GetFacts(a, fn)
}

func (s *store) Contains(a ast.Atom) bool {
	if s.stopped == nil {
		s.stopped = s.ctx.Err()
	}
	return s.stopped == nil && s.SimpleInMemoryStore.Contains(a)
}

// Evaluate implements reasoning.Evaluator. A panic inside Mangle (rule
// text is untrusted input) is returned as an error, never propagated.
func (Evaluator) Evaluate(ctx context.Context, p reasoning.Program) (res reasoning.Result, err error) {
	defer contain(&err)
	return evaluate(ctx, p)
}

// contain turns a panic in the Datalog engine into an error. Its value is
// internal: it is logged, and the error clients see is fixed.
func contain(err *error) {
	if r := recover(); r != nil {
		slog.Error("the Datalog engine panicked", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		*err = &RuleError{Rule: "program", Reason: "the Datalog engine failed on this program"}
	}
}

func evaluate(ctx context.Context, p reasoning.Program) (reasoning.Result, error) {
	l := p.Limits
	size := 0
	for _, r := range p.Rules {
		size += len(r.Text)
	}
	switch {
	case len(p.Rules) > l.Rules:
		return nil, fmt.Errorf("%w: %d rules exceed %d", ErrBudget, len(p.Rules), l.Rules)
	case size > l.ProgramBytes:
		return nil, fmt.Errorf("%w: %d bytes of rules exceed %d", ErrBudget, size, l.ProgramBytes)
	case len(p.Facts) > l.Facts:
		return nil, fmt.Errorf("%w: %d facts exceed %d", ErrBudget, len(p.Facts), l.Facts)
	}
	system, err := parse.Unit(strings.NewReader(p.System))
	if err != nil {
		return nil, fmt.Errorf("system program: %w", err)
	}
	unit := parse.SourceUnit{Clauses: system.Clauses}
	for name, arity := range p.EDB {
		unit.Decls = append(unit.Decls, ast.NewSyntheticDeclFromSym(ast.PredicateSym{Symbol: name, Arity: arity}))
	}
	for head, stored := range p.Heads {
		var arity int
		if _, err := fmt.Sscanf(head[strings.LastIndex(head, "/")+1:], "%d", &arity); err != nil {
			return nil, fmt.Errorf("head %q: %w", head, err)
		}
		unit.Decls = append(unit.Decls, ast.NewSyntheticDeclFromSym(ast.PredicateSym{Symbol: stored, Arity: arity + 1}))
	}
	slices.SortFunc(unit.Decls, func(a, b ast.Decl) int {
		return strings.Compare(a.DeclaredAtom.Predicate.Symbol, b.DeclaredAtom.Predicate.Symbol)
	})
	for _, r := range p.Rules {
		clauses, err := restrict(r, p.Heads)
		if err != nil {
			return nil, err
		}
		unit.Clauses = append(unit.Clauses, clauses...)
	}
	info, err := analysis.AnalyzeOneUnit(unit, nil)
	if err != nil {
		return nil, &RuleError{Rule: "program", Reason: "analysis: " + firstLine(err.Error())}
	}
	s := &store{SimpleInMemoryStore: factstore.NewSimpleInMemoryStore(), ctx: ctx}
	for _, f := range p.Facts {
		if arity, ok := p.EDB[f.Predicate]; !ok || arity != len(f.Args) {
			return nil, fmt.Errorf("fact %s/%d is not declared", f.Predicate, len(f.Args))
		}
		args := make([]ast.BaseTerm, len(f.Args))
		for i, a := range f.Args {
			args[i] = ast.String(a)
		}
		s.Add(ast.NewAtom(f.Predicate, args...))
	}
	if err := engine.EvalProgram(info, s, engine.WithCreatedFactLimit(l.Derived)); err != nil {
		if strings.Contains(err.Error(), "fact size limit") {
			return nil, fmt.Errorf("%w: %w", ErrBudget, err)
		}
		return nil, err
	}
	if s.stopped != nil {
		return nil, s.stopped
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := reasoning.Result{}
	for _, q := range p.Query {
		rows := [][]string{}
		for _, pred := range s.ListPredicates() {
			if pred.Symbol != q {
				continue
			}
			err := s.SimpleInMemoryStore.GetFacts(ast.NewQuery(pred), func(a ast.Atom) error {
				row := make([]string, len(a.Args))
				for i, arg := range a.Args {
					c, ok := arg.(ast.Constant)
					if !ok {
						return fmt.Errorf("non-constant in derived fact %s", a)
					}
					if row[i], err = c.StringValue(); err != nil {
						return fmt.Errorf("derived fact %s holds a non-string", a)
					}
				}
				rows = append(rows, row)
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
		slices.SortFunc(rows, func(a, b []string) int { return slices.Compare(a, b) })
		out[q] = rows
	}
	return out, nil
}
