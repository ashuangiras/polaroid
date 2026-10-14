package knowledgetest

import (
	"encoding/json/jsontext"

	"github.com/ashuangiras/polaroid/internal/knowledge"
)

// SharedProc is a shared procedure with minimal guidance.
func SharedProc(key, goal string) *knowledge.Procedure {
	return &knowledge.Procedure{
		Key: key, Goal: goal, Philosophy: "Verify what you change.", Method: "Follow the steps.",
		Applicability: knowledge.Applicability{Scope: knowledge.ScopeShared},
		Contract:      jsontext.Value(`{}`), Instructions: jsontext.Value(`{"steps":[]}`),
	}
}

// CorpusProcedures are the procedures the cross-domain corpus describes.
func CorpusProcedures() []knowledge.Change {
	var out []knowledge.Change
	for _, p := range [][2]string{
		{"change.verify", "Verify a change."},
		{"build.verify", "Verify that a change builds."},
		{"test.verify", "Verify that a change passes its tests."},
		{"ci.verify", "Verify a change the way continuous integration does."},
		{"go.verify", "Verify a Go change."},
		{"go.verify.concurrent", "Verify a concurrent Go change."},
		{"audio.phase.analyze", "Analyse the phase of an audio signal."},
		{"artifact.build", "Build a release binary."},
		{"artifact.smoke", "Smoke-test a release binary."},
		{"artifact.deploy", "Deploy a container image."},
		{"artifact.review", "Review a release by reading it."},
	} {
		out = append(out, knowledge.Change{Key: p[0], Procedure: SharedProc(p[0], p[1])})
	}
	return out
}

func rec(t knowledge.RecordType, key, label string) *knowledge.Record {
	return &knowledge.Record{Type: t, Key: key, Label: label}
}

// CorpusRecords is a small cross-domain corpus: software verification that
// specializes into Go and concurrent Go, a diamond, mathematics reused by
// audio, contested scientific evidence, protected and waivable rules,
// contradictory requirements and composable procedure contracts.
func CorpusRecords() []*knowledge.Record {
	var rs []*knowledge.Record
	add := func(r *knowledge.Record) *knowledge.Record { rs = append(rs, r); return r }

	add(rec(knowledge.TypeDomain, "domain.software", "Software engineering"))
	add(rec(knowledge.TypeDomain, "domain.software.go", "Go software")).Broader = []string{"domain.software"}
	add(rec(knowledge.TypeDomain, "domain.mathematics", "Mathematics"))
	add(rec(knowledge.TypeDomain, "domain.audio", "Audio signal processing"))
	add(rec(knowledge.TypeDomain, "domain.science", "Empirical science"))

	for _, c := range [][2]string{
		{"check.build", "The change builds"}, {"check.tests", "The tests pass"}, {"check.lint", "The linter passes"},
		{"check.vet", "go vet passes"}, {"check.race", "The race detector passes"}, {"check.leak", "No goroutine leaks"},
		{"check.review", "A person reviewed the change"},
		{"network.forbidden", "Runs without network access"}, {"network.required", "Needs network access"},
		{"artifact.binary", "A release binary"}, {"artifact.image", "A container image"}, {"artifact.report", "A test report"},
		{"state.built", "The artifact is built"}, {"state.smoked", "The artifact passed a smoke test"},
		{"state.signed", "The artifact is signed"},
	} {
		r := add(rec(knowledge.TypeConcept, c[0], c[1]))
		r.ClassifiedAs = []string{"domain.software"}
	}
	for _, r := range rs {
		switch r.Key {
		case "network.forbidden":
			r.Contradicts = []string{"network.required"}
		case "check.vet", "check.race", "check.leak":
			r.ClassifiedAs = []string{"domain.software.go"}
		}
	}

	proc := func(key string, domains []string, parents ...string) *knowledge.Record {
		r := add(rec(knowledge.TypeProcedure, key, key))
		r.ClassifiedAs, r.Specializes = domains, parents
		return r
	}
	sw, gosw := []string{"domain.software"}, []string{"domain.software.go"}
	proc("change.verify", sw).Requires = []string{"check.build", "check.tests"}
	proc("build.verify", sw, "change.verify").Requires = []string{"check.build", "network.forbidden"}
	proc("test.verify", sw, "change.verify").Requires = []string{"check.tests", "network.required"}
	proc("ci.verify", sw, "build.verify", "test.verify").Requires = []string{"check.lint"}
	proc("go.verify", gosw, "change.verify").Requires = []string{"check.lint"}
	proc("go.verify.concurrent", gosw, "go.verify").Requires = []string{"check.race", "check.leak"}

	b := proc("artifact.build", sw)
	b.Outputs, b.OutputsComplete = []knowledge.Port{{Name: "binary", Concept: "artifact.binary"}}, true
	b.Postconditions = []string{"state.built"}
	s := proc("artifact.smoke", sw)
	s.Inputs = []knowledge.Port{{Name: "binary", Concept: "artifact.binary"}}
	s.Preconditions, s.Postconditions = []string{"state.built"}, []string{"state.smoked"}
	d := proc("artifact.deploy", sw)
	d.Inputs = []knowledge.Port{{Name: "image", Concept: "artifact.image"}}
	d.Preconditions = []string{"state.built"}
	rv := proc("artifact.review", sw)
	rv.Inputs = []knowledge.Port{{Name: "release"}}
	rv.InformalConditions = []string{"The release notes read well"}

	add(rec(knowledge.TypeConcept, "math.phase", "Phase of a complex number")).ClassifiedAs = []string{"domain.mathematics"}
	claim := add(rec(knowledge.TypeClaim, "math.phase.atan2", "Phase is atan2 of the imaginary and real parts"))
	claim.ClassifiedAs, claim.Statement = []string{"domain.mathematics"}, "The phase of z = a + bi is atan2(b, a)."
	claim.Asserts = []knowledge.Assertion{{Subject: "phase", Predicate: "computed_by", Object: "atan2(imaginary, real)"}}
	claim.Uses = []string{"math.phase"}
	ap := add(rec(knowledge.TypeConcept, "audio.phase", "Phase of an audio signal's spectrum"))
	ap.ClassifiedAs, ap.Uses = []string{"domain.audio"}, []string{"math.phase.atan2"}
	proc("audio.phase.analyze", []string{"domain.audio"}).Uses = []string{"audio.phase"}

	sc := add(rec(knowledge.TypeClaim, "science.coffee.longevity", "Coffee and longevity"))
	sc.ClassifiedAs, sc.Statement = []string{"domain.science"}, "Moderate coffee consumption is associated with longer life."
	for key, rel := range map[string]string{"science.study.cohort": "supports", "science.study.trial": "contradicts"} {
		e := add(rec(knowledge.TypeEvidence, key, key))
		e.ClassifiedAs, e.Statement, e.Source = []string{"domain.science"}, "A study about "+sc.Key+".", "Fictional citation for "+key
		if rel == "supports" {
			e.Supports = []string{sc.Key}
		} else {
			e.Contradicts = []string{sc.Key}
		}
	}

	rule := func(key, statement, program string, protected bool, domains ...string) {
		r := add(rec(knowledge.TypeRule, key, statement))
		r.Statement, r.Program, r.Revision, r.Protected, r.Waivable, r.ClassifiedAs = statement, program, 1, protected, !protected, domains
	}
	rule("rule.software.tests", "Every software procedure requires passing tests.",
		`obligation(P, "check.tests") :- record(P, "Procedure"), in_domain(P, "domain.software").`, true, "domain.software")
	rule("rule.go.vet", "Every Go procedure requires go vet.",
		`obligation(P, "check.vet") :- record(P, "Procedure"), in_domain(P, "domain.software.go").`, false, "domain.software.go")
	rule("rule.go.race", "Concurrent Go code requires the race detector.",
		`obligation(P, "check.race") :- record(P, "Procedure"), in_domain(P, "domain.software.go"), context("concurrency", "true").`, false, "domain.software.go")
	rule("rule.audio.phase.method", "Audio phase analysis computes phase with the mathematical relationship it uses.",
		`derived(P, "phase_method", M) :- record(P, "Procedure"), uses(P, C), uses(C, Claim), asserts(Claim, "phase", "computed_by", M).`, false, "domain.audio")
	return rs
}

// CorpusRecordChanges are CorpusRecords as publication changes.
func CorpusRecordChanges() []knowledge.RecordChange {
	var out []knowledge.RecordChange
	for _, r := range CorpusRecords() {
		out = append(out, knowledge.RecordChange{Key: r.Key, Record: r})
	}
	return out
}
