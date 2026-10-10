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
	// VersionNode returns what a walk needs of one version, or an error
	// wrapping ErrNotFound.
	VersionNode(ctx context.Context, procedureID string, version int) (NodeVersion, error)
}

// NodeVersion is what a graph walk reads of one version.
type NodeVersion struct {
	CanonicalKey  string
	Applicability Applicability
	References    []Reference
}

// GraphNode is one procedure version in a composition graph, with the exact
// version selected for it. VerifiedBy is the execution that selected it in
// the resolution context, if any (ADR-0013), and Evidence says where that
// execution ran. Target is the status of the node's selected combination at
// a requested target (ADR-0018); nil without one. Applicability is the
// version's declared scope (ADR-0020).
type GraphNode struct {
	ProcedureID   string
	CanonicalKey  string
	Version       int
	Applicability Applicability
	VerifiedBy    string
	Evidence      *SelectionEvidence
	Target        *CombinationStatus
	Edges         []GraphEdge
}

// Selection says how a version was selected.
type Selection string

const (
	SelectedByPin      Selection = "pin"
	SelectedByEvidence Selection = "evidence"
	SelectedByLatest   Selection = "latest"
)

// GraphEdge is a reference together with the node it selected. With a
// target, a conditional edge carries the target's Decision. SkippedBy is the
// parent's evidence when that execution recorded the reference as not
// applicable, so the child was selected without it (ADR-0029).
type GraphEdge struct {
	Reference  Reference
	SelectedBy Selection
	Decision   TargetDecision
	SkippedBy  string
	Node       GraphNode
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

// NotApplicableError reports that no version of a procedure that the walk
// may select is applicable where it is selected (ADR-0020).
type NotApplicableError struct {
	ProcedureID string
	Version     int // the pinned version, or 0 for a contextual selection
}

func (e *NotApplicableError) Error() string {
	if e.Version > 0 {
		return fmt.Sprintf("version %d of procedure %q is not applicable here", e.Version, e.ProcedureID)
	}
	return fmt.Sprintf("no version of procedure %q is applicable here", e.ProcedureID)
}

// ExpandGraph walks the references of a stored version (ADR-0009): pinned
// references select their pinned version and contextual ones the target's
// latest version that the parent admits (ADR-0020). It returns a
// *ReferenceCycleError if a path reaches a procedure already on it, or a
// *GraphLimitError, never a partial graph.
func ExpandGraph(ctx context.Context, r GraphReader, procedureID string, version int) (GraphNode, error) {
	node, err := r.VersionNode(ctx, procedureID, version)
	if err != nil {
		return GraphNode{}, err
	}
	root := GraphNode{ProcedureID: procedureID, CanonicalKey: node.CanonicalKey, Version: version, Applicability: node.Applicability}
	w := &walker{r: r, nodes: 1, onPath: map[string]bool{procedureID: true}}
	if err := w.expand(ctx, &root, node.References, 0); err != nil {
		return GraphNode{}, err
	}
	return root, nil
}

type walker struct {
	r      GraphReader
	ev     *evidence // nil without a resolution context
	nodes  int
	path   []CycleStep
	onPath map[string]bool
}

func (w *walker) expand(ctx context.Context, node *GraphNode, refs []Reference, depth int) error {
	var links map[string]string
	var skipped map[string]bool
	if node.VerifiedBy != "" {
		var err error
		if links, skipped, err = w.ev.links(ctx, node.VerifiedBy); err != nil {
			return err
		}
	}
	for _, ref := range refs {
		version, by, verifiedBy, err := w.choose(ctx, ref, links, node.Applicability.Admits)
		if err != nil {
			return fmt.Errorf("resolve reference %q: %w", ref.Name, err)
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
		next, err := w.r.VersionNode(ctx, ref.ProcedureID, version)
		if err != nil {
			return fmt.Errorf("read reference %q target: %w", ref.Name, err)
		}
		child := GraphNode{ProcedureID: ref.ProcedureID, CanonicalKey: next.CanonicalKey, Version: version, Applicability: next.Applicability, VerifiedBy: verifiedBy}
		w.onPath[ref.ProcedureID] = true
		if err := w.expand(ctx, &child, next.References, depth+1); err != nil {
			return err
		}
		w.onPath[ref.ProcedureID] = false
		w.path = w.path[:len(w.path)-1]
		edge := GraphEdge{Reference: ref, SelectedBy: by, Node: child}
		if skipped[ref.Name] {
			edge.SkippedBy = node.VerifiedBy
		}
		node.Edges = append(node.Edges, edge)
	}
	return nil
}

// choose selects the version for ref (ADR-0013) among the versions admit
// accepts (ADR-0020). links maps the parent's reference names to the child
// executions of its evidence, if it has any; a link to a version admit
// rejects is ignored.
func (w *walker) choose(ctx context.Context, ref Reference, links map[string]string, admit func(Applicability) bool) (int, Selection, string, error) {
	admitted := func(version int) (bool, error) {
		node, err := w.r.VersionNode(ctx, ref.ProcedureID, version)
		if err != nil {
			return false, err
		}
		return admit(node.Applicability), nil
	}
	by := SelectedByPin
	if ref.VersionPolicy.Kind == PolicyContextual {
		by = SelectedByEvidence
	}
	if id, ok := links[ref.Name]; ok {
		run, err := w.ev.r.Run(ctx, id)
		if err != nil {
			return 0, "", "", err
		}
		if ok, err := admitted(run.Version); err != nil || ok {
			return run.Version, by, id, err
		}
	}
	if ref.VersionPolicy.Kind == PolicyPin {
		if ok, err := admitted(ref.VersionPolicy.Pin); err != nil || !ok {
			if err == nil {
				err = &NotApplicableError{ProcedureID: ref.ProcedureID, Version: ref.VersionPolicy.Pin}
			}
			return 0, "", "", err
		}
		if w.ev == nil {
			return ref.VersionPolicy.Pin, by, "", nil
		}
		id, err := w.ev.of(ctx, ref.ProcedureID, ref.VersionPolicy.Pin)
		return ref.VersionPolicy.Pin, by, id, err
	}
	if w.ev != nil {
		version, id, err := w.ev.highest(ctx, ref.ProcedureID, admitted)
		if err != nil || id != "" {
			return version, by, id, err
		}
	}
	latest, err := latestAdmitted(ctx, w.r, ref.ProcedureID, admit)
	if err == nil && latest == 0 {
		err = &NotApplicableError{ProcedureID: ref.ProcedureID}
	}
	return latest, SelectedByLatest, "", err
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

// CompositionGraph returns the composition graph of one version, resolved in
// c when c is not empty. With a context, the version must be applicable in
// its repository (ADR-0020).
func (s *Service) CompositionGraph(ctx context.Context, procedureID string, version int, c ResolutionContext) (GraphNode, error) {
	var p problems
	if version < 1 {
		p.add("version", "must be a version number of at least 1")
	}
	c = c.withOwnTarget()
	c.check(&p, true)
	if err := p.err(); err != nil {
		return GraphNode{}, err
	}
	if c.Repository != "" {
		id, err := s.identityID(ctx, c.Repository)
		if err != nil {
			return GraphNode{}, err
		}
		c.RepositoryID = id
	}
	res, err := s.store.Resolve(ctx, procedureID, VersionPolicy{Kind: PolicyPin, Pin: version}, c)
	if err == nil {
		return res.Graph, nil
	}
	if gerr := graphError(err); gerr != nil {
		return GraphNode{}, gerr
	}
	var na *NotApplicableError
	if errors.As(err, &na) && na.ProcedureID == procedureID && na.Version == version {
		return GraphNode{}, &ValidationError{Problems: []FieldProblem{{Field: "repository",
			Message: fmt.Sprintf("version %d of procedure %q does not apply in repository %q", version, procedureID, c.Repository)}}}
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
