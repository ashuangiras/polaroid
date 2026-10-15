package mcp

import (
	"context"
	"log/slog"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Agent mode (ADR-0043, finalized for Git-native knowledge by ADR-0048): a
// separate endpoint with only the tools everyday procedural-knowledge use
// needs, backed solely by living knowledge named by canonical key and an
// opaque snapshot token. It records no executions and exposes no procedure
// version, binding or evidence-based selection: the full API at /mcp keeps
// every operation, including execution and verification records and the
// legacy, versioned path.

const agentInstructions = `Polaroid agent mode: procedural knowledge, proven methods of working, served from living knowledge.
1. discover a procedure for your task; with repository, only knowledge that applies there. An empty result means nothing has been published yet: try again later or ask an operator. With kind="stats", discover instead summarizes the corpus (counts by record type, by domain, and of procedures by applicability); with kind="tools", it lists agent mode's own tools.
2. read it, by its key, for executable guidance: contract, instructions, and each reference's key, condition and mapped inputs. Read a reference's own key for its references: a read resolves only one level. Keep the snapshot token discover or read gave you: reading it again returns exactly the same knowledge however much is published later.
3. Do the work with your own tools and check the outcome against the contract. Nothing is recorded here, and following a procedure verifies nothing by itself: an audit-grade verification claim needs evidence from your verification system (or the full API at /mcp).
4. Only when the outcome shows the procedure itself is wrong, incomplete, ambiguous or improvable, send feedback with the key, the snapshot you read it from, and a classification. Product defects, environment problems and transient failures are not procedural knowledge: handle them, do not report them here.`

// discoverDescription is discover's tool description at both endpoints
// (ADR-0051): wording, not contract, so it may change without affecting
// the pinned schema.
const discoverDescription = "Find published knowledge for a task, best match first (lexical: shared words); returns a snapshot token and, for each candidate, its canonical key and goal (kind=\"procedures\", the default). An empty result means nothing is published yet. kind=\"stats\" instead returns corpus counts (by_type, by_domain, procedures); kind=\"tools\" lists this server's own tools. task is required only for kind=\"procedures\"."

// AgentKnowledge is what agent mode needs from the Git-native knowledge
// path, expressed only in this package's own request and result shapes
// (wire.Knowledge*): this package never imports internal/knowledge,
// internal/knowledgesync or internal/gitcore. internal/knowledgeops.Agent
// satisfies this interface.
type AgentKnowledge interface {
	Discover(ctx context.Context, in wire.KnowledgeDiscoverRequest) (wire.KnowledgeDiscovery, error)
	Read(ctx context.Context, in wire.KnowledgeReadRequest) (wire.KnowledgeGuidance, error)
	Feedback(ctx context.Context, in wire.KnowledgeFeedbackRequest) (wire.KnowledgeFeedbackResult, error)
}

func newAgentServer(agent AgentKnowledge, logger *slog.Logger) *sdk.Server {
	s := baseServer(agentInstructions, logger)
	t := &tools{agent: agent, logger: logger}
	const read, write = true, false
	add(s, t, "discover", discoverDescription, read, t.agentDiscover)
	add(s, t, "read", "Get one procedure's executable guidance by its canonical key, at a snapshot (the active one by default): contract, instructions, and each reference's key, condition and mapped inputs, resolved one level.", read, t.agentRead)
	add(s, t, "feedback", "Report a finding that changes what is known about the procedure at key, as served by snapshot, with a classification. Not for product defects, environment problems or transient failures.", write, t.agentFeedback)
	return s
}

func (t *tools) agentDiscover(ctx context.Context, in wire.KnowledgeDiscoverRequest) (any, error) {
	out, err := t.agent.Discover(ctx, in)
	if err != nil {
		return nil, err
	}
	// kind="tools" lists agent mode's common tools; resolve exists only at
	// /mcp/agent/v2, which this Agent cannot tell apart from v1 (one Agent
	// backs both), so the transport layer adds it here when it is served.
	if in.Kind == wire.KindTools && t.resolver != nil {
		out.Tools = append(out.Tools, wire.KnowledgeToolInfo{Name: "resolve",
			Description: "Resolve what applies to a record (procedure, concept or claim) in your context, at a snapshot.", ReadOnly: true})
	}
	return out, nil
}

func (t *tools) agentRead(ctx context.Context, in wire.KnowledgeReadRequest) (any, error) {
	return t.agent.Read(ctx, in)
}

func (t *tools) agentFeedback(ctx context.Context, in wire.KnowledgeFeedbackRequest) (any, error) {
	return t.agent.Feedback(ctx, in)
}

// Agent mode v2 (ADR-0050): the v1 tools, unchanged, plus resolve. v1 stays
// at /mcp/agent exactly as ADR-0049 finalized it.

const agentInstructionsV2 = agentInstructions + `
5. resolve a record's key (a procedure, concept or claim) with your situation as context, to learn what applies: obligations with every derivation, specializations, exceptions that verified or were rejected, derived facts, conflicts, unknowns and the evidence status of claims. An unknown means your context did not decide it, never that it does not hold; complete false means not every unknown could be examined. Your context never authorises an exception. Evidence status summarises evidence; it is not a verdict. Pass then with a second procedure's key to learn whether it can follow the goal.`

// AgentResolver is what agent mode v2 adds: resolution, expressed only in
// wire shapes. internal/knowledgeops.Resolver satisfies it.
type AgentResolver interface {
	Resolve(ctx context.Context, in wire.KnowledgeResolveRequest) (wire.KnowledgeResolution, error)
}

func newAgentServerV2(agent AgentKnowledge, resolver AgentResolver, logger *slog.Logger) *sdk.Server {
	s := baseServer(agentInstructionsV2, logger)
	t := &tools{agent: agent, resolver: resolver, logger: logger}
	const read, write = true, false
	add(s, t, "discover", discoverDescription, read, t.agentDiscover)
	add(s, t, "read", "Get one procedure's executable guidance by its canonical key, at a snapshot (the active one by default): contract, instructions, and each reference's key, condition and mapped inputs, resolved one level.", read, t.agentRead)
	add(s, t, "feedback", "Report a finding that changes what is known about the procedure at key, as served by snapshot, with a classification. Not for product defects, environment problems or transient failures.", write, t.agentFeedback)
	add(s, t, "resolve", "Resolve what applies to a record (procedure, concept or claim) in your context, at a snapshot: obligations with derivation identifiers, specializations, verified and rejected exceptions, derived facts, conflicts, unknowns, whether the result is complete, evidence status, and with then, whether a second procedure can follow it. Deterministic for the same snapshot, context and time.", read, t.agentResolve)
	return s
}

func (t *tools) agentResolve(ctx context.Context, in wire.KnowledgeResolveRequest) (any, error) {
	return t.resolver.Resolve(ctx, in)
}
