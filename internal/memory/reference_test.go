package memory

import (
	"encoding/json/jsontext"
	"slices"
	"strings"
	"testing"
)

func validReference(name string) Reference {
	return Reference{
		Name:          name,
		ProcedureID:   "child",
		VersionPolicy: VersionPolicy{Kind: PolicyPin, Pin: 1},
		Inputs:        jsontext.Value(`{"module": {"input": "driver"}, "strict": {"value": true}}`),
	}
}

func definitionWith(refs ...Reference) Definition {
	d := validDefinition()
	d.References = refs
	return d
}

func TestReferencesAreOptional(t *testing.T) {
	for _, refs := range [][]Reference{nil, {}} {
		def, err := (Revision{BaseVersion: 1, Definition: definitionWith(refs...)}).validate()
		if err != nil {
			t.Fatalf("%#v rejected: %v", refs, err)
		}
		if def.References != nil {
			t.Fatalf("an empty reference list must be stored as none, got %#v", def.References)
		}
	}
}

func TestValidReferencesAreCompactedAndCopied(t *testing.T) {
	in := definitionWith(validReference("add-driver"), Reference{
		Name:          "migrate",
		ProcedureID:   "other",
		VersionPolicy: VersionPolicy{Kind: PolicyContextual},
		Inputs:        jsontext.Value(`{ "deep" : { "value" : { "z" : [1, "x"], "a" : null } } }`),
	})
	def, err := (NewProcedure{CanonicalKey: "ok", Definition: in}).validate()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(def.References[1].Inputs), `{"deep":{"value":{"z":[1,"x"],"a":null}}}`; got != want {
		t.Fatalf("inputs = %s, want %s (order and values kept)", got, want)
	}
	if &def.References[0] == &in.References[0] || string(in.References[1].Inputs) == string(def.References[1].Inputs) {
		t.Fatal("validate must not modify or alias the caller's references")
	}
}

func TestReferenceFieldRules(t *testing.T) {
	cases := []struct {
		name   string
		edit   func(*Reference)
		fields []string
	}{
		{"missing name", func(r *Reference) { r.Name = "" }, []string{"version.references[0].name"}},
		{"invalid name", func(r *Reference) { r.Name = "Add Driver" }, []string{"version.references[0].name"}},
		{"missing procedure", func(r *Reference) { r.ProcedureID = "" }, []string{"version.references[0].procedure_id"}},
		{"no policy", func(r *Reference) { r.VersionPolicy = VersionPolicy{} }, []string{"version.references[0].version_policy"}},
		{"pin zero", func(r *Reference) { r.VersionPolicy = VersionPolicy{Kind: PolicyPin} }, []string{"version.references[0].version_policy.pin"}},
		{"missing inputs", func(r *Reference) { r.Inputs = nil }, []string{"version.references[0].inputs"}},
		{"inputs not an object", func(r *Reference) { r.Inputs = jsontext.Value(`[]`) }, []string{"version.references[0].inputs"}},
		{"everything", func(r *Reference) { *r = Reference{} }, []string{
			"version.references[0].name", "version.references[0].procedure_id",
			"version.references[0].version_policy", "version.references[0].inputs",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validReference("add-driver")
			tc.edit(&r)
			_, err := (NewProcedure{CanonicalKey: "ok", Definition: definitionWith(r)}).validate()
			if got := problemFields(t, err); !slices.Equal(got, tc.fields) {
				t.Fatalf("problem fields = %v, want %v", got, tc.fields)
			}
		})
	}
}

func TestReferenceNamesAreUnique(t *testing.T) {
	refs := []Reference{validReference("a"), validReference("b"), validReference("a")}
	_, err := (NewProcedure{CanonicalKey: "ok", Definition: definitionWith(refs...)}).validate()
	if got := problemFields(t, err); !slices.Equal(got, []string{"version.references[2].name"}) {
		t.Fatalf("problem fields = %v", got)
	}
}

func TestInputMappingShape(t *testing.T) {
	accepted := []string{
		`{}`,
		`{"a": {"input": "x"}}`,
		`{"a": {"value": null}, "b": {"value": [1, {"input": "not a source"}]}, "c": {"value": ""}}`,
	}
	for _, inputs := range accepted {
		r := validReference("ref")
		r.Inputs = jsontext.Value(inputs)
		if _, err := (NewProcedure{CanonicalKey: "ok", Definition: definitionWith(r)}).validate(); err != nil {
			t.Errorf("%s rejected: %v", inputs, err)
		}
	}
	rejected := map[string][]string{
		`{"a": "x"}`:                        {"version.references[0].inputs.a"},
		`{"a": null}`:                       {"version.references[0].inputs.a"},
		`{"a": {}}`:                         {"version.references[0].inputs.a"},
		`{"a": {"input": "x", "value": 1}}`: {"version.references[0].inputs.a"},
		`{"a": {"input": ""}}`:              {"version.references[0].inputs.a"},
		`{"a": {"input": " "}}`:             {"version.references[0].inputs.a"},
		`{"a": {"input": 5}}`:               {"version.references[0].inputs.a"},
		`{"a": {"from": "x"}}`:              {"version.references[0].inputs.a"},
		`{"a": {"input": "x"}, "b": [], "c": {"value": 1}, "d": 1}`: {"version.references[0].inputs.b", "version.references[0].inputs.d"},
		`{"": {"value": 1}}`:                  {"version.references[0].inputs"},
		`{"a": {"input": "x", "input": "y"}}`: {"version.references[0].inputs"},
	}
	for inputs, want := range rejected {
		r := validReference("ref")
		r.Inputs = jsontext.Value(inputs)
		_, err := (NewProcedure{CanonicalKey: "ok", Definition: definitionWith(r)}).validate()
		if got := problemFields(t, err); !slices.Equal(got, want) {
			t.Errorf("%s: problem fields = %v, want %v", inputs, got, want)
		}
	}
}

func TestMissingTargetsBecomeFieldErrors(t *testing.T) {
	refs := []Reference{validReference("a"), validReference("b"), validReference("c")}
	err := missingTargets(refs, &MissingTargetsError{Missing: []MissingTarget{{Index: 0, Procedure: true}, {Index: 2}}})
	got := strings.Join(problemFields(t, err), ",")
	if want := "version.references[0].procedure_id,version.references[2].version_policy.pin"; got != want {
		t.Fatalf("problem fields = %s, want %s", got, want)
	}
	if !strings.Contains(err.Error(), `procedure "child" has no version 1`) {
		t.Fatalf("message = %q", err)
	}
}
