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

// ErrContextConflict: the caller's context contradicts trusted context.
var ErrContextConflict = errors.New("the context contradicts trusted context")

// Request is one resolution's input besides the snapshot and trust.
// Context is what the caller asserts: it can add obligations and decide
// unknowns, but never authorise a waiver. Trusted is context established
// independently of the caller (policy.Establish); it is also a fact of the
// situation, so Context may omit it but never contradict it, and an
// exception's scope and conditions are judged on it alone. Then, when set,
// names a consumer procedure to compose after the goal.
type Request struct {
	Goal    string
	Context map[string]string
	Trusted map[string]string
	Then    string
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

// ContextValue is one name and value of a resolution context.
type ContextValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Unknown is a conclusion the inputs do not decide: it holds when the
// context also has When, which the caller did not give.
type Unknown struct {
	Kind        string         `json:"kind"` // obligation or derived
	Requirement string         `json:"requirement,omitzero"`
	Predicate   string         `json:"predicate,omitzero"`
	Object      string         `json:"object,omitzero"`
	Source      string         `json:"source"`
	When        []ContextValue `json:"when"`
	Reason      string         `json:"reason"`
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
// explainable by its derivation identifiers. Complete is false whenever
// the unknowns could not all be examined; Incomplete says why.
type Resolution struct {
	Version         string             `json:"version"`
	Snapshot        string             `json:"snapshot"`
	Goal            string             `json:"goal"`
	At              string             `json:"at"`
	Trusted         map[string]string  `json:"trusted,omitzero"`
	Complete        bool               `json:"complete"`
	Incomplete      []string           `json:"incomplete,omitzero"`
	Applicable      []string           `json:"applicable"`
	Specializations []Specialization   `json:"specializations,omitzero"`
	Obligations     []Obligation       `json:"obligations,omitzero"`
	Derived         []DerivedFact      `json:"derived,omitzero"`
	Exceptions      []ExceptionResult  `json:"exceptions,omitzero"`
	Conflicts       []Conflict         `json:"conflicts,omitzero"`
	Unknowns        []Unknown          `json:"unknowns,omitzero"`
	Evidence        []ClaimStatus      `json:"evidence,omitzero"`
	Superseded      []string           `json:"superseded,omitzero"`
	Composition     *CompositionResult `json:"composition,omitzero"`
}

// maxAssignments bounds the hypothetical evaluations one resolution runs:
// one per combination of a value for every missing context name. Beyond
// it the resolution is reported incomplete.
const maxAssignments = 64

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

// ExplanationID is the identifier of one derivation: the first 16 bytes,
// hex, of SHA-256 over Version, the snapshot, the goal and the derivation's
// parts (for an obligation: "obligation", requirement, source, via),
// joined by NUL bytes. Anyone holding the snapshot can recompute it.
func ExplanationID(snapshot, goal string, parts ...string) string {
	h := sha256.Sum256([]byte(Version + "\x00" + strings.Join(append([]string{snapshot, goal}, parts...), "\x00")))
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
	broader := map[string][]string{}
	for _, r := range cat.Records() {
		records[r.Key] = r
		if r.Type == knowledge.TypeDomain {
			broader[r.Key] = r.Broader
		}
	}
	effective := maps.Clone(req.Context)
	for _, n := range slices.Sorted(maps.Keys(req.Trusted)) {
		v := req.Trusted[n]
		if given, ok := req.Context[n]; ok && given != v {
			return nil, fmt.Errorf("%w: %s is %q in the context but %q in trusted context", ErrContextConflict, n, given, v)
		}
		if effective == nil {
			effective = map[string]string{}
		}
		effective[n] = v
	}
	facts, rules := Facts(cat, effective, goal.Key)
	query := []string{"obligation_of", "derived_by", "relevant", "specializes_star"}
	compiled, err := ev.Compile(Program{System: System, Rules: rules, Heads: Heads, EDB: EDB, Limits: limits})
	if err != nil {
		return nil, err
	}
	base, err := compiled.Run(ctx, facts, query, limits)
	if err != nil {
		return nil, err
	}
	res := &Resolution{Version: Version, Snapshot: string(cat.Snapshot()), Goal: goal.Key, At: req.At.UTC().Format(time.RFC3339), Trusted: req.Trusted}
	id := func(parts ...string) string { return ExplanationID(res.Snapshot, goal.Key, parts...) }

	superseded := map[string]bool{}
	for _, r := range records {
		for _, old := range r.Supersedes {
			superseded[old] = true
		}
	}
	paths := specializationPaths(records, goal.Key)

	// silenced is what a superseded rule would have contributed to the
	// goal: its program still evaluates, so its rows reach obligation_of
	// and derived_by, but a superseded source is reported only here, never
	// as an active obligation, derived fact or applicable rule.
	silenced := map[string]bool{}

	// Obligations of the goal, one derivation per (requirement, source, via).
	var derivs []policy.Derivation
	for _, row := range base["obligation_of"] {
		if row[0] != goal.Key {
			continue
		}
		if superseded[row[2]] {
			silenced[row[2]] = true
			continue
		}
		derivs = append(derivs, policy.Derivation{Subject: row[0], Requirement: row[1], Source: row[2], Via: row[3]})
	}
	policyRules := map[string]policy.Rule{}
	var exceptions []*knowledge.Record
	for _, r := range records {
		switch r.Type {
		case knowledge.TypeRule:
			policyRules[r.Key] = policy.Rule{Key: r.Key, Revision: r.Revision, Digest: knowledge.RuleDigest(r), Protected: r.Protected, Waivable: r.Waivable, Classified: r.ClassifiedAs}
		case knowledge.TypeException:
			if !superseded[r.Key] {
				exceptions = append(exceptions, r)
			}
		}
	}
	slices.SortFunc(exceptions, func(a, b *knowledge.Record) int { return strings.Compare(a.Key, b.Key) })
	outcome := policy.Decide(trust, exceptions, policyRules, derivs, policy.Request{Context: effective, Trusted: req.Trusted, Broader: broader, At: req.At})

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
		rec := records[r1]
		if rec == nil || rec.Type != knowledge.TypeConcept {
			res.Conflicts = append(res.Conflicts, Conflict{Kind: "undefined_requirement", Keys: []string{r1},
				Reason: fmt.Sprintf("%s is required but is not a concept in this snapshot", r1)})
			continue
		}
		for _, r2 := range rec.Contradicts {
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
		if row[0] != goal.Key {
			continue
		}
		if superseded[row[3]] {
			silenced[row[3]] = true
			continue
		}
		k := [2]string{row[1], row[2]}
		derived[k] = append(derived[k], row[3])
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
			silenced[k] = true
		}
	}
	res.Superseded = slices.Sorted(maps.Keys(silenced))
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

	unknowns, incomplete, err := hypothesize(ctx, ev, compiled, rules, facts, goal.Key, effective, base, limits, superseded)
	if err != nil {
		return nil, err
	}
	res.Unknowns, res.Incomplete, res.Complete = unknowns, incomplete, len(incomplete) == 0
	if req.Then != "" {
		if res.Composition, err = Compose(ctx, cat, ev, goal.Key, req.Then, limits); err != nil {
			return nil, err
		}
	}
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

// termValues is the values a rule term can take: its constant, the
// constants an equality binds it to, or nil when it is open.
func termValues(t Term) []string {
	switch {
	case t.IsConstant:
		return []string{t.Constant}
	case len(t.Bound) > 0:
		return t.Bound
	}
	return nil
}

// hypothesize finds conclusions that hold only for context the caller did
// not give. Positive inference is monotone, so every conclusion that some
// realizable context could add appears when each missing name takes one of
// the values rules compare it with: it evaluates every such combination
// (one value per name, as a real request has) and compares it with the
// base result. Absence is never read as "no". What cannot be enumerated
// (an open name or value, too many combinations) is returned as
// incompleteness, never silently skipped.
func hypothesize(ctx context.Context, ev Evaluator, compiled Compiled, rules []Rule, facts []Fact, goal string, given map[string]string,
	base Result, limits Limits, superseded map[string]bool) ([]Unknown, []string, error) {
	lits, err := ev.Literals(rules, "context", 2, false)
	if err != nil {
		return nil, nil, err
	}
	values := map[string][]string{}
	var incomplete []string
	note := func(format string, args ...any) {
		if msg := fmt.Sprintf(format, args...); !slices.Contains(incomplete, msg) {
			incomplete = append(incomplete, msg)
		}
	}
	for _, l := range lits {
		names, vals := termValues(l.Args[0]), termValues(l.Args[1])
		if names == nil {
			note("rule %s reads a context name it does not fix, so what it could add cannot be enumerated", l.Rule)
			continue
		}
		for _, n := range names {
			if _, ok := given[n]; ok {
				continue
			}
			if vals == nil {
				note("rule %s reads any value of context %q, which was not given, so what it could add cannot be enumerated", l.Rule, n)
				continue
			}
			for _, v := range vals {
				if !slices.Contains(values[n], v) {
					values[n] = append(values[n], v)
				}
			}
		}
	}
	names := slices.Sorted(maps.Keys(values))
	total := 1
	for _, n := range names {
		slices.Sort(values[n])
		total *= len(values[n])
		if total > maxAssignments {
			note("the %d missing context names %v have more than %d combinations of values; none was examined", len(names), names, maxAssignments)
			slices.Sort(incomplete)
			return nil, incomplete, nil
		}
	}
	if len(names) == 0 {
		slices.Sort(incomplete)
		return nil, incomplete, nil
	}
	known := map[string]bool{}
	for _, pred := range []string{"obligation_of", "derived_by"} {
		for _, row := range base[pred] {
			known[pred+"\x00"+strings.Join(row, "\x00")] = true
		}
	}
	type conclusion struct{ kind, a, b, source string }
	produced := map[conclusion][]int{}
	assignment := func(i int) []string {
		out := make([]string, len(names))
		for j := len(names) - 1; j >= 0; j-- {
			vs := values[names[j]]
			out[j] = vs[i%len(vs)]
			i /= len(vs)
		}
		return out
	}
	// conclusions evaluates the program with assignment i, without the
	// names marked in skip, and returns the new conclusions. The program
	// itself (System, Rules, Heads, EDB) was analyzed once, by compiled;
	// only its facts change per combination (#144).
	conclusions := func(i int, skip []bool) (map[conclusion]bool, error) {
		extra := slices.Clone(facts)
		for j, v := range assignment(i) {
			if !skip[j] {
				extra = append(extra, Fact{Predicate: "context", Args: []string{names[j], v}})
			}
		}
		res, err := compiled.Run(ctx, extra, []string{"obligation_of", "derived_by"}, limits)
		if err != nil {
			return nil, err
		}
		out := map[conclusion]bool{}
		for _, row := range res["obligation_of"] {
			if row[0] == goal && !known["obligation_of\x00"+strings.Join(row, "\x00")] && !superseded[row[2]] {
				out[conclusion{"obligation", row[1], "", row[2]}] = true
			}
		}
		for _, row := range res["derived_by"] {
			if row[0] == goal && !known["derived_by\x00"+strings.Join(row, "\x00")] && !superseded[row[3]] {
				out[conclusion{"derived", row[1], row[2], row[3]}] = true
			}
		}
		return out, nil
	}
	for i := range total {
		got, err := conclusions(i, make([]bool, len(names)))
		if err != nil {
			return nil, nil, err
		}
		for c := range got {
			produced[c] = append(produced[c], i)
		}
	}
	keys := slices.SortedFunc(maps.Keys(produced), func(x, y conclusion) int {
		return cmp.Or(strings.Compare(x.kind, y.kind), strings.Compare(x.a, y.a), strings.Compare(x.b, y.b), strings.Compare(x.source, y.source))
	})
	// When starts as the first assignment that yields the conclusion, and
	// drops each name in turn while the conclusion still holds without it
	// and every name dropped before. Every kept set was evaluated, so When
	// alone is always sufficient. Probes are bounded; past the bound the
	// remaining names are kept.
	probes, budget := map[string]map[conclusion]bool{}, maxAssignments
	var out []Unknown
	for _, c := range keys {
		at := produced[c][0]
		first := assignment(at)
		skip := make([]bool, len(names))
		reason := "holds if the context also has these values, which were not given"
		for j := range names {
			skip[j] = true
			key := fmt.Sprint(at, skip)
			got, ok := probes[key]
			if !ok && budget > 0 {
				budget--
				if got, err = conclusions(at, skip); err != nil {
					return nil, nil, err
				}
				probes[key], ok = got, true
			}
			if !ok {
				reason = "holds if the context also has these values, which were not given; not every one was tested as necessary"
			}
			if !ok || !got[c] {
				skip[j] = false
			}
		}
		var when []ContextValue
		for j, n := range names {
			if !skip[j] {
				when = append(when, ContextValue{Name: n, Value: first[j]})
			}
		}
		u := Unknown{Kind: c.kind, Source: c.source, When: when, Reason: reason}
		if c.kind == "obligation" {
			u.Requirement = c.a
		} else {
			u.Predicate, u.Object = c.a, c.b
		}
		out = append(out, u)
	}
	slices.Sort(incomplete)
	return out, incomplete, nil
}
