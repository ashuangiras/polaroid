package memory

import (
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"
)

func validDefinition() Definition {
	return Definition{
		Philosophy:     "Prefer reversible steps.",
		Method:         "Plan, act, verify.",
		Contract:       jsontext.Value(`{"inputs": {"path": "string"}}`),
		Instructions:   jsontext.Value(`{"steps": ["inspect", "change", "verify"]}`),
		RevisionReason: "Initial version.",
	}
}

// problemFields returns the field names reported by a *ValidationError.
func problemFields(t *testing.T, err error) []string {
	t.Helper()
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	fields := make([]string, len(verr.Problems))
	for i, p := range verr.Problems {
		fields[i] = p.Field
	}
	return fields
}

func TestCanonicalKeyRules(t *testing.T) {
	accepted := []string{"a", "go.dependency.add", "repo-bootstrap", "sqlite_migrate.v2", "x1.y2-z3_w4", strings.Repeat("k", MaxCanonicalKeyLength)}
	for _, key := range accepted {
		if _, err := (NewProcedure{CanonicalKey: key, Definition: validDefinition()}).validate(); err != nil {
			t.Errorf("key %q rejected: %v", key, err)
		}
	}
	rejected := []string{"", "Go.Dependency", "go..dep", ".go", "go.", "go/dep", "go dep", "gö", "-a", "a--b", strings.Repeat("k", MaxCanonicalKeyLength+1)}
	for _, key := range rejected {
		_, err := (NewProcedure{CanonicalKey: key, Definition: validDefinition()}).validate()
		if got := problemFields(t, err); len(got) != 1 || got[0] != "canonical_key" {
			t.Errorf("key %q: problem fields = %v, want [canonical_key]", key, got)
		}
	}
}

func TestDefinitionRequiresEveryEnvelopeField(t *testing.T) {
	_, err := (NewProcedure{CanonicalKey: "ok"}).validate()
	got := strings.Join(problemFields(t, err), ",")
	want := "version.philosophy,version.method,version.contract,version.instructions,version.revision_reason"
	if got != want {
		t.Fatalf("problem fields = %s, want %s", got, want)
	}

	blank := validDefinition()
	blank.Philosophy = " \t\n"
	_, err = (NewProcedure{CanonicalKey: "ok", Definition: blank}).validate()
	if got := problemFields(t, err); len(got) != 1 || got[0] != "version.philosophy" {
		t.Fatalf("whitespace-only philosophy: problem fields = %v", got)
	}
}

func TestContractAndInstructionsMustBeJSONObjects(t *testing.T) {
	for _, raw := range []string{`[]`, `null`, `"text"`, `42`, `true`, `{"a":1,"a":2}`, `{"a":`, `{} {}`} {
		def := validDefinition()
		def.Contract = jsontext.Value(raw)
		def.Instructions = jsontext.Value(raw)
		_, err := (Revision{BaseVersion: 1, Definition: def}).validate()
		got := strings.Join(problemFields(t, err), ",")
		if got != "version.contract,version.instructions" {
			t.Errorf("%s: problem fields = %s", raw, got)
		}
	}
}

func TestDefinitionObjectsAreCompactedButOtherwisePreserved(t *testing.T) {
	const original = "{ \"z\" : 1,\n \"a\" : [ \"x\", {\"k\": null} ] }"
	def := validDefinition()
	def.Contract = jsontext.Value(original)
	got, err := (NewProcedure{CanonicalKey: "ok", Definition: def}).validate()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"z":1,"a":["x",{"k":null}]}`; string(got.Contract) != want {
		t.Fatalf("contract = %s, want %s (member order and values preserved)", got.Contract, want)
	}
	if string(def.Contract) != original {
		t.Fatalf("validate modified the caller's contract: %s", def.Contract)
	}
}

func TestRevisionRequiresPositiveBaseVersion(t *testing.T) {
	for _, base := range []int{0, -1} {
		_, err := (Revision{BaseVersion: base, Definition: validDefinition()}).validate()
		if got := problemFields(t, err); len(got) != 1 || got[0] != "base_version" {
			t.Errorf("base %d: problem fields = %v", base, got)
		}
	}
	if _, err := (Revision{BaseVersion: 1, Definition: validDefinition()}).validate(); err != nil {
		t.Fatalf("valid revision rejected: %v", err)
	}
}

func TestValidationErrorListsEveryProblem(t *testing.T) {
	err := &ValidationError{Problems: []FieldProblem{{"a", "is required"}, {"b", "must be a JSON object"}}}
	if got, want := err.Error(), "invalid input: a is required; b must be a JSON object"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
