package mcp_test

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The full-mode /mcp tool contract under ADR-0038's v1 guarantee. The
// reviewed contract is testdata/full-mode-tools.json, checked here against
// both the live server and the tools table in docs/architecture/mcp.md, so a
// change to any argument's name, type, nesting or requiredness, or to a
// tool's read-only flag, fails until the contract and the documentation are
// changed deliberately. Descriptions are wording, not contract. The
// experimental /mcp/agent endpoint (ADR-0043) is outside this contract.

type toolContract struct {
	ReadOnly  bool              `json:"read_only"`
	Required  []string          `json:"required"`
	Arguments map[string]string `json:"arguments"`
}

func loadContract(t *testing.T) map[string]toolContract {
	t.Helper()
	b, err := os.ReadFile("testdata/full-mode-tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]toolContract
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// signature renders a JSON schema's shape: its types, then for an object
// its properties (! marks required), for an array its items.
func signature(s map[string]any) string {
	out := "any"
	switch t := s["type"].(type) {
	case string:
		out = t
	case []any:
		var ts []string
		for _, v := range t {
			ts = append(ts, v.(string))
		}
		out = strings.Join(ts, "|")
	}
	if props, ok := s["properties"].(map[string]any); ok {
		req := map[string]bool{}
		if r, ok := s["required"].([]any); ok {
			for _, v := range r {
				req[v.(string)] = true
			}
		}
		var parts []string
		for _, k := range slices.Sorted(maps.Keys(props)) {
			mark := ""
			if req[k] {
				mark = "!"
			}
			parts = append(parts, k+mark+":"+signature(props[k].(map[string]any)))
		}
		out += "{" + strings.Join(parts, ",") + "}"
	}
	if items, ok := s["items"].(map[string]any); ok {
		out += "[" + signature(items) + "]"
	}
	return out
}

func TestFullModeToolContract(t *testing.T) {
	want := loadContract(t)
	h := newHarness(t)
	got := map[string]toolContract{}
	for tool, err := range h.session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var s map[string]any
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		if s["type"] != "object" || s["additionalProperties"] != false {
			t.Errorf("%s: arguments must be a closed object", tool.Name)
		}
		c := toolContract{ReadOnly: tool.Annotations != nil && tool.Annotations.ReadOnlyHint, Required: []string{}, Arguments: map[string]string{}}
		if r, ok := s["required"].([]any); ok {
			for _, v := range r {
				c.Required = append(c.Required, v.(string))
			}
		}
		slices.Sort(c.Required)
		if props, ok := s["properties"].(map[string]any); ok {
			for k, v := range props {
				c.Arguments[k] = signature(v.(map[string]any))
			}
		}
		got[tool.Name] = c
	}
	for _, name := range slices.Sorted(maps.Keys(want)) {
		g, ok := got[name]
		switch {
		case !ok:
			t.Errorf("%s: in the contract, not served", name)
		case g.ReadOnly != want[name].ReadOnly:
			t.Errorf("%s: read-only %v, contract %v", name, g.ReadOnly, want[name].ReadOnly)
		case !slices.Equal(g.Required, want[name].Required):
			t.Errorf("%s: required %v, contract %v", name, g.Required, want[name].Required)
		case !maps.Equal(g.Arguments, want[name].Arguments):
			for _, a := range slices.Sorted(maps.Keys(maps.Collect(func(yield func(string, bool) bool) {
				for k := range g.Arguments {
					yield(k, true)
				}
				for k := range want[name].Arguments {
					yield(k, true)
				}
			}))) {
				if g.Arguments[a] != want[name].Arguments[a] {
					t.Errorf("%s.%s: served %q, contract %q", name, a, g.Arguments[a], want[name].Arguments[a])
				}
			}
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s: served, not in the contract", name)
		}
	}
}

var (
	toolRow  = regexp.MustCompile("^\\| `([a-z_]+)` \\| (yes|no) \\| (.*?) \\| (.*) \\|$")
	backtick = regexp.MustCompile("`([a-z_]+)`")
)

// The reviewed contract and the documented tools table agree: the same
// tools, read-only flags, and every argument named in its tool's row.
func TestFullModeContractIsDocumented(t *testing.T) {
	contract := loadContract(t)
	b, err := os.ReadFile("../../../docs/architecture/mcp.md")
	if err != nil {
		t.Fatal(err)
	}
	_, tools, ok := strings.Cut(string(b), "\n## Tools\n")
	tools, _, _ = strings.Cut(tools, "\n## ")
	if !ok {
		t.Fatal("mcp.md has no Tools section")
	}
	type row struct {
		readOnly bool
		args     map[string]bool
	}
	doc := map[string]row{}
	for _, line := range strings.Split(tools, "\n") {
		m := toolRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		r := row{readOnly: m[2] == "yes", args: map[string]bool{}}
		for _, a := range backtick.FindAllStringSubmatch(m[3], -1) {
			r.args[a[1]] = true
		}
		doc[m[1]] = r
	}
	// Rows that say "then the same ... fields" document those fields once.
	sameFields := map[string]string{"revise_procedure": "create_procedure", "revise_toil_item": "create_toil_item"}
	if !slices.Equal(slices.Sorted(maps.Keys(doc)), slices.Sorted(maps.Keys(contract))) {
		t.Fatalf("documented tools %v, contract %v", slices.Sorted(maps.Keys(doc)), slices.Sorted(maps.Keys(contract)))
	}
	for name, c := range contract {
		r := doc[name]
		if r.readOnly != c.ReadOnly {
			t.Errorf("%s: documented read-only %v, contract %v", name, r.readOnly, c.ReadOnly)
		}
		for a := range c.Arguments {
			if !r.args[a] && !doc[sameFields[name]].args[a] {
				t.Errorf("%s: argument %s is not documented in mcp.md", name, a)
			}
		}
	}
}
