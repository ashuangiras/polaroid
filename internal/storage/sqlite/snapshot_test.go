package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
)

// TestProcedureSnapshotIsAsOfItsBoundary checks that a snapshot read
// ignores every row committed after its boundary: new procedures, new
// versions (so latest_version, scope and goal stay as they were), origins,
// and aliases for the repository filter (ADR-0023).
func TestProcedureSnapshotIsAsOfItsBoundary(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	mustRegister(t, s, repository("ra", "github.com/o/a", 1))
	mustPut(t, s, "p1", 1)
	local := version("p2", 1, `{}`)
	local.Applicability = memory.Applicability{Kind: memory.ScopeLocal, RepositoryID: "ra"}
	if err := s.CreateProcedure(ctx, procedure("p2", "key.p2"), local); err != nil {
		t.Fatal(err)
	}
	snap, err := s.ProcedureSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	shared := version("p1", 2, `{}`)
	shared.Goal, shared.Applicability = "Shared now.", memory.Applicability{Kind: memory.ScopeShared}
	if err := s.AppendVersion(ctx, 1, shared); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrigin(ctx, "p1", memory.Origin{RepositoryID: "ra", Reason: "r", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, "p0", 1)
	if err := s.AddRepositoryAlias(ctx, "ra", memory.RepositoryAlias{Identifier: "old.example/o/a", Reason: "r", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}

	list := func(f memory.ProcedureFilter) []string {
		t.Helper()
		ps, err := s.ListProcedures(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			item := fmt.Sprintf("%s@%d:%s", p.ID, p.LatestVersion, p.Applicability.Name())
			if p.Origin != nil {
				item += "+origin"
			}
			out = append(out, item)
		}
		return out
	}
	for name, c := range map[string]struct {
		f    memory.ProcedureFilter
		want []string
	}{
		"snapshot":                 {memory.ProcedureFilter{Snapshot: snap}, []string{"p1@1:unspecified", "p2@1:local"}},
		"live":                     {memory.ProcedureFilter{}, []string{"p0@1:unspecified", "p1@2:shared+origin", "p2@1:local"}},
		"snapshot, shared":         {memory.ProcedureFilter{Scope: "shared", Snapshot: snap}, nil},
		"snapshot, goal":           {memory.ProcedureFilter{Query: "shared now", Snapshot: snap}, nil},
		"snapshot, alias":          {memory.ProcedureFilter{Repository: "old.example/o/a", Snapshot: snap}, []string{"p1@1:unspecified"}},
		"live, alias":              {memory.ProcedureFilter{Repository: "old.example/o/a"}, []string{"p0@1:unspecified", "p1@2:shared+origin", "p2@1:local"}},
		"snapshot, canonical":      {memory.ProcedureFilter{Repository: "github.com/o/a", Snapshot: snap}, []string{"p1@1:unspecified", "p2@1:local"}},
		"snapshot, another":        {memory.ProcedureFilter{Repository: "github.com/o/b", Snapshot: snap}, []string{"p1@1:unspecified"}},
		"snapshot after key.p1 id": {memory.ProcedureFilter{Snapshot: snap, After: &memory.Position{Key: "key.p1", ID: "p1"}}, []string{"p2@1:local"}},
	} {
		if got := list(c.f); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	if _, err := s.ListProcedures(ctx, memory.ProcedureFilter{Snapshot: snap[:2]}); !errors.Is(err, memory.ErrInvalidSnapshot) {
		t.Fatalf("a boundary of the wrong size: %v", err)
	}
	if _, err := s.ListBindings(ctx, memory.BindingFilter{Repository: "github.com/o/a", Snapshot: snap}); !errors.Is(err, memory.ErrInvalidSnapshot) {
		t.Fatalf("a procedure boundary on the binding list: %v", err)
	}
}
