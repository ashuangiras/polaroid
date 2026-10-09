package memory

import (
	"encoding/json/jsontext"
	"slices"
	"strings"
	"testing"
)

func validExecution() ExecutionRecord {
	return ExecutionRecord{
		ProcedureID: "p1",
		Version:     2,
		Repository:  "github.com/ashuangiras/polaroid",
		Commit:      strings.Repeat("a1", 20),
		Environment: Environment{Name: "ci.ubuntu-latest", Attributes: jsontext.Value(`{"os": "linux", "go": "1.27.2"}`)},
		Inputs:      jsontext.Value(`{"module": "modernc.org/sqlite"}`),
		Outcome:     OutcomeSucceeded,
		Evidence:    jsontext.Value(`{"commands": [{"run": "make ci", "exit": 0}]}`),
	}
}

func executionFields(t *testing.T, edit func(*ExecutionRecord)) []string {
	t.Helper()
	r := validExecution()
	edit(&r)
	_, err := r.validate()
	if err == nil {
		return nil
	}
	return problemFields(t, err)
}

func TestValidExecutionIsCompacted(t *testing.T) {
	r, err := validExecution().validate()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(r.Environment.Attributes) + string(r.Inputs) + string(r.Evidence); got != `{"os":"linux","go":"1.27.2"}{"module":"modernc.org/sqlite"}{"commands":[{"run":"make ci","exit":0}]}` {
		t.Fatalf("stored form = %s", got)
	}
}

func TestExecutionRequiresEveryField(t *testing.T) {
	_, err := ExecutionRecord{}.validate()
	got := problemFields(t, err)
	want := []string{"procedure_id", "version", "repository", "commit", "environment.name", "environment.attributes", "inputs", "outcome", "evidence"}
	if !slices.Equal(got, want) {
		t.Fatalf("problem fields = %v, want %v", got, want)
	}
}

func TestExecutionFieldRules(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*ExecutionRecord)
		field string // "" means valid
	}{
		{"sha-256 commit", func(r *ExecutionRecord) { r.Commit = strings.Repeat("0f", 32) }, ""},
		{"abbreviated commit", func(r *ExecutionRecord) { r.Commit = "a1b2c3d" }, "commit"},
		{"uppercase commit", func(r *ExecutionRecord) { r.Commit = strings.Repeat("A1", 20) }, "commit"},
		{"41-character commit", func(r *ExecutionRecord) { r.Commit = strings.Repeat("a", 41) }, "commit"},
		{"non-hex commit", func(r *ExecutionRecord) { r.Commit = strings.Repeat("g", 40) }, "commit"},
		{"non-canonical repository", func(r *ExecutionRecord) { r.Repository = "https://github.com/o/r.git" }, "repository"},
		{"invalid environment name", func(r *ExecutionRecord) { r.Environment.Name = "CI Ubuntu" }, "environment.name"},
		{"empty attributes are fine", func(r *ExecutionRecord) { r.Environment.Attributes = jsontext.Value(`{}`) }, ""},
		{"attributes not an object", func(r *ExecutionRecord) { r.Environment.Attributes = jsontext.Value(`["linux"]`) }, "environment.attributes"},
		{"inputs not an object", func(r *ExecutionRecord) { r.Inputs = jsontext.Value(`"x"`) }, "inputs"},
		{"failed outcome", func(r *ExecutionRecord) { r.Outcome = OutcomeFailed }, ""},
		{"unknown outcome", func(r *ExecutionRecord) { r.Outcome = "passed" }, "outcome"},
		{"evidence not an object", func(r *ExecutionRecord) { r.Evidence = jsontext.Value(`[1]`) }, "evidence"},
		{"empty evidence", func(r *ExecutionRecord) { r.Evidence = jsontext.Value(` { } `) }, "evidence"},
		{"binding with revision", func(r *ExecutionRecord) { r.BindingID, r.BindingRevision = "b1", 1 }, ""},
		{"binding without revision", func(r *ExecutionRecord) { r.BindingID = "b1" }, "binding_revision"},
		{"revision without binding", func(r *ExecutionRecord) { r.BindingRevision = 1 }, "binding_id"},
		{"version zero", func(r *ExecutionRecord) { r.Version = 0 }, "version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := executionFields(t, tc.edit)
			switch {
			case tc.field == "" && got != nil:
				t.Fatalf("rejected: %v", got)
			case tc.field != "" && !slices.Equal(got, []string{tc.field}):
				t.Fatalf("problem fields = %v, want [%s]", got, tc.field)
			}
		})
	}
}
