package memory

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"
)

// Interactive, field-by-field proposal construction (ADR-0060,
// Grounded publication M3): /mcp/agent/v5's propose_start and
// propose_advance. Complements single-shot propose (ADR-0059): instead
// of an agent supplying every field up front, Polaroid walks an
// existing Procedure's still-undecided content fields one at a time,
// requiring an explicit answer -- real, citation-grounded content, or an
// explicit, reasoned decline -- for each. A Draft is per-agent runtime
// scratch state about one proposal under construction, exactly the same
// category of thing a Walkthrough already is (ADR-0053): not published
// knowledge, not kept forever, with a real, sliding TTL and no trace
// once it expires or closes. internal/memory knows nothing about
// Procedures, citations or composition; internal/knowledgeops resolves
// those from a knowledge.Catalog, using Drafts only for the fold and the
// TTL, exactly as Walkthroughs already does.

// DraftField names one of a Procedure's content lists a Draft can walk.
type DraftField string

const (
	DraftFieldPreconditions      DraftField = "preconditions"
	DraftFieldPostconditions     DraftField = "postconditions"
	DraftFieldInvariants         DraftField = "invariants"
	DraftFieldInformalConditions DraftField = "informal_conditions"
)

// DraftFields is every field a Draft can walk, in the fixed order
// propose_start/propose_advance always offer them.
var DraftFields = []DraftField{DraftFieldPreconditions, DraftFieldPostconditions, DraftFieldInvariants, DraftFieldInformalConditions}

func validDraftField(f DraftField) bool {
	for _, k := range DraftFields {
		if k == f {
			return true
		}
	}
	return false
}

// ErrInvalidDraftAnswer reports an answer whose field is not one of
// DraftFields, or that gives neither Values nor ConfirmedAbsent (or
// both).
var ErrInvalidDraftAnswer = errors.New("invalid draft answer")

// DraftCitation grounds one piece of new prose: Quote must appear
// verbatim in Values' combined text -- internal/knowledgeops checks
// this, not this package, the same boundary citation-checking already
// has for single-shot propose.
type DraftCitation struct {
	Source string `json:"source"`
	Quote  string `json:"quote"`
}

// DraftFieldAnswer is one field's answer, given to Advance: either
// Values (new entries to append, grounded by Citations) or
// ConfirmedAbsent (agent testimony that the field genuinely does not
// apply -- never verified by Polaroid, the same epistemic category
// WalkthroughBlocked/WalkthroughEscalate already are); exactly one of
// them is required. Reason is free text specific to a ConfirmedAbsent
// answer.
type DraftFieldAnswer struct {
	Field           DraftField
	Values          []string
	Citations       []DraftCitation
	ConfirmedAbsent bool
	Reason          string
}

func (a DraftFieldAnswer) valid() bool {
	return validDraftField(a.Field) && (len(a.Values) > 0) != a.ConfirmedAbsent
}

// DraftEvent is one row of a draft's append-only event log: either the
// creating event (Key/Snapshot/Proposer/Rationale set; Answer nil) or a
// later event (Answer set when a field was answered; nil for a pure
// peek).
type DraftEvent struct {
	Token     string
	Key       string
	Snapshot  string
	Proposer  string
	Rationale string
	Answer    *DraftFieldAnswer
	CreatedAt time.Time
}

// DraftState is a token's current, folded state: Key, Snapshot,
// Proposer and Rationale from its creating event; Answers from the
// answer last given per field (a later answer for the same field
// overrides an earlier one, the same per-key fold Walkthroughs already
// uses, not "latest row wins"); ExpiresAt is the latest event's time
// plus the TTL.
type DraftState struct {
	Token     string
	Key       string
	Snapshot  string
	Proposer  string
	Rationale string
	Answers   map[DraftField]DraftFieldAnswer
	CreatedAt time.Time
	ExpiresAt time.Time
}

// DraftStore is Drafts' storage contract, narrower than Store: append
// one event, read a token's events oldest first, and delete every trace
// of a token. Only *sqlite.Store implements it; Drafts is its only
// caller. No operator-facing list of every token exists in this
// milestone (ADR-0060 defers it, the same way ADR-0053 originally
// deferred list_walkthroughs until ADR-0056 showed real need).
type DraftStore interface {
	AppendDraftEvent(ctx context.Context, ev DraftEvent) error
	DraftEvents(ctx context.Context, token string) ([]DraftEvent, error)
	DeleteDraft(ctx context.Context, token string) error
}

// DraftTTL is how long a draft survives without activity: a real,
// sliding TTL, reset by every event including a peek -- the same
// implementation-detail status WalkthroughTTL already has.
const DraftTTL = 24 * time.Hour

// Drafts is the domain service for interactive proposal construction
// (ADR-0060): stateful, per-agent progress through one Procedure's
// still-undecided content fields, with a real, sliding TTL. It knows
// nothing about Procedures, citations or composition:
// internal/knowledgeops resolves those from a knowledge.Catalog, using
// Drafts only for the fold and the TTL.
type Drafts struct {
	store DraftStore
	now   func() time.Time
	ttl   time.Duration
}

// NewDrafts builds a Drafts backed by store, with the default TTL and
// clock.
func NewDrafts(store DraftStore) *Drafts {
	return &Drafts{store: store, now: timestamp, ttl: DraftTTL}
}

// Start creates a new draft for key at snapshot, with a fresh, opaque
// UUIDv7 token, and returns its initial state: no fields answered yet.
func (d *Drafts) Start(ctx context.Context, key, snapshot, proposer, rationale string) (DraftState, error) {
	ev := DraftEvent{Token: uuid.NewV7().String(), Key: key, Snapshot: snapshot, Proposer: proposer, Rationale: rationale, CreatedAt: d.now()}
	if err := d.store.AppendDraftEvent(ctx, ev); err != nil {
		return DraftState{}, err
	}
	return foldDraft([]DraftEvent{ev}, d.ttl), nil
}

// State returns token's current, folded state, without appending an
// event or resetting its sliding TTL. An expired or unknown token is
// ErrNotFound, indistinguishable from each other; an expired one is
// deleted outright as part of answering (no trace is kept), the same as
// Advance.
func (d *Drafts) State(ctx context.Context, token string) (DraftState, error) {
	_, st, err := d.loadState(ctx, token)
	return st, err
}

// Advance appends answer (nil for a pure peek) to token's event log,
// resetting its sliding TTL, and returns the updated state. answer, when
// given, must be valid (DraftFieldAnswer.valid) or this is
// ErrInvalidDraftAnswer. An expired or unknown token is ErrNotFound,
// indistinguishable from each other; an expired one is deleted outright
// as part of answering (no trace is kept).
func (d *Drafts) Advance(ctx context.Context, token string, answer *DraftFieldAnswer) (DraftState, error) {
	if answer != nil && !answer.valid() {
		return DraftState{}, ErrInvalidDraftAnswer
	}
	events, _, err := d.loadState(ctx, token)
	if err != nil {
		return DraftState{}, err
	}
	ev := DraftEvent{Token: token, Answer: answer, CreatedAt: d.now()}
	if err := d.store.AppendDraftEvent(ctx, ev); err != nil {
		return DraftState{}, err
	}
	return foldDraft(append(events, ev), d.ttl), nil
}

// Close ends token immediately: no trace is kept from this point on,
// exactly as if it had expired -- an explicit abandon. An already-closed
// or already-expired token is ErrNotFound, indistinguishable from one
// that never existed, the same as every other operation on one.
func (d *Drafts) Close(ctx context.Context, token string) error {
	if _, _, err := d.loadState(ctx, token); err != nil {
		return err
	}
	return d.store.DeleteDraft(ctx, token)
}

// loadState reads token's events, folds them, and applies expiry: an
// expired token is deleted and reported as ErrNotFound, indistinguishable
// from one that never existed. It returns the raw events too, so Advance
// can re-fold after appending without a second store read.
func (d *Drafts) loadState(ctx context.Context, token string) ([]DraftEvent, DraftState, error) {
	events, err := d.store.DraftEvents(ctx, token)
	if err != nil {
		return nil, DraftState{}, err
	}
	st := foldDraft(events, d.ttl)
	if !st.ExpiresAt.After(d.now()) {
		_ = d.store.DeleteDraft(ctx, token)
		return nil, DraftState{}, fmt.Errorf("draft %q: %w", token, ErrNotFound)
	}
	return events, st, nil
}

// foldDraft derives a DraftState from a token's complete event log,
// oldest first: Key/Snapshot/Proposer/Rationale from the creating event,
// Answers folded per field (the latest event naming a field decides its
// current answer), ExpiresAt from the latest event's time.
func foldDraft(events []DraftEvent, ttl time.Duration) DraftState {
	st := DraftState{Answers: map[DraftField]DraftFieldAnswer{}}
	for _, ev := range events {
		if ev.Key != "" {
			st.Token, st.Key, st.Snapshot, st.Proposer, st.Rationale = ev.Token, ev.Key, ev.Snapshot, ev.Proposer, ev.Rationale
			st.CreatedAt = ev.CreatedAt
		}
		if ev.Answer != nil {
			st.Answers[ev.Answer.Field] = *ev.Answer
		}
		st.ExpiresAt = ev.CreatedAt.Add(ttl)
	}
	return st
}
