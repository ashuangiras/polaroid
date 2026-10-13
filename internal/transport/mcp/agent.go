package mcp

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"log/slog"
	"slices"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Agent mode (ADR-0043): a separate endpoint with only the tools everyday
// procedural-knowledge use needs. It records no executions; the full API at
// /mcp keeps every operation, including execution and verification records.

const agentInstructions = `Polaroid agent mode: procedural knowledge, proven methods of working.
1. discover a procedure for your task; with repository it also names that repository's bindings of the candidates.
2. read it, or a repository binding, for executable guidance: contract, instructions, effective inputs, and each reference with the exact version and inputs of its child. Read a child before following it. Decide each conditional reference yourself from its condition.
3. Do the work with your own tools and check the outcome against the contract. Nothing is recorded here, and following a procedure verifies nothing by itself: an audit-grade verification claim needs evidence from your verification system (or the full API at /mcp).
4. Only when the outcome shows the procedure itself is wrong, incomplete, ambiguous or improvable, send feedback with a classification. Product defects, environment problems and transient failures are not procedural knowledge: handle them, do not report them here.`

// procedural are the feedback classifications that concern a procedure,
// and nonProcedural those that do not and are refused.
var (
	procedural    = []string{"incorrect_applicability", "missing_precondition", "incorrect_instruction", "inadequate_verification", "improved_method"}
	nonProcedural = []string{"product_defect", "environment_problem", "transient_failure"}
)

type (
	agentDiscoverArgs struct {
		Task       string `json:"task" jsonschema:"the task, in your own words"`
		Repository string `json:"repository,omitzero" jsonschema:"your repository identifier, e.g. github.com/owner/repo: only procedures that apply there, and its bindings of them"`
		Limit      int    `json:"limit,omitzero" jsonschema:"at most this many candidates, 1 to 50 (default 5)"`
	}
	agentReadArgs struct {
		ProcedureID  string         `json:"procedure_id,omitzero" jsonschema:"give this, canonical_key, or repository with binding"`
		CanonicalKey string         `json:"canonical_key,omitzero"`
		Version      int            `json:"version,omitzero" jsonschema:"default: the latest; not with binding"`
		Repository   string         `json:"repository,omitzero"`
		Environment  string         `json:"environment,omitzero" jsonschema:"with repository: select contextual versions from evidence there, as resolve_binding does; required when a binding's selection depends on evidence"`
		Binding      string         `json:"binding,omitzero" jsonschema:"a binding name in repository: its procedure, selected version and inputs"`
		Inputs       jsontext.Value `json:"inputs,omitzero" jsonschema:"your effective inputs, to get each reference's mapped child inputs; not with binding"`
		Full         bool           `json:"full,omitzero" jsonschema:"also philosophy, applicability, revision_reason and created_at"`
	}
	agentFeedbackArgs struct {
		ProcedureID    string         `json:"procedure_id"`
		Version        int            `json:"version" jsonschema:"the version you followed"`
		Classification string         `json:"classification" jsonschema:"incorrect_applicability, missing_precondition, incorrect_instruction, inadequate_verification or improved_method"`
		Summary        string         `json:"summary" jsonschema:"one line"`
		Details        string         `json:"details" jsonschema:"what you expected, what happened, why the procedure is at fault"`
		Reporter       string         `json:"reporter" jsonschema:"who reports, canonical-key form, e.g. copilot.agent"`
		Repository     string         `json:"repository,omitzero"`
		Evidence       jsontext.Value `json:"evidence,omitzero" jsonschema:"a small object to reproduce the finding: the step, command, exit status, a short excerpt; never whole logs"`
	}
)

type (
	agentCandidate struct {
		ProcedureID  string `json:"procedure_id"`
		CanonicalKey string `json:"canonical_key"`
		Version      int    `json:"version"`
		Scope        string `json:"scope"`
		Goal         string `json:"goal,omitzero"`
	}
	agentBindingRef struct {
		Name        string `json:"name"`
		ProcedureID string `json:"procedure_id"`
	}
	agentDiscovery struct {
		Candidates []agentCandidate  `json:"candidates"`
		Bindings   []agentBindingRef `json:"bindings,omitzero"`
	}
	agentBinding struct {
		ID         string `json:"id"`
		Revision   int    `json:"revision"`
		Repository string `json:"repository"`
		Name       string `json:"name"`
	}
	agentReference struct {
		Name         string         `json:"name"`
		ProcedureID  string         `json:"procedure_id"`
		Version      int            `json:"version"`
		SelectedBy   string         `json:"selected_by"`
		Condition    *string        `json:"condition,omitzero"`
		Inputs       jsontext.Value `json:"inputs,omitzero"`
		InputMapping jsontext.Value `json:"input_mapping,omitzero"`
	}
	agentGuidance struct {
		ProcedureID    string              `json:"procedure_id"`
		CanonicalKey   string              `json:"canonical_key"`
		Version        int                 `json:"version"`
		SelectedBy     string              `json:"selected_by,omitzero"`
		LatestVersion  int                 `json:"latest_version"`
		Retired        bool                `json:"retired,omitzero"`
		Scope          string              `json:"scope"`
		Goal           string              `json:"goal,omitzero"`
		Method         string              `json:"method"`
		Binding        *agentBinding       `json:"binding,omitzero"`
		Inputs         jsontext.Value      `json:"inputs,omitzero"`
		Contract       jsontext.Value      `json:"contract"`
		Instructions   jsontext.Value      `json:"instructions"`
		References     []agentReference    `json:"references,omitzero"`
		Philosophy     *string             `json:"philosophy,omitzero"`
		Applicability  *wire.Applicability `json:"applicability,omitzero"`
		RevisionReason *string             `json:"revision_reason,omitzero"`
		CreatedAt      *time.Time          `json:"created_at,omitzero"`
	}
	agentFeedbackContext struct {
		Classification string         `json:"classification"`
		Evidence       jsontext.Value `json:"evidence,omitzero"`
	}
	agentFeedbackResult struct {
		ID             string    `json:"id"`
		Classification string    `json:"classification"`
		CreatedAt      time.Time `json:"created_at"`
	}
)

func newAgentServer(svc *memory.Service, logger *slog.Logger) *sdk.Server {
	s := baseServer(agentInstructions, logger)
	t := &tools{svc: svc, logger: logger}
	const read, write = true, false
	add(s, t, "discover", "Find procedures for a task, best match first (lexical: shared words). Returns identity, latest version, scope and goal; with repository, also its bindings of them.", read, t.agentDiscover)
	add(s, t, "read", "Get the executable guidance of one procedure version or repository binding: contract, instructions, effective inputs, and references with each child's exact version and inputs.", read, t.agentRead)
	add(s, t, "feedback", "Report a finding that changes what is known about a procedure version, with a classification. Not for product defects, environment problems or transient failures.", write, t.agentFeedback)
	return s
}

func (t *tools) agentDiscover(ctx context.Context, in agentDiscoverArgs) (any, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 5
	}
	d, err := t.svc.DiscoverProcedures(ctx, memory.DiscoveryRequest{Task: in.Task, Repository: in.Repository, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := agentDiscovery{Candidates: make([]agentCandidate, len(d.Candidates))}
	found := map[string]bool{}
	for i, c := range d.Candidates {
		out.Candidates[i] = agentCandidate{ProcedureID: c.ProcedureID, CanonicalKey: c.CanonicalKey, Version: c.Version, Scope: c.Applicability.Name(), Goal: c.Goal}
		found[c.ProcedureID] = true
	}
	if in.Repository != "" {
		bindings, _, err := t.svc.ListBindings(ctx, in.Repository, memory.Page{})
		if err != nil {
			return nil, err
		}
		for _, b := range bindings {
			if found[b.ProcedureID] {
				out.Bindings = append(out.Bindings, agentBindingRef{Name: b.Name, ProcedureID: b.ProcedureID})
			}
		}
	}
	return out, nil
}

func (t *tools) agentRead(ctx context.Context, in agentReadArgs) (any, error) {
	var p []memory.FieldProblem
	given := 0
	for _, s := range []string{in.ProcedureID, in.CanonicalKey, in.Binding} {
		if s != "" {
			given++
		}
	}
	if given != 1 {
		p = append(p, memory.FieldProblem{Field: "procedure_id", Message: "give exactly one of procedure_id, canonical_key or binding"})
	}
	if in.Binding != "" {
		if in.Repository == "" {
			p = append(p, memory.FieldProblem{Field: "repository", Message: "is required with binding"})
		}
		if in.Version != 0 {
			p = append(p, memory.FieldProblem{Field: "version", Message: "must not be given with binding: the binding selects it"})
		}
		if in.Inputs != nil {
			p = append(p, memory.FieldProblem{Field: "inputs", Message: "must not be given with binding: the binding supplies them"})
		}
	}
	if in.Inputs != nil {
		var obj map[string]jsontext.Value
		if json.Unmarshal(in.Inputs, &obj) != nil {
			p = append(p, memory.FieldProblem{Field: "inputs", Message: "must be a JSON object"})
		}
	}
	if len(p) > 0 {
		return nil, &memory.ValidationError{Problems: p}
	}

	procedureID, version, inputs := in.ProcedureID, in.Version, in.Inputs
	var binding *agentBinding
	var resolved *memory.GraphNode
	var selectedBy memory.Selection
	if in.Binding != "" {
		bindings, _, err := t.svc.ListBindings(ctx, in.Repository, memory.Page{})
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(bindings, func(b memory.Binding) bool { return b.Name == in.Binding })
		if i < 0 {
			return nil, fmt.Errorf("binding %q in repository %q: %w", in.Binding, in.Repository, memory.ErrNotFound)
		}
		h, err := t.svc.Binding(ctx, bindings[i].ID)
		if err != nil {
			return nil, err
		}
		rev := h.Revisions[len(h.Revisions)-1]
		procedureID, inputs = h.Binding.ProcedureID, rev.Inputs
		binding = &agentBinding{ID: h.Binding.ID, Revision: rev.Number, Repository: h.Binding.Repository, Name: h.Binding.Name}
		switch {
		case in.Environment != "":
			res, err := t.svc.ResolveBinding(ctx, h.Binding.ID, in.Environment, nil)
			if err != nil {
				return nil, err
			}
			resolved, version, selectedBy = &res.Graph, res.Graph.Version, res.SelectedBy
		case rev.VersionPolicy.Kind == memory.PolicyPin:
			version, selectedBy = rev.VersionPolicy.Pin, memory.SelectedByPin
		default:
			return nil, contextRequired(in.Binding, "selects its version from evidence (a contextual policy)")
		}
	}

	var h memory.History
	var err error
	if in.CanonicalKey != "" {
		h, err = t.svc.ProcedureByKey(ctx, in.CanonicalKey)
	} else {
		h, err = t.svc.Procedure(ctx, procedureID)
	}
	if err != nil {
		return nil, err
	}
	if version == 0 {
		version = h.Procedure.LatestVersion
	}
	if version < 1 || version > len(h.Versions) {
		return nil, fmt.Errorf("procedure %q version %d: %w", h.Procedure.ID, version, memory.ErrNotFound)
	}
	v := h.Versions[version-1]
	if resolved == nil {
		var rc memory.ResolutionContext
		if binding == nil {
			rc = memory.ResolutionContext{Repository: in.Repository, Environment: in.Environment}
		}
		g, err := t.svc.CompositionGraph(ctx, h.Procedure.ID, version, rc)
		if err != nil {
			return nil, err
		}
		if binding != nil && hasContextual(g) {
			return nil, contextRequired(in.Binding, "composes a reference selected from evidence (a contextual policy)")
		}
		resolved = &g
	}
	g := *resolved

	out := agentGuidance{
		ProcedureID: h.Procedure.ID, CanonicalKey: h.Procedure.CanonicalKey, Version: version, SelectedBy: string(selectedBy), LatestVersion: h.Procedure.LatestVersion,
		Retired: h.Procedure.Lifecycle == memory.LifecycleRetired, Scope: v.Applicability.Name(), Goal: v.Goal, Method: v.Method,
		Binding: binding, Inputs: inputs, Contract: v.Contract, Instructions: v.Instructions,
	}
	for _, e := range g.Edges {
		ref := agentReference{Name: e.Reference.Name, ProcedureID: e.Node.ProcedureID, Version: e.Node.Version, SelectedBy: string(e.SelectedBy), Condition: e.Reference.Condition}
		if inputs != nil {
			if ref.Inputs, err = memory.MapInputs(e.Reference.Inputs, inputs); err != nil {
				return nil, fmt.Errorf("map inputs of reference %q: %w", e.Reference.Name, err)
			}
		} else {
			ref.InputMapping = e.Reference.Inputs
		}
		out.References = append(out.References, ref)
	}
	if in.Full {
		out.Philosophy, out.RevisionReason, out.CreatedAt = &v.Philosophy, &v.RevisionReason, &v.CreatedAt
		out.Applicability = wire.NewApplicability(v.Applicability)
	}
	return out, nil
}

// contextRequired refuses a binding read whose selection depends on evidence
// when no environment was given: the full API's resolution needs one, and
// guessing the latest version could hand the agent a version it never would
// have selected (ADR-0013).
func contextRequired(binding, why string) error {
	return &memory.ValidationError{Problems: []memory.FieldProblem{{Field: "environment",
		Message: fmt.Sprintf("context required: binding %q %s; pass environment to resolve it from evidence, as resolve_binding does", binding, why)}}}
}

func hasContextual(n memory.GraphNode) bool {
	for _, e := range n.Edges {
		if e.Reference.VersionPolicy.Kind == memory.PolicyContextual || hasContextual(e.Node) {
			return true
		}
	}
	return false
}

func (t *tools) agentFeedback(ctx context.Context, in agentFeedbackArgs) (any, error) {
	var p []memory.FieldProblem
	switch {
	case slices.Contains(nonProcedural, in.Classification):
		p = append(p, memory.FieldProblem{Field: "classification", Message: in.Classification +
			" is not procedural knowledge: handle it in your execution system; feedback records only findings that change what is known about a procedure"})
	case !slices.Contains(procedural, in.Classification):
		p = append(p, memory.FieldProblem{Field: "classification", Message: "must be one of incorrect_applicability, missing_precondition, incorrect_instruction, inadequate_verification, improved_method"})
	}
	if in.ProcedureID == "" {
		p = append(p, memory.FieldProblem{Field: "procedure_id", Message: "is required"})
	}
	if in.Version < 1 {
		p = append(p, memory.FieldProblem{Field: "version", Message: "must be the version you followed, at least 1"})
	}
	if len(p) > 0 {
		return nil, &memory.ValidationError{Problems: p}
	}
	fbContext, err := json.Marshal(agentFeedbackContext{Classification: in.Classification, Evidence: in.Evidence})
	if err != nil {
		return nil, fmt.Errorf("encode feedback context: %w", err)
	}
	kind := memory.FeedbackProblem
	if in.Classification == "improved_method" {
		kind = memory.FeedbackSuggestion
	}
	f, err := t.svc.ReportFeedback(ctx, memory.FeedbackRecord{
		Kind: kind, Summary: in.Summary, Details: in.Details, Reporter: in.Reporter, Context: fbContext,
		Subject:    &memory.Subject{Type: memory.SubjectProcedure, ProcedureID: in.ProcedureID, Version: in.Version},
		Repository: in.Repository,
	})
	if err != nil {
		return nil, err
	}
	return agentFeedbackResult{ID: f.ID, Classification: in.Classification, CreatedAt: f.CreatedAt}, nil
}
