package memory

import (
	"slices"
	"testing"
)

func TestChildLinkShape(t *testing.T) {
	cases := []struct {
		name     string
		children []ChildExecution
		fields   []string // nil means valid
	}{
		{"none", nil, nil},
		{"empty list", []ChildExecution{}, nil},
		{"two references", []ChildExecution{{"pinned-child", "e1"}, {"latest-child", "e2"}}, nil},
		{"invalid reference name", []ChildExecution{{"Pinned Child", "e1"}}, []string{"children[0].reference"}},
		{"missing execution", []ChildExecution{{"pinned-child", ""}}, []string{"children[0].execution_id"}},
		{"reference fulfilled twice", []ChildExecution{{"a", "e1"}, {"a", "e2"}}, []string{"children[1].reference"}},
		{"child listed twice", []ChildExecution{{"a", "e1"}, {"b", "e1"}}, []string{"children[1].execution_id"}},
		{"everything missing", []ChildExecution{{}}, []string{"children[0].reference", "children[0].execution_id"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validExecution()
			r.Children = tc.children
			got, err := r.validate()
			if tc.fields == nil {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				if len(tc.children) == 0 && got.Children != nil {
					t.Fatalf("an empty child list must be stored as none, got %#v", got.Children)
				}
				return
			}
			if fields := problemFields(t, err); !slices.Equal(fields, tc.fields) {
				t.Fatalf("problem fields = %v, want %v", fields, tc.fields)
			}
		})
	}
}

func TestChildLinksAreCopied(t *testing.T) {
	r := validExecution()
	r.Children = []ChildExecution{{"a", "e1"}}
	got, err := r.validate()
	if err != nil {
		t.Fatal(err)
	}
	r.Children[0].ExecutionID = "changed"
	if got.Children[0].ExecutionID != "e1" {
		t.Fatal("validate must not alias the caller's children")
	}
}
