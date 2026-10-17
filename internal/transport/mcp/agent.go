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
	// kind="tools" lists agent mode's common tools; resolve and
	// start/advance exist only at /mcp/agent/v2 and /mcp/agent/v3
	// respectively, which this Agent cannot tell apart from v1 (one Agent
	// backs all three), so the transport layer adds them here when served.
	if in.Kind == wire.KindTools && t.resolver != nil {
		out.Tools = append(out.Tools, wire.KnowledgeToolInfo{Name: "resolve",
			Description: "Resolve what applies to a record (procedure, concept or claim) in your context, at a snapshot.", ReadOnly: true})
	}
	if in.Kind == wire.KindTools && t.guide != nil {
		out.Tools = append(out.Tools,
			wire.KnowledgeToolInfo{Name: "start", Description: "Start a stateful walkthrough of a procedure's composed steps, pinned at the active snapshot."},
			wire.KnowledgeToolInfo{Name: "advance", Description: "Report an outcome for one of a walkthrough's composed steps, or peek its current plan unchanged."})
	}
	if in.Kind == wire.KindTools && t.proposer != nil {
		out.Tools = append(out.Tools, wire.KnowledgeToolInfo{Name: "propose",
			Description: "Propose new, citation-grounded knowledge: committed immediately if it passes citation, completeness, non-vacuous composition and novelty checks."})
	}
	if in.Kind == wire.KindTools && t.drafter != nil {
		out.Tools = append(out.Tools,
			wire.KnowledgeToolInfo{Name: "propose_start", Description: "Start an interactive, field-by-field draft of a propose call for an existing Procedure's key: walks every content field, every time, including ones that already have values."},
			wire.KnowledgeToolInfo{Name: "propose_advance", Description: "Answer a draft's next field (to append new values or confirm nothing further), or peek its current state unchanged; attempts the real commit once every field is answered."})
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

// Agent mode v3 (ADR-0053, amended by ADR-0054, ADR-0055 and ADR-0056):
// the v2 tools, unchanged, plus start and advance: a token-identified,
// expiring walkthrough of a procedure's composed steps, remembered
// across sessions without the caller resending a completed list. v1 and
// v2 stay exactly as finalized.

const agentInstructionsV3 = agentInstructionsV2 + `
6. For a multi-step procedure you expect to work through across several turns or sessions, start it once to get a token and its plan (ordered composed steps, possibly several levels deep); keep the token. Pass expand true to start or advance to also see every nested step at once (children, recursively, each "done"|"escalate"|"blocked"|"pending") instead of discovering them one leaf at a time via path. Call advance with the token to report an outcome ({key or path, status: "done"|"skipped"|"escalate"|"blocked", reason?, note?}) for one leaf, or with no outcome to peek the current plan unchanged. plan's entries name only the goal's immediate composed children; an entry whose own step is itself composed reports a derived status and, when it is "next", "escalate" or "blocked", a path: the full chain down to the actual leaf to act on or that is stuck — use that path, not the bare key, to advance it, unless it is already a one-element path (then the bare key is equivalent). "next" names the first step not yet done or skipped; escalate (waiting on a person's judgment) and blocked (an external, technical precondition you testify is unmet — give reason) each freeze next there (and every composed step above it also reports the same status) until a later advance reports done or skipped for that same leaf. The result's status is "complete" once nothing is left "next", "escalate" or "blocked", "in_progress" otherwise; it is not final — a later advance correcting an already-done leaf can move it back to "in_progress". Call advance with close true when you are done with the walkthrough, whether it reached "complete" or you are abandoning it; an outcome may still be given in the same call. A token expires after a period of inactivity (any call, including a peek, resets it); an expired, closed or unknown token is not_found, indistinguishable from each other.`

// AgentGuide is what agent mode v3 adds: stateful walkthroughs, expressed
// only in wire shapes. internal/knowledgeops.Guide satisfies it.
type AgentGuide interface {
	Start(ctx context.Context, in wire.WalkthroughStartRequest) (wire.WalkthroughResult, error)
	Advance(ctx context.Context, in wire.WalkthroughAdvanceRequest) (wire.WalkthroughResult, error)
}

func newAgentServerV3(agent AgentKnowledge, resolver AgentResolver, guide AgentGuide, logger *slog.Logger) *sdk.Server {
	s := baseServer(agentInstructionsV3, logger)
	t := &tools{agent: agent, resolver: resolver, guide: guide, logger: logger}
	const read, write = true, false
	add(s, t, "discover", discoverDescription, read, t.agentDiscover)
	add(s, t, "read", "Get one procedure's executable guidance by its canonical key, at a snapshot (the active one by default): contract, instructions, and each reference's key, condition and mapped inputs, resolved one level.", read, t.agentRead)
	add(s, t, "feedback", "Report a finding that changes what is known about the procedure at key, as served by snapshot, with a classification. Not for product defects, environment problems or transient failures.", write, t.agentFeedback)
	add(s, t, "resolve", "Resolve what applies to a record (procedure, concept or claim) in your context, at a snapshot: obligations with derivation identifiers, specializations, verified and rejected exceptions, derived facts, conflicts, unknowns, whether the result is complete, evidence status, and with then, whether a second procedure can follow it. Deterministic for the same snapshot, context and time.", read, t.agentResolve)
	add(s, t, "start", "Start a stateful walkthrough of goal's composed steps, pinned at the active snapshot. Returns {token, snapshot, plan}: keep the token to call advance across later turns or sessions. Pass expand true for the whole nested plan tree instead of one leaf at a time.", write, t.agentStart)
	add(s, t, "advance", "Report an outcome (done, skipped, escalate or blocked, with an optional reason) for one of a walkthrough's composed steps, or peek its current plan unchanged when outcome is omitted. Returns the updated {token, snapshot, plan, status}. Pass close true to end the walkthrough immediately, whether or not it is complete; pass expand true as start's.", write, t.agentAdvance)
	return s
}

func (t *tools) agentStart(ctx context.Context, in wire.WalkthroughStartRequest) (any, error) {
	return t.guide.Start(ctx, in)
}

func (t *tools) agentAdvance(ctx context.Context, in wire.WalkthroughAdvanceRequest) (any, error) {
	return t.guide.Advance(ctx, in)
}

// Agent mode v4 (ADR-0059 Parts 2-3): the v3 tools, unchanged, plus
// propose -- agent mode's first write to Git-native knowledge itself
// (start/advance write only one agent's own runtime progress, never
// knowledge content). v1, v2 and v3 stay exactly as before.

const agentInstructionsV4 = agentInstructionsV3 + `
7. propose new, grounded knowledge directly, when you have it: new formal content on an existing Procedure, a new supporting Concept or Claim (each citation-grounded: cite the exact existing text -- a file, or another record -- you are mirroring; Polaroid only checks that your quote appears verbatim in what you are adding, never that the citation is accurate against a real file), or a wholly new orchestration Procedure that only composes Procedures that already exist, in order (no citation needed for that). Polaroid also checks your proposal changes something: a candidate that alters no resolution anywhere in the catalog is refused as redundant, not committed. Passing every check, propose commits immediately: no draft, no queue, no operator approval.`

// AgentProposer is what agent mode v4 adds: agent-proposed knowledge,
// expressed only in wire shapes. internal/knowledgeops.Proposer satisfies
// it.
type AgentProposer interface {
	Propose(ctx context.Context, in wire.ProposeRequest) (wire.ProposeResult, error)
}

func newAgentServerV4(agent AgentKnowledge, resolver AgentResolver, guide AgentGuide, proposer AgentProposer, logger *slog.Logger) *sdk.Server {
	s := baseServer(agentInstructionsV4, logger)
	t := &tools{agent: agent, resolver: resolver, guide: guide, proposer: proposer, logger: logger}
	const read, write = true, false
	add(s, t, "discover", discoverDescription, read, t.agentDiscover)
	add(s, t, "read", "Get one procedure's executable guidance by its canonical key, at a snapshot (the active one by default): contract, instructions, and each reference's key, condition and mapped inputs, resolved one level.", read, t.agentRead)
	add(s, t, "feedback", "Report a finding that changes what is known about the procedure at key, as served by snapshot, with a classification. Not for product defects, environment problems or transient failures.", write, t.agentFeedback)
	add(s, t, "resolve", "Resolve what applies to a record (procedure, concept or claim) in your context, at a snapshot: obligations with derivation identifiers, specializations, verified and rejected exceptions, derived facts, conflicts, unknowns, whether the result is complete, evidence status, and with then, whether a second procedure can follow it. Deterministic for the same snapshot, context and time.", read, t.agentResolve)
	add(s, t, "start", "Start a stateful walkthrough of goal's composed steps, pinned at the active snapshot. Returns {token, snapshot, plan}: keep the token to call advance across later turns or sessions. Pass expand true for the whole nested plan tree instead of one leaf at a time.", write, t.agentStart)
	add(s, t, "advance", "Report an outcome (done, skipped, escalate or blocked, with an optional reason) for one of a walkthrough's composed steps, or peek its current plan unchanged when outcome is omitted. Returns the updated {token, snapshot, plan, status}. Pass close true to end the walkthrough immediately, whether or not it is complete; pass expand true as start's.", write, t.agentAdvance)
	add(s, t, "propose", "Propose new, citation-grounded knowledge -- new formal content on an existing Procedure, a new supporting Concept or Claim, or a wholly new orchestration Procedure that only composes already-existing Procedures (no citation needed for that one case) -- checked for citation self-consistency, completeness, non-vacuous composition and novelty, then committed immediately: no draft, no queue, no operator step.", write, t.agentPropose)
	return s
}

func (t *tools) agentPropose(ctx context.Context, in wire.ProposeRequest) (any, error) {
	return t.proposer.Propose(ctx, in)
}

// Agent mode v5 (ADR-0060): the v4 tools, unchanged, plus propose_start
// and propose_advance: interactive, field-by-field construction of a
// propose request for an existing Procedure, requiring an explicit
// answer -- real, citation-grounded content, or an explicit, reasoned
// decline -- for every content field the Procedure does not already
// decide, instead of allowing one to be silently omitted. Complements
// single-shot propose (v4), unaffected. v1 through v4 stay exactly as
// before.

const agentInstructionsV5 = agentInstructionsV4 + `
8. To add to an existing Procedure's preconditions, postconditions, invariants or informal_conditions -- including one that already has values, to extend it, not only one that starts empty -- propose_start it (key, proposer, rationale) instead of assembling a whole propose call yourself: Polaroid walks all four fields, in that fixed order, every single draft, every time, whatever each already holds. Its result names next_field and, when a field already has values, existing_values (and existing_complete) so you can see what is already published there before deciding what to do. Call propose_advance with the token and an outcome for that field -- either {field, values, citations} (new entries to append to whatever is already there, citation-grounded exactly as propose's own existing_procedure requires) or {field, confirmed_absent: true, reason} (your own testimony that this field needs no further values right now -- whether it genuinely does not apply, or what is already published there is already complete; never verified by Polaroid, the same epistemic category escalate/blocked already are for walkthroughs) -- or with no outcome to peek the current state unchanged. The moment every field is answered, that same call automatically attempts the real commit, through the same checks propose itself runs; a failure (still vacuous composition, or redundant) is returned as the specific error, and the draft stays open, every earlier answer intact, so you can correct exactly the field responsible and call propose_advance again. On success the result's status is "complete" with the new snapshot and key, and the token then behaves exactly like a closed or expired one on any later call (not_found, no trace). Call propose_advance with close true to abandon the draft immediately instead.`

// AgentDrafter is what agent mode v5 adds: interactive proposal
// construction, expressed only in wire shapes.
// internal/knowledgeops.Drafter satisfies it.
type AgentDrafter interface {
	Start(ctx context.Context, in wire.DraftStartRequest) (wire.DraftResult, error)
	Advance(ctx context.Context, in wire.DraftAdvanceRequest) (wire.DraftResult, error)
}

func newAgentServerV5(agent AgentKnowledge, resolver AgentResolver, guide AgentGuide, proposer AgentProposer, drafter AgentDrafter, logger *slog.Logger) *sdk.Server {
	s := baseServer(agentInstructionsV5, logger)
	t := &tools{agent: agent, resolver: resolver, guide: guide, proposer: proposer, drafter: drafter, logger: logger}
	const read, write = true, false
	add(s, t, "discover", discoverDescription, read, t.agentDiscover)
	add(s, t, "read", "Get one procedure's executable guidance by its canonical key, at a snapshot (the active one by default): contract, instructions, and each reference's key, condition and mapped inputs, resolved one level.", read, t.agentRead)
	add(s, t, "feedback", "Report a finding that changes what is known about the procedure at key, as served by snapshot, with a classification. Not for product defects, environment problems or transient failures.", write, t.agentFeedback)
	add(s, t, "resolve", "Resolve what applies to a record (procedure, concept or claim) in your context, at a snapshot: obligations with derivation identifiers, specializations, verified and rejected exceptions, derived facts, conflicts, unknowns, whether the result is complete, evidence status, and with then, whether a second procedure can follow it. Deterministic for the same snapshot, context and time.", read, t.agentResolve)
	add(s, t, "start", "Start a stateful walkthrough of goal's composed steps, pinned at the active snapshot. Returns {token, snapshot, plan}: keep the token to call advance across later turns or sessions. Pass expand true for the whole nested plan tree instead of one leaf at a time.", write, t.agentStart)
	add(s, t, "advance", "Report an outcome (done, skipped, escalate or blocked, with an optional reason) for one of a walkthrough's composed steps, or peek its current plan unchanged when outcome is omitted. Returns the updated {token, snapshot, plan, status}. Pass close true to end the walkthrough immediately, whether or not it is complete; pass expand true as start's.", write, t.agentAdvance)
	add(s, t, "propose", "Propose new, citation-grounded knowledge -- new formal content on an existing Procedure, a new supporting Concept or Claim, or a wholly new orchestration Procedure that only composes already-existing Procedures (no citation needed for that one case) -- checked for citation self-consistency, completeness, non-vacuous composition and novelty, then committed immediately: no draft, no queue, no operator step.", write, t.agentPropose)
	add(s, t, "propose_start", "Start an interactive, field-by-field draft of a propose call for an existing Procedure's key. Returns {token, snapshot, next_field, existing_values, existing_complete, status}: the next content field to answer, in a fixed order -- every draft walks all four fields, every time, even ones that already hold values, surfacing them so new entries can be appended.", write, t.agentProposeStart)
	add(s, t, "propose_advance", "Answer token's next_field (values to append, with citations, or confirmed_absent with a reason when there is nothing further to add), or peek its current state unchanged when outcome is omitted. The moment every field is answered, this call attempts the real commit automatically; a failure leaves the draft open for correction. Pass close true to abandon the draft immediately.", write, t.agentProposeAdvance)
	return s
}

func (t *tools) agentProposeStart(ctx context.Context, in wire.DraftStartRequest) (any, error) {
	return t.drafter.Start(ctx, in)
}

func (t *tools) agentProposeAdvance(ctx context.Context, in wire.DraftAdvanceRequest) (any, error) {
	return t.drafter.Advance(ctx, in)
}
