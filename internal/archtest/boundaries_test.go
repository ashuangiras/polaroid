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
	_ "github.com/ashuangiras/polaroid/internal/memory"
	_ "github.com/ashuangiras/polaroid/internal/storage/sqlite"
	_ "github.com/ashuangiras/polaroid/internal/transport/http"
	_ "github.com/ashuangiras/polaroid/internal/transport/mcp"
	_ "github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Dependency direction (see docs/architecture/overview.md): cmd wires
// transport and storage to memory; memory depends on neither. The two
// transports share only transport/wire, never each other.
func TestPackageBoundaries(t *testing.T) {
	root, module := moduleRootAndPath(t)
	storage := []string{"database/sql", "modernc.org/sqlite", module + "/internal/storage", module + "/cmd"}
	rules := []struct {
		pkg       string
		forbidden []string
	}{
		{"internal/memory", []string{"net/http", "database/sql", "modernc.org/sqlite",
			module + "/internal/storage", module + "/internal/transport", module + "/cmd"}},
		{"internal/storage/sqlite", []string{"net/http", module + "/internal/transport", module + "/cmd"}},
		{"internal/transport/wire", slices.Concat(storage, []string{module + "/internal/transport/http", module + "/internal/transport/mcp", "github.com/modelcontextprotocol"})},
		{"internal/transport/http", slices.Concat(storage, []string{module + "/internal/transport/mcp", "github.com/modelcontextprotocol"})},
		{"internal/transport/mcp", slices.Concat(storage, []string{module + "/internal/transport/http"})},
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
