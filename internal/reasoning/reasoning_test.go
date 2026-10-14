package reasoning_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
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
	t      *testing.T
	kernel *knowledge.Kernel
	store  *knowledgetest.MemoryStore
	snap   knowledge.Snapshot
	keys   map[string]ed25519.PrivateKey
	trust  policy.Trust
}

func newWorld(t *testing.T) *world {
	t.Helper()
	v, err := semantic.New(semantic.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	store := knowledgetest.NewMemoryStore()
	w := &world{t: t, store: store, kernel: knowledge.New(store, knowledge.WithRecordValidator(v), knowledge.WithRecordValidator(reasoning.RuleValidator{Evaluator: ev})),
		keys: map[string]ed25519.PrivateKey{}, trust: policy.Trust{Keys: map[string]policy.Key{}, Revoked: map[string]bool{}}}
	w.snap, err = w.kernel.Publish(context.Background(), knowledge.Proposal{
		Changes: knowledgetest.CorpusProcedures(), Records: knowledgetest.CorpusRecordChanges(),
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "corpus", Rationale: "test"}})
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
	s, err := w.kernel.Publish(context.Background(), knowledge.Proposal{Base: w.snap, Records: changes,
		Provenance: knowledge.Provenance{Author: "test.agent", Summary: "more", Rationale: "test"}})
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
	e := &knowledge.Record{Type: knowledge.TypeException, Key: key, Label: key, Excepts: []string{rule}, Revision: 1,
		AppliesTo: appliesTo, Waives: waives, Justification: "Justified for " + key, Authority: &auth,
		Approval: &knowledge.Approval{Reference: "approval-" + key, Key: signer, ValidFrom: at.Add(-24 * time.Hour), ValidUntil: at.Add(24 * time.Hour)}}
	w.sign(e, signer)
	return e
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
	if len(r.Unknowns) != 1 || r.Unknowns[0].Requirement != "check.race" || r.Unknowns[0].Context != "concurrency" ||
		!slices.Equal(r.Unknowns[0].Values, []string{"true"}) || r.Unknowns[0].Source != "rule.go.race" {
		t.Fatalf("unknowns %+v", r.Unknowns)
	}
	if r2 := newWorld(t).resolve("go.verify", map[string]string{"concurrency": "false"}); len(r2.Unknowns) != 0 || obligation(r2, "check.race") != nil {
		t.Fatalf("with concurrency=false: %+v", r2)
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

	r := w.resolve("go.verify", map[string]string{"repository": repo})
	if o := obligation(r, "check.vet"); o == nil || o.Status != "waived" {
		t.Fatalf("check.vet %+v", o)
	}
	if e := exceptionStatus(r, "exc.vet"); e.Status != policy.Applied || len(e.Derivations) != 1 {
		t.Fatalf("exception %+v", e)
	}
	// The child's own derivation of check.vet is independent of go.verify's.
	r = w.resolve("go.verify.concurrent", map[string]string{"repository": repo, "concurrency": "true"})
	if got := derivations(obligation(r, "check.vet")); !slices.Equal(got, []string{
		"rule:rule.go.vet@go.verify.concurrent=active", "rule:rule.go.vet@go.verify=waived"}) || obligation(r, "check.vet").Status != "required" {
		t.Fatalf("check.vet derivations %v", got)
	}
	// Another repository is not covered.
	r = w.resolve("go.verify", map[string]string{"repository": "github.com/other/app"})
	if o := obligation(r, "check.vet"); o.Status != "required" || exceptionStatus(r, "exc.vet").Status != policy.NotApplicable {
		t.Fatalf("other repository: %+v %+v", o, r.Exceptions)
	}
}

func TestProtectedInvariant(t *testing.T) {
	w := newWorld(t)
	w.publish(
		w.exception("exc.tests.project", "project", knowledge.Authority{Level: knowledge.AuthorityProject, Repository: repo}, "rule.software.tests", "check.tests", "go.verify"),
		w.exception("exc.tests.domain", "software", knowledge.Authority{Level: knowledge.AuthorityDomain, Domain: "domain.software"}, "rule.software.tests", "check.tests", "go.verify"),
	)
	r := w.resolve("go.verify", map[string]string{"repository": repo})
	for _, k := range []string{"exc.tests.project", "exc.tests.domain"} {
		if e := exceptionStatus(r, k); e.Status != policy.Rejected || !strings.Contains(e.Reason, "protected invariant") {
			t.Fatalf("%s: %+v", k, e)
		}
	}
	if o := obligation(r, "check.tests"); o.Status != "required" || slices.ContainsFunc(o.Derivations, func(d reasoning.Derivation) bool { return d.Status != "active" }) {
		t.Fatalf("check.tests %+v", o)
	}
}

func TestCorpusAuthorityAndIndependentDerivation(t *testing.T) {
	w := newWorld(t)
	w.publish(w.exception("exc.tests.corpus", "corpus", knowledge.Authority{Level: knowledge.AuthorityCorpus}, "rule.software.tests", "check.tests", "change.verify"))
	r := w.resolve("change.verify", nil)
	o := obligation(r, "check.tests")
	if got := derivations(o); !slices.Equal(got, []string{"declared:change.verify@change.verify=active", "rule:rule.software.tests@change.verify=waived"}) || o.Status != "required" {
		t.Fatalf("check.tests: %v %s", got, o.Status)
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
		}, "is not in domain domain.mathematics"},
		"old revision": {func(w *world) *knowledge.Record {
			cat, _ := w.kernel.At(context.Background(), w.snap)
			rule, _ := cat.Record("rule.go.vet")
			rule.Revision = 2
			w.publish(rule)
			return w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
		}, "approved for revision 1 of rule.go.vet, which is now at revision 2"},
		"not waivable": {func(w *world) *knowledge.Record {
			cat, _ := w.kernel.At(context.Background(), w.snap)
			rule, _ := cat.Record("rule.go.vet")
			rule.Waivable = false
			w.publish(rule)
			return w.exception("exc", "project", proj, "rule.go.vet", "check.vet", "go.verify")
		}, "is not waivable"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.publish(tc.build(w))
			r := w.resolve("go.verify", map[string]string{"repository": repo})
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
	r := w.resolve("go.verify", map[string]string{"repository": repo})
	if s := exceptionStatus(r, "exc.vet.ci"); s.Status != policy.NotApplicable || !strings.Contains(s.Reason, `requires context ci = "true"`) {
		t.Fatalf("without ci: %+v", s)
	}
	r = w.resolve("go.verify", map[string]string{"repository": repo, "ci": "true"})
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
	r := w.resolve("go.verify", map[string]string{"repository": repo})
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
	prod := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "artifact.build", Label: "artifact.build", ClassifiedAs: []string{"domain.software"},
		Outputs: []knowledge.Port{{Name: "binary", Concept: "artifact.binary"}}, OutputsComplete: true, Postconditions: []string{"state.built", "network.forbidden"}}
	cons := &knowledge.Record{Type: knowledge.TypeProcedure, Key: "artifact.smoke", Label: "artifact.smoke", ClassifiedAs: []string{"domain.software"},
		Inputs: []knowledge.Port{{Name: "binary", Concept: "artifact.binary"}}, Preconditions: []string{"state.built"}, Postconditions: []string{"state.smoked"}}
	cat, _ := w.kernel.At(context.Background(), w.snap)
	compose := func(p, q string) *reasoning.CompositionResult {
		t.Helper()
		c, err := w.kernel.At(context.Background(), w.snap)
		if err != nil {
			t.Fatal(err)
		}
		r, err := reasoning.Compose(context.Background(), c, ev, p, q, nil, reasoning.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	_ = cat
	if r := compose("artifact.build", "artifact.smoke"); r.Verdict != reasoning.Compatible {
		t.Fatalf("build -> smoke: %+v", r)
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
		_, err := w.kernel.Publish(context.Background(), knowledge.Proposal{Base: w.snap, Records: []knowledge.RecordChange{{Key: r.Key, Record: r}},
			Provenance: knowledge.Provenance{Author: "test.agent", Summary: name, Rationale: "test"}})
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
