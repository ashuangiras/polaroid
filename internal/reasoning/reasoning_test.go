package reasoning_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/knowledge/semantic"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
)

var (
	at   = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ev   = datalog.Evaluator{}
	repo = "github.com/team/app"
)

type world struct {
	t      testing.TB
	kernel *knowledge.Kernel
	store  *knowledgetest.MemoryStore
	snap   knowledge.Snapshot
	keys   map[string]ed25519.PrivateKey
	trust  policy.Trust
}

func newWorld(t testing.TB) *world {
	t.Helper()
	v, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	store := knowledgetest.NewMemoryStore()
	w := &world{t: t, store: store, kernel: knowledge.New(store, knowledge.WithRecordValidator(v), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev})),
		keys: map[string]ed25519.PrivateKey{}, trust: policy.Trust{Keys: map[string]policy.Key{}, Revoked: map[string]bool{}}}
	w.snap, err = w.kernel.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{
		Changes: knowledgetest.CorpusProcedures(), Records: knowledgetest.CorpusRecordChanges(),
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "corpus", Rationale: "test"}}))
	if err != nil {
		t.Fatal(err)
	}
	for i, a := range []knowledge.Authority{
		{Level: knowledge.AuthorityCorpus},
		{Level: knowledge.AuthorityDomain, Domain: "domain.software"},
		{Level: knowledge.AuthorityDomain, Domain: "domain.mathematics"},
		{Level: knowledge.AuthorityProject, Repository: repo},
	} {
		id := []string{"corpus", "software", "mathematics", "project"}[i]
		priv := ed25519.NewKeyFromSeed(slices.Repeat([]byte{byte(i + 1)}, ed25519.SeedSize))
		w.keys[id] = priv
		w.trust.Keys[id] = policy.Key{ID: id, Public: priv.Public().(ed25519.PublicKey), Authority: a}
	}
	return w
}

func (w *world) publish(records ...*knowledge.Record) {
	w.t.Helper()
	var changes []knowledge.RecordChange
	for _, r := range records {
		changes = append(changes, knowledge.RecordChange{Key: r.Key, Record: r})
	}
	s, err := w.kernel.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{Base: w.snap, Records: changes,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "more", Rationale: "test"}}))
	if err != nil {
		w.t.Fatal(err)
	}
	w.snap = s
}

func (w *world) resolve(goal string, ctx map[string]string) *reasoning.Resolution {
	w.t.Helper()
	cat, err := w.kernel.At(context.Background(), w.snap)
	if err != nil {
		w.t.Fatal(err)
	}
	r, err := reasoning.Resolve(context.Background(), cat, ev, w.trust, reasoning.Request{Goal: goal, Context: ctx, At: at})
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

// exception builds and signs an exception with the named key.
func (w *world) exception(key, signer string, auth knowledge.Authority, rule, waives string, appliesTo ...string) *knowledge.Record {
	w.t.Helper()
	cat, err := w.kernel.At(context.Background(), w.snap)
	if err != nil {
		w.t.Fatal(err)
	}
	target, err := cat.Record(rule)
	if err != nil {
		w.t.Fatal(err)
	}
	if auth.Level == knowledge.AuthorityDomain && auth.Path == nil {
		auth.Path = domainPath(cat, target.ClassifiedAs, auth.Domain)
	}
	e := &knowledge.Record{Type: knowledge.TypeException, Key: key, Label: key, Excepts: []string{rule}, Revision: target.Revision,
		RuleDigest: knowledge.RuleDigest(target), AppliesTo: appliesTo, Waives: waives, Justification: "Justified for " + key, Authority: &auth,
		Approval: &knowledge.Approval{Reference: "approval-" + key, Key: signer, ValidFrom: at.Add(-24 * time.Hour), ValidUntil: at.Add(24 * time.Hour)}}
	w.sign(e, signer)
	return e
}

// domainPath is the path an approver would sign today: the shortest chain
// along broader from one of from to to, or a direct claim when none exists.
func domainPath(cat *knowledge.Catalog, from []string, to string) []string {
	prev := map[string]string{}
	queue := slices.Clone(from)
	for _, f := range from {
		prev[f] = ""
	}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if d == to {
			var path []string
			for ; d != ""; d = prev[d] {
				path = append([]string{d}, path...)
			}
			return path
		}
		if r, err := cat.Record(d); err == nil {
			for _, b := range r.Broader {
				if _, seen := prev[b]; !seen {
					prev[b] = d
					queue = append(queue, b)
				}
			}
		}
	}
	return []string{from[0], to}
}

// resolveTrusted resolves with context the server established itself.
func (w *world) resolveTrusted(goal string, ctx, trusted map[string]string) *reasoning.Resolution {
	w.t.Helper()
	cat, err := w.kernel.At(context.Background(), w.snap)
	if err != nil {
		w.t.Fatal(err)
	}
	r, err := reasoning.Resolve(context.Background(), cat, ev, w.trust, reasoning.Request{Goal: goal, Context: ctx, Trusted: trusted, At: at})
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

// publishErr publishes records and returns the error.
func (w *world) publishErr(records ...*knowledge.Record) error {
	var changes []knowledge.RecordChange
	for _, r := range records {
		changes = append(changes, knowledge.RecordChange{Key: r.Key, Record: r})
	}
	_, err := w.kernel.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{Base: w.snap, Records: changes,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "try", Rationale: "test"}}))
	return err
}

func (w *world) rule(key string) *knowledge.Record {
	w.t.Helper()
	cat, err := w.kernel.At(context.Background(), w.snap)
	if err != nil {
		w.t.Fatal(err)
	}
	r, err := cat.Record(key)
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func (w *world) sign(e *knowledge.Record, signer string) {
	w.t.Helper()
	sig, err := policy.Sign(w.keys[signer], e)
	if err != nil {
		w.t.Fatal(err)
	}
	e.Approval.Signature = sig
}

func obligation(r *reasoning.Resolution, req string) *reasoning.Obligation {
	for i := range r.Obligations {
		if r.Obligations[i].Requirement == req {
			return &r.Obligations[i]
		}
	}
	return nil
}

func derivations(o *reasoning.Obligation) []string {
	var out []string
	for _, d := range o.Derivations {
		out = append(out, fmt.Sprintf("%s:%s@%s=%s", d.SourceType, d.Source, d.Via, d.Status))
	}
	slices.Sort(out)
	return out
}

func requirements(r *reasoning.Resolution, status string) []string {
	var out []string
	for _, o := range r.Obligations {
		if o.Status == status {
			out = append(out, o.Requirement)
		}
	}
	return out
}

func exceptionStatus(r *reasoning.Resolution, key string) reasoning.ExceptionResult {
	for _, e := range r.Exceptions {
		if e.Key == key {
			return e
		}
	}
	return reasoning.ExceptionResult{}
}

func TestSoftwareSpecialization(t *testing.T) {
	w := newWorld(t)
	r := w.resolve("go.verify.concurrent", map[string]string{"concurrency": "true"})
	if got := requirements(r, "required"); !slices.Equal(got, []string{"check.build", "check.leak", "check.lint", "check.race", "check.tests", "check.vet"}) {
		t.Fatalf("required %v", got)
	}
	if got := derivations(obligation(r, "check.race")); !slices.Equal(got, []string{
		"declared:go.verify.concurrent@go.verify.concurrent=active", "rule:rule.go.race@go.verify.concurrent=active", "rule:rule.go.race@go.verify=active"}) {
		t.Fatalf("check.race derivations %v", got)
	}
	if got := derivations(obligation(r, "check.build")); !slices.Equal(got, []string{"declared:change.verify@change.verify=active"}) {
		t.Fatalf("check.build derivations %v", got)
	}
	if len(r.Specializations) != 2 || !slices.Equal(r.Specializations[0].Path, []string{"go.verify.concurrent", "go.verify", "change.verify"}) {
		t.Fatalf("specializations %+v", r.Specializations)
	}
	if len(r.Unknowns) != 0 || len(r.Conflicts) != 0 {
		t.Fatalf("unknowns %+v conflicts %+v", r.Unknowns, r.Conflicts)
	}
}

func TestMissingContextIsUndetermined(t *testing.T) {
	r := newWorld(t).resolve("go.verify", nil)
	if obligation(r, "check.race") != nil {
		t.Fatal("check.race required without knowing whether the change is concurrent")
	}
	if len(r.Unknowns) != 1 || r.Unknowns[0].Kind != "obligation" || r.Unknowns[0].Requirement != "check.race" || r.Unknowns[0].Source != "rule.go.race" ||
		!slices.Equal(r.Unknowns[0].When, []reasoning.ContextValue{{Name: "concurrency", Value: "true"}}) || !r.Complete {
		t.Fatalf("unknowns %+v complete %v %v", r.Unknowns, r.Complete, r.Incomplete)
	}
	if r2 := newWorld(t).resolve("go.verify", map[string]string{"concurrency": "false"}); len(r2.Unknowns) != 0 || obligation(r2, "check.race") != nil || !r2.Complete {
		t.Fatalf("with concurrency=false: %+v", r2)
	}
}

func goRule(key, program string) *knowledge.Record {
	return &knowledge.Record{Type: knowledge.TypeRule, Key: key, Label: key, Statement: key, Program: program, Revision: 1, Waivable: true,
		ClassifiedAs: []string{"domain.software.go"}}
}

const goProc = `record(P, "Procedure"), in_domain(P, "domain.software.go")`

// Unknowns cover equality-bound values and combinations of names, and
// never conflate two values of one name.
func TestUnknownsAreEnumerated(t *testing.T) {
	w := newWorld(t)
	w.publish(
		goRule("rule.eq", `obligation(P, "check.review") :- `+goProc+`, context("mode", V), V = "ci".`),
		goRule("rule.both", `obligation(P, "check.leak") :- `+goProc+`, context("mode", "a"), context("mode", "b").`),
		goRule("rule.multi", `obligation(P, "check.review") :- `+goProc+`, context("os", "linux"), context("arch", "arm64").`),
		// Two ways to the same conclusion: when must still suffice alone.
		goRule("rule.either", `obligation(P, "check.build") :- `+goProc+`, context("stage", "ci"), context("x", "1").`+"\n"+
			`obligation(P, "check.build") :- `+goProc+`, context("env", "prod"), context("x", "1").`),
	)
	r := w.resolve("go.verify", nil)
	got := map[string][]reasoning.ContextValue{}
	for _, u := range r.Unknowns {
		got[u.Requirement+"/"+u.Source] = u.When
	}
	want := map[string][]reasoning.ContextValue{
		"check.race/rule.go.race": {{Name: "concurrency", Value: "true"}},
		"check.review/rule.eq":    {{Name: "mode", Value: "ci"}},
		"check.review/rule.multi": {{Name: "arch", Value: "arm64"}, {Name: "os", Value: "linux"}},
		"check.build/rule.either": {{Name: "stage", Value: "ci"}, {Name: "x", Value: "1"}},
	}
	if len(got) != len(want) || !r.Complete {
		t.Fatalf("unknowns %+v complete %v %v", r.Unknowns, r.Complete, r.Incomplete)
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Errorf("%s: when %+v, want %+v", k, got[k], v)
		}
	}
}

// When the probe budget runs out, when keeps the untested names, so it
// still suffices, and its reason says it may not be minimal.
func TestWhenBudgetIsDisclosed(t *testing.T) {
	w := newWorld(t)
	var clauses []string
	for i := range 70 {
		clauses = append(clauses, fmt.Sprintf(`obligation(P, "check.review") :- %s, context("n%02d", "v").`, goProc, i))
	}
	w.publish(goRule("rule.many", strings.Join(clauses, "\n")))
	r := w.resolve("go.verify.concurrent", map[string]string{"concurrency": "true"})
	if len(r.Unknowns) != 1 || !r.Complete {
		t.Fatalf("unknowns %+v complete %v %v", r.Unknowns, r.Complete, r.Incomplete)
	}
	u := r.Unknowns[0]
	if !strings.Contains(u.Reason, "not every one was tested as necessary") || len(u.When) != 6 || u.When[0].Name != "n64" {
		t.Fatalf("unknown %+v", u)
	}
}

// What cannot be enumerated makes the resolution explicitly incomplete.
func TestIncompleteIsExplicit(t *testing.T) {
	w := newWorld(t)
	w.publish(goRule("rule.open", `obligation(P, "check.review") :- `+goProc+`, context("mode", V).`))
	r := w.resolve("go.verify", nil)
	if r.Complete || len(r.Incomplete) != 1 || !strings.Contains(r.Incomplete[0], `rule rule.open reads any value of context "mode"`) {
		t.Fatalf("open value: complete %v %v", r.Complete, r.Incomplete)
	}
	if r := w.resolve("go.verify", map[string]string{"mode": "x"}); !r.Complete || obligation(r, "check.review") == nil {
		t.Fatalf("given mode: complete %v %v", r.Complete, r.Incomplete)
	}

	w = newWorld(t)
	var clauses []string
	for i := range 7 {
		for _, v := range []string{"a", "b"} {
			clauses = append(clauses, fmt.Sprintf(`obligation(P, "check.review") :- %s, context("n%d", %q).`, goProc, i, v))
		}
	}
	w.publish(goRule("rule.wide", strings.Join(clauses, "\n")))
	r = w.resolve("go.verify", nil)
	if r.Complete || len(r.Unknowns) != 0 || len(r.Incomplete) != 1 || !strings.Contains(r.Incomplete[0], "more than 64 combinations") {
		t.Fatalf("too many combinations: complete %v %v unknowns %+v", r.Complete, r.Incomplete, r.Unknowns)
	}
}

// A requirement that is not a concept is refused at publication when a
// rule states it, and reported, never dereferenced, when it is derived.
func TestUndefinedRequirement(t *testing.T) {
	w := newWorld(t)
	if err := w.publishErr(goRule("rule.undefined", `obligation(P, "check.nothing") :- `+goProc+`.`)); !errors.Is(err, knowledge.ErrInvalid) ||
		!strings.Contains(err.Error(), `requires "check.nothing", which is not a Concept record`) {
		t.Fatalf("constant: %v", err)
	}
	claim := &knowledge.Record{Type: knowledge.TypeClaim, Key: "claim.required", Label: "required", Statement: "Something is required.",
		ClassifiedAs: []string{"domain.software.go"}, Asserts: []knowledge.Assertion{{Subject: "go", Predicate: "requires", Object: "not.a.concept"}}}
	w.publish(claim, goRule("rule.indirect", `obligation(P, X) :- `+goProc+`, asserts(C, "go", "requires", X).`))
	r := w.resolve("go.verify", nil)
	if !slices.ContainsFunc(r.Conflicts, func(c reasoning.Conflict) bool {
		return c.Kind == "undefined_requirement" && slices.Equal(c.Keys, []string{"not.a.concept"})
	}) {
		t.Fatalf("conflicts %+v", r.Conflicts)
	}
}

// Every explanation identifier can be recomputed from the snapshot alone.
func TestExplanationIDsAreReconstructible(t *testing.T) {
	r := newWorld(t).resolve("go.verify.concurrent", map[string]string{"concurrency": "true"})
	recompute := func(parts ...string) string {
		h := sha256.Sum256([]byte(strings.Join(append([]string{reasoning.Version, r.Snapshot, r.Goal}, parts...), "\x00")))
		return hex.EncodeToString(h[:16])
	}
	n := 0
	for _, o := range r.Obligations {
		for _, d := range o.Derivations {
			if d.ID != recompute("obligation", o.Requirement, d.Source, d.Via) {
				t.Fatalf("%s via %s: %s", o.Requirement, d.Via, d.ID)
			}
			n++
		}
	}
	if n == 0 {
		t.Fatal("no derivations")
	}
	r = newWorld(t).resolve("audio.phase.analyze", nil)
	if d := r.Derived[0]; d.ID != recompute("derived", d.Predicate, d.Object) {
		t.Fatalf("derived %s", d.ID)
	}
}

func TestDiamondInheritance(t *testing.T) {
	r := newWorld(t).resolve("ci.verify", nil)
	if got := derivations(obligation(r, "check.build")); !slices.Equal(got, []string{
		"declared:build.verify@build.verify=active", "declared:change.verify@change.verify=active"}) {
		t.Fatalf("check.build derivations %v", got)
	}
	seen := map[string]bool{}
	for _, o := range r.Obligations {
		for _, d := range o.Derivations {
			if seen[d.ID] {
				t.Fatalf("derivation %s listed twice", d.ID)
			}
			seen[d.ID] = true
		}
	}
	if len(r.Conflicts) != 1 || !slices.Equal(r.Conflicts[0].Keys, []string{"network.forbidden", "network.required"}) {
		t.Fatalf("conflicts %+v", r.Conflicts)
	}
}

func TestMathematicsReusedByAudio(t *testing.T) {
	r := newWorld(t).resolve("audio.phase.analyze", nil)
	if len(r.Derived) != 1 || r.Derived[0].Predicate != "phase_method" || r.Derived[0].Object != "atan2(imaginary, real)" ||
		!slices.Equal(r.Derived[0].Rules, []string{"rule.audio.phase.method"}) {
		t.Fatalf("derived %+v", r.Derived)
	}
	for _, k := range []string{"audio.phase", "math.phase.atan2", "math.phase", "domain.audio", "domain.mathematics"} {
		if !slices.Contains(r.Applicable, k) {
			t.Errorf("%s is not applicable: %v", k, r.Applicable)
		}
	}
	if len(r.Evidence) != 1 || r.Evidence[0].Claim != "math.phase.atan2" || r.Evidence[0].Status != "unsupported" {
		t.Fatalf("evidence %+v", r.Evidence)
	}
}

func TestConflictingEvidenceIsPreserved(t *testing.T) {
	r := newWorld(t).resolve("science.coffee.longevity", nil)
	want := reasoning.ClaimStatus{Claim: "science.coffee.longevity", Status: "contested",
		Supporting: []string{"science.study.cohort"}, Contradicting: []string{"science.study.trial"}}
	if len(r.Evidence) != 1 || r.Evidence[0].Status != want.Status ||
		!slices.Equal(r.Evidence[0].Supporting, want.Supporting) || !slices.Equal(r.Evidence[0].Contradicting, want.Contradicting) {
		t.Fatalf("evidence %+v", r.Evidence)
	}
	if len(r.Derived) != 0 || len(r.Obligations) != 0 {
		t.Fatalf("a contested claim produced conclusions: %+v %+v", r.Derived, r.Obligations)
	}
}

func TestDeterminism(t *testing.T) {
	w := newWorld(t)
	ctx := map[string]string{"concurrency": "true", "repository": repo}
	w.publish(w.exception("exc.vet", "project", knowledge.Authority{Level: knowledge.AuthorityProject, Repository: repo}, "rule.go.vet", "check.vet", "go.verify"))
	first, err := json.Marshal(w.resolve("go.verify.concurrent", ctx))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		again, _ := json.Marshal(w.resolve("go.verify.concurrent", ctx))
		if string(again) != string(first) {
			t.Fatalf("not deterministic:\n%s\n%s", first, again)
		}
	}
	v, _ := semantic.New(semantic.DefaultLimits)
	cat, err := knowledge.New(w.store, knowledge.WithRecordValidator(v)).At(context.Background(), w.snap)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := reasoning.Resolve(context.Background(), cat, ev, w.trust, reasoning.Request{Goal: "go.verify.concurrent", Context: ctx, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(fresh); string(b) != string(first) {
		t.Fatalf("a fresh kernel resolves differently")
	}
}

func TestAuthorizedExceptionWaivesOnlyItsDerivation(t *testing.T) {
	w := newWorld(t)
	proj := knowledge.Authority{Level: knowledge.AuthorityProject, Repository: repo}
	w.publish(w.exception("exc.vet", "project", proj, "rule.go.vet", "check.vet", "go.verify"))
	trusted := map[string]string{"repository": repo}

	r := w.resolveTrusted("go.verify", nil, trusted)
	if o := obligation(r, "check.vet"); o == nil || o.Status != "waived" {
		t.Fatalf("check.vet %+v", o)
	}
	if e := exceptionStatus(r, "exc.vet"); e.Status != policy.Applied || len(e.Derivations) != 1 {
		t.Fatalf("exception %+v", e)
	}
	// The child's own derivation of check.vet is independent of go.verify's.
	r = w.resolveTrusted("go.verify.concurrent", map[string]string{"concurrency": "true"}, trusted)
	if got := derivations(obligation(r, "check.vet")); !slices.Equal(got, []string{
		"rule:rule.go.vet@go.verify.concurrent=active", "rule:rule.go.vet@go.verify=waived"}) || obligation(r, "check.vet").Status != "required" {
		t.Fatalf("check.vet derivations %v", got)
	}
	// Another repository is not covered.
	r = w.resolveTrusted("go.verify", nil, map[string]string{"repository": "github.com/other/app"})
	if o := obligation(r, "check.vet"); o.Status != "required" || exceptionStatus(r, "exc.vet").Status != policy.NotApplicable {
		t.Fatalf("other repository: %+v %+v", o, r.Exceptions)
	}
}

// A caller that merely asserts the approved repository, or a condition,
// never activates a project or conditional waiver (priority B).
func TestAssertedContextNeverAuthorizes(t *testing.T) {
	w := newWorld(t)
	proj := knowledge.Authority{Level: knowledge.AuthorityProject, Repository: repo}
	cond := w.exception("exc.vet.ci", "corpus", knowledge.Authority{Level: knowledge.AuthorityCorpus}, "rule.go.vet", "check.vet", "go.verify")
	cond.Conditions = []knowledge.Condition{{Name: "ci", Value: "true"}}
	w.sign(cond, "corpus")
	w.publish(w.exception("exc.vet", "project", proj, "rule.go.vet", "check.vet", "go.verify"), cond)
	r := w.resolve("go.verify", map[string]string{"repository": repo, "ci": "true"})
	if o := obligation(r, "check.vet"); o.Status != "required" {
		t.Fatalf("an asserted context waived check.vet: %+v", o)
	}
	for k, want := range map[string]string{"exc.vet": "a caller's assertion of its repository does not authorise a waiver",
		"exc.vet.ci": "a caller's assertion does not authorise a waiver"} {
		if e := exceptionStatus(r, k); e.Status != policy.NotApplicable || !strings.Contains(e.Reason, want) {
			t.Fatalf("%s: %+v", k, e)
		}
	}
}

// Protected invariants are never waived by an exception, at any authority
// (priority C): publication refuses such exceptions, and policy rejects
// one even if it were present.
func TestProtectedInvariant(t *testing.T) {
	w := newWorld(t)
	for signer, auth := range map[string]knowledge.Authority{
		"project":  {Level: knowledge.AuthorityProject, Repository: repo},
		"software": {Level: knowledge.AuthorityDomain, Domain: "domain.software"},
		"corpus":   {Level: knowledge.AuthorityCorpus},
	} {
		err := w.publishErr(w.exception("exc.tests."+signer, signer, auth, "rule.software.tests", "check.tests", "go.verify"))
		if !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "rule.software.tests is a protected invariant; no exception may target it") {
			t.Errorf("%s exception published: %v", signer, err)
		}
	}
	rule := w.rule("rule.software.tests")
	corpusExc := w.exception("exc.tests.corpus", "corpus", knowledge.Authority{Level: knowledge.AuthorityCorpus}, "rule.software.tests", "check.tests", "go.verify")
	out := policy.Decide(w.trust, []*knowledge.Record{corpusExc}, map[string]policy.Rule{rule.Key: {Key: rule.Key, Revision: rule.Revision,
		Digest: knowledge.RuleDigest(rule), Protected: true, Waivable: true, Classified: []string{"domain.software"}}},
		[]policy.Derivation{{Subject: "go.verify", Requirement: "check.tests", Source: rule.Key, Via: "go.verify"}}, policy.Request{At: at})
	if v := out.Verdicts[0]; v.Status != policy.Rejected || !strings.Contains(v.Reason, "no exception may waive it, at any authority") || len(out.Effects) != 0 {
		t.Fatalf("policy accepted a corpus exception to a protected invariant: %+v", out)
	}

	unprotect := w.rule("rule.software.tests")
	unprotect.Protected, unprotect.Waivable, unprotect.Revision = false, true, 2
	superseding := &knowledge.Record{Type: knowledge.TypeRule, Key: "rule.software.tests.lax", Label: "lax", Statement: "lax", Revision: 1, Waivable: true,
		Supersedes: []string{"rule.software.tests"}, Program: `obligation(P, "check.build") :- record(P, "Procedure").`}
	waivable := w.rule("rule.software.tests")
	waivable.Waivable, waivable.Revision = true, 2
	for name, tc := range map[string]struct {
		r    *knowledge.Record
		want string
	}{
		"unprotect":              {unprotect, "a protected invariant cannot be unprotected"},
		"supersede":              {superseding, "only a protected rule may supersede it"},
		"protected and waivable": {waivable, "a protected invariant is never waivable"},
	} {
		if err := w.publishErr(tc.r); !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := w.kernel.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{Base: w.snap, Records: []knowledge.RecordChange{{Key: "rule.software.tests"}},
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "remove", Rationale: "test"}}))
	if !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "is a protected invariant and cannot be removed") {
		t.Errorf("remove: %v", err)
	}
	// A governed revision may change a protected invariant's program.
	governed := w.rule("rule.software.tests")
	governed.Program, governed.Revision = `obligation(P, "check.tests") :- record(P, "Procedure"), in_domain(P, "domain.software"), record(P, T).`, 2
	if err := w.publishErr(governed); err != nil {
		t.Fatalf("governed revision: %v", err)
	}
}

// An approval binds the exact semantics of the rule it waives (priority A).
func TestApprovalBindsRuleSemantics(t *testing.T) {
	w := newWorld(t)
	corpus := knowledge.Authority{Level: knowledge.AuthorityCorpus}
	e := w.exception("exc.vet", "corpus", corpus, "rule.go.vet", "check.vet", "go.verify")
	w.publish(e)
	if o := obligation(w.resolve("go.verify", nil), "check.vet"); o.Status != "waived" {
		t.Fatalf("approved exception does not apply: %+v", o)
	}
	// The rule's program cannot change under the same revision.
	altered := w.rule("rule.go.vet")
	altered.Program = `obligation(P, "check.vet") :- record(P, "Procedure").`
	if err := w.publishErr(altered); !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "semantics changed, so its revision must rise above 1") {
		t.Fatalf("same-revision change: %v", err)
	}
	// Nor may the revision go back.
	w.publish(func() *knowledge.Record { r := w.rule("rule.go.vet"); r.Revision = 3; return r }())
	if err := w.publishErr(func() *knowledge.Record { r := w.rule("rule.go.vet"); r.Revision = 2; return r }()); err == nil || !strings.Contains(err.Error(), "below the published revision 3") {
		t.Fatalf("revision decrease: %v", err)
	}
	// A properly revised rule leaves the old approval behind.
	altered.Revision = 4
	w.publish(altered)
	r := w.resolve("go.verify", nil)
	if s := exceptionStatus(r, "exc.vet"); s.Status != policy.Rejected {
		t.Fatalf("old approval after a revision: %+v", s)
	}
	if o := obligation(r, "check.vet"); o.Status != "required" {
		t.Fatalf("check.vet %+v", o)
	}
	// Policy refuses a semantics mismatch even at an equal revision, which
	// history that skipped the transition check could present.
	rule := w.rule("rule.go.vet")
	sameRev := w.exception("exc.same", "corpus", corpus, "rule.go.vet", "check.vet", "go.verify")
	out := policy.Decide(w.trust, []*knowledge.Record{sameRev}, map[string]policy.Rule{rule.Key: {Key: rule.Key, Revision: rule.Revision,
		Digest: knowledge.RuleDigest(&knowledge.Record{Key: rule.Key, Program: "something else"}), Waivable: true}},
		[]policy.Derivation{{Subject: "go.verify", Requirement: "check.vet", Source: rule.Key, Via: "go.verify"}}, policy.Request{At: at})
	if v := out.Verdicts[0]; v.Status != policy.Rejected || !strings.Contains(v.Reason, "approved for different semantics") {
		t.Fatalf("digest mismatch: %+v", v)
	}
	// The digest is signed: editing it breaks the signature.
	tampered := w.exception("exc.tampered", "corpus", corpus, "rule.go.vet", "check.vet", "go.verify")
	tampered.RuleDigest = knowledge.RuleDigest(&knowledge.Record{Key: "x"})
	w.publish(tampered)
	if s := exceptionStatus(w.resolve("go.verify", nil), "exc.tampered"); s.Status != policy.Rejected || !strings.Contains(s.Reason, "signature does not verify") {
		t.Fatalf("tampered digest: %+v", s)
	}
}

func TestCorpusAuthorityAndIndependentDerivation(t *testing.T) {
	w := newWorld(t)
	w.publish(w.exception("exc.race.corpus", "corpus", knowledge.Authority{Level: knowledge.AuthorityCorpus}, "rule.go.race", "check.race", "go.verify.concurrent"))
	r := w.resolve("go.verify.concurrent", map[string]string{"concurrency": "true"})
	o := obligation(r, "check.race")
	if got := derivations(o); !slices.Equal(got, []string{"declared:go.verify.concurrent@go.verify.concurrent=active",
		"rule:rule.go.race@go.verify.concurrent=waived", "rule:rule.go.race@go.verify=waived"}) || o.Status != "required" {
		t.Fatalf("check.race: %v %s", got, o.Status)
	}
}

func TestInvalidExceptionsNeverApply(t *testing.T) {
	proj := knowledge.Authority{Level: knowledge.AuthorityProject, Repository: repo}
	for name, tc := range map[string]struct {
		build func(w *world) *knowledge.Record
		want  string
	}{
		"untrusted key": {func(w *world) *knowledge.Record {
			e := w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
			delete(w.trust.Keys, "project")
			return e
		}, `key "project" is not trusted`},
		"revoked approval": {func(w *world) *knowledge.Record {
			w.trust.Revoked["approval-exc"] = true
			return w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
		}, "is revoked"},
		"revoked key": {func(w *world) *knowledge.Record {
			w.trust.Revoked["project"] = true
			return w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
		}, `key "project" is revoked`},
		"expired": {func(w *world) *knowledge.Record {
			e := w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
			e.Approval.ValidFrom, e.Approval.ValidUntil = at.Add(-48*time.Hour), at.Add(-time.Hour)
			w.sign(e, "project")
			return e
		}, "expired"},
		"not yet valid": {func(w *world) *knowledge.Record {
			e := w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
			e.Approval.ValidFrom, e.Approval.ValidUntil = at.Add(time.Hour), at.Add(48*time.Hour)
			w.sign(e, "project")
			return e
		}, "not valid before"},
		"tampered": {func(w *world) *knowledge.Record {
			e := w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
			e.Justification = "Changed after approval"
			return e
		}, "signature does not verify"},
		"tampered scope": {func(w *world) *knowledge.Record {
			e := w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
			e.AppliesTo = []string{"go.verify", "go.verify.concurrent"}
			return e
		}, "signature does not verify"},
		"wrong authority": {func(w *world) *knowledge.Record {
			return w.exception("exc", "mathematics", proj, "rule.go.vet", "check.vet", "go.verify")
		}, "holds domain domain.mathematics authority, not the project"},
		"domain outside its domain": {func(w *world) *knowledge.Record {
			return w.exception("exc", "mathematics", knowledge.Authority{Level: knowledge.AuthorityDomain, Domain: "domain.mathematics"}, "rule.go.vet", "check.vet", "go.verify")
		}, "approved by domain domain.mathematics through the path [domain.software.go domain.mathematics], which this snapshot does not hold (domain.mathematics is not directly broader than domain.software.go)"},
		"old revision": {func(w *world) *knowledge.Record {
			e := w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
			rule := w.rule("rule.go.vet")
			rule.Revision = 2
			w.publish(rule)
			return e
		}, "approved for revision 1 of rule.go.vet, which is now at revision 2"},
		"not waivable": {func(w *world) *knowledge.Record {
			rule := w.rule("rule.go.vet")
			rule.Waivable, rule.Revision = false, 2
			w.publish(rule)
			return w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
		}, "is not waivable"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.publish(tc.build(w))
			r := w.resolveTrusted("go.verify", nil, map[string]string{"repository": repo})
			e := exceptionStatus(r, "exc")
			if e.Status != policy.Rejected || !strings.Contains(e.Reason, tc.want) {
				t.Fatalf("exception %+v, want rejected: %q", e, tc.want)
			}
			if o := obligation(r, "check.vet"); o.Status != "required" {
				t.Fatalf("check.vet %+v", o)
			}
		})
	}
}

func TestExceptionConditionsAndReplacement(t *testing.T) {
	w := newWorld(t)
	proj := knowledge.Authority{Level: knowledge.AuthorityProject, Repository: repo}
	e := w.exception("exc.vet.ci", "project", proj, "rule.go.vet", "check.vet", "go.verify")
	e.Conditions, e.Replacement = []knowledge.Condition{{Name: "ci", Value: "true"}}, "check.lint"
	w.sign(e, "project")
	w.publish(e)
	trusted := map[string]string{"repository": repo}
	r := w.resolveTrusted("go.verify", nil, trusted)
	if s := exceptionStatus(r, "exc.vet.ci"); s.Status != policy.NotApplicable || !strings.Contains(s.Reason, `requires trusted context ci = "true"`) {
		t.Fatalf("without ci: %+v", s)
	}
	r = w.resolveTrusted("go.verify", nil, map[string]string{"repository": repo, "ci": "true"})
	if o := obligation(r, "check.vet"); o.Status != "waived" || o.Derivations[0].Status != "replaced" {
		t.Fatalf("check.vet %+v", o)
	}
	if got := derivations(obligation(r, "check.lint")); !slices.Contains(got, "exception:exc.vet.ci@go.verify=active") {
		t.Fatalf("check.lint %v", got)
	}
}

func TestConflictingExceptions(t *testing.T) {
	w := newWorld(t)
	proj := knowledge.Authority{Level: knowledge.AuthorityProject, Repository: repo}
	waive := w.exception("exc.a", "project", proj, "rule.go.vet", "check.vet", "go.verify")
	replace := w.exception("exc.b", "project", proj, "rule.go.vet", "check.vet", "go.verify")
	replace.Replacement = "check.lint"
	w.sign(replace, "project")
	w.publish(waive, replace)
	r := w.resolveTrusted("go.verify", nil, map[string]string{"repository": repo})
	for _, k := range []string{"exc.a", "exc.b"} {
		if s := exceptionStatus(r, k); s.Status != policy.Conflicting {
			t.Fatalf("%s %+v", k, s)
		}
	}
	if o := obligation(r, "check.vet"); o.Status != "required" {
		t.Fatalf("check.vet %+v", o)
	}
}

func TestComposition(t *testing.T) {
	w := newWorld(t)
	prod := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "artifact.build", Label: "artifact.build", Definition: "Build a release binary.", ClassifiedAs: []string{"domain.software"},
		Outputs: []knowledge.Port{{Name: "binary", Concept: "artifact.binary"}}, OutputsComplete: true, Postconditions: []string{"state.built", "network.forbidden"}}
	cons := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "artifact.smoke", Label: "artifact.smoke", Definition: "Smoke-test a release binary.", ClassifiedAs: []string{"domain.software"},
		Inputs: []knowledge.Port{{Name: "binary", Concept: "artifact.binary"}}, Preconditions: []string{"state.built"}, Postconditions: []string{"state.smoked"}}
	compose := func(p, q string) *reasoning.CompositionResult {
		t.Helper()
		c, err := w.kernel.At(context.Background(), w.snap)
		if err != nil {
			t.Fatal(err)
		}
		r, err := reasoning.Compose(context.Background(), c, ev, p, q, reasoning.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := compose("artifact.build", "artifact.smoke"); r.Verdict != reasoning.Compatible {
		t.Fatalf("build -> smoke: %+v", r)
	}
	// Through resolve: the goal composes with then, contracts included.
	c, _ := w.kernel.At(context.Background(), w.snap)
	res, err := reasoning.Resolve(context.Background(), c, ev, w.trust, reasoning.Request{Goal: "artifact.build", Then: "artifact.deploy", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if cp := res.Composition; cp == nil || cp.Verdict != reasoning.Incompatible || cp.Producer.Key != "artifact.build" || !cp.Producer.OutputsComplete ||
		cp.Consumer.Inputs[0].Concept != "artifact.image" || cp.Findings[0].Status != "missing" {
		t.Fatalf("resolve then: %+v", res.Composition)
	}
	for _, then := range []string{"nope", "check.vet"} {
		if _, err := reasoning.Resolve(context.Background(), c, ev, w.trust, reasoning.Request{Goal: "artifact.build", Then: then, At: at}); !errors.Is(err, reasoning.ErrUnknownGoal) {
			t.Fatalf("then %s: %v", then, err)
		}
	}
	if r := compose("artifact.build", "artifact.deploy"); r.Verdict != reasoning.Incompatible || r.Findings[0].Status != "missing" {
		t.Fatalf("build -> deploy: %+v", r)
	}
	if r := compose("artifact.smoke", "artifact.review"); r.Verdict != reasoning.Undetermined {
		t.Fatalf("smoke -> review: %+v", r)
	}
	if r := compose("artifact.smoke", "artifact.deploy"); r.Verdict != reasoning.Undetermined {
		t.Fatalf("smoke -> deploy (outputs not declared complete): %+v", r)
	}
	cons.Preconditions = []string{"state.built", "network.required"}
	w.publish(prod, cons)
	if r := compose("artifact.build", "artifact.smoke"); r.Verdict != reasoning.Incompatible ||
		!slices.ContainsFunc(r.Findings, func(f reasoning.Finding) bool { return f.Item == "network.required" && f.Status == "contradicted" }) {
		t.Fatalf("contradicted precondition: %+v", r)
	}
}

func TestPublicationRefusesUnsafeRules(t *testing.T) {
	w := newWorld(t)
	for name, program := range map[string]string{
		"negation":  `obligation(P, "check.vet") :- record(P, "Procedure"), !specializes(P, "go.verify").`,
		"aggregate": `obligation(P, N) :- record(P, T) |> do fn:group_by(P), let N = fn:count().`,
		"helper":    `helper(P) :- record(P, "Procedure").`,
	} {
		r := &knowledge.Record{Type: knowledge.TypeRule, Key: "rule.bad", Label: name, Statement: name, Program: program, Revision: 1}
		_, err := w.kernel.Publish(context.Background(), knowledgetest.WithFullReview(knowledge.Proposal{Base: w.snap, Records: []knowledge.RecordChange{{Key: r.Key, Record: r}},
			Provenance: knowledge.Provenance{Author: "test.agent", Summary: name, Rationale: "test"}}))
		if !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "rule rule.bad") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestUnknownGoal(t *testing.T) {
	w := newWorld(t)
	cat, _ := w.kernel.At(context.Background(), w.snap)
	if _, err := reasoning.Resolve(context.Background(), cat, ev, w.trust, reasoning.Request{Goal: "nope", At: at}); !errors.Is(err, reasoning.ErrUnknownGoal) {
		t.Fatalf("err = %v", err)
	}
}

func TestTrustMaterial(t *testing.T) {
	pub := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey))
	tr, err := policy.ParseTrust([]byte(`{"keys":[{"id":"p","public_key":"` + pub + `","authority":{"level":"project","repository":"` + repo + `"}}],"revoked":["x"]}`))
	if err != nil || tr.Keys["p"].Authority.Repository != repo || !tr.Revoked["x"] {
		t.Fatalf("%+v %v", tr, err)
	}
	for _, bad := range []string{`{"keys":[{"id":"p","public_key":"AAAA","authority":{"level":"project"}}]}`, `{"keys":[],"extra":1}`,
		`{"keys":[{"id":"p","public_key":"` + pub + `","authority":{"level":"root"}}]}`} {
		if _, err := policy.ParseTrust([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
