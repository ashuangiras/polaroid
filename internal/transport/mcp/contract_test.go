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

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Both /mcp (full mode, ADR-0038's v1 guarantee) and /mcp/agent (agent
// mode, finalized under the same guarantee by ADR-0049) have a reviewed
// contract file, checked here against both the live server and the tools
// table in docs/architecture/mcp.md: a change to any argument's name,
// type, nesting or requiredness, or to a tool's read-only flag, fails
// until the contract and the documentation are changed deliberately.
// Descriptions are wording, not contract.

type toolContract struct {
	ReadOnly  bool              `json:"read_only"`
	Required  []string          `json:"required"`
	Arguments map[string]string `json:"arguments"`
}

func loadContract(t *testing.T, path string) map[string]toolContract {
	t.Helper()
	b, err := os.ReadFile(path)
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

// toolsOf collects the served contract of every tool in session, in the
// same shape as the reviewed fixtures.
func toolsOf(t *testing.T, session *sdk.ClientSession) map[string]toolContract {
	t.Helper()
	got := map[string]toolContract{}
	for tool, err := range session.Tools(context.Background(), nil) {
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
	return got
}

// checkToolContract asserts that got matches want exactly: the same tools,
// with the same read-only flag, required fields and argument signatures.
func checkToolContract(t *testing.T, want, got map[string]toolContract) {
	t.Helper()
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

func TestFullModeToolContract(t *testing.T) {
	want := loadContract(t, "testdata/full-mode-tools.json")
	h := newHarness(t)
	checkToolContract(t, want, toolsOf(t, h.session))
}

func TestAgentModeToolContract(t *testing.T) {
	want := loadContract(t, "testdata/agent-mode-tools.json")
	agent := newKnowledgeHarness(t)
	checkToolContract(t, want, toolsOf(t, agent.session))
}

var (
	toolRow  = regexp.MustCompile("^\\| `([a-z_]+)` \\| (yes|no) \\| (.*?) \\| (.*) \\|$")
	backtick = regexp.MustCompile("`([a-z_]+)`")
)

type docRow struct {
	readOnly bool
	args     map[string]bool
}

// toolsDocumentedUnder parses the tools table of mcp.md's section starting
// at startMarker, up to the next "## " heading (or the end of the file).
func toolsDocumentedUnder(t *testing.T, startMarker string) map[string]docRow {
	t.Helper()
	b, err := os.ReadFile("../../../docs/architecture/mcp.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(b), startMarker)
	if !ok {
		t.Fatalf("mcp.md has no section starting %q", startMarker)
	}
	section, _, _ = strings.Cut(section, "\n## ")
	doc := map[string]docRow{}
	for _, line := range strings.Split(section, "\n") {
		m := toolRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		r := docRow{readOnly: m[2] == "yes", args: map[string]bool{}}
		for _, a := range backtick.FindAllStringSubmatch(m[3], -1) {
			r.args[a[1]] = true
		}
		doc[m[1]] = r
	}
	return doc
}

// checkContractDocumented asserts that the reviewed contract and the
// documented tools table agree: the same tools, read-only flags, and every
// argument named in its tool's row (or, per sameFields, in another row that
// says it shares the same fields).
func checkContractDocumented(t *testing.T, contract map[string]toolContract, doc map[string]docRow, sameFields map[string]string) {
	t.Helper()
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

func TestFullModeContractIsDocumented(t *testing.T) {
	contract := loadContract(t, "testdata/full-mode-tools.json")
	doc := toolsDocumentedUnder(t, "\n## Tools\n")
	// Rows that say "then the same ... fields" document those fields once.
	sameFields := map[string]string{"revise_procedure": "create_procedure", "revise_toil_item": "create_toil_item"}
	checkContractDocumented(t, contract, doc, sameFields)
}

func TestAgentModeContractIsDocumented(t *testing.T) {
	contract := loadContract(t, "testdata/agent-mode-tools.json")
	doc := toolsDocumentedUnder(t, "\n## Agent mode (`/mcp/agent`)\n")
	checkContractDocumented(t, contract, doc, nil)
}
