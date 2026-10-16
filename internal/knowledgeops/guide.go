package knowledgeops

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Guide answers /mcp/agent/v3's start and advance (ADR-0053, amended by
// ADR-0054 for multi-level plans): a goal resolved and pinned at a
// snapshot, plus persisted, per-agent walkthrough state
// (internal/memory), folded into a plan by reasoning.PlanTree — not
// reasoning.Plan (resolve's own one-level function, unaffected by this)
// and not the full reasoning.Resolve, since a walkthrough's context is
// only captured here (ADR-0053), not yet reasoned over.
//
// Note on ADR-0053's text: start's goal is resolved through
// knowledge.Catalog.Record, a typed record, which — unlike read's legacy
// Procedure documents — has no Applicability to check. So, despite the
// ADR comparing start's not_found/not_applicable handling to read's, only
// not_found is actually implementable for a typed-record goal; there is
// no repository-scoped not_applicable to report.
//
// Note on ADR-0054's text: see reasoning.PlanTree's own doc comment for
// a second correction, about the composes graph's actual (not) being
// depth/node bounded at publish time.
type Guide struct {
	kernel       *knowledge.Kernel
	walkthroughs *memory.Walkthroughs
}

// NewGuide builds a Guide.
func NewGuide(kernel *knowledge.Kernel, walkthroughs *memory.Walkthroughs) *Guide {
	return &Guide{kernel: kernel, walkthroughs: walkthroughs}
}

// Start resolves in.Goal at the active snapshot, pins it for the new
// walkthrough's lifetime, and returns {token, snapshot, plan} with
// nothing reported yet. A goal without composed children is not an
// error — its plan is nil, so start returns a trivially complete
// walkthrough.
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
	plan, err := reasoning.PlanTree(cat, goal, noOutcomesYet)
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
// snapshot, so it stays stable across later publications. in.Outcome's
// key or path (ADR-0054) must resolve to an actual leaf reached from the
// goal by following composes at every level; anything else — a key or
// path naming a composed node, or a chain that does not match the
// goal's own composition — is invalid_request. An expired or unknown
// token is not_found.
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
	outcome, err := decodeWalkthroughOutcome(cat, goal, in.Outcome)
	if err != nil {
		return wire.WalkthroughResult{}, err
	}
	state, err = g.walkthroughs.Advance(ctx, in.Token, outcome, in.Context)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	plan, err := reasoning.PlanTree(cat, goal, leafStatusFrom(state.Statuses))
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	return newWalkthroughResult(state, plan), nil
}

// pathSeparator joins a multi-level outcome's path into a single string,
// for memory.Walkthroughs' existing flat, per-key fold (ADR-0053): this
// milestone adds no new persisted concept (ADR-0054), reusing that same
// fold with a compound key instead. Safe and unambiguous, since a
// canonical key is only ever lowercase letters, digits, '.', '-' and '_'
// — never '/'. A one-element path (the common case, and the only case
// before this milestone) encodes to exactly the bare key itself, so
// existing stored data and the shorthand bare-key form of outcome are
// both read back identically.
const pathSeparator = "/"

func encodePath(path []string) string { return strings.Join(path, pathSeparator) }

// decodeWalkthroughOutcome validates in against goal's own composition
// (ADR-0054): its key or path must name an actual leaf, reached from
// goal by following composes at every level — never a composed node,
// whose status is always derived from its descendants, never directly
// reported. Returns nil for a pure peek.
func decodeWalkthroughOutcome(cat *knowledge.Catalog, goal *knowledge.Record, in *wire.WalkthroughOutcomeArg) (*memory.WalkthroughOutcome, error) {
	if in == nil {
		return nil, nil
	}
	path, err := outcomePath(in)
	if err != nil {
		return nil, err
	}
	if err := checkLeafPath(cat, goal, path); err != nil {
		return nil, err
	}
	status := memory.WalkthroughOutcomeStatus(in.Status)
	switch status {
	case memory.WalkthroughDone, memory.WalkthroughSkipped, memory.WalkthroughEscalate:
	default:
		return nil, invalidField("outcome.status", "must be done, skipped or escalate")
	}
	return &memory.WalkthroughOutcome{Key: encodePath(path), Status: status, Note: in.Note}, nil
}

// outcomePath normalizes in's key/path shorthand (ADR-0054) into one
// explicit path: exactly one of them must be given.
func outcomePath(in *wire.WalkthroughOutcomeArg) ([]string, error) {
	switch {
	case in.Key != "" && len(in.Path) > 0:
		return nil, invalidField("outcome", "key and path must not both be given")
	case in.Key != "":
		return []string{in.Key}, nil
	case len(in.Path) > 0:
		return in.Path, nil
	default:
		return nil, requiredField("outcome.key or outcome.path")
	}
}

// checkLeafPath walks path from goal through composes at every level
// (ADR-0054): path[0] must be one of goal's immediate composed children,
// path[1] one of path[0]'s, and so on; the final key must itself have no
// composed children (a leaf). advance never accepts a composed node as
// a target.
func checkLeafPath(cat *knowledge.Catalog, goal *knowledge.Record, path []string) error {
	parent, parentKey := goal, goal.Key
	for i, key := range path {
		if !slices.Contains(parent.Composes, key) {
			return invalidField("outcome.path", fmt.Sprintf("%s is not a composed child of %s", key, parentKey))
		}
		rec, err := cat.Record(key)
		if err != nil {
			return classify(err)
		}
		if i == len(path)-1 && len(rec.Composes) > 0 {
			return invalidField("outcome.path", fmt.Sprintf("%s is not a leaf: it has its own composed children", key))
		}
		parent, parentKey = rec, key
	}
	return nil
}

// noOutcomesYet is the leaf-status lookup for a freshly started
// walkthrough: nothing has been reported for any path yet.
func noOutcomesYet([]string) reasoning.LeafStatus { return reasoning.LeafStatus{} }

// leafStatusFrom adapts a walkthrough's flat, path-keyed fold (ADR-0053,
// ADR-0054) into reasoning.PlanTree's leaf-status lookup.
func leafStatusFrom(statuses map[string]memory.WalkthroughOutcomeStatus) func([]string) reasoning.LeafStatus {
	return func(path []string) reasoning.LeafStatus {
		switch statuses[encodePath(path)] {
		case memory.WalkthroughDone, memory.WalkthroughSkipped:
			return reasoning.LeafStatus{Done: true}
		case memory.WalkthroughEscalate:
			return reasoning.LeafStatus{Escalate: true}
		default:
			return reasoning.LeafStatus{}
		}
	}
}

// newWalkthroughResult builds a WalkthroughResult from st and plan.
func newWalkthroughResult(st memory.WalkthroughState, plan []reasoning.PlanEntry) wire.WalkthroughResult {
	out := wire.WalkthroughResult{Token: st.Token, Snapshot: st.Snapshot}
	for _, e := range plan {
		out.Plan = append(out.Plan, wire.KnowledgePlanEntry{Key: e.Key, Status: e.Status, Path: e.Path})
	}
	return out
}
