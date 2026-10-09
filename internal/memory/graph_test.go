package memory

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// fakeGraph is an in-memory GraphReader: versions[procedure][version] holds
// that version's references.
type fakeGraph map[string]map[int][]Reference

func (g fakeGraph) LatestVersion(_ context.Context, id string) (int, error) {
	latest := 0
	for v := range g[id] {
		latest = max(latest, v)
	}
	if latest == 0 {
		return 0, ErrNotFound
	}
	return latest, nil
}

func (g fakeGraph) VersionNode(_ context.Context, id string, version int) (string, []Reference, error) {
	refs, ok := g[id][version]
	if !ok {
		return "", nil, ErrNotFound
	}
	return "key." + id, refs, nil
}

func pin(name, target string, n int) Reference {
	return Reference{Name: name, ProcedureID: target, VersionPolicy: VersionPolicy{Kind: PolicyPin, Pin: n}, Inputs: jsontext.Value(`{}`)}
}

func latest(name, target string) Reference {
	return Reference{Name: name, ProcedureID: target, VersionPolicy: VersionPolicy{Kind: PolicyContextual}, Inputs: jsontext.Value(`{}`)}
}

// shape renders a graph as nested "procedure@version" strings.
func shape(n GraphNode) string {
	s := fmt.Sprintf("%s@%d", n.ProcedureID, n.Version)
	if len(n.Edges) == 0 {
		return s
	}
	s += "("
	for i, e := range n.Edges {
		if i > 0 {
			s += " "
		}
		s += e.Reference.Name + ":" + shape(e.Node)
	}
	return s + ")"
}

func TestExpandGraphSelectsExactVersions(t *testing.T) {
	g := fakeGraph{
		"a": {1: {pin("b-old", "b", 1), latest("b-new", "b"), latest("c", "c")}},
		"b": {1: nil, 2: {pin("c", "c", 1)}},
		"c": {1: nil, 2: nil, 3: nil},
	}
	root, err := ExpandGraph(context.Background(), g, "a", 1)
	if err != nil {
		t.Fatal(err)
	}
	// A shared target appears under each reference: diamonds are not cycles.
	if got, want := shape(root), "a@1(b-old:b@1 b-new:b@2(c:c@1) c:c@3)"; got != want {
		t.Fatalf("graph = %s, want %s", got, want)
	}
	if root.CanonicalKey != "key.a" || root.Edges[1].Node.CanonicalKey != "key.b" {
		t.Fatalf("canonical keys missing: %+v", root)
	}
}

func TestExpandGraphRejectsCycles(t *testing.T) {
	cases := []struct {
		name  string
		graph fakeGraph
		root  string
		want  []CycleStep
	}{
		{
			name:  "self-reference",
			graph: fakeGraph{"a": {1: {latest("me", "a")}}},
			root:  "a",
			want:  []CycleStep{{"a", 1, "me"}, {"a", 1, ""}},
		},
		{
			name:  "self-reference to an older version",
			graph: fakeGraph{"a": {1: nil, 2: {pin("old-me", "a", 1)}}},
			root:  "a",
			want:  []CycleStep{{"a", 2, "old-me"}, {"a", 1, ""}},
		},
		{
			name:  "two procedures",
			graph: fakeGraph{"a": {1: {latest("b", "b")}}, "b": {1: {latest("a", "a")}}},
			root:  "a",
			want:  []CycleStep{{"a", 1, "b"}, {"b", 1, "a"}, {"a", 1, ""}},
		},
		{
			name: "cycle below the root",
			graph: fakeGraph{
				"r": {1: {pin("x", "x", 1)}},
				"x": {1: {pin("y", "y", 1)}},
				"y": {1: {latest("x", "x")}},
			},
			root: "r",
			want: []CycleStep{{"x", 1, "y"}, {"y", 1, "x"}, {"x", 1, ""}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			latestRoot, _ := tc.graph.LatestVersion(context.Background(), tc.root)
			_, err := ExpandGraph(context.Background(), tc.graph, tc.root, latestRoot)
			var cycle *ReferenceCycleError
			if !errors.As(err, &cycle) || !reflect.DeepEqual(cycle.Cycle, tc.want) {
				t.Fatalf("err = %v, want cycle %+v", err, tc.want)
			}
		})
	}
}

func TestExpandGraphLimits(t *testing.T) {
	// A chain p0 -> p1 -> ... -> pN is N references deep.
	chain := func(n int) fakeGraph {
		g := fakeGraph{}
		for i := range n {
			g[fmt.Sprint("p", i)] = map[int][]Reference{1: {pin("next", fmt.Sprint("p", i+1), 1)}}
		}
		g[fmt.Sprint("p", n)] = map[int][]Reference{1: nil}
		return g
	}
	if _, err := ExpandGraph(context.Background(), chain(MaxGraphDepth), "p0", 1); err != nil {
		t.Fatalf("a graph exactly %d deep was rejected: %v", MaxGraphDepth, err)
	}
	var limit *GraphLimitError
	if _, err := ExpandGraph(context.Background(), chain(MaxGraphDepth+1), "p0", 1); !errors.As(err, &limit) || limit.Limit != "depth" {
		t.Fatalf("too deep: err = %v, want depth limit", err)
	}

	// Level i references level i+1 twice, so the tree doubles at each level:
	// levels 0..10 hold 2^11-1 = 2047 nodes and level 11 pushes past 2048.
	binary := func(levels int) fakeGraph {
		g := fakeGraph{}
		for i := range levels {
			next := fmt.Sprint("l", i+1)
			g[fmt.Sprint("l", i)] = map[int][]Reference{1: {pin("left", next, 1), pin("right", next, 1)}}
		}
		g[fmt.Sprint("l", levels)] = map[int][]Reference{1: nil}
		return g
	}
	if _, err := ExpandGraph(context.Background(), binary(10), "l0", 1); err != nil {
		t.Fatalf("a 2047-node graph was rejected: %v", err)
	}
	if _, err := ExpandGraph(context.Background(), binary(11), "l0", 1); !errors.As(err, &limit) || limit.Limit != "nodes" {
		t.Fatalf("too many nodes: err = %v, want node limit", err)
	}
}

func TestExpandGraphOfMissingVersion(t *testing.T) {
	if _, err := ExpandGraph(context.Background(), fakeGraph{}, "nope", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCycleErrorMessage(t *testing.T) {
	err := &ReferenceCycleError{Cycle: []CycleStep{{"a", 2, "b"}, {"b", 1, "a"}, {"a", 2, ""}}}
	if got, want := err.Error(), "references form a cycle: a@2 -[b]-> b@1 -[a]-> a@2"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
