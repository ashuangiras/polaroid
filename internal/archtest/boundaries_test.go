package archtest

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	// Importing the checked packages makes `go test` re-run this test
	// whenever they change, instead of reusing a cached pass.
	_ "github.com/ashuangiras/polaroid/internal/gitcore"
	_ "github.com/ashuangiras/polaroid/internal/knowledge"
	_ "github.com/ashuangiras/polaroid/internal/knowledge/gitstore"
	_ "github.com/ashuangiras/polaroid/internal/knowledge/semantic"
	_ "github.com/ashuangiras/polaroid/internal/knowledgeops"
	_ "github.com/ashuangiras/polaroid/internal/knowledgesync"
	_ "github.com/ashuangiras/polaroid/internal/lifecycle"
	_ "github.com/ashuangiras/polaroid/internal/memory"
	_ "github.com/ashuangiras/polaroid/internal/reasoning"
	_ "github.com/ashuangiras/polaroid/internal/reasoning/datalog"
	_ "github.com/ashuangiras/polaroid/internal/reasoning/policy"
	_ "github.com/ashuangiras/polaroid/internal/recovery"
	_ "github.com/ashuangiras/polaroid/internal/storage/sqlite"
	_ "github.com/ashuangiras/polaroid/internal/transport/http"
	_ "github.com/ashuangiras/polaroid/internal/transport/httpops"
	_ "github.com/ashuangiras/polaroid/internal/transport/mcp"
	_ "github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Dependency direction (see docs/architecture/overview.md): cmd wires
// transport and storage to memory; memory depends on neither. The two
// transports share only transport/wire, never each other.
func TestPackageBoundaries(t *testing.T) {
	root, module := moduleRootAndPath(t)
	storage := []string{"database/sql", "modernc.org/sqlite", module + "/internal/storage", module + "/cmd"}
	// The Git-native knowledge path (ADR-0045, ADR-0046) is reachable from no
	// legacy package: nothing writes knowledge to both stores.
	knowledge := []string{module + "/internal/knowledge", module + "/internal/knowledgesync", module + "/internal/gitcore"}
	// knowledgeAll also forbids the orchestration layer above the knowledge
	// path (ADR-0048): a package that must not reach Git-core or the kernel
	// directly must not reach it through knowledgeops either.
	knowledgeAll := append(slices.Clone(knowledge), module+"/internal/knowledgeops")
	notKnowledgeInfra := []string{module + "/internal/gitcore", "github.com/go-git", module + "/internal/knowledge/gitstore", module + "/internal/knowledgesync",
		module + "/internal/knowledgeops", module + "/internal/memory", module + "/internal/transport", module + "/internal/recovery", module + "/internal/lifecycle"}
	mangle := []string{"codeberg.org/TauCeti/mangle-go", "github.com/google/mangle"}
	rdf := []string{"github.com/tggo/goRDFlib", "github.com/piprate/json-gold"}
	rdfInfra := []string{"github.com/tggo/goRDFlib/store/sqlitestore", "github.com/tggo/goRDFlib/store/badgerstore", "github.com/tggo/goRDFlib/store/sparqlstore",
		"github.com/tggo/goRDFlib/endpoint", "github.com/tggo/goRDFlib/rdfloader", "github.com/dgraph-io"}
	rules := []struct {
		pkg       string
		forbidden []string
	}{
		{"internal/memory", slices.Concat(knowledgeAll, []string{"net/http", "database/sql", "modernc.org/sqlite",
			module + "/internal/storage", module + "/internal/transport", module + "/cmd"})},
		{"internal/storage/sqlite", slices.Concat(knowledgeAll, []string{"net/http", module + "/internal/transport", module + "/cmd"})},
		// The knowledge domain sees storage only through its Store port: no
		// Git, no SQL, no transport, no legacy records.
		{"internal/knowledge", slices.Concat(storage, []string{"net/http", module + "/internal/gitcore", "github.com/go-git",
			module + "/internal/knowledge/gitstore", module + "/internal/knowledge/semantic", module + "/internal/reasoning",
			module + "/internal/knowledgesync", module + "/internal/knowledgeops", module + "/internal/memory", module + "/internal/transport",
			module + "/internal/recovery", module + "/internal/lifecycle"})},
		// The adapter joins the domain to Git-core and nothing else.
		{"internal/knowledge/gitstore", slices.Concat(storage, []string{module + "/internal/memory", module + "/internal/knowledgesync", module + "/internal/knowledgeops",
			module + "/internal/transport", module + "/internal/recovery", module + "/internal/lifecycle"})},
		// Synchronisation joins the kernel, its adapter and Git-core; no agent
		// interface, legacy record or SQL reaches it (ADR-0046).
		{"internal/knowledgesync", slices.Concat(storage, []string{module + "/internal/memory", module + "/internal/knowledgeops", module + "/internal/transport",
			module + "/internal/recovery", module + "/internal/lifecycle", "github.com/modelcontextprotocol"})},
		// Git-core stores files and snapshots; it knows no procedures, records or agent interfaces.
		{"internal/gitcore", slices.Concat(storage, []string{module + "/internal/memory", module + "/internal/knowledge", module + "/internal/knowledgesync", module + "/internal/knowledgeops",
			module + "/internal/transport", module + "/internal/recovery", module + "/internal/lifecycle"})},
		// knowledgeops orchestrates the knowledge path for the agent and
		// operator interfaces (ADR-0048); it has no storage, versioning or
		// transport of its own, and no transport reaches it except through
		// cmd/polaroidd's composition (transport/httpops is the one exception,
		// since exposing it is httpops's whole purpose).
		{"internal/knowledgeops", slices.Concat(storage, []string{module + "/internal/transport/http", module + "/internal/transport/mcp",
			module + "/internal/recovery", module + "/internal/lifecycle", "github.com/modelcontextprotocol"})},
		{"internal/transport/wire", slices.Concat(storage, knowledgeAll, []string{module + "/internal/transport/http", module + "/internal/transport/httpops", module + "/internal/transport/mcp", "github.com/modelcontextprotocol"})},
		{"internal/transport/http", slices.Concat(storage, knowledgeAll, []string{module + "/internal/transport/mcp", module + "/internal/transport/httpops", "github.com/modelcontextprotocol"})},
		{"internal/transport/mcp", slices.Concat(storage, knowledgeAll, []string{module + "/internal/transport/http", module + "/internal/transport/httpops"})},
		// The operator HTTP surface reaches the knowledge path only through
		// knowledgeops, never storage, the legacy API, or MCP directly.
		{"internal/transport/httpops", slices.Concat(storage, []string{module + "/internal/transport/http", module + "/internal/transport/mcp", "github.com/modelcontextprotocol"})},
		// Lifecycle code manages files, a service manager and processes,
		// never procedures or their storage (ADR-0026).
		{"internal/lifecycle", slices.Concat(storage, knowledgeAll, []string{"net/http/httptest", module + "/internal/memory", module + "/internal/transport", module + "/internal/recovery"})},
		// Backup and restore use storage and lifecycle, never the API (ADR-0030).
		{"internal/recovery", slices.Concat(knowledgeAll, []string{"net/http/httptest", module + "/internal/transport", module + "/cmd"})},
		// The CLI reaches storage only through internal/recovery; see
		// TestCLIReachesStorageOnlyThroughRecovery.
		{"cmd/polaroid", slices.Concat(knowledgeAll, []string{module + "/internal/transport"})},
		// cmd/polaroidd is the composition root for both the legacy and the
		// Git-native knowledge paths (ADR-0048): it is the only place that may
		// wire knowledge, knowledgesync, gitcore and knowledgeops in, so it is
		// unrestricted here.

		// Corpus Logic (ADR-0050). The semantic adapter joins the record model
		// to goRDFlib and json-gold, in memory only: no persistent graph store,
		// SPARQL endpoint or remote RDF loader. (github.com/google/uuid, through
		// goRDFlib's terms, compiles the interface-only database/sql/driver.)
		{"internal/knowledge/semantic", slices.Concat([]string{"modernc.org/sqlite", module + "/internal/storage", module + "/cmd"},
			notKnowledgeInfra, mangle, rdfInfra, []string{module + "/internal/reasoning"})},
		// Reasoning is pure: no Git, storage, transport, network, or library
		// implementation; it reaches Mangle only through its Evaluator contract.
		{"internal/reasoning", slices.Concat(storage, notKnowledgeInfra, mangle, rdf, []string{"net/http",
			module + "/internal/knowledge/semantic", module + "/internal/reasoning/datalog"})},
		{"internal/reasoning/policy", slices.Concat(storage, notKnowledgeInfra, mangle, rdf, []string{"net/http",
			module + "/internal/knowledge/semantic", module + "/internal/reasoning/datalog"})},
		// The Mangle adapter is the only package that imports Mangle.
		{"internal/reasoning/datalog", slices.Concat(storage, notKnowledgeInfra, rdf, []string{"net/http", module + "/internal/knowledge/semantic"})},
	}
	// Only the two adapters import the libraries they adapt.
	for i, r := range rules {
		if r.pkg != "internal/knowledge/semantic" {
			rules[i].forbidden = append(rules[i].forbidden, rdf...)
		}
		if r.pkg != "internal/reasoning/datalog" {
			rules[i].forbidden = append(rules[i].forbidden, mangle...)
		}
	}
	for _, rule := range rules {
		deps := goList(t, root, "-deps", "-f", "{{.ImportPath}}", module+"/"+rule.pkg)
		// A vacuous result (e.g. a wrong working directory) must not pass.
		if len(deps) < 10 || deps[len(deps)-1] != module+"/"+rule.pkg {
			t.Fatalf("%s: implausible dependency list %v", rule.pkg, deps)
		}
		for _, dep := range deps {
			for _, f := range rule.forbidden {
				if dep == f || strings.HasPrefix(dep, f+"/") {
					t.Errorf("%s must not depend on %s (found %s)", rule.pkg, f, dep)
				}
			}
		}
	}
}

// TestCLIReachesStorageOnlyThroughRecovery: the CLI's API commands never touch
// storage; its local backup and restore commands open the catalog only
// through internal/recovery (ADR-0030).
func TestCLIReachesStorageOnlyThroughRecovery(t *testing.T) {
	_, module := moduleRootAndPath(t)
	imports := goList(t, ".", "-f", `{{join .Imports " "}}`, module+"/cmd/polaroid")
	if !slices.Contains(imports, module+"/internal/recovery") {
		t.Fatalf("implausible import list %v", imports)
	}
	for _, imp := range imports {
		for _, f := range []string{"database/sql", "modernc.org/sqlite", module + "/internal/storage", module + "/internal/memory"} {
			if imp == f || strings.HasPrefix(imp, f+"/") {
				t.Errorf("cmd/polaroid imports %s directly; reach storage through internal/recovery", imp)
			}
		}
	}
}

func moduleRootAndPath(t *testing.T) (string, string) {
	t.Helper()
	gomod := goList(t, ".", "-m", "-f", "{{.GoMod}}")
	module := goList(t, ".", "-m", "-f", "{{.Path}}")
	if len(gomod) != 1 || len(module) != 1 {
		t.Fatalf("cannot resolve module: %v %v", gomod, module)
	}
	return filepath.Dir(gomod[0]), module[0]
}

func goList(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "go", append([]string{"list"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %v: %v", args, err)
	}
	return strings.Fields(string(out))
}
