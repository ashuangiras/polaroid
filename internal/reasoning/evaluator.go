package reasoning

import (
	"context"
	"errors"
)

// ErrBudget is returned when an evaluation exceeds its limits.
var ErrBudget = errors.New("reasoning budget exceeded")

// Fact is one ground atom; every argument is a string constant.
type Fact struct {
	Predicate string
	Args      []string
}

// Rule is an untrusted rule record's program.
type Rule struct {
	Key  string
	Text string
}

// Program is one bounded evaluation: a trusted system program (the
// reasoning version's built-in rules), untrusted rule programs, and the
// extensional facts. Heads maps each head a rule may define, "name/arity",
// to the predicate it is stored as, with the rule's key appended as a
// final argument: that is every derivation's provenance.
type Program struct {
	System string
	Rules  []Rule
	Heads  map[string]string
	// EDB declares each extensional predicate's arity, so a rule may read
	// a predicate that has no facts in this snapshot.
	EDB    map[string]int
	Facts  []Fact
	Query  []string // predicates to return
	Limits Limits
}

// Limits bound an evaluation. A stopped or exceeded evaluation is an error,
// never a partial result.
type Limits struct {
	Rules        int // rule programs
	ProgramBytes int // all rule text together
	Facts        int // extensional facts
	Derived      int // facts created by evaluation
}

// DefaultLimits suit catalogs of thousands of records.
var DefaultLimits = Limits{Rules: 1000, ProgramBytes: 1 << 20, Facts: 200_000, Derived: 500_000}

// Result holds the queried predicates' facts, each predicate's rows sorted.
type Result map[string][][]string

// Term is one argument of a rule literal: a string constant, or a variable
// with the constants an `=` premise of the same clause binds it to (none:
// the variable is open).
type Term struct {
	Constant   string
	IsConstant bool
	Bound      []string
}

// Literal is one occurrence of a predicate in a rule.
type Literal struct {
	Rule string
	Args []Term
}

// Compiled is one program's System, Rules, Heads and EDB, parsed and
// analyzed once by Compile. Run evaluates it to a fixed point against
// different extensional facts without repeating that analysis, so a
// caller that evaluates the same program many times (resolve's missing
// context hypotheses, #144) pays parsing and analysis once, not per
// evaluation.
type Compiled interface {
	Run(ctx context.Context, facts []Fact, query []string, limits Limits) (Result, error)
}

// Evaluator evaluates programs to a fixed point. internal/reasoning/datalog
// implements it on Mangle.
type Evaluator interface {
	Evaluate(ctx context.Context, p Program) (Result, error)
	// Literals lists the occurrences of predicate/arity in the rules'
	// bodies, or in their heads (before provenance is added) when head is
	// true.
	Literals(rules []Rule, predicate string, arity int, head bool) ([]Literal, error)
	// Compile analyzes p's System, Rules, Heads and EDB once; p.Facts and
	// p.Query are ignored, since Run takes its own per call. p.Limits.Rules
	// and ProgramBytes are checked here; Facts and Derived are checked by
	// Run, against the limits it is given.
	Compile(p Program) (Compiled, error)
}
