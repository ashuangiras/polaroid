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

// rec builds a typed record with a definition, meeting ADR-0057's
// completeness bar (#164): every test corpus using these must publish
// successfully now that Kernel.publish enforces it.
func rec(t knowledge.RecordType, key, label, definition string) *knowledge.Record {
	return &knowledge.Record{Type: t, Key: key, Label: label, Definition: definition}
}

// CorpusRecords is a small cross-domain corpus: software verification that
// specializes into Go and concurrent Go, a diamond, mathematics reused by
// audio, contested scientific evidence, protected and waivable rules,
// contradictory requirements and composable procedure contracts.
func CorpusRecords() []*knowledge.Record {
	var rs []*knowledge.Record
	add := func(r *knowledge.Record) *knowledge.Record { rs = append(rs, r); return r }

	add(rec(knowledge.TypeDomain, "domain.software", "Software engineering", "The design, construction, verification and maintenance of computer software."))
	add(rec(knowledge.TypeDomain, "domain.software.go", "Go software", "Software written in the Go programming language.")).Broader = []string{"domain.software"}
	add(rec(knowledge.TypeDomain, "domain.mathematics", "Mathematics", "The study of quantity, structure and their formal relationships."))
	add(rec(knowledge.TypeDomain, "domain.audio", "Audio signal processing", "The analysis and transformation of recorded or synthesized sound."))
	add(rec(knowledge.TypeDomain, "domain.science", "Empirical science", "Knowledge built and tested by systematic observation and experiment."))

	for _, c := range [][3]string{
		{"check.build", "The change builds", "The change compiles or assembles without error."},
		{"check.tests", "The tests pass", "Every automated test of the change passes."},
		{"check.lint", "The linter passes", "The configured linter reports no issues."},
		{"check.vet", "go vet passes", "Go's go vet static-analysis tool reports no findings."},
		{"check.race", "The race detector passes", "The tests pass under the race detector, with no race condition detected."},
		{"check.leak", "No goroutine leaks", "No goroutine started by a test outlives it."},
		{"check.review", "A person reviewed the change", "A person read the change and judged it ready."},
		{"network.forbidden", "Runs without network access", "The procedure completes with no outbound network access."},
		{"network.required", "Needs network access", "The procedure needs outbound network access to complete."},
		{"artifact.binary", "A release binary", "An executable built for release."},
		{"artifact.image", "A container image", "A container image built for release."},
		{"artifact.report", "A test report", "A report of a test run's results."},
		{"state.built", "The artifact is built", "The release artifact has been built from the source state being released."},
		{"state.smoked", "The artifact passed a smoke test", "The built artifact ran a basic smoke test successfully."},
		{"state.signed", "The artifact is signed", "The built artifact carries a valid release signature."},
	} {
		r := add(rec(knowledge.TypeConcept, c[0], c[1], c[2]))
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

	proc := func(key, definition string, domains []string, parents ...string) *knowledge.Record {
		r := add(rec(knowledge.TypeProcedure, key, key, definition))
		r.ClassifiedAs, r.Specializes = domains, parents
		return r
	}
	sw, gosw := []string{"domain.software"}, []string{"domain.software.go"}
	proc("change.verify", "Verify a change.", sw).Requires = []string{"check.build", "check.tests"}
	proc("build.verify", "Verify that a change builds.", sw, "change.verify").Requires = []string{"check.build", "network.forbidden"}
	proc("test.verify", "Verify that a change passes its tests.", sw, "change.verify").Requires = []string{"check.tests", "network.required"}
	proc("ci.verify", "Verify a change the way continuous integration does.", sw, "build.verify", "test.verify").Requires = []string{"check.lint"}
	proc("go.verify", "Verify a Go change.", gosw, "change.verify").Requires = []string{"check.lint"}
	proc("go.verify.concurrent", "Verify a concurrent Go change.", gosw, "go.verify").Requires = []string{"check.race", "check.leak"}

	b := proc("artifact.build", "Build a release binary.", sw)
	b.Outputs, b.OutputsComplete = []knowledge.Port{{Name: "binary", Concept: "artifact.binary"}}, true
	b.Postconditions = []string{"state.built"}
	s := proc("artifact.smoke", "Smoke-test a release binary.", sw)
	s.Inputs = []knowledge.Port{{Name: "binary", Concept: "artifact.binary"}}
	s.Preconditions, s.Postconditions = []string{"state.built"}, []string{"state.smoked"}
	d := proc("artifact.deploy", "Deploy a container image.", sw)
	d.Inputs = []knowledge.Port{{Name: "image", Concept: "artifact.image"}}
	d.Preconditions = []string{"state.built"}
	rv := proc("artifact.review", "Review a release by reading it.", sw)
	rv.Inputs = []knowledge.Port{{Name: "release"}}
	rv.InformalConditions = []string{"The release notes read well"}
	rv.Preconditions = []string{"state.smoked"}

	add(rec(knowledge.TypeConcept, "math.phase", "Phase of a complex number", "The angle, in the complex plane, between a complex number and the positive real axis.")).ClassifiedAs = []string{"domain.mathematics"}
	claim := add(rec(knowledge.TypeClaim, "math.phase.atan2", "Phase is atan2 of the imaginary and real parts", ""))
	claim.ClassifiedAs, claim.Statement = []string{"domain.mathematics"}, "The phase of z = a + bi is atan2(b, a)."
	claim.Asserts = []knowledge.Assertion{{Subject: "phase", Predicate: "computed_by", Object: "atan2(imaginary, real)"}}
	claim.Uses = []string{"math.phase"}
	ap := add(rec(knowledge.TypeConcept, "audio.phase", "Phase of an audio signal's spectrum", "The phase component of an audio signal's frequency-domain representation."))
	ap.ClassifiedAs, ap.Uses = []string{"domain.audio"}, []string{"math.phase.atan2"}
	audioAnalyze := proc("audio.phase.analyze", "Analyse the phase of an audio signal.", []string{"domain.audio"})
	audioAnalyze.Uses = []string{"audio.phase"}
	audioAnalyze.Postconditions = []string{"audio.phase"}

	sc := add(rec(knowledge.TypeClaim, "science.coffee.longevity", "Coffee and longevity", ""))
	sc.ClassifiedAs, sc.Statement = []string{"domain.science"}, "Moderate coffee consumption is associated with longer life."
	for key, rel := range map[string]string{"science.study.cohort": "supports", "science.study.trial": "contradicts"} {
		e := add(rec(knowledge.TypeEvidence, key, key, ""))
		e.ClassifiedAs, e.Statement, e.Source = []string{"domain.science"}, "A study about "+sc.Key+".", "Fictional citation for "+key
		if rel == "supports" {
			e.Supports = []string{sc.Key}
		} else {
			e.Contradicts = []string{sc.Key}
		}
	}

	rule := func(key, statement, program string, protected bool, domains ...string) {
		r := add(rec(knowledge.TypeRule, key, statement, ""))
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
