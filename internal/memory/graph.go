package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Limits on walking a composition graph, on reads and writes alike.
const (
	MaxGraphDepth = 32
	MaxGraphNodes = 2048
)

// GraphReader gives a graph walk a consistent view of stored versions.
type GraphReader interface {
	// LatestVersion returns the latest version of a procedure, or an error
	// wrapping ErrNotFound.
	LatestVersion(ctx context.Context, procedureID string) (int, error)
	// VersionNode returns a version's canonical key and references, or an
	// error wrapping ErrNotFound.
	VersionNode(ctx context.Context, procedureID string, version int) (canonicalKey string, refs []Reference, err error)
}

// GraphNode is one procedure version in a composition graph, with the exact
// version selected for it.
type GraphNode struct {
	ProcedureID  string
	CanonicalKey string
	Version      int
	Edges        []GraphEdge
}

// GraphEdge is a reference together with the node it selected.
type GraphEdge struct {
	Reference Reference
	Node      GraphNode
}

// CycleStep is one node on a reference cycle and the reference followed out
// of it. The last step repeats an earlier procedure and has no Reference.
type CycleStep struct {
	ProcedureID string
	Version     int
	Reference   string
}

// ReferenceCycleError reports a path of references that returns to a
// procedure already on it.
type ReferenceCycleError struct {
	Cycle []CycleStep
}

func (e *ReferenceCycleError) Error() string {
	var b strings.Builder
	b.WriteString("references form a cycle: ")
	for _, s := range e.Cycle {
		fmt.Fprintf(&b, "%s@%d", s.ProcedureID, s.Version)
		if s.Reference != "" {
			fmt.Fprintf(&b, " -[%s]-> ", s.Reference)
		}
	}
	return b.String()
}

// GraphLimitError reports a composition graph deeper than MaxGraphDepth or
// larger than MaxGraphNodes.
type GraphLimitError struct {
	Limit string // "depth" or "nodes"
	Max   int
}

func (e *GraphLimitError) Error() string {
	if e.Limit == "depth" {
		return fmt.Sprintf("composition graph is deeper than %d references", e.Max)
	}
	return fmt.Sprintf("composition graph has more than %d nodes", e.Max)
}

// ExpandGraph walks the references of a stored version (ADR-0009): pinned
// references select their pinned version and contextual ones the target's
// latest version. It returns a *ReferenceCycleError if a path reaches a
// procedure already on it, or a *GraphLimitError, never a partial graph.
func ExpandGraph(ctx context.Context, r GraphReader, procedureID string, version int) (GraphNode, error) {
	key, refs, err := r.VersionNode(ctx, procedureID, version)
	if err != nil {
		return GraphNode{}, err
	}
	root := GraphNode{ProcedureID: procedureID, CanonicalKey: key, Version: version}
	w := &walker{r: r, nodes: 1, onPath: map[string]bool{procedureID: true}}
	if err := w.expand(ctx, &root, refs, 0); err != nil {
		return GraphNode{}, err
	}
	return root, nil
}

type walker struct {
	r      GraphReader
	nodes  int
	path   []CycleStep
	onPath map[string]bool
}

func (w *walker) expand(ctx context.Context, node *GraphNode, refs []Reference, depth int) error {
	for _, ref := range refs {
		version := ref.VersionPolicy.Pin
		if ref.VersionPolicy.Kind == PolicyContextual {
			latest, err := w.r.LatestVersion(ctx, ref.ProcedureID)
			if err != nil {
				return fmt.Errorf("resolve reference %q: %w", ref.Name, err)
			}
			version = latest
		}
		w.path = append(w.path, CycleStep{ProcedureID: node.ProcedureID, Version: node.Version, Reference: ref.Name})
		if w.onPath[ref.ProcedureID] {
			return &ReferenceCycleError{Cycle: w.cycleTo(ref.ProcedureID, version)}
		}
		if depth+1 > MaxGraphDepth {
			return &GraphLimitError{Limit: "depth", Max: MaxGraphDepth}
		}
		if w.nodes++; w.nodes > MaxGraphNodes {
			return &GraphLimitError{Limit: "nodes", Max: MaxGraphNodes}
		}
		key, childRefs, err := w.r.VersionNode(ctx, ref.ProcedureID, version)
		if err != nil {
			return fmt.Errorf("read reference %q target: %w", ref.Name, err)
		}
		child := GraphNode{ProcedureID: ref.ProcedureID, CanonicalKey: key, Version: version}
		w.onPath[ref.ProcedureID] = true
		if err := w.expand(ctx, &child, childRefs, depth+1); err != nil {
			return err
		}
		w.onPath[ref.ProcedureID] = false
		w.path = w.path[:len(w.path)-1]
		node.Edges = append(node.Edges, GraphEdge{Reference: ref, Node: child})
	}
	return nil
}

// cycleTo returns the path from the first visit of procedureID, closed by
// the repeated procedure at the version the last reference selected.
func (w *walker) cycleTo(procedureID string, version int) []CycleStep {
	start := 0
	for i, s := range w.path {
		if s.ProcedureID == procedureID {
			start = i
			break
		}
	}
	cycle := append([]CycleStep(nil), w.path[start:]...)
	return append(cycle, CycleStep{ProcedureID: procedureID, Version: version})
}

// CompositionGraph returns the composition graph of one version.
func (s *Service) CompositionGraph(ctx context.Context, procedureID string, version int) (GraphNode, error) {
	if version < 1 {
		return GraphNode{}, &ValidationError{Problems: []FieldProblem{{Field: "version", Message: "must be a version number of at least 1"}}}
	}
	g, err := s.store.CompositionGraph(ctx, procedureID, version)
	if err == nil {
		return g, nil
	}
	if gerr := graphError(err); gerr != nil {
		return GraphNode{}, gerr
	}
	return GraphNode{}, describeLookup(err, fmt.Sprintf("procedure %q version %d", procedureID, version))
}

// graphError returns err's *ReferenceCycleError or *GraphLimitError, or nil.
func graphError(err error) error {
	var cycle *ReferenceCycleError
	if errors.As(err, &cycle) {
		return cycle
	}
	var limit *GraphLimitError
	if errors.As(err, &limit) {
		return limit
	}
	return nil
}
