package memory

import (
	"encoding/json/jsontext"
	"strings"
	"testing"
)

func validBinding() NewBinding {
	return NewBinding{
		Repository:  "github.com/ashuangiras/polaroid",
		Name:        "add-dependency",
		ProcedureID: "p1",
		Config: BindingConfig{
			Inputs:         jsontext.Value(`{"module": "modernc.org/sqlite"}`),
			VersionPolicy:  VersionPolicy{Kind: PolicyPin, Pin: 1},
			RevisionReason: "Bind the shared procedure.",
		},
	}
}

func TestRepositoryIdentifierRules(t *testing.T) {
	accepted := []string{
		"scratch",
		"github.com/ashuangiras/polaroid",
		"github.com/ashuangiras/.github",
		"gitlab.example.com/group/sub-group/my--repo_2",
		strings.Repeat("r", MaxRepositoryLength),
	}
	for _, repo := range accepted {
		in := validBinding()
		in.Repository = repo
		if _, err := in.validate(); err != nil {
			t.Errorf("repository %q rejected: %v", repo, err)
		}
	}
	rejected := []string{
		"",
		"GitHub.com/ashuangiras/polaroid",
		"https://github.com/ashuangiras/polaroid",
		"git@github.com:ashuangiras/polaroid",
		"github.com/ashuangiras/polaroid.git",
		"github.com:8443/o/r",
		"/github.com/o/r",
		"github.com/o/r/",
		"github.com//r",
		"github.com/../r",
		"./r",
		"my repo",
		"répo",
		strings.Repeat("r", MaxRepositoryLength+1),
	}
	for _, repo := range rejected {
		in := validBinding()
		in.Repository = repo
		_, err := in.validate()
		if got := problemFields(t, err); len(got) != 1 || got[0] != "repository" {
			t.Errorf("repository %q: problem fields = %v, want [repository]", repo, got)
		}
	}
}

func TestBindingNameFollowsCanonicalKeyRules(t *testing.T) {
	for _, name := range []string{"", "Add", "add dependency", "a--b", "a/b"} {
		in := validBinding()
		in.Name = name
		_, err := in.validate()
		if got := problemFields(t, err); len(got) != 1 || got[0] != "name" {
			t.Errorf("name %q: problem fields = %v, want [name]", name, got)
		}
	}
}

func TestBindingRequiresEveryField(t *testing.T) {
	_, err := NewBinding{}.validate()
	got := strings.Join(problemFields(t, err), ",")
	want := "repository,name,procedure_id,revision.inputs,revision.version_policy,revision.revision_reason"
	if got != want {
		t.Fatalf("problem fields = %s, want %s", got, want)
	}
}

func TestVersionPolicyRules(t *testing.T) {
	cases := []struct {
		policy VersionPolicy
		field  string // "" means valid
	}{
		{VersionPolicy{Kind: PolicyPin, Pin: 1}, ""},
		{VersionPolicy{Kind: PolicyContextual}, ""},
		{VersionPolicy{Kind: PolicyPin}, "revision.version_policy.pin"},
		{VersionPolicy{Kind: PolicyPin, Pin: -2}, "revision.version_policy.pin"},
		{VersionPolicy{Kind: PolicyContextual, Pin: 1}, "revision.version_policy"},
		{VersionPolicy{}, "revision.version_policy"},
		{VersionPolicy{Kind: "latest"}, "revision.version_policy"},
	}
	for _, tc := range cases {
		in := BindingRevise{BaseRevision: 1, Config: validBinding().Config}
		in.Config.VersionPolicy = tc.policy
		_, err := in.validate()
		if tc.field == "" {
			if err != nil {
				t.Errorf("%+v rejected: %v", tc.policy, err)
			}
			continue
		}
		if got := problemFields(t, err); len(got) != 1 || got[0] != tc.field {
			t.Errorf("%+v: problem fields = %v, want [%s]", tc.policy, got, tc.field)
		}
	}
}

func TestBindingInputsMustBeAJSONObjectAndAreCompacted(t *testing.T) {
	for _, raw := range []string{`[]`, `null`, `"text"`, `{"a":1,"a":2}`, `{"a":`} {
		in := validBinding()
		in.Config.Inputs = jsontext.Value(raw)
		_, err := in.validate()
		if got := problemFields(t, err); len(got) != 1 || got[0] != "revision.inputs" {
			t.Errorf("%s: problem fields = %v", raw, got)
		}
	}
	cfg, err := validBinding().validate()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"module":"modernc.org/sqlite"}`; string(cfg.Inputs) != want {
		t.Fatalf("inputs = %s, want %s", cfg.Inputs, want)
	}
}

func TestBindingReviseRequiresPositiveBaseRevision(t *testing.T) {
	for _, base := range []int{0, -1} {
		_, err := BindingRevise{BaseRevision: base, Config: validBinding().Config}.validate()
		if got := problemFields(t, err); len(got) != 1 || got[0] != "base_revision" {
			t.Errorf("base %d: problem fields = %v", base, got)
		}
	}
}
