package knowledgeops

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/reasoning"
	"github.com/ashuangiras/polaroid/internal/reasoning/policy"
	"github.com/ashuangiras/polaroid/internal/transport/wire"
)

// Guide answers /mcp/agent/v3's start and advance (ADR-0053, amended by
// ADR-0054 for multi-level plans, ADR-0055 for completion semantics and
// an early close, and ADR-0058 for reasoned steps): a goal resolved and
// pinned at a snapshot, plus persisted, per-agent walkthrough state
// (internal/memory), folded into a plan by reasoning.PlanTree — not
// reasoning.Plan (resolve's own one-level function, unaffected by this).
// On every call, the plan's own current next leaf (if any) is
// additionally resolved through the full reasoning.Resolve (ADR-0058),
// against the walkthrough's accumulated Context but call-scoped
// Assertions/Trusted, never persisted across calls the way Context is.
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
	ev           reasoning.Evaluator
	trust        policy.Trust
	now          func() time.Time
}

// NewGuide builds a Guide. ev and trust are the same reasoning evaluator
// and operator-held trust material Resolver already uses (ADR-0058):
// Guide's own per-leaf resolution needs them for exactly the same reason.
func NewGuide(kernel *knowledge.Kernel, walkthroughs *memory.Walkthroughs, ev reasoning.Evaluator, trust policy.Trust) *Guide {
	return &Guide{kernel: kernel, walkthroughs: walkthroughs, ev: ev, trust: trust, now: time.Now}
}

// StartV7 starts a walkthrough that may accept dynamically discovered
// Procedure children, pinned at the supplied snapshot or the active one.
func (g *Guide) StartV7(ctx context.Context, in wire.WalkthroughStartRequestV7) (wire.WalkthroughResult, error) {
	if in.Goal == "" {
		return wire.WalkthroughResult{}, requiredField("goal")
	}
	cat, err := g.catalogAt(ctx, in.Snapshot)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	goal, err := cat.Record(in.Goal)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	if in.AllowDynamicChildren {
		if goal.Type != knowledge.TypeProcedure {
			return wire.WalkthroughResult{}, invalidField("goal", "must be a Procedure when dynamic children are enabled")
		}
		doc, err := cat.Get(in.Goal)
		if err != nil {
			return wire.WalkthroughResult{}, classify(err)
		}
		if !doc.Applicability.ApplicableIn(in.Repository) {
			return wire.WalkthroughResult{}, classify(knowledge.ErrNotApplicable)
		}
	}
	plan, err := planFor(cat, goal, noOutcomesYet, in.Expand)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	var state memory.WalkthroughState
	if in.AllowDynamicChildren {
		state, err = g.walkthroughs.StartWithDynamicChildren(ctx, in.Goal, string(cat.Snapshot()), in.Repository, in.Context)
	} else {
		state, err = g.walkthroughs.Start(ctx, in.Goal, string(cat.Snapshot()), in.Repository, in.Context)
	}
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	resolution, err := g.resolveNextLeaf(ctx, cat, plan, state.Context, in.Assertions)
	if err != nil {
		return wire.WalkthroughResult{}, err
	}
	return newWalkthroughResultForState(state, plan, resolution), nil
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
	plan, err := planFor(cat, goal, noOutcomesYet, in.Expand)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	st, err := g.walkthroughs.Start(ctx, in.Goal, string(cat.Snapshot()), in.Repository, in.Context)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	resolution, err := g.resolveNextLeaf(ctx, cat, plan, st.Context, in.Assertions)
	if err != nil {
		return wire.WalkthroughResult{}, err
	}
	return newWalkthroughResult(st, plan, resolution), nil
}

// Advance reports in.Outcome (nil for a pure peek) and in.Context on
// in.Token, and returns the updated {token, snapshot, plan, status}. The
// plan is resolved from the goal pinned at Start, not the currently
// active snapshot, so it stays stable across later publications.
// in.Outcome's key or path (ADR-0054) must resolve to an actual leaf
// reached from the goal by following composes at every level; anything
// else — a key or path naming a composed node, or a chain that does not
// match the goal's own composition — is invalid_request. An expired or
// unknown token is not_found. When in.Close is true (ADR-0055), the
// walkthrough is ended immediately after in.Outcome (if any) is applied
// and reflected in the returned result; a later call with the same
// token is then not_found, the same as an expired one.
func (g *Guide) Advance(ctx context.Context, in wire.WalkthroughAdvanceRequest) (wire.WalkthroughResult, error) {
	return g.advance(ctx, in, false)
}

// AdvanceV7 advances either a static walkthrough or a v7 dynamic-parent
// walkthrough. Earlier endpoints keep using Advance and cannot operate
// on tokens created with dynamic children.
func (g *Guide) AdvanceV7(ctx context.Context, in wire.WalkthroughAdvanceRequest) (wire.WalkthroughResult, error) {
	return g.advance(ctx, in, true)
}

func (g *Guide) advance(ctx context.Context, in wire.WalkthroughAdvanceRequest, allowDynamicChildren bool) (wire.WalkthroughResult, error) {
	if in.Token == "" {
		return wire.WalkthroughResult{}, requiredField("token")
	}
	state, err := g.walkthroughs.State(ctx, in.Token)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	if state.AllowDynamicChildren && !allowDynamicChildren {
		return wire.WalkthroughResult{}, invalidField("token", "dynamic-child walkthroughs require /mcp/agent/v7")
	}
	cat, err := g.kernel.At(ctx, knowledge.Snapshot(state.Snapshot))
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	goal, err := cat.Record(state.Goal)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	walkGoal := withDynamicChildren(goal, state.DynamicChildren)
	outcome, err := decodeWalkthroughOutcome(cat, walkGoal, in.Outcome)
	if err != nil {
		return wire.WalkthroughResult{}, err
	}
	state, err = g.walkthroughs.Advance(ctx, in.Token, outcome, in.Context)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	walkGoal = withDynamicChildren(goal, state.DynamicChildren)
	plan, err := planFor(cat, walkGoal, leafStatusFrom(state.Statuses), in.Expand)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	resolution, err := g.resolveNextLeaf(ctx, cat, plan, state.Context, in.Assertions)
	if err != nil {
		return wire.WalkthroughResult{}, err
	}
	result := newWalkthroughResultForState(state, plan, resolution)
	if in.Close {
		if err := g.walkthroughs.Close(ctx, in.Token); err != nil {
			return wire.WalkthroughResult{}, classify(err)
		}
	}
	return result, nil
}

// AttachChild appends a dynamically selected Procedure to an active v7
// walkthrough and recomputes the parent plan from its pinned snapshot.
func (g *Guide) AttachChild(ctx context.Context, in wire.WalkthroughAttachChildRequest) (wire.WalkthroughResult, error) {
	if in.Token == "" {
		return wire.WalkthroughResult{}, requiredField("token")
	}
	if in.Key == "" {
		return wire.WalkthroughResult{}, requiredField("key")
	}
	state, err := g.walkthroughs.State(ctx, in.Token)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	if !state.AllowDynamicChildren {
		return wire.WalkthroughResult{}, classify(memory.ErrDynamicChildrenDisabled)
	}
	cat, err := g.kernel.At(ctx, knowledge.Snapshot(state.Snapshot))
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	goal, err := cat.Record(state.Goal)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	child, err := cat.Record(in.Key)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	if child.Type != knowledge.TypeProcedure {
		return wire.WalkthroughResult{}, invalidField("key", "must identify a Procedure record")
	}
	childDoc, err := cat.Get(in.Key)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	if !childDoc.Applicability.ApplicableIn(state.Repository) {
		return wire.WalkthroughResult{}, classify(knowledge.ErrNotApplicable)
	}
	cyclic, err := compositionReaches(cat, in.Key, goal.Key, map[string]bool{})
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	if cyclic {
		return wire.WalkthroughResult{}, invalidField("key", "would create a cycle in the walkthrough plan")
	}
	existingKeys, err := composedKeys(cat, withDynamicChildren(goal, state.DynamicChildren))
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	childKeys, err := composedKeys(cat, child)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	for key := range childKeys {
		if _, exists := existingKeys[key]; exists {
			return wire.WalkthroughResult{}, invalidField("key", "would duplicate a Procedure already present in the walkthrough plan")
		}
	}
	state, err = g.walkthroughs.AttachChild(ctx, in.Token, in.Key)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	walkGoal := withDynamicChildren(goal, state.DynamicChildren)
	plan, err := planFor(cat, walkGoal, leafStatusFrom(state.Statuses), false)
	if err != nil {
		return wire.WalkthroughResult{}, classify(err)
	}
	resolution, err := g.resolveNextLeaf(ctx, cat, plan, state.Context, nil)
	if err != nil {
		return wire.WalkthroughResult{}, err
	}
	return newWalkthroughResultForState(state, plan, resolution), nil
}

func (g *Guide) catalogAt(ctx context.Context, snapshot string) (*knowledge.Catalog, error) {
	if snapshot == "" {
		return g.kernel.Active(ctx)
	}
	return g.kernel.At(ctx, knowledge.Snapshot(snapshot))
}

func withDynamicChildren(goal *knowledge.Record, children []string) *knowledge.Record {
	if len(children) == 0 {
		return goal
	}
	copy := *goal
	copy.Composes = append(slices.Clone(goal.Composes), children...)
	return &copy
}

func compositionReaches(cat *knowledge.Catalog, from, target string, visited map[string]bool) (bool, error) {
	if from == target {
		return true, nil
	}
	if visited[from] {
		return false, nil
	}
	visited[from] = true
	record, err := cat.Record(from)
	if err != nil {
		return false, err
	}
	for _, child := range record.Composes {
		reaches, err := compositionReaches(cat, child, target, visited)
		if err != nil || reaches {
			return reaches, err
		}
	}
	return false, nil
}

func composedKeys(cat *knowledge.Catalog, root *knowledge.Record) (map[string]struct{}, error) {
	keys := make(map[string]struct{})
	var visit func(*knowledge.Record) error
	visit = func(record *knowledge.Record) error {
		if _, visited := keys[record.Key]; visited {
			return nil
		}
		keys[record.Key] = struct{}{}
		for _, key := range record.Composes {
			child, err := cat.Record(key)
			if err != nil {
				return err
			}
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	return keys, nil
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
	case memory.WalkthroughDone, memory.WalkthroughSkipped, memory.WalkthroughEscalate, memory.WalkthroughBlocked:
	default:
		return nil, invalidField("outcome.status", "must be done, skipped, escalate or blocked")
	}
	return &memory.WalkthroughOutcome{Key: encodePath(path), Status: status, Note: in.Note, Reason: in.Reason}, nil
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
// ADR-0054, ADR-0056) into reasoning.PlanTree's/ExpandTree's leaf-status
// lookup.
func leafStatusFrom(statuses map[string]memory.WalkthroughOutcomeStatus) func([]string) reasoning.LeafStatus {
	return func(path []string) reasoning.LeafStatus {
		switch statuses[encodePath(path)] {
		case memory.WalkthroughDone, memory.WalkthroughSkipped:
			return reasoning.LeafStatus{Done: true}
		case memory.WalkthroughEscalate:
			return reasoning.LeafStatus{Escalate: true}
		case memory.WalkthroughBlocked:
			return reasoning.LeafStatus{Blocked: true}
		default:
			return reasoning.LeafStatus{}
		}
	}
}

// newWalkthroughResult builds a WalkthroughResult from st, plan and
// resolution (ADR-0058, nil when nothing is resolved), deriving status
// (ADR-0055).
func newWalkthroughResult(st memory.WalkthroughState, plan []reasoning.PlanEntry, resolution *wire.KnowledgeResolution) wire.WalkthroughResult {
	out := wire.WalkthroughResult{Token: st.Token, Snapshot: st.Snapshot, Status: walkthroughStatus(plan), Resolution: resolution}
	out.Plan = newPlanEntries(plan)
	return out
}

func newWalkthroughResultForState(st memory.WalkthroughState, plan []reasoning.PlanEntry, resolution *wire.KnowledgeResolution) wire.WalkthroughResult {
	out := newWalkthroughResult(st, plan, resolution)
	if st.AllowDynamicChildren && len(st.DynamicChildren) == 0 {
		out.Status = "in_progress"
	}
	return out
}

// nextLeafPath finds plan's own single "next", "escalate" or "blocked"
// top-level entry (there is at most one, PlanTree/ExpandTree's own
// construction: every entry after it is a bare, pathless key) and
// returns its full path down to the actual leaf; nil once nothing
// remains (ADR-0055's "complete").
func nextLeafPath(plan []reasoning.PlanEntry) []string {
	for _, e := range plan {
		if len(e.Path) > 0 {
			return e.Path
		}
	}
	return nil
}

// resolveNextLeaf resolves plan's own current next leaf (ADR-0058)
// against cat, accumulatedContext (the walkthrough's own fold, ADR-0053)
// and assertions presented on this call alone — never persisted or
// inherited from an earlier call, unlike accumulatedContext. Returns nil
// when plan has no next leaf (nothing to resolve, not an error).
func (g *Guide) resolveNextLeaf(ctx context.Context, cat *knowledge.Catalog, plan []reasoning.PlanEntry,
	accumulatedContext map[string]string, assertions []wire.KnowledgeAssertion) (*wire.KnowledgeResolution, error) {
	path := nextLeafPath(plan)
	if path == nil {
		return nil, nil
	}
	at := g.now().UTC().Truncate(time.Second)
	trusted, err := establishTrusted(g.trust, assertions, at, at)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, ResolveTimeout)
	defer cancel()
	res, err := reasoning.Resolve(rctx, cat, g.ev, g.trust, reasoning.Request{Goal: path[len(path)-1], Context: accumulatedContext, Trusted: trusted, At: at})
	if err != nil {
		return nil, classifyResolveErr(err)
	}
	out, err := toWireResolution(res)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// newPlanEntries converts a plan, recursively (ADR-0056's expand).
func newPlanEntries(plan []reasoning.PlanEntry) []wire.KnowledgePlanEntry {
	if plan == nil {
		return nil
	}
	out := make([]wire.KnowledgePlanEntry, len(plan))
	for i, e := range plan {
		out[i] = wire.KnowledgePlanEntry{Key: e.Key, Status: e.Status, Path: e.Path, Children: newPlanEntries(e.Children)}
	}
	return out
}

// planFor resolves goal's plan, expanded (ADR-0056) when expand is true.
func planFor(cat *knowledge.Catalog, goal *knowledge.Record, leafStatus func([]string) reasoning.LeafStatus, expand bool) ([]reasoning.PlanEntry, error) {
	if expand {
		return reasoning.ExpandTree(cat, goal, leafStatus)
	}
	return reasoning.PlanTree(cat, goal, leafStatus)
}

// walkthroughStatus derives a walkthrough's top-level status (ADR-0055)
// from plan: "complete" exactly when no entry remains "next" or
// "escalate" (equivalently, every entry is "done", or plan is empty),
// "in_progress" otherwise. Non-terminal: since plan's own derivation
// already folds the latest report per leaf (ADR-0053), a later
// correction that reopens an already-done leaf naturally moves this
// back to "in_progress" too — nothing new to compute.
func walkthroughStatus(plan []reasoning.PlanEntry) string {
	for _, e := range plan {
		if e.Status == "next" || e.Status == "escalate" || e.Status == "blocked" {
			return "in_progress"
		}
	}
	return "complete"
}
