package memory

import (
	"context"
	"errors"
	"fmt"
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
)

func validWalkthroughStatus(s WalkthroughOutcomeStatus) bool {
	switch s {
	case WalkthroughDone, WalkthroughSkipped, WalkthroughEscalate:
		return true
	}
	return false
}

// ErrInvalidOutcome reports an outcome whose key is empty or whose status
// is not one of done, skipped or escalate.
var ErrInvalidOutcome = errors.New("invalid outcome")

// WalkthroughOutcome is one leaf's reported outcome, given to Advance.
type WalkthroughOutcome struct {
	Key    string
	Status WalkthroughOutcomeStatus
	Note   string
}

// WalkthroughEvent is one row of a walkthrough's append-only event log:
// either the creating event (Goal and Snapshot set; Outcome nil) or a
// later event (Outcome set when an outcome was reported; nil for a pure
// peek). Context is whatever facts were given with this event, if any.
type WalkthroughEvent struct {
	Token      string
	Goal       string
	Snapshot   string
	Repository string
	Outcome    *WalkthroughOutcome
	Context    map[string]string
	CreatedAt  time.Time
}

// WalkthroughState is a token's current, folded state (ADR-0053): Goal,
// Snapshot and Repository from its creating event; Statuses from the
// outcome last reported per key (a later report for the same key
// overrides an earlier one: a fold, not a union, and not "latest row
// wins" the way other append-only tables in this package work); Context
// folded the same way, by name; ExpiresAt is the latest event's time plus
// the TTL.
type WalkthroughState struct {
	Token      string
	Goal       string
	Snapshot   string
	Repository string
	Statuses   map[string]WalkthroughOutcomeStatus
	Context    map[string]string
	ExpiresAt  time.Time
}

// WalkthroughStore is guided-procedure walkthroughs' storage contract
// (ADR-0053), narrower than Store: append one event, read a token's
// events oldest first, and delete every trace of a token. Only
// *sqlite.Store implements it; Walkthroughs is its only caller.
type WalkthroughStore interface {
	// AppendWalkthroughEvent stores ev.
	AppendWalkthroughEvent(ctx context.Context, ev WalkthroughEvent) error
	// WalkthroughEvents returns every event for token, oldest first, or
	// ErrNotFound if none exists (never created, or already deleted).
	WalkthroughEvents(ctx context.Context, token string) ([]WalkthroughEvent, error)
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
	ev := WalkthroughEvent{Token: uuid.NewV7().String(), Goal: goal, Snapshot: snapshot, Repository: repository, Context: context_, CreatedAt: w.now()}
	if err := w.store.AppendWalkthroughEvent(ctx, ev); err != nil {
		return WalkthroughState{}, err
	}
	return foldWalkthrough([]WalkthroughEvent{ev}, w.ttl), nil
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
// event log, oldest first: Goal/Snapshot/Repository from the creating
// event, Statuses and Context folded per key (the latest event naming a
// key decides its current value), ExpiresAt from the latest event's time.
func foldWalkthrough(events []WalkthroughEvent, ttl time.Duration) WalkthroughState {
	st := WalkthroughState{Statuses: map[string]WalkthroughOutcomeStatus{}, Context: map[string]string{}}
	for _, ev := range events {
		if ev.Goal != "" {
			st.Token, st.Goal, st.Snapshot, st.Repository = ev.Token, ev.Goal, ev.Snapshot, ev.Repository
		}
		if ev.Outcome != nil {
			st.Statuses[ev.Outcome.Key] = ev.Outcome.Status
		}
		for k, v := range ev.Context {
			st.Context[k] = v
		}
		st.ExpiresAt = ev.CreatedAt.Add(ttl)
	}
	return st
}
