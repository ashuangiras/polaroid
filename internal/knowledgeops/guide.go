package knowledgeops

import (
	"context"
	"slices"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Guide answers /mcp/agent/v3's start and advance (ADR-0053): a goal
// resolved and pinned at a snapshot, plus persisted, per-agent
// walkthrough state (internal/memory), folded into resolve's plan shape
// (ADR-0052) by calling reasoning.Plan directly — not the full
// reasoning.Resolve, since a walkthrough's context is only captured here
// (ADR-0053), not yet reasoned over.
//
// Note on the ADR's text: start's goal is resolved through
// knowledge.Catalog.Record, a typed record, which — unlike read's legacy
// Procedure documents — has no Applicability to check. So, despite the
// ADR comparing start's not_found/not_applicable handling to read's, only
// not_found is actually implementable for a typed-record goal; there is
// no repository-scoped not_applicable to report.
type Guide struct {
	kernel       *knowledge.Kernel
	walkthroughs *memory.Walkthroughs
}

// NewGuide builds a Guide.
func NewGuide(kernel *knowledge.Kernel, walkthroughs *memory.Walkthroughs) *Guide {
	return &Guide{kernel: kernel, walkthroughs: walkthroughs}
}

// Start resolves in.Goal at the active snapshot, pins it for the new
// walkthrough's lifetime, and returns {token, snapshot, plan} with an
// empty completed list: nothing reported yet. A goal without composed
// children is not an error — its plan is nil (ADR-0052), so start
// returns a trivially complete walkthrough.
func (g *Guide) Start(ctx context.Context, in wire.WalkthroughStartRequest) (wire.WalkthroughResult, error) {
	if in.Goal == "" {
		return wire.WalkthroughResult{}, requiredField("goal")
	}
	cat, err := g.kernel.Active(ctx)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	goal, err := cat.Record(in.Goal)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	plan, err := reasoning.Plan(cat, goal, nil)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	st, err := g.walkthroughs.Start(ctx, in.Goal, string(cat.Snapshot()), in.Repository, in.Context)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	return newWalkthroughResult(st, plan), nil
}

// Advance reports in.Outcome (nil for a pure peek) and in.Context on
// in.Token, and returns the updated {token, snapshot, plan}. The plan is
// resolved from the goal pinned at Start, not the currently active
// snapshot, so it stays stable across later publications. An
// in.Outcome.Key that is not one of the goal's immediate composed
// children is invalid_request; an expired or unknown token is not_found.
func (g *Guide) Advance(ctx context.Context, in wire.WalkthroughAdvanceRequest) (wire.WalkthroughResult, error) {
	if in.Token == "" {
		return wire.WalkthroughResult{}, requiredField("token")
	}
	state, err := g.walkthroughs.State(ctx, in.Token)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	cat, err := g.kernel.At(ctx, knowledge.Snapshot(state.Snapshot))
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	goal, err := cat.Record(state.Goal)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	outcome, err := decodeWalkthroughOutcome(goal, in.Outcome)
	if err != nil {
		return wire.WalkthroughResult{}, err
	}
	state, err = g.walkthroughs.Advance(ctx, in.Token, outcome, in.Context)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	plan, err := reasoning.Plan(cat, goal, completedKeys(state.Statuses))
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	return newWalkthroughResult(state, plan), nil
}

// decodeWalkthroughOutcome validates in against goal's immediate composed
// children — checked here, not by memory.Walkthroughs, which knows
// nothing about composition — and returns nil for a pure peek.
func decodeWalkthroughOutcome(goal *knowledge.Record, in *wire.WalkthroughOutcomeArg) (*memory.WalkthroughOutcome, error) {
	if in == nil {
		return nil, nil
	}
	if in.Key == "" {
		return nil, requiredField("outcome.key")
	}
	if !slices.Contains(goal.Composes, in.Key) {
		return nil, invalidField("outcome.key", "must be one of the goal's immediate composed children")
	}
	status := memory.WalkthroughOutcomeStatus(in.Status)
	switch status {
	case memory.WalkthroughDone, memory.WalkthroughSkipped, memory.WalkthroughEscalate:
	default:
		return nil, invalidField("outcome.status", "must be done, skipped or escalate")
	}
	return &memory.WalkthroughOutcome{Key: in.Key, Status: status, Note: in.Note}, nil
}

// completedKeys are the keys whose last-reported status makes them
// no-longer-blocking for reasoning.Plan: done or skipped. escalate is
// deliberately left out, so Plan's existing "first not-done entry is
// next" logic freezes next on an escalated key, with no change to Plan
// itself (ADR-0053).
func completedKeys(statuses map[string]memory.WalkthroughOutcomeStatus) []string {
	var out []string
	for k, s := range statuses {
		if s == memory.WalkthroughDone || s == memory.WalkthroughSkipped {
			out = append(out, k)
		}
	}
	return out
}

// newWalkthroughResult builds a WalkthroughResult from st and plan.
func newWalkthroughResult(st memory.WalkthroughState, plan []reasoning.PlanEntry) wire.WalkthroughResult {
	out := wire.WalkthroughResult{Token: st.Token, Snapshot: st.Snapshot}
	for _, e := range plan {
		out.Plan = append(out.Plan, wire.KnowledgePlanEntry{Key: e.Key, Status: e.Status})
	}
	return out
}
