package sqlite_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// TestLatestVersionsForDiscovery reads each procedure's latest version in key
// order, with its descriptive content, and keeps local versions only in
// their repository, by identity (ADR-0032).
func TestLatestVersionsForDiscovery(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	mustRegister(t, s, repository("ra", "github.com/o/a", 1))
	mustRegister(t, s, repository("rb", "github.com/o/b", 2))
	if err := s.AddRepositoryAlias(ctx, "ra", memory.RepositoryAlias{Identifier: "mirror.example/o/a", Reason: "r", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, "p-shared", 1)
	revised := version("p-shared", 2, `{"steps":["second"]}`)
	revised.Goal, revised.Applicability = "Second goal.", memory.Applicability{Kind: memory.ScopeShared}
	if err := s.AppendVersion(ctx, 1, revised); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ id, key, repo string }{{"p-a", "key.local-a", "ra"}, {"p-b", "key.local-b", "rb"}} {
		v := version(p.id, 1, `{}`)
		v.Applicability = memory.Applicability{Kind: memory.ScopeLocal, RepositoryID: p.repo}
		if err := s.CreateProcedure(ctx, procedure(p.id, p.key), v); err != nil {
			t.Fatal(err)
		}
	}

	read := func(repository string) []string {
		t.Helper()
		vs, err := s.LatestVersions(ctx, repository)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range vs {
			out = append(out, fmt.Sprintf("%s@%d:%s", v.CanonicalKey, v.Version, v.Applicability.Name()))
		}
		return out
	}
	all := []string{"key.local-a@1:local", "key.local-b@1:local", "key.p-shared@2:shared"}
	for repository, want := range map[string][]string{
		"":                          all,
		"github.com/o/a":            {"key.local-a@1:local", "key.p-shared@2:shared"},
		"mirror.example/o/a":        {"key.local-a@1:local", "key.p-shared@2:shared"},
		"github.com/o/b":            {"key.local-b@1:local", "key.p-shared@2:shared"},
		"github.com/o/unregistered": {"key.p-shared@2:shared"},
	} {
		if got := read(repository); !slices.Equal(got, want) {
			t.Errorf("repository %q: %v, want %v", repository, got, want)
		}
	}

	vs, err := s.LatestVersions(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	got := vs[2]
	if got.ProcedureID != "p-shared" || got.Goal != "Second goal." || got.Method == "" || got.Philosophy == "" ||
		string(got.Instructions) != `{"steps":["second"]}` || len(got.Contract) == 0 {
		t.Fatalf("latest version content: %+v", got)
	}
}
