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
1. discover a procedure for your task; with repository, only knowledge that applies there. An empty result means nothing has been published yet: try again later or ask an operator.
2. read it, by its key, for executable guidance: contract, instructions, and each reference's key, condition and mapped inputs. Read a reference's own key for its references: a read resolves only one level. Keep the snapshot token discover or read gave you: reading it again returns exactly the same knowledge however much is published later.
3. Do the work with your own tools and check the outcome against the contract. Nothing is recorded here, and following a procedure verifies nothing by itself: an audit-grade verification claim needs evidence from your verification system (or the full API at /mcp).
4. Only when the outcome shows the procedure itself is wrong, incomplete, ambiguous or improvable, send feedback with the key, the snapshot you read it from, and a classification. Product defects, environment problems and transient failures are not procedural knowledge: handle them, do not report them here.`

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
	add(s, t, "discover", "Find published knowledge for a task, best match first (lexical: shared words). Returns a snapshot token and, for each candidate, its canonical key and goal. An empty result means nothing is published yet.", read, t.agentDiscover)
	add(s, t, "read", "Get one procedure's executable guidance by its canonical key, at a snapshot (the active one by default): contract, instructions, and each reference's key, condition and mapped inputs, resolved one level.", read, t.agentRead)
	add(s, t, "feedback", "Report a finding that changes what is known about the procedure at key, as served by snapshot, with a classification. Not for product defects, environment problems or transient failures.", write, t.agentFeedback)
	return s
}

func (t *tools) agentDiscover(ctx context.Context, in wire.KnowledgeDiscoverRequest) (any, error) {
	return t.agent.Discover(ctx, in)
}

func (t *tools) agentRead(ctx context.Context, in wire.KnowledgeReadRequest) (any, error) {
	return t.agent.Read(ctx, in)
}

func (t *tools) agentFeedback(ctx context.Context, in wire.KnowledgeFeedbackRequest) (any, error) {
	return t.agent.Feedback(ctx, in)
}
