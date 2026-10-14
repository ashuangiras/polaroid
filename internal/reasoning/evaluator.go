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

// Evaluator evaluates programs to a fixed point. internal/reasoning/datalog
// implements it on Mangle.
type Evaluator interface {
	Evaluate(ctx context.Context, p Program) (Result, error)
	// Constants returns, for each rule, the distinct argument tuples of the
	// body atoms of predicate/arity whose arguments are all constants.
	Constants(rules []Rule, predicate string, arity int) (map[string][][]string, error)
}
