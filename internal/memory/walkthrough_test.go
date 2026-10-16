package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFoldWalkthrough(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	events := []WalkthroughEvent{
		{Token: "t1", Goal: "g", Snapshot: "s1", Repository: "github.com/a/b", CreatedAt: t0},
		{Token: "t1", Outcome: &WalkthroughOutcome{Key: "x", Status: WalkthroughEscalate}, CreatedAt: t0.Add(time.Minute)},
		{Token: "t1", Context: map[string]string{"concurrency": "true"}, CreatedAt: t0.Add(2 * time.Minute)},
		{Token: "t1", Outcome: &WalkthroughOutcome{Key: "x", Status: WalkthroughDone}, CreatedAt: t0.Add(3 * time.Minute)},
	}
	st := foldWalkthrough(events, time.Hour)
	if st.Token != "t1" || st.Goal != "g" || st.Snapshot != "s1" || st.Repository != "github.com/a/b" {
		t.Fatalf("identity = %+v", st)
	}
	// A later report for the same key overrides an earlier one: escalate,
	// then done, folds to done, not a union of both.
	if st.Statuses["x"] != WalkthroughDone {
		t.Fatalf("x = %q, want done (the later report must override escalate)", st.Statuses["x"])
	}
	if st.Context["concurrency"] != "true" {
		t.Fatalf("context = %v", st.Context)
	}
	if want := t0.Add(3 * time.Minute).Add(time.Hour); !st.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v (the latest event's time plus the TTL)", st.ExpiresAt, want)
	}
}

func TestFoldWalkthroughContextAccumulates(t *testing.T) {
	t0 := time.Now()
	events := []WalkthroughEvent{
		{Token: "t1", Goal: "g", CreatedAt: t0, Context: map[string]string{"a": "1"}},
		{Token: "t1", CreatedAt: t0.Add(time.Minute), Context: map[string]string{"b": "2"}},
		{Token: "t1", CreatedAt: t0.Add(2 * time.Minute), Context: map[string]string{"a": "3"}},
	}
	st := foldWalkthrough(events, time.Hour)
	if st.Context["a"] != "3" || st.Context["b"] != "2" {
		t.Fatalf("context = %v, want a=3 (overridden), b=2 (accumulated)", st.Context)
	}
}

// fakeWalkthroughStore is an in-memory double of WalkthroughStore, used
// only to test Walkthroughs' own orchestration (fold selection, TTL
// expiry, deletion) with a controllable clock; real persistence is
// exercised separately, against a real SQLite file, in
// internal/storage/sqlite.
type fakeWalkthroughStore struct {
	events  map[string][]WalkthroughEvent
	deleted []string
}

func newFakeWalkthroughStore() *fakeWalkthroughStore {
	return &fakeWalkthroughStore{events: map[string][]WalkthroughEvent{}}
}

func (f *fakeWalkthroughStore) AppendWalkthroughEvent(_ context.Context, ev WalkthroughEvent) error {
	f.events[ev.Token] = append(f.events[ev.Token], ev)
	return nil
}

func (f *fakeWalkthroughStore) WalkthroughEvents(_ context.Context, token string) ([]WalkthroughEvent, error) {
	events, ok := f.events[token]
	if !ok || len(events) == 0 {
		return nil, ErrNotFound
	}
	return events, nil
}

func (f *fakeWalkthroughStore) DeleteWalkthrough(_ context.Context, token string) error {
	delete(f.events, token)
	f.deleted = append(f.deleted, token)
	return nil
}

func TestWalkthroughsStartAndAdvance(t *testing.T) {
	store := newFakeWalkthroughStore()
	now := time.Now()
	w := &Walkthroughs{store: store, now: func() time.Time { return now }, ttl: time.Hour}
	ctx := context.Background()

	st, err := w.Start(ctx, "goal.key", "snap1", "github.com/a/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Goal != "goal.key" || st.Snapshot != "snap1" || len(st.Statuses) != 0 {
		t.Fatalf("start state = %+v", st)
	}

	// A peek (no outcome) still resets the TTL.
	now = now.Add(30 * time.Minute)
	st, err = w.Advance(ctx, st.Token, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Hour); !st.ExpiresAt.Equal(want) {
		t.Fatalf("a peek must still reset the TTL: expires_at = %v, want %v", st.ExpiresAt, want)
	}

	// escalate freezes, then a later done for the same key overrides it.
	st, err = w.Advance(ctx, st.Token, &WalkthroughOutcome{Key: "step.a", Status: WalkthroughEscalate}, nil)
	if err != nil || st.Statuses["step.a"] != WalkthroughEscalate {
		t.Fatalf("escalate = %+v, %v", st, err)
	}
	st, err = w.Advance(ctx, st.Token, &WalkthroughOutcome{Key: "step.a", Status: WalkthroughDone}, nil)
	if err != nil || st.Statuses["step.a"] != WalkthroughDone {
		t.Fatalf("done must override the earlier escalate: %+v, %v", st, err)
	}
}

func TestWalkthroughsExpiredIsNotFoundAndDeleted(t *testing.T) {
	store := newFakeWalkthroughStore()
	now := time.Now()
	w := &Walkthroughs{store: store, now: func() time.Time { return now }, ttl: time.Minute}
	ctx := context.Background()

	st, err := w.Start(ctx, "goal.key", "snap1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute) // past the 1-minute TTL
	_, err = w.Advance(ctx, st.Token, nil, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired token must be ErrNotFound, got %v", err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != st.Token {
		t.Fatalf("an expired token's events must be deleted (no trace kept): deleted = %v", store.deleted)
	}
	if _, ok := store.events[st.Token]; ok {
		t.Fatalf("the token's events must no longer exist after expiry")
	}
}

func TestWalkthroughsAdvanceRejectsInvalidOutcome(t *testing.T) {
	store := newFakeWalkthroughStore()
	w := &Walkthroughs{store: store, now: time.Now, ttl: time.Hour}
	ctx := context.Background()
	st, err := w.Start(ctx, "goal.key", "snap1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []*WalkthroughOutcome{
		{Key: "", Status: WalkthroughDone},
		{Key: "step.a", Status: "bogus"},
	} {
		if _, err := w.Advance(ctx, st.Token, outcome, nil); !errors.Is(err, ErrInvalidOutcome) {
			t.Fatalf("outcome %+v: err = %v, want ErrInvalidOutcome", outcome, err)
		}
	}
}

func TestWalkthroughsStateDoesNotAppendOrResetTTL(t *testing.T) {
	store := newFakeWalkthroughStore()
	now := time.Now()
	w := &Walkthroughs{store: store, now: func() time.Time { return now }, ttl: time.Hour}
	ctx := context.Background()
	st, err := w.Start(ctx, "goal.key", "snap1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	peeked, err := w.State(ctx, st.Token)
	if err != nil {
		t.Fatal(err)
	}
	if peeked.Goal != "goal.key" {
		t.Fatalf("state = %+v", peeked)
	}
	if len(store.events[st.Token]) != 1 {
		t.Fatalf("State must not append an event: events = %v", store.events[st.Token])
	}
	if !peeked.ExpiresAt.Equal(st.ExpiresAt) {
		t.Fatalf("State must not reset the TTL: got %v, want %v", peeked.ExpiresAt, st.ExpiresAt)
	}
}

func TestWalkthroughsUnknownTokenIsNotFound(t *testing.T) {
	store := newFakeWalkthroughStore()
	w := &Walkthroughs{store: store, now: time.Now, ttl: time.Hour}
	if _, err := w.Advance(context.Background(), "never-created", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown token must be ErrNotFound, got %v", err)
	}
}
