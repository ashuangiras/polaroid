package memory

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
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
// repository and environment: a commit and the root's effective inputs.
type Target struct {
	Commit string
	Inputs jsontext.Value
}

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
		}
		if err := w.ev.annotate(ctx, &root, inputs); err != nil {
			return Resolution{}, err
		}
	}
	return Resolution{SelectedBy: by, Graph: root}, nil
}

// annotate adds each node's selection evidence and, with a target, the
// status of the node's selected combination at the target. inputs are the
// node's effective inputs at the target.
func (e *evidence) annotate(ctx context.Context, node *GraphNode, inputs jsontext.Value) error {
	if node.VerifiedBy != "" {
		run, err := e.r.Run(ctx, node.VerifiedBy)
		if err != nil {
			return fmt.Errorf("read evidence %q: %w", node.VerifiedBy, err)
		}
		node.Evidence = &SelectionEvidence{ExecutionID: run.ID, Repository: run.Repository, RepositoryID: run.Identity.ID, Commit: run.Commit, Environment: run.Environment.Name}
	}
	if e.c.Target != nil {
		status, err := e.targetStatus(ctx, node, inputs)
		if err != nil {
			return err
		}
		node.Target = &status
	}
	for i := range node.Edges {
		edge := &node.Edges[i]
		var childInputs jsontext.Value
		if e.c.Target != nil {
			var err error
			if childInputs, err = mapInputs(edge.Reference.Inputs, inputs); err != nil {
				return fmt.Errorf("map inputs of reference %q: %w", edge.Reference.Name, err)
			}
		}
		if err := e.annotate(ctx, &edge.Node, childInputs); err != nil {
			return err
		}
	}
	return nil
}

// targetStatus returns the status of node's selected combination at the
// target: its latest execution there decides, as for any combination
// (ADR-0012). With no execution it is unverified.
func (e *evidence) targetStatus(ctx context.Context, node *GraphNode, inputs jsontext.Value) (CombinationStatus, error) {
	canonical := inputs.Clone()
	if err := canonical.Canonicalize(jsontext.CanonicalizeRawInts(false)); err != nil {
		return CombinationStatus{}, fmt.Errorf("canonicalize target inputs: %w", err)
	}
	identity, err := e.r.Identity(ctx, e.c.Repository)
	if err != nil {
		return CombinationStatus{}, err
	}
	repository, repositoryID := combinationRepository(e.c.Repository, identity)
	want := Combination{
		Repository:   repository,
		RepositoryID: repositoryID,
		Commit:       e.c.Target.Commit,
		Environment:  e.c.Environment,
		Inputs:       canonical,
		Children:     selectedChildren(node),
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

// selectedChildren is the child-version tree a graph node selected, in the
// form a combination records it.
func selectedChildren(node *GraphNode) []ChildVersion {
	var out []ChildVersion
	for i := range node.Edges {
		e := &node.Edges[i]
		out = append(out, ChildVersion{Reference: e.Reference.Name, Version: e.Node.Version, Children: selectedChildren(&e.Node)})
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

// links maps each reference of a verified execution to its child execution.
func (e *evidence) links(ctx context.Context, id string) (map[string]string, error) {
	run, err := e.r.Run(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read evidence %q: %w", id, err)
	}
	links := make(map[string]string, len(run.Children))
	for _, c := range run.Children {
		links[c.Reference] = c.ExecutionID
	}
	return links, nil
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
