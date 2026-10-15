package knowledgeops

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// DefaultDiscoverLimit is discover's limit when none is given.
const DefaultDiscoverLimit = 5

// EmptyCatalogMessage explains an empty discover result to an agent.
const EmptyCatalogMessage = "no knowledge has been published yet; try again later or ask an operator to publish or activate some"

// Procedural are the feedback classifications that concern a procedure;
// NonProcedural describe something else (a product defect, an environment
// problem, a transient failure) and are refused: they are not procedural
// knowledge.
var (
	Procedural    = []string{"incorrect_applicability", "missing_precondition", "incorrect_instruction", "inadequate_verification", "improved_method"}
	NonProcedural = []string{"product_defect", "environment_problem", "transient_failure"}
)

// Agent is the knowledge-first agent interface (ADR-0048): discover, read
// and report feedback on Git-native living knowledge, named only by
// canonical key and opaque snapshot token. It never exposes a procedure
// version, a binding, or evidence-based selection: those remain the full
// API's concerns, and an agent that only uses Agent never handles Git
// operations, procedure versions or historical selection.
type Agent struct {
	kernel   *knowledge.Kernel
	feedback *memory.Service
}

// NewAgent builds an Agent reading kernel and recording feedback through
// feedback.
func NewAgent(kernel *knowledge.Kernel, feedback *memory.Service) *Agent {
	return &Agent{kernel: kernel, feedback: feedback}
}

// Discover ranks published knowledge for a task (kind "" or "procedures",
// the default and unchanged), summarizes the corpus (kind "stats"), or
// self-describes agent mode's common tools (kind "tools": discover, read,
// feedback; the MCP transport layer adds resolve where it serves one,
// since this Agent backs both /mcp/agent and /mcp/agent/v2 and has no
// notion of which endpoint called it). An empty catalog (nothing
// published yet) is a well-formed, explicit empty result for
// "procedures", never an error; "stats" reports zero counts instead,
// itself a true answer, not a special case.
func (a *Agent) Discover(ctx context.Context, in wire.KnowledgeDiscoverRequest) (wire.KnowledgeDiscovery, error) {
	switch in.Kind {
	case "", wire.KindProcedures:
		if in.Task == "" {
			return wire.KnowledgeDiscovery{}, requiredField("task")
		}
		return a.discoverProcedures(ctx, in)
	case wire.KindStats:
		if in.Limit != 0 {
			return wire.KnowledgeDiscovery{}, invalidField("limit", `is not used by kind "stats"`)
		}
		return a.discoverStats(ctx, in)
	case wire.KindTools:
		if in.Limit != 0 {
			return wire.KnowledgeDiscovery{}, invalidField("limit", `is not used by kind "tools"`)
		}
		return wire.KnowledgeDiscovery{Kind: wire.KindTools, Tools: commonTools()}, nil
	default:
		return wire.KnowledgeDiscovery{}, invalidField("kind", `must be "procedures", "stats" or "tools"`)
	}
}

func (a *Agent) discoverProcedures(ctx context.Context, in wire.KnowledgeDiscoverRequest) (wire.KnowledgeDiscovery, error) {
	cat, err := a.kernel.Active(ctx)
	if err != nil {
		return wire.KnowledgeDiscovery{}, classify(err)
	}
	if cat.Snapshot() == "" {
		return wire.KnowledgeDiscovery{Empty: true, Message: EmptyCatalogMessage}, nil
	}
	limit := in.Limit
	if limit == 0 {
		limit = DefaultDiscoverLimit
	}
	cands := cat.Discover(in.Task, in.Repository, limit)
	out := wire.KnowledgeDiscovery{Snapshot: string(cat.Snapshot()), Candidates: make([]wire.KnowledgeCandidate, len(cands))}
	for i, c := range cands {
		out.Candidates[i] = wire.KnowledgeCandidate{Key: c.Key, Goal: c.Goal, Score: c.Score}
	}
	return out, nil
}

// discoverStats answers discover(kind="stats") at a snapshot (the active
// one, or a given one, exactly as Read resolves one).
func (a *Agent) discoverStats(ctx context.Context, in wire.KnowledgeDiscoverRequest) (wire.KnowledgeDiscovery, error) {
	var cat *knowledge.Catalog
	var err error
	if in.Snapshot != "" {
		cat, err = a.kernel.At(ctx, knowledge.Snapshot(in.Snapshot))
	} else {
		cat, err = a.kernel.Active(ctx)
	}
	if err != nil {
		return wire.KnowledgeDiscovery{}, classify(err)
	}
	st := cat.Stats(in.Repository)
	return wire.KnowledgeDiscovery{
		Snapshot: string(cat.Snapshot()), Kind: wire.KindStats, GeneratedAt: time.Now().UTC().Round(0),
		ByType: st.ByType, ByDomain: st.ByDomain,
		Procedures:   &wire.KnowledgeProcedureStats{Total: st.Procedures.Total, ByRepository: st.Procedures.ByRepository},
		Completeness: &wire.KnowledgeCompletenessStats{MissingDefinition: st.Completeness.MissingDefinition, MissingObligation: st.Completeness.MissingObligation},
	}, nil
}

// commonTools is discover(kind="tools")'s self-description of the tools
// every agent-mode endpoint serves.
func commonTools() []wire.KnowledgeToolInfo {
	return []wire.KnowledgeToolInfo{
		{Name: "discover", Description: "Find published knowledge for a task, best match first (lexical: shared words), summarize the corpus, or list agent mode's own tools.", ReadOnly: true},
		{Name: "read", Description: "Get one procedure's executable guidance by its canonical key, at a snapshot.", ReadOnly: true},
		{Name: "feedback", Description: "Report a finding that changes what is known about a procedure.", ReadOnly: false},
	}
}

// Read returns one procedure's executable guidance at a snapshot (the
// active one by default), with its references resolved one level: read a
// reference's own key for its references.
func (a *Agent) Read(ctx context.Context, in wire.KnowledgeReadRequest) (wire.KnowledgeGuidance, error) {
	if in.Key == "" {
		return wire.KnowledgeGuidance{}, requiredField("key")
	}
	if in.Inputs != nil {
		var obj map[string]jsontext.Value
		if json.Unmarshal(in.Inputs, &obj) != nil {
			return wire.KnowledgeGuidance{}, invalidField("inputs", "must be a JSON object")
		}
	}
	var cat *knowledge.Catalog
	var err error
	if in.Snapshot != "" {
		cat, err = a.kernel.At(ctx, knowledge.Snapshot(in.Snapshot))
	} else {
		cat, err = a.kernel.Active(ctx)
	}
	if err != nil {
		return wire.KnowledgeGuidance{}, classify(err)
	}
	if cat.Snapshot() == "" {
		return wire.KnowledgeGuidance{}, classify(fmt.Errorf("knowledge: %w", knowledge.ErrNotFound))
	}
	proc, err := cat.Get(in.Key)
	if err != nil {
		return wire.KnowledgeGuidance{}, classify(err)
	}
	if in.Repository != "" && !proc.Applicability.ApplicableIn(in.Repository) {
		return wire.KnowledgeGuidance{}, classify(fmt.Errorf("procedure %q in %q: %w", in.Key, in.Repository, knowledge.ErrNotApplicable))
	}
	out := wire.KnowledgeGuidance{
		Snapshot: string(cat.Snapshot()), Key: proc.Key, Scope: string(proc.Applicability.Scope),
		Goal: proc.Goal, Method: proc.Method, Contract: proc.Contract, Instructions: proc.Instructions,
	}
	for _, r := range proc.References {
		ref := wire.KnowledgeReference{Name: r.Name, Key: r.Key}
		if r.Condition != nil {
			ref.Condition = *r.Condition
		}
		if child, err := cat.Get(r.Key); err == nil {
			ref.Goal = child.Goal
		}
		if in.Inputs != nil {
			mapped, err := knowledge.MapInputs(r.Inputs, in.Inputs)
			if err != nil {
				return wire.KnowledgeGuidance{}, fmt.Errorf("map inputs of reference %q: %w", r.Name, err)
			}
			ref.Inputs = mapped
		} else {
			ref.InputMapping = r.Inputs
		}
		out.References = append(out.References, ref)
	}
	if in.Full {
		out.Philosophy = proc.Philosophy
	}
	return out, nil
}

// feedbackContext is the "knowledge" member reserved in a feedback report's
// free-form Context, carrying the typed FeedbackRef the report concerns.
type feedbackContext struct {
	Classification string         `json:"classification"`
	Knowledge      jsontext.Value `json:"knowledge"`
	Evidence       jsontext.Value `json:"evidence,omitzero"`
}

// Feedback records a finding about one procedure as served by one snapshot.
// The pair is confirmed against the kernel before anything is recorded: a
// snapshot that never served the key, or a key never published, is refused.
func (a *Agent) Feedback(ctx context.Context, in wire.KnowledgeFeedbackRequest) (wire.KnowledgeFeedbackResult, error) {
	var p []knowledge.Problem
	switch {
	case slices.Contains(NonProcedural, in.Classification):
		p = append(p, knowledge.Problem{Field: "classification", Message: in.Classification +
			" is not procedural knowledge: handle it in your execution system; feedback records only findings that change what is known about a procedure"})
	case !slices.Contains(Procedural, in.Classification):
		p = append(p, knowledge.Problem{Field: "classification", Message: "must be one of " + strings.Join(Procedural, ", ")})
	}
	if in.Key == "" {
		p = append(p, knowledge.Problem{Field: "key", Message: "is required"})
	}
	if in.Snapshot == "" {
		p = append(p, knowledge.Problem{Field: "snapshot", Message: "is required: the snapshot you read the procedure from"})
	}
	if len(p) > 0 {
		return wire.KnowledgeFeedbackResult{}, classify(&knowledge.ValidationError{Problems: p})
	}
	ref := knowledge.FeedbackRef{Key: in.Key, Snapshot: knowledge.Snapshot(in.Snapshot)}
	if err := a.kernel.CheckFeedbackRef(ctx, ref); err != nil {
		return wire.KnowledgeFeedbackResult{}, classify(err)
	}
	encoded, err := ref.Encode()
	if err != nil {
		return wire.KnowledgeFeedbackResult{}, classify(err)
	}
	ctxBody, err := json.Marshal(feedbackContext{Classification: in.Classification, Knowledge: encoded, Evidence: in.Evidence})
	if err != nil {
		return wire.KnowledgeFeedbackResult{}, fmt.Errorf("encode feedback context: %w", err)
	}
	kind := memory.FeedbackProblem
	if in.Classification == "improved_method" {
		kind = memory.FeedbackSuggestion
	}
	f, err := a.feedback.ReportFeedback(ctx, memory.FeedbackRecord{
		Kind: kind, Summary: in.Summary, Details: in.Details, Reporter: in.Reporter,
		Context: ctxBody, Repository: in.Repository,
	})
	if err != nil {
		return wire.KnowledgeFeedbackResult{}, err
	}
	return wire.KnowledgeFeedbackResult{ID: f.ID, Classification: in.Classification, CreatedAt: f.CreatedAt}, nil
}

// refFromContext extracts the FeedbackRef a feedback report's context
// carries, under the reserved "knowledge" member.
func refFromContext(ctxBody jsontext.Value) (knowledge.FeedbackRef, error) {
	var fc struct {
		Knowledge jsontext.Value `json:"knowledge"`
	}
	if err := json.Unmarshal(ctxBody, &fc); err != nil || len(fc.Knowledge) == 0 {
		return knowledge.FeedbackRef{}, fmt.Errorf("feedback does not concern knowledge: its context has no knowledge reference")
	}
	return knowledge.ParseFeedbackRef(fc.Knowledge)
}
