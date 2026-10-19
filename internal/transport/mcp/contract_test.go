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

// The v8 tool contract snapshot catches unintended schema or read-only changes.

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

func TestAgentV8ToolContract(t *testing.T) {
	agent, _ := newKnowledgeHarnessV8(t)
	got := toolsOf(t, agent.session)
	if len(got) != 10 {
		t.Fatalf("v8 tools: got %d, want 10", len(got))
	}
	checkToolContract(t, loadContract(t, "testdata/agent-mode-v8-tools.json"), got)
}

var (
	toolRow = regexp.MustCompile("^\\| `([a-z_]+)` \\| (yes|no) \\| (.*?) \\| (.*) \\|$")
)

type docRow struct {
	readOnly bool
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
		doc[m[1]] = docRow{readOnly: m[2] == "yes"}
	}
	return doc
}

func TestAgentV8ContractIsDocumented(t *testing.T) {
	contract := loadContract(t, "testdata/agent-mode-v8-tools.json")
	doc := toolsDocumentedUnder(t, "\n## Agent mode v8 (`/mcp/agent/v8`)\n")
	// The v8 table can refer to inherited arguments in earlier sections.
	for name, row := range doc {
		expected, ok := contract[name]
		if !ok {
			t.Errorf("%s is documented at v8 but is not served", name)
			continue
		}
		if row.readOnly != expected.ReadOnly {
			t.Errorf("%s read-only is documented as %v, served as %v", name, row.readOnly, expected.ReadOnly)
		}
	}
}
