package reasoning_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/knowledge/knowledgetest"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
)

// A rollback restores earlier semantics under a new revision, so an
// approval given for the earlier revision does not apply again (ADR-0050).
func TestRollbackNeverRevalidatesApprovals(t *testing.T) {
	w := newWorld(t)
	w.publish(w.exception("exc.vet", "corpus", knowledge.Authority{Level: knowledge.AuthorityCorpus}, "rule.go.vet", "check.vet", "go.verify"))
	approved := w.snap
	if s := exceptionStatus(w.resolve("go.verify", nil), "exc.vet"); s.Status != policy.Applied {
		t.Fatalf("before: %+v", s)
	}
	original := w.rule("rule.go.vet")
	changed := original.Clone()
	changed.Program, changed.Revision = `obligation(P, "check.vet") :- record(P, "Procedure").`, 2
	w.publish(changed)
	target, err := w.kernel.At(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	s, err := w.kernel.Rollback(context.Background(), w.snap, approved,
		knowledge.Provenance{Author: "test.agent", Summary: "roll back", Rationale: "test"}, knowledgetest.ReviewCatalog(target))
	if err != nil {
		t.Fatal(err)
	}
	w.snap = s
	restored := w.rule("rule.go.vet")
	if restored.Revision != 3 || knowledge.RuleDigest(restored) != knowledge.RuleDigest(original) {
		t.Fatalf("restored %+v", restored)
	}
	r := w.resolve("go.verify", nil)
	if e := exceptionStatus(r, "exc.vet"); e.Status != policy.Rejected || !strings.Contains(e.Reason, "approved for revision 1 of rule.go.vet, which is now at revision 3") {
		t.Fatalf("after rollback: %+v", e)
	}
	if o := obligation(r, "check.vet"); o.Status != "required" {
		t.Fatalf("check.vet %+v", o)
	}
}

// A domain approval covers exactly the domain path it signed: reclassifying
// the hierarchy never silently widens it, a broken path needs a new
// approval, unrelated changes do not disturb it, and a past snapshot still
// resolves as it did (ADR-0050).
func TestDomainAuthorityIsBoundToItsPath(t *testing.T) {
	w := newWorld(t)
	software := knowledge.Authority{Level: knowledge.AuthorityDomain, Domain: "domain.software"}
	maths := knowledge.Authority{Level: knowledge.AuthorityDomain, Domain: "domain.mathematics", Path: []string{"domain.mathematics"}}
	sw := w.exception("exc.vet.software", "software", software, "rule.go.vet", "check.vet", "go.verify")
	if !slices.Equal(sw.Authority.Path, []string{"domain.software.go", "domain.software"}) {
		t.Fatalf("signed path %v", sw.Authority.Path)
	}
	w.publish(sw, w.exception("exc.vet.maths", "mathematics", maths, "rule.go.vet", "check.vet", "go.verify"))
	original := w.snap
	digest := knowledge.RuleDigest(w.rule("rule.go.vet"))
	first, err := json.Marshal(w.resolve("go.verify", nil))
	if err != nil {
		t.Fatal(err)
	}
	reparent := func(broader ...string) {
		t.Helper()
		d := w.rule("domain.software.go")
		d.Broader = broader
		w.publish(d)
	}
	expect := func(step, swStatus, swReason, mathsStatus, vet string) {
		t.Helper()
		r := w.resolve("go.verify", nil)
		if e := exceptionStatus(r, "exc.vet.software"); e.Status != swStatus || !strings.Contains(e.Reason, swReason) {
			t.Errorf("%s: software %+v", step, e)
		}
		if e := exceptionStatus(r, "exc.vet.maths"); e.Status != mathsStatus || !strings.Contains(e.Reason, "rule.go.vet is not classified in domain.mathematics") {
			t.Errorf("%s: mathematics %+v", step, e)
		}
		if o := obligation(r, "check.vet"); o.Status != vet {
			t.Errorf("%s: check.vet %s", step, o.Status)
		}
		if knowledge.RuleDigest(w.rule("rule.go.vet")) != digest {
			t.Errorf("%s: the rule's identity changed with the taxonomy", step)
		}
	}
	expect("original", policy.Applied, "", policy.Rejected, "waived")
	reparent("domain.software", "domain.mathematics")
	expect("also beneath mathematics", policy.Applied, "", policy.Rejected, "waived")
	reparent("domain.mathematics")
	expect("moved beneath mathematics", policy.Rejected, "domain.software is not directly broader than domain.software.go); a changed hierarchy needs a new approval", policy.Rejected, "required")
	reparent()
	expect("ancestry removed", policy.Rejected, "domain.software is not directly broader than domain.software.go", policy.Rejected, "required")
	reparent("domain.software")
	expect("taxonomy restored", policy.Applied, "", policy.Rejected, "waived")

	cat, err := w.kernel.At(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reasoning.Resolve(context.Background(), cat, ev, w.trust, reasoning.Request{Goal: "go.verify", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(again); string(b) != string(first) {
		t.Fatalf("the original snapshot resolves differently now:\n%s\n%s", first, b)
	}
}

// Trusted context comes only from signed assertions that verify with an
// attestation key for their repository and names, during their validity,
// and agree with each other and with the caller's context (ADR-0050).
func TestTrustedContextFromAssertions(t *testing.T) {
	w := newWorld(t)
	attest := func(id, repository string, names ...string) ed25519.PrivateKey {
		priv := ed25519.NewKeyFromSeed(slices.Repeat([]byte(id[:1]), ed25519.SeedSize))
		w.keys[id] = priv
		w.trust.Keys[id] = policy.Key{ID: id, Public: priv.Public().(ed25519.PublicKey), Attests: &policy.Attestation{Repository: repository, Names: names}}
		return priv
	}
	attest("ci", repo, "ci")
	attest("other.ci", "github.com/other/app", "ci")
	assertion := func(key, repository string, facts map[string]string) policy.Assertion {
		a := policy.Assertion{Reference: "assert-" + key, Key: key, Repository: repository, Facts: facts,
			ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}
		sig, err := policy.SignAssertion(w.keys[key], a)
		if err != nil {
			t.Fatal(err)
		}
		a.Signature = sig
		return a
	}
	cond := w.exception("exc.race.ci", "corpus", knowledge.Authority{Level: knowledge.AuthorityCorpus}, "rule.go.race", "check.race", "go.verify.concurrent")
	cond.Conditions = []knowledge.Condition{{Name: "ci", Value: "true"}}
	w.sign(cond, "corpus")
	w.publish(w.exception("exc.vet", "project", knowledge.Authority{Level: knowledge.AuthorityProject, Repository: repo}, "rule.go.vet", "check.vet", "go.verify"), cond)
	establish := func(as ...policy.Assertion) (map[string]string, error) { return policy.Establish(w.trust, as, at) }

	// A valid project assertion activates the project's exception, and only there.
	trusted, err := establish(assertion("ci", repo, map[string]string{"ci": "true"}))
	if err != nil || trusted["repository"] != repo || trusted["ci"] != "true" || len(trusted) != 2 {
		t.Fatalf("establish: %v %v", trusted, err)
	}
	r := w.resolveTrusted("go.verify", nil, trusted)
	if e := exceptionStatus(r, "exc.vet"); e.Status != policy.Applied || r.Trusted["repository"] != repo {
		t.Fatalf("own project: %+v trusted %v", e, r.Trusted)
	}
	other, err := establish(assertion("other.ci", "github.com/other/app", nil))
	if err != nil {
		t.Fatal(err)
	}
	if e := exceptionStatus(w.resolveTrusted("go.verify", nil, other), "exc.vet"); e.Status != policy.NotApplicable ||
		!strings.Contains(e.Reason, "granted for repository github.com/team/app") {
		t.Fatalf("another project: %+v", e)
	}

	// Conditional exceptions: a known trusted fact, an unknown one, and a
	// caller who contradicts the trusted fact.
	r = w.resolveTrusted("go.verify.concurrent", map[string]string{"concurrency": "true"}, trusted)
	if e := exceptionStatus(r, "exc.race.ci"); e.Status != policy.Applied {
		t.Fatalf("known fact: %+v", e)
	}
	noCI, err := establish(assertion("ci", repo, nil))
	if err != nil {
		t.Fatal(err)
	}
	r = w.resolveTrusted("go.verify.concurrent", map[string]string{"concurrency": "true", "ci": "true"}, noCI)
	if e := exceptionStatus(r, "exc.race.ci"); e.Status != policy.NotApplicable || !strings.Contains(e.Reason, "requires trusted context ci") {
		t.Fatalf("unknown fact: %+v", e)
	}
	cat, _ := w.kernel.At(context.Background(), w.snap)
	if _, err := reasoning.Resolve(context.Background(), cat, ev, w.trust, reasoning.Request{Goal: "go.verify.concurrent",
		Context: map[string]string{"concurrency": "true", "ci": "false"}, Trusted: trusted, At: at}); !errors.Is(err, reasoning.ErrContextConflict) {
		t.Fatalf("contradicting caller: %v", err)
	}

	// Refused assertions establish nothing.
	expired := assertion("ci", repo, map[string]string{"ci": "true"})
	expired.ValidFrom, expired.ValidUntil = at.Add(-2*time.Hour), at.Add(-time.Hour)
	expired.Signature, _ = policy.SignAssertion(w.keys["ci"], expired)
	tampered := assertion("ci", repo, map[string]string{"ci": "false"})
	tampered.Facts["ci"] = "true"
	wrongRepo := assertion("ci", "github.com/other/app", nil)
	notAttester := assertion("ci", repo, nil)
	notAttester.Key = "project"
	notAttester.Signature, _ = policy.SignAssertion(w.keys["project"], notAttester)
	revoked := assertion("ci", repo, nil)
	revoked.Reference = "assert-revoked"
	revoked.Signature, _ = policy.SignAssertion(w.keys["ci"], revoked)
	w.trust.Revoked["assert-revoked"] = true
	unknown := assertion("ci", repo, nil)
	unknown.Key = "nobody"
	unknown.Signature, _ = policy.SignAssertion(w.keys["ci"], unknown)
	early := assertion("ci", repo, nil)
	early.ValidFrom, early.ValidUntil = at.Add(time.Hour), at.Add(2*time.Hour)
	early.Signature, _ = policy.SignAssertion(w.keys["ci"], early)
	empty := assertion("ci", repo, nil)
	empty.ValidFrom, empty.ValidUntil = at.Add(time.Hour), at.Add(time.Hour)
	empty.Signature, _ = policy.SignAssertion(w.keys["ci"], empty)
	fractional := assertion("ci", repo, nil)
	fractional.ValidUntil = fractional.ValidUntil.Add(900 * time.Millisecond)
	for name, tc := range map[string]struct {
		as   []policy.Assertion
		want string
	}{
		"expired":           {[]policy.Assertion{expired}, "expired at"},
		"not yet valid":     {[]policy.Assertion{early}, "not valid before"},
		"empty validity":    {[]policy.Assertion{empty}, "its validity period is empty"},
		"fractional":        {[]policy.Assertion{fractional}, "validity times must be whole seconds"},
		"tampered":          {[]policy.Assertion{tampered}, "the signature does not verify"},
		"unauthorised name": {[]policy.Assertion{assertion("ci", repo, map[string]string{"environment": "prod"})}, `may not attest "environment"`},
		"other repository":  {[]policy.Assertion{wrongRepo}, "attests only for github.com/team/app"},
		"approval key":      {[]policy.Assertion{notAttester}, "is not an attestation key"},
		"unknown key":       {[]policy.Assertion{unknown}, "is not trusted"},
		"revoked":           {[]policy.Assertion{revoked}, "is revoked"},
		"inconsistent": {[]policy.Assertion{assertion("ci", repo, map[string]string{"ci": "true"}), assertion("ci", repo, map[string]string{"ci": "false"})},
			`ci = "false" contradicts "true"`},
		"two repositories": {[]policy.Assertion{assertion("ci", repo, nil), assertion("other.ci", "github.com/other/app", nil)}, "repository = "},
	} {
		if got, err := establish(tc.as...); !errors.Is(err, policy.ErrAssertion) || !strings.Contains(err.Error(), tc.want) || got != nil {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	if _, err := policy.Establish(w.trust, []policy.Assertion{assertion("ci", repo, nil)}, at.Add(2*time.Hour)); err == nil {
		t.Error("an assertion was accepted after its validity")
	}
}

// Policy judges the signed domain path itself, independently of the
// publication checks: the path is signed, must be present, must end at the
// approving domain, and only an approval key can approve.
func TestPolicyJudgesTheSignedPath(t *testing.T) {
	w := newWorld(t)
	vet := w.rule("rule.go.vet")
	rules := map[string]policy.Rule{vet.Key: {Key: vet.Key, Revision: vet.Revision, Digest: knowledge.RuleDigest(vet), Waivable: true,
		Classified: []string{"domain.software.go"}}}
	derivs := []policy.Derivation{{Subject: "go.verify", Requirement: "check.vet", Source: vet.Key, Via: "go.verify"}}
	req := policy.Request{At: at, Broader: map[string][]string{"domain.software.go": {"domain.software", "domain.tools"}, "domain.tools": {"domain.software"}}}
	exc := func(signer string, auth knowledge.Authority) *knowledge.Record {
		e := &knowledge.Record{Type: knowledge.TypeException, Key: "exc", Label: "exc", Excepts: []string{vet.Key}, Revision: vet.Revision,
			RuleDigest: knowledge.RuleDigest(vet), AppliesTo: []string{"go.verify"}, Waives: "check.vet", Justification: "j", Authority: &auth,
			Approval: &knowledge.Approval{Reference: "ref", Key: signer, ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}}
		w.sign(e, signer)
		return e
	}
	verdict := func(e *knowledge.Record) policy.Verdict {
		return policy.Decide(w.trust, []*knowledge.Record{e}, rules, derivs, req).Verdicts[0]
	}
	domain := func(path ...string) knowledge.Authority {
		return knowledge.Authority{Level: knowledge.AuthorityDomain, Domain: "domain.software", Path: path}
	}
	direct, via := []string{"domain.software.go", "domain.software"}, []string{"domain.software.go", "domain.tools", "domain.software"}
	for _, p := range [][]string{direct, via} {
		if v := verdict(exc("software", domain(p...))); v.Status != policy.Applied {
			t.Fatalf("path %v: %+v", p, v)
		}
	}
	tampered := exc("software", domain(direct...))
	tampered.Authority.Path = via
	for name, tc := range map[string]struct {
		e    *knowledge.Record
		want string
	}{
		"path changed after signing": {tampered, "the approval signature does not verify"},
		"no path":                    {exc("software", domain()), "no path was approved"},
		"path ends elsewhere":        {exc("software", domain("domain.software.go", "domain.tools")), "the path does not end at the approving domain"},
	} {
		if v := verdict(tc.e); v.Status != policy.Rejected || !strings.Contains(v.Reason, tc.want) {
			t.Errorf("%s: %+v", name, v)
		}
	}
	priv := ed25519.NewKeyFromSeed(slices.Repeat([]byte{42}, ed25519.SeedSize))
	w.keys["ci"] = priv
	w.trust.Keys["ci"] = policy.Key{ID: "ci", Public: priv.Public().(ed25519.PublicKey), Attests: &policy.Attestation{Repository: repo}}
	if v := verdict(exc("ci", knowledge.Authority{})); v.Status != policy.Rejected || !strings.Contains(v.Reason, `key "ci" holds`) {
		t.Errorf("an attestation key approved an exception: %+v", v)
	}
}

// Supersession silences a rule, so it is part of the superseding rule's
// governed semantics: adding it takes a new revision; only a rule may
// supersede a rule (supersedes names a record of its own type), and only
// a protected rule a protected one (ADR-0050).
func TestSupersessionIsGoverned(t *testing.T) {
	w := newWorld(t)
	guard := &knowledge.Record{Type: knowledge.TypeRule, Key: "rule.software.guard", Label: "guard", Statement: "Software builds.", Revision: 1,
		Protected: true, ClassifiedAs: []string{"domain.software"}, Program: `obligation(P, "check.build") :- record(P, "Procedure"), in_domain(P, "domain.software").`}
	w.publish(guard)
	silencing := w.rule(guard.Key)
	silencing.Supersedes = []string{"rule.software.tests"}
	if err := w.publishErr(silencing); !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "the rule's semantics changed, so its revision must rise above 1") {
		t.Fatalf("supersession without a revision: %v", err)
	}
	if knowledge.RuleDigest(silencing) == knowledge.RuleDigest(guard) {
		t.Fatal("supersedes is not part of the rule digest")
	}
	using := w.rule(guard.Key)
	using.Uses = []string{"check.build"}
	if knowledge.RuleDigest(using) == knowledge.RuleDigest(guard) {
		t.Fatal("uses is not part of the rule digest")
	}
	concept := &knowledge.Record{Type: knowledge.TypeConcept, Key: "retired.vet", Label: "retired", Supersedes: []string{"rule.go.vet"}}
	if err := w.publishErr(concept); !errors.Is(err, knowledge.ErrInvalid) || !strings.Contains(err.Error(), "names the Rule rule.go.vet; it must name a Concept") {
		t.Fatalf("a concept superseding a rule: %v", err)
	}
	silencing.Revision = 2
	w.publish(silencing)
	if r := w.resolve("go.verify", nil); !slices.Contains(r.Superseded, "rule.software.tests") {
		t.Fatalf("governed supersession: %v", r.Superseded)
	}
}

func TestTrustMaterialRoles(t *testing.T) {
	pub := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey))
	tr, err := policy.ParseTrust([]byte(`{"keys":[{"id":"ci","public_key":"` + pub + `","attests":{"repository":"` + repo + `","names":["ci"]}}]}`))
	if err != nil || tr.Keys["ci"].Attests == nil || tr.Keys["ci"].Attests.Repository != repo {
		t.Fatalf("%+v %v", tr, err)
	}
	for _, bad := range []string{
		`{"keys":[{"id":"x","public_key":"` + pub + `","authority":{"level":"corpus"},"attests":{"repository":"r","names":[]}}]}`,
		`{"keys":[{"id":"x","public_key":"` + pub + `"}]}`,
		`{"keys":[{"id":"x","public_key":"` + pub + `","attests":{"repository":"r","names":["repository"]}}]}`,
		`{"keys":[{"id":"x","public_key":"` + pub + `","attests":{"repository":"","names":[]}}]}`,
	} {
		if _, err := policy.ParseTrust([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
