package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

// put stores version n of procedure id with the given references, creating
// the procedure for n == 1.
func put(s *sqlite.Store, id string, n int, refs ...memory.Reference) error {
	v := version(id, n, `{}`)
	v.References = refs
	if n == 1 {
		return s.CreateProcedure(context.Background(), procedure(id, "key."+id), v)
	}
	return s.AppendVersion(context.Background(), n-1, v)
}

func mustPut(t *testing.T, s *sqlite.Store, id string, n int, refs ...memory.Reference) {
	t.Helper()
	if err := put(s, id, n, refs...); err != nil {
		t.Fatalf("put %s@%d: %v", id, n, err)
	}
}

func pinTo(name, target string, n int) memory.Reference {
	return reference(name, target, memory.VersionPolicy{Kind: memory.PolicyPin, Pin: n}, `{}`)
}

func latestOf(name, target string) memory.Reference {
	return reference(name, target, contextual, `{}`)
}

// compositionGraph reads a version's graph without a resolution context.
func compositionGraph(s *sqlite.Store, ctx context.Context, id string, n int) (memory.GraphNode, error) {
	res, err := s.Resolve(ctx, id, memory.VersionPolicy{Kind: memory.PolicyPin, Pin: n}, memory.ResolutionContext{})
	return res.Graph, err
}

func wantCycle(t *testing.T, err error, want []memory.CycleStep) {
	t.Helper()
	var cycle *memory.ReferenceCycleError
	if !errors.As(err, &cycle) || !reflect.DeepEqual(cycle.Cycle, want) {
		t.Fatalf("err = %v, want cycle %+v", err, want)
	}
}

func TestWritesThatCloseACycleAreRejected(t *testing.T) {
	s := openStore(t, dbPath(t))
	mustPut(t, s, "a", 1)
	mustPut(t, s, "b", 1, latestOf("a", "a"))
	before := history(t, s, "a")

	// A -> A, at an older pinned version.
	wantCycle(t, put(s, "a", 2, pinTo("me", "a", 1)), []memory.CycleStep{{ProcedureID: "a", Version: 2, Reference: "me"}, {ProcedureID: "a", Version: 1}})
	// A -> B -> A, where B follows A's latest version, which would be this one.
	wantCycle(t, put(s, "a", 2, latestOf("b", "b")), []memory.CycleStep{
		{ProcedureID: "a", Version: 2, Reference: "b"}, {ProcedureID: "b", Version: 1, Reference: "a"}, {ProcedureID: "a", Version: 2},
	})
	if got := history(t, s, "a"); !reflect.DeepEqual(got, before) {
		t.Fatalf("a rejected revision was stored: %+v", got)
	}
	if _, err := s.Version(context.Background(), "a", 2); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("version 2 exists after rejection: %v", err)
	}
}

// A revision of a target can close a cycle through a contextual reference.
func TestCycleIntroducedByALaterRevisionOfTheTarget(t *testing.T) {
	s := openStore(t, dbPath(t))
	mustPut(t, s, "b", 1)
	mustPut(t, s, "a", 1, latestOf("b", "b"))

	wantCycle(t, put(s, "b", 2, pinTo("a", "a", 1)), []memory.CycleStep{
		{ProcedureID: "b", Version: 2, Reference: "a"}, {ProcedureID: "a", Version: 1, Reference: "b"}, {ProcedureID: "b", Version: 2},
	})
	if h := history(t, s, "b"); len(h.Versions) != 1 {
		t.Fatalf("b has %d versions, want 1", len(h.Versions))
	}
	// Pinning b's acyclic version 1 is fine.
	mustPut(t, s, "c", 1, pinTo("a", "a", 1), pinTo("b", "b", 1))
}

// Two revisions that each add half of a cycle cannot both succeed.
func TestConcurrentRevisionsCannotFormACycle(t *testing.T) {
	s := openStore(t, dbPath(t))
	mustPut(t, s, "a", 1)
	mustPut(t, s, "b", 1)

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, w := range []struct{ id, target string }{{"a", "b"}, {"b", "a"}} {
		wg.Go(func() {
			<-start
			errs[i] = put(s, w.id, 2, latestOf(w.target, w.target))
		})
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for i, err := range errs {
		var cycle *memory.ReferenceCycleError
		switch {
		case err == nil:
			succeeded++
		case !errors.As(err, &cycle):
			t.Fatalf("writer %d: unexpected error %v", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d of the two half-cycles were stored, want exactly 1", succeeded)
	}
}

func TestCompositionGraphReadsExactVersions(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	mustPut(t, s, "leaf", 1)
	mustPut(t, s, "leaf", 2)
	mustPut(t, s, "mid", 1, latestOf("leaf", "leaf"))
	mustPut(t, s, "root", 1, pinTo("old-leaf", "leaf", 1), latestOf("mid", "mid"))

	g, err := compositionGraph(s, ctx, "root", 1)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%s@%d[%s@%d %s@%d[%s@%d]]", g.ProcedureID, g.Version,
		g.Edges[0].Node.ProcedureID, g.Edges[0].Node.Version,
		g.Edges[1].Node.ProcedureID, g.Edges[1].Node.Version,
		g.Edges[1].Node.Edges[0].Node.ProcedureID, g.Edges[1].Node.Edges[0].Node.Version)
	if want := "root@1[leaf@1 mid@1[leaf@2]]"; got != want {
		t.Fatalf("graph = %s, want %s", got, want)
	}
	if g.CanonicalKey != "key.root" || g.Edges[1].Reference.Name != "mid" {
		t.Fatalf("graph lost its keys or names: %+v", g)
	}

	// Contextual references follow the latest version at read time.
	mustPut(t, s, "leaf", 3)
	g, err = compositionGraph(s, ctx, "root", 1)
	if err != nil || g.Edges[1].Node.Edges[0].Node.Version != 3 || g.Edges[0].Node.Version != 1 {
		t.Fatalf("after leaf@3: %+v, %v", g, err)
	}

	if _, err := compositionGraph(s, ctx, "root", 2); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing version: err = %v", err)
	}
}

func TestCompositionGraphLimits(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	// p(i) references p(i-1), so p(N)'s graph is N references deep.
	mustPut(t, s, "p0", 1)
	for i := 1; i <= memory.MaxGraphDepth; i++ {
		mustPut(t, s, fmt.Sprint("p", i), 1, pinTo("next", fmt.Sprint("p", i-1), 1))
	}
	if _, err := compositionGraph(s, ctx, fmt.Sprint("p", memory.MaxGraphDepth), 1); err != nil {
		t.Fatalf("a graph exactly %d deep: %v", memory.MaxGraphDepth, err)
	}
	var limit *memory.GraphLimitError
	err := put(s, "too-deep", 1, pinTo("next", fmt.Sprint("p", memory.MaxGraphDepth), 1))
	if !errors.As(err, &limit) || limit.Limit != "depth" {
		t.Fatalf("writing a version %d deep: err = %v, want depth limit", memory.MaxGraphDepth+1, err)
	}
	if _, err := s.History(ctx, "too-deep"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("a version over the limit was stored: %v", err)
	}
}

// Versions stored before the cycle check existed may hold cycles; reading
// their graph reports the cycle instead of looping.
func TestCompositionGraphOfAStoredCycle(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	mustPut(t, s, "a", 1)

	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	tx, err := raw.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO procedure_version_references VALUES ('a', 2, 0, 'me', 'a', 'contextual', NULL, '{}')`,
		`INSERT INTO procedure_versions VALUES ('a', 2, 'p', 'm', '{}', '{}', 'r', '2026-10-09T12:00:00.000000000Z')`,
	} {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	_, err = compositionGraph(s, context.Background(), "a", 2)
	wantCycle(t, err, []memory.CycleStep{{ProcedureID: "a", Version: 2, Reference: "me"}, {ProcedureID: "a", Version: 2}})
}
