package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFoldDraft(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	events := []DraftEvent{
		{Token: "d1", Key: "proc.key", Snapshot: "s1", Proposer: "test.agent", Rationale: "r", CreatedAt: t0},
		{Token: "d1", Answer: &DraftFieldAnswer{Field: DraftFieldPostconditions, Values: []string{"a"}}, CreatedAt: t0.Add(time.Minute)},
		{Token: "d1", Answer: &DraftFieldAnswer{Field: DraftFieldPostconditions, Values: []string{"b"}}, CreatedAt: t0.Add(2 * time.Minute)},
	}
	st := foldDraft(events, time.Hour)
	if st.Token != "d1" || st.Key != "proc.key" || st.Snapshot != "s1" || st.Proposer != "test.agent" || st.Rationale != "r" {
		t.Fatalf("identity = %+v", st)
	}
	// A later answer for the same field overrides an earlier one: b, not
	// a union of both.
	ans := st.Answers[DraftFieldPostconditions]
	if len(ans.Values) != 1 || ans.Values[0] != "b" {
		t.Fatalf("postconditions = %+v, want only b (the later answer must override the earlier one)", ans)
	}
	if want := t0.Add(2 * time.Minute).Add(time.Hour); !st.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", st.ExpiresAt, want)
	}
}

// fakeDraftStore is an in-memory double of DraftStore, used only to test
// Drafts' own orchestration (fold selection, TTL expiry, deletion) with
// a controllable clock; real persistence is exercised separately,
// against a real SQLite file, in internal/storage/sqlite.
type fakeDraftStore struct {
	events  map[string][]DraftEvent
	deleted []string
}

func newFakeDraftStore() *fakeDraftStore { return &fakeDraftStore{events: map[string][]DraftEvent{}} }

func (f *fakeDraftStore) AppendDraftEvent(_ context.Context, ev DraftEvent) error {
	f.events[ev.Token] = append(f.events[ev.Token], ev)
	return nil
}

func (f *fakeDraftStore) DraftEvents(_ context.Context, token string) ([]DraftEvent, error) {
	events, ok := f.events[token]
	if !ok || len(events) == 0 {
		return nil, ErrNotFound
	}
	return events, nil
}

func (f *fakeDraftStore) DeleteDraft(_ context.Context, token string) error {
	delete(f.events, token)
	f.deleted = append(f.deleted, token)
	return nil
}

func TestDraftsStartAndAdvance(t *testing.T) {
	store := newFakeDraftStore()
	now := time.Now()
	d := &Drafts{store: store, now: func() time.Time { return now }, ttl: time.Hour}
	ctx := context.Background()

	st, err := d.Start(ctx, "proc.key", "snap1", "test.agent", "r")
	if err != nil {
		t.Fatal(err)
	}
	if st.Key != "proc.key" || st.Snapshot != "snap1" || len(st.Answers) != 0 {
		t.Fatalf("start state = %+v", st)
	}

	// A peek (nil answer) still resets the TTL.
	now = now.Add(30 * time.Minute)
	st, err = d.Advance(ctx, st.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Hour); !st.ExpiresAt.Equal(want) {
		t.Fatalf("a peek must still reset the TTL: expires_at = %v, want %v", st.ExpiresAt, want)
	}

	st, err = d.Advance(ctx, st.Token, &DraftFieldAnswer{Field: DraftFieldPreconditions, Values: []string{"concept.a"}})
	if err != nil {
		t.Fatal(err)
	}
	if ans := st.Answers[DraftFieldPreconditions]; len(ans.Values) != 1 || ans.Values[0] != "concept.a" {
		t.Fatalf("answers = %+v", st.Answers)
	}

	st, err = d.Advance(ctx, st.Token, &DraftFieldAnswer{Field: DraftFieldInvariants, ConfirmedAbsent: true, Reason: "genuinely none"})
	if err != nil {
		t.Fatal(err)
	}
	if ans := st.Answers[DraftFieldInvariants]; !ans.ConfirmedAbsent || ans.Reason != "genuinely none" {
		t.Fatalf("decline = %+v", ans)
	}
}

func TestDraftsAdvanceRejectsInvalidAnswer(t *testing.T) {
	store := newFakeDraftStore()
	d := &Drafts{store: store, now: time.Now, ttl: time.Hour}
	ctx := context.Background()
	st, err := d.Start(ctx, "proc.key", "snap1", "test.agent", "r")
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []*DraftFieldAnswer{
		{Field: "bogus_field", Values: []string{"x"}},
		{Field: DraftFieldPreconditions},                                               // neither Values nor ConfirmedAbsent
		{Field: DraftFieldPreconditions, Values: []string{"x"}, ConfirmedAbsent: true}, // both
	} {
		if _, err := d.Advance(ctx, st.Token, answer); !errors.Is(err, ErrInvalidDraftAnswer) {
			t.Fatalf("answer %+v: err = %v, want ErrInvalidDraftAnswer", answer, err)
		}
	}
}

func TestDraftsExpiredIsNotFoundAndDeleted(t *testing.T) {
	store := newFakeDraftStore()
	now := time.Now()
	d := &Drafts{store: store, now: func() time.Time { return now }, ttl: time.Minute}
	ctx := context.Background()
	st, err := d.Start(ctx, "proc.key", "snap1", "test.agent", "r")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := d.Advance(ctx, st.Token, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired token must be ErrNotFound, got %v", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != st.Token {
		t.Fatalf("an expired token's events must be deleted (no trace kept): deleted = %v", store.deleted)
	}
}

func TestDraftsStateDoesNotAppendOrResetTTL(t *testing.T) {
	store := newFakeDraftStore()
	now := time.Now()
	d := &Drafts{store: store, now: func() time.Time { return now }, ttl: time.Hour}
	ctx := context.Background()
	st, err := d.Start(ctx, "proc.key", "snap1", "test.agent", "r")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	peeked, err := d.State(ctx, st.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.events[st.Token]) != 1 {
		t.Fatalf("State must not append an event: events = %v", store.events[st.Token])
	}
	if !peeked.ExpiresAt.Equal(st.ExpiresAt) {
		t.Fatalf("State must not reset the TTL: got %v, want %v", peeked.ExpiresAt, st.ExpiresAt)
	}
}

func TestDraftsUnknownTokenIsNotFound(t *testing.T) {
	store := newFakeDraftStore()
	d := &Drafts{store: store, now: time.Now, ttl: time.Hour}
	if _, err := d.Advance(context.Background(), "never-created", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown token must be ErrNotFound, got %v", err)
	}
}

func TestDraftsCloseEndsItImmediately(t *testing.T) {
	store := newFakeDraftStore()
	d := &Drafts{store: store, now: time.Now, ttl: time.Hour}
	ctx := context.Background()
	st, err := d.Start(ctx, "proc.key", "snap1", "test.agent", "r")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(ctx, st.Token); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.events[st.Token]; ok {
		t.Fatalf("Close must leave no trace: events = %v", store.events[st.Token])
	}
	if _, err := d.State(ctx, st.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a closed token must be ErrNotFound on any later call, got %v", err)
	}
}
