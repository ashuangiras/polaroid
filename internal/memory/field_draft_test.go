package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeFieldDraftStore struct {
	events map[string][]FieldDraftEvent
}

func newFakeFieldDraftStore() *fakeFieldDraftStore {
	return &fakeFieldDraftStore{events: map[string][]FieldDraftEvent{}}
}

func (s *fakeFieldDraftStore) AppendFieldDraftEvent(_ context.Context, event FieldDraftEvent, expectedEvents int) error {
	if len(s.events[event.Token]) != expectedEvents {
		if len(s.events[event.Token]) == 0 {
			return ErrNotFound
		}
		return ErrFieldDraftConflict
	}
	s.events[event.Token] = append(s.events[event.Token], event)
	return nil
}

func (s *fakeFieldDraftStore) FieldDraftEvents(_ context.Context, token string) ([]FieldDraftEvent, error) {
	events := s.events[token]
	if len(events) == 0 {
		return nil, ErrNotFound
	}
	return events, nil
}

func (s *fakeFieldDraftStore) DeleteFieldDraft(_ context.Context, token string) error {
	delete(s.events, token)
	return nil
}

func TestFieldDraftsFoldLatestDecisionPerField(t *testing.T) {
	store := newFakeFieldDraftStore()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	drafts := &FieldDrafts{store: store, now: func() time.Time { return now }, ttl: time.Hour}
	ctx := context.Background()
	state, err := drafts.Start(ctx, "sample", "", "Procedure", true, "test.agent", "review all fields")
	if err != nil {
		t.Fatal(err)
	}
	if state.Key != "sample" || state.RecordType != "Procedure" || !state.New || len(state.Answers) != 0 {
		t.Fatalf("start state = %+v", state)
	}
	first := &FieldDraftAnswer{Entity: "record", Field: "invariants", Operation: "add", Value: []byte(`["concept.a"]`)}
	if _, err := drafts.Advance(ctx, state.Token, first); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	second := &FieldDraftAnswer{Entity: "record", Field: "invariants", Operation: "replace", Value: []byte(`["concept.b"]`)}
	state, err = drafts.Advance(ctx, state.Token, second)
	if err != nil {
		t.Fatal(err)
	}
	answer := state.Answers["record/invariants"]
	if answer.Operation != "replace" || string(answer.Value) != `["concept.b"]` {
		t.Fatalf("folded answer = %+v", answer)
	}
	if want := now.Add(time.Hour); !state.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", state.ExpiresAt, want)
	}
}

func TestFieldDraftsPeekCloseAndExpiry(t *testing.T) {
	store := newFakeFieldDraftStore()
	now := time.Now()
	drafts := &FieldDrafts{store: store, now: func() time.Time { return now }, ttl: time.Hour}
	ctx := context.Background()
	state, err := drafts.Start(ctx, "sample", "snapshot", "Concept", false, "test.agent", "refresh")
	if err != nil {
		t.Fatal(err)
	}
	peek, err := drafts.Advance(ctx, state.Token, nil)
	if err != nil || peek.Token != state.Token || len(peek.Answers) != 0 {
		t.Fatalf("peek = %+v, %v", peek, err)
	}
	if err := drafts.Close(ctx, state.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := drafts.State(ctx, state.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed state = %v, want ErrNotFound", err)
	}

	expiring, err := drafts.Start(ctx, "other", "snapshot", "Concept", true, "test.agent", "new")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := drafts.State(ctx, expiring.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired state = %v, want ErrNotFound", err)
	}
	if _, ok := store.events[expiring.Token]; ok {
		t.Fatal("expired draft left event rows behind")
	}
}

func TestFieldDraftsRejectMalformedAnswer(t *testing.T) {
	drafts := NewFieldDrafts(newFakeFieldDraftStore())
	if _, err := drafts.Advance(context.Background(), "missing", &FieldDraftAnswer{Field: "x", Operation: "bogus"}); !errors.Is(err, ErrInvalidFieldDraftAnswer) {
		t.Fatalf("err = %v, want invalid field draft answer", err)
	}
}
