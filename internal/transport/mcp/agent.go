package mcp

import (
	"context"
	"log/slog"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

const agentInstructionsV8 = `Polaroid agent mode serves Git-native procedural knowledge.
1. Discover a procedure for your task, then read its executable guidance at the returned snapshot. With kind="stats", discover summarizes the corpus; kind="tools" lists this server's ten tools.
2. Resolve typed relationships when needed. Start a walkthrough of a procedure, optionally as a dynamic parent; attach task-specific children at its pinned snapshot and advance their outcomes. A completed parent may accept another child.
3. Check your work with your own tools; following a procedure does not itself verify or record execution evidence. Use feedback only for genuine deficiencies of an existing procedure, not product defects or transient failures.
4. To propose knowledge, propose_start begins a complete field review. propose_advance validates the candidate after every field is reviewed and returns ready and candidate_hash without publishing. For capture, bind the draft to an open polaroid.using parent with its local capture child; finish all task children before calling propose_publish_capture with the exact candidate_hash while capture is the active final step. Other drafts require authenticated operator approval through POST /v2/knowledge/drafts/{token}/publication. Never push or sync automatically.`

const discoverDescription = "Find published knowledge for a task, best match first (lexical: shared words); returns a snapshot token and, for each candidate, its canonical key and goal (kind=\"procedures\", the default). An empty result means nothing has been published yet. kind=\"stats\" instead returns corpus counts (by_type, by_domain, procedures); kind=\"tools\" lists this server's own tools. task is required only for kind=\"procedures\"."

// AgentKnowledge uses transport shapes, not storage or knowledge internals.
type AgentKnowledge interface {
	Discover(context.Context, wire.KnowledgeDiscoverRequest) (wire.KnowledgeDiscovery, error)
	Read(context.Context, wire.KnowledgeReadRequest) (wire.KnowledgeGuidance, error)
	Feedback(context.Context, wire.KnowledgeFeedbackRequest) (wire.KnowledgeFeedbackResult, error)
}

type AgentResolver interface {
	Resolve(context.Context, wire.KnowledgeResolveRequest) (wire.KnowledgeResolution, error)
}

type AgentGuideV7 interface {
	StartV7(context.Context, wire.WalkthroughStartRequestV7) (wire.WalkthroughResult, error)
	AttachChild(context.Context, wire.WalkthroughAttachChildRequest) (wire.WalkthroughResult, error)
	AdvanceV7(context.Context, wire.WalkthroughAdvanceRequest) (wire.WalkthroughResult, error)
}

type AgentFieldDrafterV8 interface {
	StartV8(context.Context, wire.FieldDraftStartRequestV8) (wire.FieldDraftResult, error)
	Advance(context.Context, wire.FieldDraftAdvanceRequest) (wire.FieldDraftResult, error)
	PublishCapture(context.Context, wire.FieldDraftPublishRequest) (wire.FieldDraftResult, error)
}

func newAgentServerV8(agent AgentKnowledge, resolver AgentResolver, guide AgentGuideV7, drafter AgentFieldDrafterV8, logger *slog.Logger) *sdk.Server {
	s := baseServer(agentInstructionsV8, logger)
	t := &tools{agent: agent, resolver: resolver, guideV7: guide, fieldDrafterV8: drafter, logger: logger}
	const read, write = true, false
	add(s, t, "discover", discoverDescription, read, t.agentDiscover)
	add(s, t, "read", "Get one procedure's executable guidance by its canonical key, at a snapshot (the active one by default).", read, t.agentRead)
	add(s, t, "feedback", "Report a genuine deficiency in existing procedural knowledge.", write, t.agentFeedback)
	add(s, t, "resolve", "Resolve typed knowledge against context at a pinned snapshot.", read, t.agentResolve)
	add(s, t, "start", "Start a walkthrough pinned to a snapshot, optionally allowing dynamic children.", write, t.agentStartV7)
	add(s, t, "attach_child", "Attach a runtime Procedure child to the open dynamic parent.", write, t.agentAttachChildV7)
	add(s, t, "advance", "Advance or inspect a walkthrough.", write, t.agentAdvanceV7)
	add(s, t, "propose_start", "Start a complete field review. Optionally bind to an open capture walkthrough. This does not publish.", write, t.agentFieldProposeStartV8)
	add(s, t, "propose_advance", "Review the next field; final validation returns ready and candidate_hash without publishing.", write, t.agentFieldProposeAdvanceV8)
	add(s, t, "propose_publish_capture", "Publish an exact ready draft only if its bound local capture walkthrough is currently eligible.", write, t.agentFieldPublishCapture)
	return s
}

func (t *tools) agentDiscover(ctx context.Context, in wire.KnowledgeDiscoverRequest) (any, error) {
	out, err := t.agent.Discover(ctx, in)
	if err != nil {
		return nil, err
	}
	if in.Kind == wire.KindTools {
		out.Tools = append(out.Tools,
			wire.KnowledgeToolInfo{Name: "resolve", Description: "Resolve typed knowledge.", ReadOnly: true},
			wire.KnowledgeToolInfo{Name: "start", Description: "Start a walkthrough."},
			wire.KnowledgeToolInfo{Name: "attach_child", Description: "Attach a dynamic child."},
			wire.KnowledgeToolInfo{Name: "advance", Description: "Advance a walkthrough."},
			wire.KnowledgeToolInfo{Name: "propose_start", Description: "Start a complete field review without publishing."},
			wire.KnowledgeToolInfo{Name: "propose_advance", Description: "Review a field and validate a draft without publishing."},
			wire.KnowledgeToolInfo{Name: "propose_publish_capture", Description: "Publish an authorized, ready capture draft."},
		)
	}
	return out, nil
}

func (t *tools) agentRead(ctx context.Context, in wire.KnowledgeReadRequest) (any, error) {
	return t.agent.Read(ctx, in)
}

func (t *tools) agentFeedback(ctx context.Context, in wire.KnowledgeFeedbackRequest) (any, error) {
	return t.agent.Feedback(ctx, in)
}

func (t *tools) agentResolve(ctx context.Context, in wire.KnowledgeResolveRequest) (any, error) {
	return t.resolver.Resolve(ctx, in)
}

func (t *tools) agentStartV7(ctx context.Context, in wire.WalkthroughStartRequestV7) (any, error) {
	return t.guideV7.StartV7(ctx, in)
}

func (t *tools) agentAttachChildV7(ctx context.Context, in wire.WalkthroughAttachChildRequest) (any, error) {
	return t.guideV7.AttachChild(ctx, in)
}

func (t *tools) agentAdvanceV7(ctx context.Context, in wire.WalkthroughAdvanceRequest) (any, error) {
	return t.guideV7.AdvanceV7(ctx, in)
}

func (t *tools) agentFieldProposeStartV8(ctx context.Context, in wire.FieldDraftStartRequestV8) (any, error) {
	return t.fieldDrafterV8.StartV8(ctx, in)
}

func (t *tools) agentFieldProposeAdvanceV8(ctx context.Context, in wire.FieldDraftAdvanceRequest) (any, error) {
	return t.fieldDrafterV8.Advance(ctx, in)
}

func (t *tools) agentFieldPublishCapture(ctx context.Context, in wire.FieldDraftPublishRequest) (any, error) {
	return t.fieldDrafterV8.PublishCapture(ctx, in)
}
