package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"
)

// WalkthroughOutcomeStatus is one leaf's reported status within a
// walkthrough (ADR-0053).
type WalkthroughOutcomeStatus string

const (
	WalkthroughDone     WalkthroughOutcomeStatus = "done"
	WalkthroughSkipped  WalkthroughOutcomeStatus = "skipped"
	WalkthroughEscalate WalkthroughOutcomeStatus = "escalate"
	// WalkthroughBlocked is agent-reported testimony that an external,
	// technical precondition is not met (ADR-0056) — never something
	// Polaroid itself derives: the reasoning kernel is open-world and
	// never infers a fact is false merely because it was not asserted
	// (internal/reasoning/datalog). Kept distinct from WalkthroughEscalate
	// so a reader can tell "waiting on a person's judgment" apart from
	// "waiting on an external, technical precondition." Freezes next the
	// same way escalate does.
	WalkthroughBlocked WalkthroughOutcomeStatus = "blocked"
)

func validWalkthroughStatus(s WalkthroughOutcomeStatus) bool {
	switch s {
	case WalkthroughDone, WalkthroughSkipped, WalkthroughEscalate, WalkthroughBlocked:
		return true
	}
	return false
}

// ErrInvalidOutcome reports an outcome whose key is empty or whose status
// is not one of done, skipped, escalate or blocked.
var ErrInvalidOutcome = errors.New("invalid outcome")

var (
	ErrDynamicChildrenDisabled = errors.New("walkthrough does not accept dynamic children")
	ErrDynamicChildrenChanged  = errors.New("walkthrough dynamic children changed concurrently")
	ErrDynamicChildExists      = errors.New("dynamic child is already attached")
	ErrInvalidDynamicChild     = errors.New("invalid dynamic child")
)

// WalkthroughOutcome is one leaf's reported outcome, given to Advance.
// Reason is free text specific to a blocked report (ADR-0056, for example
// "precondition check.vet's required tool is not installed"), though
// nothing refuses it alongside another status.
type WalkthroughOutcome struct {
	Key    string
	Status WalkthroughOutcomeStatus
	Note   string
	Reason string
}

// WalkthroughEvent is one row of a walkthrough's append-only event log:
// either the creating event (Goal and Snapshot set) or a later event
// recording an outcome, a dynamic child, or a pure peek. Context is
// whatever facts were given with this event, if any.
type WalkthroughEvent struct {
	Token                   string
	Goal                    string
	Snapshot                string
	Repository              string
	AllowDynamicChildren    bool
	DynamicChild            string
	ExpectedDynamicChildren []string
	Outcome                 *WalkthroughOutcome
	Context                 map[string]string
	CreatedAt               time.Time
}

// WalkthroughState is a token's current, folded state (ADR-0053): Goal,
// Snapshot, Repository and dynamic-child mode from its creating event;
// DynamicChildren in attachment order; Statuses from the outcome last
// reported per key (a later report for the same key overrides an earlier
// one: a fold, not a union, and not "latest row wins" the way other
// append-only tables in this package work); Reasons and Notes folded the
// same way, by key (ADR-0056); Context folded the same way, by name;
// CreatedAt is the creating event's own time, used to order an operator's
// list_walkthroughs; ExpiresAt is the latest event's time plus the TTL.
type WalkthroughState struct {
	Token                string
	Goal                 string
	Snapshot             string
	Repository           string
	AllowDynamicChildren bool
	DynamicChildren      []string
	Statuses             map[string]WalkthroughOutcomeStatus
	Reasons              map[string]string
	Notes                map[string]string
	Context              map[string]string
	CreatedAt            time.Time
	ExpiresAt            time.Time
}

// WalkthroughActive, WalkthroughBlockedList and WalkthroughEscalated are
// an operator's coarse view of a walkthrough's status (ADR-0056), derived
// purely from its own per-leaf fold: "blocked" if any leaf is currently
// blocked, "escalate" if any leaf is currently escalate (and none
// blocked), "active" otherwise. Deliberately coarser than
// /mcp/agent/v3's own in_progress/complete status (ADR-0055): that one is
// derived from the full composition tree (reasoning.PlanTree), which
// this legacy, operator-facing surface has no access to and must not —
// internal/archtest already keeps internal/memory, internal/transport/http
// and internal/transport/mcp free of internal/knowledge and
// internal/knowledgeops, the same boundary every other milestone of this
// initiative kept on the agent-mode side alone. "active" therefore covers
// both a walkthrough with nothing currently stuck and one that may
// already be entirely done — this surface cannot tell those apart
// without the tree; an operator wanting that distinction reads
// /mcp/agent/v3 with the agent's own token.
const (
	WalkthroughOperatorActive   = "active"
	WalkthroughOperatorEscalate = "escalate"
	WalkthroughOperatorBlocked  = "blocked"
)

// Status is this state's coarse, operator-facing status (ADR-0056); see
// WalkthroughOperatorActive's doc comment for what it deliberately does
// not, and cannot, mean.
func (st WalkthroughState) Status() string {
	escalated := false
	for _, s := range st.Statuses {
		switch s {
		case WalkthroughBlocked:
			return WalkthroughOperatorBlocked
		case WalkthroughEscalate:
			escalated = true
		}
	}
	if escalated {
		return WalkthroughOperatorEscalate
	}
	return WalkthroughOperatorActive
}

// WalkthroughStore is guided-procedure walkthroughs' storage contract
// (ADR-0053), narrower than Store: append one event, read a token's
// events oldest first, read every token's events (for an operator's
// list_walkthroughs, ADR-0056), and delete every trace of a token. Only
// *sqlite.Store implements it; Walkthroughs is its only caller.
type WalkthroughStore interface {
	// AppendWalkthroughEvent stores ev. For a dynamic-child event, it
	// atomically compares ExpectedDynamicChildren with the current child
	// list and returns ErrDynamicChildrenChanged if the list has changed.
	AppendWalkthroughEvent(ctx context.Context, ev WalkthroughEvent) error
	// WalkthroughEvents returns every event for token, oldest first, or
	// ErrNotFound if none exists (never created, or already deleted).
	WalkthroughEvents(ctx context.Context, token string) ([]WalkthroughEvent, error)
	// AllWalkthroughEvents returns every event of every token, oldest
	// first within each token, grouped by token (every event of one
	// token is contiguous): the raw material for an operator's
	// list_walkthroughs (ADR-0056), folded and filtered in this package,
	// not in the store, since the expected scale (a handful of
	// concurrent, ephemeral walkthroughs) does not justify pushing the
	// fold into SQL.
	AllWalkthroughEvents(ctx context.Context) ([]WalkthroughEvent, error)
	// DeleteWalkthrough removes every event for token: no trace is kept
	// (ADR-0053), unlike every other record in this package.
	DeleteWalkthrough(ctx context.Context, token string) error
}

// WalkthroughTTL is how long a walkthrough survives without activity: a
// real, sliding TTL, reset by every event including a peek (ADR-0053).
// An implementation detail, not a governed, versioned policy like write
// limits.
const WalkthroughTTL = 24 * time.Hour

// Walkthroughs is the domain service for guided-procedure walkthroughs
// (ADR-0053): stateful, per-agent progress through a procedure's
// composed steps, with a real, sliding TTL — the first record in
// Polaroid that is not kept forever. It knows nothing about procedures,
// composition or plans: internal/knowledgeops resolves those from a
// knowledge.Catalog, using Walkthroughs only for the fold and the TTL.
type Walkthroughs struct {
	store WalkthroughStore
	now   func() time.Time
	ttl   time.Duration
}

// NewWalkthroughs builds a Walkthroughs backed by store, with the default
// TTL and clock.
func NewWalkthroughs(store WalkthroughStore) *Walkthroughs {
	return &Walkthroughs{store: store, now: timestamp, ttl: WalkthroughTTL}
}

// Start creates a new walkthrough for goal at snapshot, with a fresh,
// opaque UUIDv7 token (the same identity convention every other record in
// this package uses), and returns its initial state: no outcomes
// reported yet.
func (w *Walkthroughs) Start(ctx context.Context, goal, snapshot, repository string, context_ map[string]string) (WalkthroughState, error) {
	return w.start(ctx, goal, snapshot, repository, context_, false)
}

// StartWithDynamicChildren creates a walkthrough that may receive runtime
// child procedures after it starts (ADR-0063).
func (w *Walkthroughs) StartWithDynamicChildren(ctx context.Context, goal, snapshot, repository string, context_ map[string]string) (WalkthroughState, error) {
	return w.start(ctx, goal, snapshot, repository, context_, true)
}

func (w *Walkthroughs) start(ctx context.Context, goal, snapshot, repository string, context_ map[string]string, allowDynamicChildren bool) (WalkthroughState, error) {
	ev := WalkthroughEvent{
		Token: uuid.NewV7().String(), Goal: goal, Snapshot: snapshot, Repository: repository,
		AllowDynamicChildren: allowDynamicChildren, Context: context_, CreatedAt: w.now(),
	}
	if err := w.store.AppendWalkthroughEvent(ctx, ev); err != nil {
		return WalkthroughState{}, err
	}
	return foldWalkthrough([]WalkthroughEvent{ev}, w.ttl), nil
}

// AttachChild adds one runtime-selected Procedure to an open dynamic
// walkthrough. Knowledgeops validates the child against the pinned catalog;
// this layer owns only the per-token append-only state.
func (w *Walkthroughs) AttachChild(ctx context.Context, token, key string) (WalkthroughState, error) {
	if key == "" {
		return WalkthroughState{}, ErrInvalidDynamicChild
	}
	events, state, err := w.loadState(ctx, token)
	if err != nil {
		return WalkthroughState{}, err
	}
	if !state.AllowDynamicChildren {
		return WalkthroughState{}, ErrDynamicChildrenDisabled
	}
	if slices.Contains(state.DynamicChildren, key) {
		return WalkthroughState{}, ErrDynamicChildExists
	}
	ev := WalkthroughEvent{
		Token: token, DynamicChild: key,
		ExpectedDynamicChildren: slices.Clone(state.DynamicChildren),
		CreatedAt:               w.now(),
	}
	if err := w.store.AppendWalkthroughEvent(ctx, ev); err != nil {
		return WalkthroughState{}, err
	}
	return foldWalkthrough(append(events, ev), w.ttl), nil
}

// State returns token's current, folded state, without appending an
// event or resetting its sliding TTL: a read-only check, used by
// internal/knowledgeops to resolve a walkthrough's pinned goal and
// snapshot — for example, to validate an outcome's key against the
// goal's composed children before calling Advance with it. An expired or
// unknown token is ErrNotFound, indistinguishable from each other; an
// expired one is deleted outright as part of answering (no trace is
// kept), the same as Advance.
func (w *Walkthroughs) State(ctx context.Context, token string) (WalkthroughState, error) {
	_, st, err := w.loadState(ctx, token)
	return st, err
}

// Advance appends outcome (nil for a pure peek) and context (nil for
// none) to token's event log, resetting its sliding TTL, and returns the
// updated state. An expired or unknown token is ErrNotFound,
// indistinguishable from each other; an expired one is deleted outright
// as part of answering (no trace is kept).
func (w *Walkthroughs) Advance(ctx context.Context, token string, outcome *WalkthroughOutcome, context_ map[string]string) (WalkthroughState, error) {
	if outcome != nil && (outcome.Key == "" || !validWalkthroughStatus(outcome.Status)) {
		return WalkthroughState{}, ErrInvalidOutcome
	}
	events, _, err := w.loadState(ctx, token)
	if err != nil {
		return WalkthroughState{}, err
	}
	ev := WalkthroughEvent{Token: token, Outcome: outcome, Context: context_, CreatedAt: w.now()}
	if err := w.store.AppendWalkthroughEvent(ctx, ev); err != nil {
		return WalkthroughState{}, err
	}
	return foldWalkthrough(append(events, ev), w.ttl), nil
}

// Close ends token immediately (ADR-0055): whether or not every leaf is
// done, no trace is kept from this point on, exactly as if it had
// expired — an explicit abandon, not only a completion shortcut. An
// already-closed or already-expired token is ErrNotFound,
// indistinguishable from one that never existed, the same as every
// other operation on one.
func (w *Walkthroughs) Close(ctx context.Context, token string) error {
	if _, _, err := w.loadState(ctx, token); err != nil {
		return err
	}
	return w.store.DeleteWalkthrough(ctx, token)
}

// loadState reads token's events, folds them, and applies expiry: an
// expired token is deleted and reported as ErrNotFound, indistinguishable
// from one that never existed. It returns the raw events too, so Advance
// can re-fold after appending without a second store read.
func (w *Walkthroughs) loadState(ctx context.Context, token string) ([]WalkthroughEvent, WalkthroughState, error) {
	events, err := w.store.WalkthroughEvents(ctx, token)
	if err != nil {
		return nil, WalkthroughState{}, err
	}
	st := foldWalkthrough(events, w.ttl)
	if !st.ExpiresAt.After(w.now()) {
		_ = w.store.DeleteWalkthrough(ctx, token)
		return nil, WalkthroughState{}, fmt.Errorf("walkthrough %q: %w", token, ErrNotFound)
	}
	return events, st, nil
}

// foldWalkthrough derives a WalkthroughState from a token's complete
// event log, oldest first: Goal/Snapshot/Repository and dynamic-child
// mode from the creating event; DynamicChildren in attachment order;
// Statuses, Reasons and Notes folded per key; Context folded per name;
// ExpiresAt from the latest event's time.
func foldWalkthrough(events []WalkthroughEvent, ttl time.Duration) WalkthroughState {
	st := WalkthroughState{Statuses: map[string]WalkthroughOutcomeStatus{}, Reasons: map[string]string{}, Notes: map[string]string{}, Context: map[string]string{}}
	for _, ev := range events {
		if ev.Goal != "" {
			st.Token, st.Goal, st.Snapshot, st.Repository = ev.Token, ev.Goal, ev.Snapshot, ev.Repository
			st.AllowDynamicChildren = ev.AllowDynamicChildren
			st.CreatedAt = ev.CreatedAt
		}
		if ev.DynamicChild != "" {
			st.DynamicChildren = append(st.DynamicChildren, ev.DynamicChild)
		}
		if ev.Outcome != nil {
			st.Statuses[ev.Outcome.Key] = ev.Outcome.Status
			st.Reasons[ev.Outcome.Key] = ev.Outcome.Reason
			st.Notes[ev.Outcome.Key] = ev.Outcome.Note
		}
		for k, v := range ev.Context {
			st.Context[k] = v
		}
		st.ExpiresAt = ev.CreatedAt.Add(ttl)
	}
	return st
}

// WalkthroughFilter selects walkthroughs for an operator to list
// (ADR-0056). Status is one of WalkthroughOperatorActive,
// WalkthroughOperatorEscalate or WalkthroughOperatorBlocked; "" lists
// every one.
type WalkthroughFilter struct {
	Repository string
	Goal       string
	Status     string
	Page       Page
}

// List returns the walkthroughs f selects, oldest first (by when each was
// started), and the cursor of the next page if there is one (ADR-0056).
// An expired walkthrough found along the way is deleted outright, the
// same as any other operation that happens to touch one, narrowing (but
// not closing: one never listed or otherwise touched still lingers until
// its own next access) the gap ADR-0053 already accepted of no background
// sweep.
func (w *Walkthroughs) List(ctx context.Context, f WalkthroughFilter) ([]WalkthroughState, string, error) {
	var p problems
	switch f.Status {
	case "", WalkthroughOperatorActive, WalkthroughOperatorEscalate, WalkthroughOperatorBlocked:
	default:
		p.add("status", `must be "active", "escalate" or "blocked"`)
	}
	if f.Repository != "" {
		checkRepository(&p, "repository", f.Repository)
	}
	g := newPager("walkthroughs", true, f.Page, f.Repository, f.Goal, f.Status)
	after := g.start(&p)
	if err := p.err(); err != nil {
		return nil, "", err
	}
	events, err := w.store.AllWalkthroughEvents(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("list walkthroughs: %w", err)
	}
	now := w.now()
	var states []WalkthroughState
	for i := 0; i < len(events); {
		j := i + 1
		for j < len(events) && events[j].Token == events[i].Token {
			j++
		}
		st := foldWalkthrough(events[i:j], w.ttl)
		if !st.ExpiresAt.After(now) {
			_ = w.store.DeleteWalkthrough(ctx, st.Token)
		} else if (f.Repository == "" || st.Repository == f.Repository) &&
			(f.Goal == "" || st.Goal == f.Goal) &&
			(f.Status == "" || st.Status() == f.Status) {
			states = append(states, st)
		}
		i = j
	}
	slices.SortFunc(states, func(a, b WalkthroughState) int { return a.CreatedAt.Compare(b.CreatedAt) })
	if after != nil {
		i := 0
		for i < len(states) {
			c := states[i].CreatedAt.Compare(after.At)
			if c > 0 || (c == 0 && states[i].Token > after.ID) {
				break
			}
			i++
		}
		states = states[i:]
	}
	states, next := trim(f.Page, states, func(st WalkthroughState) string { return g.next("", st.CreatedAt, st.Token) })
	return states, next, nil
}
