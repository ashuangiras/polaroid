package memory

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ResolutionContext is the context a version is resolved for (ADR-0013).
// The zero value means no context: contextual references select the latest
// version. Target, if set, asks for each selected combination's status at
// an exact commit and inputs (ADR-0018); it never changes the selection.
// RepositoryID is the ID Repository is registered to, "" if it is not; the
// root must be applicable there (ADR-0020).
type ResolutionContext struct {
	Repository   string
	Environment  string
	Target       *Target
	RepositoryID string
}

// Target is what a resolution is checked against, in its context's
// repository and environment: a commit, the root's effective inputs and
// the target's applicability decisions for conditional references, keyed
// by reference path (names from the root joined by "/", ADR-0029).
type Target struct {
	Commit    string
	Inputs    jsontext.Value
	Decisions jsontext.Value
	decided   map[string]bool
}

// TargetDecision is a target's decision for one conditional reference.
type TargetDecision string

const (
	DecisionApplicable    TargetDecision = "applicable"
	DecisionNotApplicable TargetDecision = "not_applicable"
	DecisionUndecided     TargetDecision = "undecided"
)

// SelectionEvidence is the execution that selected a node, and where it ran:
// the identifier it was recorded with and that identifier's registered
// repository, if any. It says nothing about any other commit, inputs or
// environment.
type SelectionEvidence struct {
	ExecutionID  string
	Repository   string
	RepositoryID string
	Commit       string
	Environment  string
}

// check validates c. With optional, an empty context is accepted. A valid
// target's inputs are replaced with their compacted form.
func (c ResolutionContext) check(p *problems, optional bool) {
	if optional && c == (ResolutionContext{}) {
		return
	}
	checkRepository(p, "repository", c.Repository)
	checkKey(p, "environment", c.Environment)
	if c.Target != nil {
		c.Target.check(p)
	}
}

// check validates t and replaces valid inputs with their compacted form.
func (t *Target) check(p *problems) {
	if t.Commit == "" && len(t.Inputs) == 0 && len(t.Decisions) > 0 {
		p.add("decisions", "requires commit and inputs")
		return
	}
	switch {
	case t.Commit == "":
		p.add("commit", "is required with inputs")
	case !commitPattern.MatchString(t.Commit):
		p.add("commit", "must be a full commit hash: 40 or 64 lowercase hex characters")
	}
	if len(t.Inputs) == 0 {
		p.add("inputs", "is required with commit")
	} else {
		t.Inputs = checkObject(p, "inputs", t.Inputs)
	}
	if len(t.Decisions) > 0 {
		t.decided = checkTargetDecisions(p, t.Decisions)
	}
}

const decisionsRule = `must be a JSON object mapping reference paths (names from the root joined by "/") to true or false`

// checkTargetDecisions parses a target's decisions.
func checkTargetDecisions(p *problems, v jsontext.Value) map[string]bool {
	c := checkObject(p, "decisions", v)
	if c == nil {
		return nil
	}
	var decided map[string]bool
	if err := json.Unmarshal(c, &decided); err != nil {
		p.add("decisions", decisionsRule)
		return nil
	}
	for path := range decided {
		for _, name := range strings.Split(path, "/") {
			if !canonicalKeyPattern.MatchString(name) {
				p.add("decisions", fmt.Sprintf("path %q: %s", path, decisionsRule))
				break
			}
		}
	}
	return decided
}

// checkDecisionPaths reports every decided path that does not reach a
// conditional reference of the graph outside a reference decided not
// applicable.
func checkDecisionPaths(root *GraphNode, decided map[string]bool) error {
	valid := map[string]bool{}
	var walk func(node *GraphNode, path string)
	walk = func(node *GraphNode, path string) {
		for i := range node.Edges {
			edge := &node.Edges[i]
			child := joinPath(path, edge.Reference.Name)
			if edge.Reference.Conditional() {
				valid[child] = true
				if applicable, ok := decided[child]; ok && !applicable {
					continue
				}
			}
			walk(&edge.Node, child)
		}
	}
	walk(root, "")
	var p problems
	for _, path := range slices.Sorted(maps.Keys(decided)) {
		if !valid[path] {
			p.add("decisions", fmt.Sprintf("path %q does not reach a conditional reference of the selected graph outside a reference decided not applicable", path))
		}
	}
	return p.err()
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "/" + name
}

// withOwnTarget returns c with a copy of its target, so that checking it
// never changes the caller's value.
func (c ResolutionContext) withOwnTarget() ResolutionContext {
	if c.Target != nil {
		t := *c.Target
		c.Target = &t
	}
	return c
}

// VersionRun is the latest execution of one version in a context.
type VersionRun struct {
	Version     int
	ExecutionID string
}

// ResolutionReader gives a resolution a consistent view of versions and
// executions.
type ResolutionReader interface {
	GraphReader
	VerificationReader
	// LatestRuns returns the latest execution of each version of a procedure
	// in c, highest version first. c.Repository matches by identity.
	LatestRuns(ctx context.Context, procedureID string, c ResolutionContext) ([]VersionRun, error)
	// Identity returns the registered repository identifier belongs to, or
	// the zero value if it is not registered.
	Identity(ctx context.Context, identifier string) (RepositoryIdentity, error)
}

// Resolution is the version selected for a policy and its resolved graph.
type Resolution struct {
	SelectedBy Selection
	Graph      GraphNode
}

// BindingResolution resolves a binding's latest revision in its repository
// and one environment.
type BindingResolution struct {
	Binding     Binding
	Revision    BindingRevision
	Environment string
	Resolution
}

// ResolveGraph selects a version of a procedure for policy and walks its
// graph, both in c (ADR-0013). Without a context it selects as ExpandGraph
// does. It returns the graph errors of ExpandGraph, never a partial graph.
func ResolveGraph(ctx context.Context, r ResolutionReader, procedureID string, policy VersionPolicy, c ResolutionContext) (Resolution, error) {
	w := &walker{r: r, nodes: 1, onPath: map[string]bool{procedureID: true}}
	admit := func(Applicability) bool { return true }
	if c != (ResolutionContext{}) {
		w.ev = &evidence{r: r, c: c, v: newVerifier(r), runs: map[string][]VersionRun{}}
		admit = func(a Applicability) bool { return a.ApplicableIn(c.RepositoryID) }
	}
	version, by, verifiedBy, err := w.choose(ctx, Reference{ProcedureID: procedureID, VersionPolicy: policy}, nil, admit)
	if err != nil {
		return Resolution{}, err
	}
	node, err := r.VersionNode(ctx, procedureID, version)
	if err != nil {
		return Resolution{}, err
	}
	root := GraphNode{ProcedureID: procedureID, CanonicalKey: node.CanonicalKey, Version: version, Applicability: node.Applicability, VerifiedBy: verifiedBy}
	if err := w.expand(ctx, &root, node.References, 0); err != nil {
		return Resolution{}, err
	}
	if w.ev != nil {
		var inputs jsontext.Value
		if c.Target != nil {
			inputs = c.Target.Inputs
			if err := checkDecisionPaths(&root, c.Target.decided); err != nil {
				return Resolution{}, err
			}
		}
		if err := w.ev.annotate(ctx, &root, inputs, "", c.Target != nil); err != nil {
			return Resolution{}, err
		}
	}
	return Resolution{SelectedBy: by, Graph: root}, nil
}

// annotate adds each node's selection evidence and, when target is set, the
// status of the node's expected combination at the target. inputs are the
// node's effective inputs at the target and path its reference path. A
// reference the target decides not applicable gets no target status below
// it (ADR-0029).
func (e *evidence) annotate(ctx context.Context, node *GraphNode, inputs jsontext.Value, path string, target bool) error {
	if node.VerifiedBy != "" {
		run, err := e.r.Run(ctx, node.VerifiedBy)
		if err != nil {
			return fmt.Errorf("read evidence %q: %w", node.VerifiedBy, err)
		}
		node.Evidence = &SelectionEvidence{ExecutionID: run.ID, Repository: run.Repository, RepositoryID: run.Identity.ID, Commit: run.Commit, Environment: run.Environment.Name}
	}
	if target {
		status, err := e.targetStatus(ctx, node, inputs, path)
		if err != nil {
			return err
		}
		node.Target = &status
	}
	for i := range node.Edges {
		edge := &node.Edges[i]
		childPath := joinPath(path, edge.Reference.Name)
		childTarget := target
		if target && edge.Reference.Conditional() {
			edge.Decision = e.c.Target.decision(childPath)
			childTarget = edge.Decision != DecisionNotApplicable
		}
		var childInputs jsontext.Value
		if childTarget {
			var err error
			if childInputs, err = mapInputs(edge.Reference.Inputs, inputs); err != nil {
				return fmt.Errorf("map inputs of reference %q: %w", edge.Reference.Name, err)
			}
		}
		if err := e.annotate(ctx, &edge.Node, childInputs, childPath, childTarget); err != nil {
			return err
		}
	}
	return nil
}

// decision returns t's decision for the conditional reference at path.
func (t *Target) decision(path string) TargetDecision {
	applicable, ok := t.decided[path]
	switch {
	case !ok:
		return DecisionUndecided
	case applicable:
		return DecisionApplicable
	}
	return DecisionNotApplicable
}

// targetStatus returns the status of node's expected combination at the
// target: its latest execution there decides, as for any combination
// (ADR-0012). With no execution it is unverified. With an undecided
// conditional reference below the node, no execution is looked up and the
// node is unverified (ADR-0029).
func (e *evidence) targetStatus(ctx context.Context, node *GraphNode, inputs jsontext.Value, path string) (CombinationStatus, error) {
	canonical, err := canonicalInputs(inputs)
	if err != nil {
		return CombinationStatus{}, fmt.Errorf("canonicalize target inputs: %w", err)
	}
	identity, err := e.r.Identity(ctx, e.c.Repository)
	if err != nil {
		return CombinationStatus{}, err
	}
	repository, repositoryID := combinationRepository(e.c.Repository, identity)
	var undecided []string
	want := Combination{
		Repository:   repository,
		RepositoryID: repositoryID,
		Commit:       e.c.Target.Commit,
		Environment:  e.c.Environment,
		Inputs:       canonical,
		Children:     e.c.Target.expected(node, path, &undecided),
	}
	if len(undecided) > 0 {
		return CombinationStatus{Combination: want, ExecutionIDs: []string{}, Undecided: undecided}, nil
	}
	statuses, err := ListCombinations(ctx, e.r, VerificationFilter{
		ProcedureID: node.ProcedureID, Version: node.Version,
		Repository: e.c.Repository, Commit: want.Commit, Environment: want.Environment,
	})
	if err != nil {
		return CombinationStatus{}, err
	}
	for _, s := range statuses {
		if s.Combination.key() == want.key() {
			return s, nil
		}
	}
	return CombinationStatus{Combination: want, ExecutionIDs: []string{}}, nil
}

// expected is the child-version tree the target expects under node at path:
// a reference decided not applicable is skipped, and an undecided one is
// left out and its path added to undecided.
func (t *Target) expected(node *GraphNode, path string, undecided *[]string) []ChildVersion {
	var out []ChildVersion
	for i := range node.Edges {
		e := &node.Edges[i]
		child := joinPath(path, e.Reference.Name)
		if e.Reference.Conditional() {
			switch t.decision(child) {
			case DecisionUndecided:
				*undecided = append(*undecided, child)
				continue
			case DecisionNotApplicable:
				out = append(out, ChildVersion{Reference: e.Reference.Name, Skipped: true})
				continue
			}
		}
		out = append(out, ChildVersion{Reference: e.Reference.Name, Version: e.Node.Version, Children: t.expected(&e.Node, child, undecided)})
	}
	return out
}

// mapInputs applies a reference's input mapping (ADR-0008) to the parent's
// effective inputs: {"input": p} takes the parent's member p, {"value": v}
// takes v. A child input whose parent member is absent is left out.
func mapInputs(mapping, parent jsontext.Value) (jsontext.Value, error) {
	var from map[string]jsontext.Value
	if err := json.Unmarshal(parent, &from); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := jsontext.NewEncoder(&out)
	dec := jsontext.NewDecoder(bytes.NewReader(mapping))
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return nil, err
	}
	for dec.PeekKind() != jsontext.KindEndObject {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		name := tok.String()
		var source map[string]jsontext.Value
		if err := json.UnmarshalDecode(dec, &source); err != nil {
			return nil, err
		}
		value, ok := source["value"]
		if !ok {
			var input string
			if err := json.Unmarshal(source["input"], &input); err != nil {
				return nil, err
			}
			value, ok = from[input]
		}
		if !ok {
			continue
		}
		if err := enc.WriteToken(jsontext.String(name)); err != nil {
			return nil, err
		}
		if err := enc.WriteValue(value); err != nil {
			return nil, err
		}
	}
	if err := enc.WriteToken(jsontext.EndObject); err != nil {
		return nil, err
	}
	return jsontext.Value(bytes.TrimSpace(out.Bytes())), nil
}

// evidence finds the executions that verify versions in one context.
type evidence struct {
	r    ResolutionReader
	c    ResolutionContext
	v    *verifier
	runs map[string][]VersionRun
}

func (e *evidence) latestRuns(ctx context.Context, procedureID string) ([]VersionRun, error) {
	if runs, ok := e.runs[procedureID]; ok {
		return runs, nil
	}
	runs, err := e.r.LatestRuns(ctx, procedureID, e.c)
	if err != nil {
		return nil, err
	}
	e.runs[procedureID] = runs
	return runs, nil
}

// of returns the execution that verifies procedureID@version in the
// context, or "" if its latest execution there is not verified.
func (e *evidence) of(ctx context.Context, procedureID string, version int) (string, error) {
	runs, err := e.latestRuns(ctx, procedureID)
	if err != nil {
		return "", err
	}
	for _, run := range runs {
		if run.Version == version {
			return e.verified(ctx, run.ExecutionID)
		}
	}
	return "", nil
}

// highest returns the highest version of procedureID that admitted accepts
// and that is verified in the context, and its evidence, or an empty ID if
// none is.
func (e *evidence) highest(ctx context.Context, procedureID string, admitted func(int) (bool, error)) (int, string, error) {
	runs, err := e.latestRuns(ctx, procedureID)
	if err != nil {
		return 0, "", err
	}
	for _, run := range runs {
		if ok, err := admitted(run.Version); err != nil || !ok {
			if err != nil {
				return 0, "", err
			}
			continue
		}
		id, err := e.verified(ctx, run.ExecutionID)
		if err != nil || id != "" {
			return run.Version, id, err
		}
	}
	return 0, "", nil
}

func (e *evidence) verified(ctx context.Context, id string) (string, error) {
	v, err := e.v.verify(ctx, id)
	if err != nil || !v.Verified {
		return "", err
	}
	return id, nil
}

// links maps each reference of a verified execution to its child execution,
// and returns the conditional references it recorded as not applicable.
func (e *evidence) links(ctx context.Context, id string) (map[string]string, map[string]bool, error) {
	run, err := e.r.Run(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("read evidence %q: %w", id, err)
	}
	links := make(map[string]string, len(run.Children))
	for _, c := range run.Children {
		links[c.Reference] = c.ExecutionID
	}
	skipped := map[string]bool{}
	for _, d := range run.Decisions {
		if d.Applicable != nil && !*d.Applicable {
			skipped[d.Reference] = true
		}
	}
	return links, skipped, nil
}

// ResolveBinding resolves a binding's latest revision in the binding's
// repository and environment, and checks it against target if given.
func (s *Service) ResolveBinding(ctx context.Context, bindingID, environment string, target *Target) (BindingResolution, error) {
	var p problems
	checkKey(&p, "environment", environment)
	if target != nil {
		t := *target
		target = &t
		target.check(&p)
	}
	if err := p.err(); err != nil {
		return BindingResolution{}, err
	}
	h, err := s.store.BindingHistory(ctx, bindingID)
	if err != nil {
		return BindingResolution{}, describeLookup(err, fmt.Sprintf("binding %q", bindingID))
	}
	rev := h.Revisions[len(h.Revisions)-1]
	repositoryID, err := s.identityID(ctx, h.Binding.Repository)
	if err != nil {
		return BindingResolution{}, err
	}
	c := ResolutionContext{Repository: h.Binding.Repository, Environment: environment, Target: target, RepositoryID: repositoryID}
	res, err := s.store.Resolve(ctx, h.Binding.ProcedureID, rev.VersionPolicy, c)
	if err != nil {
		if gerr := graphError(err); gerr != nil {
			return BindingResolution{}, gerr
		}
		return BindingResolution{}, fmt.Errorf("resolve binding: %w", err)
	}
	return BindingResolution{Binding: h.Binding, Revision: rev, Environment: environment, Resolution: res}, nil
}
