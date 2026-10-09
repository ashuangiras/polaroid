package memory

import (
	"context"
	"fmt"
)

// ResolutionContext is the context a version is resolved for (ADR-0013).
// The zero value means no context: contextual references select the latest
// version.
type ResolutionContext struct {
	Repository  string
	Environment string
}

// check validates c. With optional, an empty context is accepted.
func (c ResolutionContext) check(p *problems, optional bool) {
	if optional && c == (ResolutionContext{}) {
		return
	}
	checkRepository(p, "repository", c.Repository)
	checkKey(p, "environment", c.Environment)
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
	// in c, highest version first.
	LatestRuns(ctx context.Context, procedureID string, c ResolutionContext) ([]VersionRun, error)
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
	if c != (ResolutionContext{}) {
		w.ev = &evidence{r: r, c: c, v: newVerifier(r), runs: map[string][]VersionRun{}}
	}
	version, by, verifiedBy, err := w.choose(ctx, Reference{ProcedureID: procedureID, VersionPolicy: policy}, nil)
	if err != nil {
		return Resolution{}, err
	}
	key, refs, err := r.VersionNode(ctx, procedureID, version)
	if err != nil {
		return Resolution{}, err
	}
	root := GraphNode{ProcedureID: procedureID, CanonicalKey: key, Version: version, VerifiedBy: verifiedBy}
	if err := w.expand(ctx, &root, refs, 0); err != nil {
		return Resolution{}, err
	}
	return Resolution{SelectedBy: by, Graph: root}, nil
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

// highest returns the highest version of procedureID verified in the
// context and its evidence, or an empty ID if none is.
func (e *evidence) highest(ctx context.Context, procedureID string) (int, string, error) {
	runs, err := e.latestRuns(ctx, procedureID)
	if err != nil {
		return 0, "", err
	}
	for _, run := range runs {
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
// repository and environment.
func (s *Service) ResolveBinding(ctx context.Context, bindingID, environment string) (BindingResolution, error) {
	var p problems
	checkKey(&p, "environment", environment)
	if err := p.err(); err != nil {
		return BindingResolution{}, err
	}
	h, err := s.store.BindingHistory(ctx, bindingID)
	if err != nil {
		return BindingResolution{}, describeLookup(err, fmt.Sprintf("binding %q", bindingID))
	}
	rev := h.Revisions[len(h.Revisions)-1]
	c := ResolutionContext{Repository: h.Binding.Repository, Environment: environment}
	res, err := s.store.Resolve(ctx, h.Binding.ProcedureID, rev.VersionPolicy, c)
	if err != nil {
		if gerr := graphError(err); gerr != nil {
			return BindingResolution{}, gerr
		}
		return BindingResolution{}, fmt.Errorf("resolve binding: %w", err)
	}
	return BindingResolution{Binding: h.Binding, Revision: rev, Environment: environment, Resolution: res}, nil
}
