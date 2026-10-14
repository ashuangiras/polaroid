// Package reasoning is Corpus Logic (ADR-0050): pure, deterministic
// resolution of what applies to a goal in one knowledge snapshot. Positive
// inference is delegated to an Evaluator (internal/reasoning/datalog, on
// Mangle); exceptions to internal/reasoning/policy. This package imports
// neither Git, storage nor any transport, and no inference library.
package reasoning

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
)

// Version names the reasoning semantics; it is part of every explanation.
const Version = "corpus-logic/1"

// System is the built-in program of Version: specialization, domains,
// obligation inheritance with provenance, and relevance. Rule records
// define obligation/2 and derived/3; their heads are stored with the rule's
// key appended (own_obligation/3, derived_by/4).
const System = `
broader_star(X, Y) :- broader(X, Y).
broader_star(X, Z) :- broader(X, Y), broader_star(Y, Z).
in_domain(K, D) :- classified_as(K, D).
in_domain(K, D) :- classified_as(K, C), broader_star(C, D).
specializes_star(X, Y) :- specializes(X, Y).
specializes_star(X, Z) :- specializes(X, Y), specializes_star(Y, Z).
own_obligation(S, R, S) :- requires(S, R).
obligation_of(S, R, Src, S) :- own_obligation(S, R, Src).
obligation_of(S, R, Src, V) :- specializes_star(S, V), own_obligation(V, R, Src).
obligation(S, R) :- obligation_of(S, R, Src, V).
derived(S, P, O) :- derived_by(S, P, O, Src).
relevant(G) :- goal(G).
relevant(Y) :- relevant(X), specializes(X, Y).
relevant(Y) :- relevant(X), uses(X, Y).
relevant(Y) :- relevant(X), composes(X, Y).
relevant(Y) :- relevant(X), requires(X, Y).
relevant(D) :- relevant(X), classified_as(X, D).
relevant(E) :- relevant(C), supports(E, C).
relevant(E) :- relevant(C), contradicts(E, C).
established(Q, C) :- composition(P, Q), precondition(Q, C), postcondition(P, C).
established(Q, C) :- composition(P, Q), precondition(Q, C), invariant(P, C).
established(Q, C) :- composition(P, Q), precondition(Q, C), context("established", C).
contradicted(Q, C) :- composition(P, Q), precondition(Q, C), postcondition(P, D), contradicts(D, C).
contradicted(Q, C) :- composition(P, Q), precondition(Q, C), postcondition(P, D), contradicts(C, D).
supplied(Q, N) :- composition(P, Q), input_type(Q, N, T), output_type(P, M, T).
`

// EDB are the extensional predicates facts are stated in.
var EDB = map[string]int{
	"record": 2, "classified_as": 2, "broader": 2, "specializes": 2, "uses": 2, "composes": 2, "requires": 2,
	"supports": 2, "contradicts": 2, "supersedes": 2, "excepts": 2, "asserts": 4, "context": 2, "goal": 1,
	"composition": 2, "input_type": 3, "output_type": 3, "precondition": 2, "postcondition": 2, "invariant": 2,
}

// Heads are what a rule record may define.
var Heads = map[string]string{"obligation/2": "own_obligation", "derived/3": "derived_by"}

// ErrUnknownGoal: the goal is not a record of the snapshot.
var ErrUnknownGoal = errors.New("goal is not a record in this snapshot")

// Request is one resolution's input besides the snapshot and trust.
type Request struct {
	Goal    string
	Context map[string]string
	At      time.Time
	Limits  Limits
}

// Derivation is one independent way an obligation holds.
type Derivation struct {
	ID         string   `json:"id"`
	Source     string   `json:"source"`
	SourceType string   `json:"source_type"` // "rule" or "declared"
	Via        string   `json:"via"`
	Path       []string `json:"path,omitzero"` // goal ... via, along specializes
	Status     string   `json:"status"`        // "active", "waived" or "replaced"
	Exception  string   `json:"exception,omitzero"`
}

// Obligation is a requirement on the goal with every derivation of it.
type Obligation struct {
	Requirement string       `json:"requirement"`
	Status      string       `json:"status"` // "required" or "waived"
	Derivations []Derivation `json:"derivations"`
}

// DerivedFact is a fact a rule derived about the goal.
type DerivedFact struct {
	ID        string   `json:"id"`
	Predicate string   `json:"predicate"`
	Object    string   `json:"object"`
	Rules     []string `json:"rules"`
}

// Specialization is one ancestor of the goal.
type Specialization struct {
	Key  string   `json:"key"`
	Path []string `json:"path"`
}

// ExceptionResult reports one exception that concerns this resolution.
type ExceptionResult struct {
	Key         string   `json:"key"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason,omitzero"`
	Derivations []string `json:"derivations,omitzero"`
}

// Conflict is information that cannot all hold.
type Conflict struct {
	Kind   string   `json:"kind"`
	Keys   []string `json:"keys"`
	Reason string   `json:"reason"`
}

// Unknown is a conclusion the inputs do not decide.
type Unknown struct {
	Kind        string   `json:"kind"`
	Requirement string   `json:"requirement,omitzero"`
	Source      string   `json:"source,omitzero"`
	Context     string   `json:"context,omitzero"`
	Values      []string `json:"values,omitzero"`
	Reason      string   `json:"reason"`
}

// ClaimStatus summarises the evidence present for a claim. It is never a
// verdict on the claim.
type ClaimStatus struct {
	Claim         string   `json:"claim"`
	Status        string   `json:"status"` // supported, contradicted, contested or unsupported
	Supporting    []string `json:"supporting,omitzero"`
	Contradicting []string `json:"contradicting,omitzero"`
}

// Resolution is what applies to a goal: concise, deterministic, and
// explainable by its derivation identifiers.
type Resolution struct {
	Version         string            `json:"version"`
	Snapshot        string            `json:"snapshot"`
	Goal            string            `json:"goal"`
	At              string            `json:"at"`
	Applicable      []string          `json:"applicable"`
	Specializations []Specialization  `json:"specializations,omitzero"`
	Obligations     []Obligation      `json:"obligations,omitzero"`
	Derived         []DerivedFact     `json:"derived,omitzero"`
	Exceptions      []ExceptionResult `json:"exceptions,omitzero"`
	Conflicts       []Conflict        `json:"conflicts,omitzero"`
	Unknowns        []Unknown         `json:"unknowns,omitzero"`
	Evidence        []ClaimStatus     `json:"evidence,omitzero"`
	Superseded      []string          `json:"superseded,omitzero"`
}

// maxHypotheses bounds the context names probed for unknowns.
const maxHypotheses = 8

// Facts states a catalog's records, a context and a goal as facts.
func Facts(cat *knowledge.Catalog, ctxVals map[string]string, goal string) ([]Fact, []Rule) {
	var facts []Fact
	add := func(pred string, args ...string) { facts = append(facts, Fact{Predicate: pred, Args: args}) }
	var rules []Rule
	for _, r := range cat.Records() {
		add("record", r.Key, string(r.Type))
		for pred, targets := range map[string][]string{
			"classified_as": r.ClassifiedAs, "broader": r.Broader, "specializes": r.Specializes, "uses": r.Uses,
			"composes": r.Composes, "requires": r.Requires, "supports": r.Supports, "contradicts": r.Contradicts,
			"supersedes": r.Supersedes, "excepts": r.Excepts, "precondition": r.Preconditions,
			"postcondition": r.Postconditions, "invariant": r.Invariants,
		} {
			for _, t := range targets {
				add(pred, r.Key, t)
			}
		}
		for _, a := range r.Asserts {
			add("asserts", r.Key, a.Subject, a.Predicate, a.Object)
		}
		for _, p := range r.Inputs {
			if p.Concept != "" {
				add("input_type", r.Key, p.Name, p.Concept)
			}
		}
		for _, p := range r.Outputs {
			if p.Concept != "" {
				add("output_type", r.Key, p.Name, p.Concept)
			}
		}
		if r.Type == knowledge.TypeRule {
			rules = append(rules, Rule{Key: r.Key, Text: r.Program})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(ctxVals)) {
		add("context", name, ctxVals[name])
	}
	if goal != "" {
		add("goal", goal)
	}
	slices.SortFunc(facts, func(a, b Fact) int {
		return cmp.Or(strings.Compare(a.Predicate, b.Predicate), slices.Compare(a.Args, b.Args))
	})
	return facts, rules
}

func explanationID(parts ...string) string {
	h := sha256.Sum256([]byte(Version + "\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:16])
}

// Resolve resolves what applies to req.Goal in cat. The result depends only
// on cat's snapshot, req (goal, context, time), Version and trust.
func Resolve(ctx context.Context, cat *knowledge.Catalog, ev Evaluator, trust policy.Trust, req Request) (*Resolution, error) {
	goal, err := cat.Record(req.Goal)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownGoal, req.Goal)
	}
	if req.At.IsZero() {
		return nil, errors.New("an evaluation time is required")
	}
	limits := req.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits
	}
	records := map[string]*knowledge.Record{}
	for _, r := range cat.Records() {
		records[r.Key] = r
	}
	facts, rules := Facts(cat, req.Context, goal.Key)
	query := []string{"obligation_of", "derived_by", "relevant", "specializes_star", "in_domain"}
	base, err := ev.Evaluate(ctx, Program{System: System, Rules: rules, Heads: Heads, EDB: EDB, Facts: facts, Query: query, Limits: limits})
	if err != nil {
		return nil, err
	}
	res := &Resolution{Version: Version, Snapshot: string(cat.Snapshot()), Goal: goal.Key, At: req.At.UTC().Format(time.RFC3339)}
	id := func(parts ...string) string {
		return explanationID(append([]string{res.Snapshot, goal.Key}, parts...)...)
	}

	superseded := map[string]bool{}
	for _, r := range records {
		for _, old := range r.Supersedes {
			superseded[old] = true
		}
	}
	paths := specializationPaths(records, goal.Key)

	// Obligations of the goal, one derivation per (requirement, source, via).
	var derivs []policy.Derivation
	for _, row := range base["obligation_of"] {
		if row[0] == goal.Key && !superseded[row[2]] {
			derivs = append(derivs, policy.Derivation{Subject: row[0], Requirement: row[1], Source: row[2], Via: row[3]})
		}
	}
	domains := map[string][]string{}
	for _, row := range base["in_domain"] {
		domains[row[0]] = append(domains[row[0]], row[1])
	}
	policyRules := map[string]policy.Rule{}
	var exceptions []*knowledge.Record
	for _, r := range records {
		switch r.Type {
		case knowledge.TypeRule:
			policyRules[r.Key] = policy.Rule{Key: r.Key, Revision: r.Revision, Protected: r.Protected, Waivable: r.Waivable, Domains: domains[r.Key]}
		case knowledge.TypeException:
			if !superseded[r.Key] {
				exceptions = append(exceptions, r)
			}
		}
	}
	slices.SortFunc(exceptions, func(a, b *knowledge.Record) int { return strings.Compare(a.Key, b.Key) })
	outcome := policy.Decide(trust, exceptions, policyRules, derivs, policy.Request{Context: req.Context, At: req.At})

	byReq := map[string]*Obligation{}
	derivIDs := make([]string, len(derivs))
	for i, d := range derivs {
		o := byReq[d.Requirement]
		if o == nil {
			o = &Obligation{Requirement: d.Requirement}
			byReq[d.Requirement] = o
		}
		dv := Derivation{ID: id("obligation", d.Requirement, d.Source, d.Via), Source: d.Source, SourceType: "declared", Via: d.Via, Status: "active"}
		if records[d.Source] != nil && records[d.Source].Type == knowledge.TypeRule {
			dv.SourceType = "rule"
		}
		if d.Via != goal.Key {
			dv.Path = paths[d.Via]
		}
		if e, ok := outcome.Effects[i]; ok {
			dv.Exception, dv.Status = e.Exception, "waived"
			if e.Replacement != "" {
				dv.Status = "replaced"
				r := byReq[e.Replacement]
				if r == nil {
					r = &Obligation{Requirement: e.Replacement}
					byReq[e.Replacement] = r
				}
				r.Derivations = append(r.Derivations, Derivation{ID: id("replacement", e.Replacement, e.Exception, d.Requirement),
					Source: e.Exception, SourceType: "exception", Via: goal.Key, Status: "active"})
			}
		}
		derivIDs[i] = dv.ID
		o.Derivations = append(o.Derivations, dv)
	}
	required := map[string]bool{}
	for _, k := range slices.Sorted(maps.Keys(byReq)) {
		o := byReq[k]
		o.Status = "waived"
		for _, d := range o.Derivations {
			if d.Status == "active" {
				o.Status = "required"
				required[k] = true
			}
		}
		slices.SortFunc(o.Derivations, func(a, b Derivation) int { return strings.Compare(a.ID, b.ID) })
		res.Obligations = append(res.Obligations, *o)
	}

	for _, v := range outcome.Verdicts {
		if v.Status == policy.NotApplicable && !concerns(records[v.Exception], goal.Key, derivs) {
			continue
		}
		er := ExceptionResult{Key: v.Exception, Status: v.Status, Reason: v.Reason}
		for _, i := range v.Derivations {
			er.Derivations = append(er.Derivations, derivIDs[i])
		}
		slices.Sort(er.Derivations)
		res.Exceptions = append(res.Exceptions, er)
		if v.Status == policy.Conflicting {
			res.Conflicts = append(res.Conflicts, Conflict{Kind: "exceptions", Keys: []string{v.Exception}, Reason: v.Reason})
		}
	}

	for _, r1 := range slices.Sorted(maps.Keys(required)) {
		for _, r2 := range records[r1].Contradicts {
			if required[r2] {
				pair := []string{r1, r2}
				slices.Sort(pair)
				c := Conflict{Kind: "requirements", Keys: pair, Reason: fmt.Sprintf("%s contradicts %s, and both are required", r1, r2)}
				if !slices.ContainsFunc(res.Conflicts, func(x Conflict) bool { return slices.Equal(x.Keys, pair) }) {
					res.Conflicts = append(res.Conflicts, c)
				}
			}
		}
	}

	derived := map[[2]string][]string{}
	for _, row := range base["derived_by"] {
		if row[0] == goal.Key && !superseded[row[3]] {
			k := [2]string{row[1], row[2]}
			derived[k] = append(derived[k], row[3])
		}
	}
	for _, k := range slices.SortedFunc(maps.Keys(derived), func(a, b [2]string) int { return slices.Compare(a[:], b[:]) }) {
		res.Derived = append(res.Derived, DerivedFact{ID: id("derived", k[0], k[1]), Predicate: k[0], Object: k[1], Rules: derived[k]})
	}

	applicable := map[string]bool{}
	for _, row := range base["relevant"] {
		applicable[row[0]] = true
	}
	for _, d := range derivs {
		applicable[d.Source] = true
	}
	for _, rules := range derived {
		for _, r := range rules {
			applicable[r] = true
		}
	}
	for _, e := range res.Exceptions {
		applicable[e.Key] = true
	}
	for k := range applicable {
		if superseded[k] {
			res.Superseded = append(res.Superseded, k)
		}
	}
	slices.Sort(res.Superseded)
	res.Applicable = slices.Sorted(maps.Keys(applicable))

	for _, row := range base["specializes_star"] {
		if row[0] == goal.Key {
			res.Specializations = append(res.Specializations, Specialization{Key: row[1], Path: paths[row[1]]})
		}
	}

	for _, k := range res.Applicable {
		if r := records[k]; r != nil && r.Type == knowledge.TypeClaim {
			res.Evidence = append(res.Evidence, claimStatus(records, k))
		}
	}

	unknowns, err := hypothesize(ctx, ev, rules, facts, goal.Key, req.Context, base["obligation_of"], limits, superseded)
	if err != nil {
		return nil, err
	}
	res.Unknowns = unknowns
	return res, nil
}

// concerns reports whether a not-applicable exception is about the goal or
// one of the derivations, so unrelated exceptions are not listed.
func concerns(e *knowledge.Record, goal string, derivs []policy.Derivation) bool {
	if slices.Contains(e.AppliesTo, goal) {
		return true
	}
	return slices.ContainsFunc(derivs, func(d policy.Derivation) bool {
		return len(e.Excepts) == 1 && d.Source == e.Excepts[0] && slices.Contains(e.AppliesTo, d.Via)
	})
}

// specializationPaths finds, for every ancestor of goal, the shortest path
// along specializes, choosing the lexically first parent at each step.
func specializationPaths(records map[string]*knowledge.Record, goal string) map[string][]string {
	paths := map[string][]string{goal: {goal}}
	queue := []string{goal}
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		r := records[k]
		if r == nil {
			continue
		}
		for _, p := range slices.Sorted(slices.Values(r.Specializes)) {
			if _, seen := paths[p]; !seen {
				paths[p] = append(slices.Clone(paths[k]), p)
				queue = append(queue, p)
			}
		}
	}
	return paths
}

func claimStatus(records map[string]*knowledge.Record, claim string) ClaimStatus {
	cs := ClaimStatus{Claim: claim}
	for _, k := range slices.Sorted(maps.Keys(records)) {
		r := records[k]
		if slices.Contains(r.Supports, claim) {
			cs.Supporting = append(cs.Supporting, k)
		}
		if slices.Contains(r.Contradicts, claim) {
			cs.Contradicting = append(cs.Contradicting, k)
		}
	}
	switch {
	case len(cs.Supporting) > 0 && len(cs.Contradicting) > 0:
		cs.Status = "contested"
	case len(cs.Supporting) > 0:
		cs.Status = "supported"
	case len(cs.Contradicting) > 0:
		cs.Status = "contradicted"
	default:
		cs.Status = "unsupported"
	}
	return cs
}

// hypothesize finds obligations that hold only for a context value the
// caller did not give: positive inference is monotone, so adding the
// candidate values of one missing name and comparing with the base result
// shows exactly what that name could add. Absence is never read as "no".
func hypothesize(ctx context.Context, ev Evaluator, rules []Rule, facts []Fact, goal string, given map[string]string,
	base [][]string, limits Limits, superseded map[string]bool) ([]Unknown, error) {
	consts, err := ev.Constants(rules, "context", 2)
	if err != nil {
		return nil, err
	}
	values := map[string][]string{}
	for _, rows := range consts {
		for _, row := range rows {
			if _, ok := given[row[0]]; !ok && !slices.Contains(values[row[0]], row[1]) {
				values[row[0]] = append(values[row[0]], row[1])
			}
		}
	}
	names := slices.Sorted(maps.Keys(values))
	var out []Unknown
	if len(names) > maxHypotheses {
		out = append(out, Unknown{Kind: "context", Reason: fmt.Sprintf("rules read %d context names that were not given; only the first %d were examined", len(names), maxHypotheses)})
		names = names[:maxHypotheses]
	}
	known := map[string]bool{}
	for _, row := range base {
		known[strings.Join(row, "\x00")] = true
	}
	for _, name := range names {
		vals := slices.Sorted(slices.Values(values[name]))
		extra := slices.Clone(facts)
		for _, v := range vals {
			extra = append(extra, Fact{Predicate: "context", Args: []string{name, v}})
		}
		res, err := ev.Evaluate(ctx, Program{System: System, Rules: rules, Heads: Heads, EDB: EDB, Facts: extra, Query: []string{"obligation_of"}, Limits: limits})
		if err != nil {
			return nil, err
		}
		seen := map[[2]string]bool{}
		for _, row := range res["obligation_of"] {
			if row[0] != goal || known[strings.Join(row, "\x00")] || superseded[row[2]] || seen[[2]string{row[1], row[2]}] {
				continue
			}
			seen[[2]string{row[1], row[2]}] = true
			out = append(out, Unknown{Kind: "obligation", Requirement: row[1], Source: row[2], Context: name, Values: vals,
				Reason: fmt.Sprintf("%s may require %s depending on context %q, which was not given", row[2], row[1], name)})
		}
	}
	return out, nil
}
