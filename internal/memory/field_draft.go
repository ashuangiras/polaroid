package memory

import (
	"context"
	"errors"
	"slices"
	"time"
	"uuid"
)

var (
	ErrInvalidFieldDraftAnswer = errors.New("invalid field-review draft answer")
	ErrFieldDraftConflict      = errors.New("field-review draft changed during advance")
)

type FieldDraftCitation struct {
	Source string
	Quote  string
}

// FieldDraftAnswer is a generic decision for one named entity field.
// Knowledge-specific field names and values are interpreted above this
// package, in internal/knowledgeops.
type FieldDraftAnswer struct {
	Entity    string
	Field     string
	Operation string
	Value     []byte
	Citations []FieldDraftCitation
}

func (a FieldDraftAnswer) valid() bool {
	return (a.Entity == "procedure" || a.Entity == "record") && a.Field != "" &&
		slices.Contains([]string{"keep", "add", "remove", "replace", "clear"}, a.Operation)
}

type FieldDraftEvent struct {
	Token            string
	Key              string
	Snapshot         string
	WalkthroughToken string
	RecordType       string
	New              bool
	Proposer         string
	Rationale        string
	Answer           *FieldDraftAnswer
	CreatedAt        time.Time
}

type FieldDraftState struct {
	Token            string
	Key              string
	Snapshot         string
	WalkthroughToken string
	RecordType       string
	New              bool
	Proposer         string
	Rationale        string
	Answers          map[string]FieldDraftAnswer
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

type FieldDraftStore interface {
	// AppendFieldDraftEvent appends only if the token currently has
	// expectedEvents, so concurrent advances cannot overwrite one another.
	AppendFieldDraftEvent(context.Context, FieldDraftEvent, int) error
	FieldDraftEvents(context.Context, string) ([]FieldDraftEvent, error)
	DeleteFieldDraft(context.Context, string) error
}

type FieldDrafts struct {
	store FieldDraftStore
	now   func() time.Time
	ttl   time.Duration
}

func NewFieldDrafts(store FieldDraftStore) *FieldDrafts {
	return &FieldDrafts{store: store, now: timestamp, ttl: DraftTTL}
}

func (d *FieldDrafts) Start(ctx context.Context, key, snapshot, recordType string, isNew bool, proposer, rationale string, walkthroughToken ...string) (FieldDraftState, error) {
	var boundToken string
	if len(walkthroughToken) > 0 {
		boundToken = walkthroughToken[0]
	}
	ev := FieldDraftEvent{
		Token: uuid.NewV7().String(), Key: key, Snapshot: snapshot, RecordType: recordType,
		New: isNew, Proposer: proposer, Rationale: rationale, WalkthroughToken: boundToken, CreatedAt: d.now(),
	}
	if err := d.store.AppendFieldDraftEvent(ctx, ev, 0); err != nil {
		return FieldDraftState{}, err
	}
	return foldFieldDraft([]FieldDraftEvent{ev}, d.ttl), nil
}

func (d *FieldDrafts) State(ctx context.Context, token string) (FieldDraftState, error) {
	_, state, err := d.loadState(ctx, token)
	return state, err
}

func (d *FieldDrafts) Advance(ctx context.Context, token string, answer *FieldDraftAnswer) (FieldDraftState, error) {
	if answer != nil && !answer.valid() {
		return FieldDraftState{}, ErrInvalidFieldDraftAnswer
	}
	events, _, err := d.loadState(ctx, token)
	if err != nil {
		return FieldDraftState{}, err
	}
	ev := FieldDraftEvent{Token: token, Answer: cloneFieldDraftAnswer(answer), CreatedAt: d.now()}
	if err := d.store.AppendFieldDraftEvent(ctx, ev, len(events)); err != nil {
		return FieldDraftState{}, err
	}
	return foldFieldDraft(append(events, ev), d.ttl), nil
}

func (d *FieldDrafts) Close(ctx context.Context, token string) error {
	if _, _, err := d.loadState(ctx, token); err != nil {
		return err
	}
	return d.store.DeleteFieldDraft(ctx, token)
}

func (d *FieldDrafts) loadState(ctx context.Context, token string) ([]FieldDraftEvent, FieldDraftState, error) {
	events, err := d.store.FieldDraftEvents(ctx, token)
	if err != nil {
		return nil, FieldDraftState{}, err
	}
	state := foldFieldDraft(events, d.ttl)
	if !d.now().Before(state.ExpiresAt) {
		if err := d.store.DeleteFieldDraft(ctx, token); err != nil {
			return nil, FieldDraftState{}, err
		}
		return nil, FieldDraftState{}, ErrNotFound
	}
	return events, state, nil
}

func foldFieldDraft(events []FieldDraftEvent, ttl time.Duration) FieldDraftState {
	state := FieldDraftState{Answers: map[string]FieldDraftAnswer{}}
	if len(events) == 0 {
		return state
	}
	state.Token, state.CreatedAt = events[0].Token, events[0].CreatedAt
	for _, ev := range events {
		if ev.Key != "" {
			state.Key, state.Snapshot, state.RecordType, state.New = ev.Key, ev.Snapshot, ev.RecordType, ev.New
			state.Proposer, state.Rationale = ev.Proposer, ev.Rationale
			state.WalkthroughToken = ev.WalkthroughToken
		}
		if ev.Answer != nil {
			state.Answers[fieldDraftAnswerKey(ev.Answer.Entity, ev.Answer.Field)] = *cloneFieldDraftAnswer(ev.Answer)
		}
	}
	state.ExpiresAt = events[len(events)-1].CreatedAt.Add(ttl)
	return state
}

func fieldDraftAnswerKey(entity, field string) string {
	return entity + "/" + field
}

func cloneFieldDraftAnswer(answer *FieldDraftAnswer) *FieldDraftAnswer {
	if answer == nil {
		return nil
	}
	out := *answer
	out.Value = append([]byte(nil), answer.Value...)
	out.Citations = slices.Clone(answer.Citations)
	return &out
}
