package memory

import (
	"context"
	"errors"
	"slices"
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

// AllWalkthroughEvents implements WalkthroughStore, mirroring the real
// store's "ORDER BY token, id" contract: every token's events contiguous,
// oldest first within it, tokens in sorted order (deterministic for
// tests; the real store's order is whatever SQLite's index gives, not
// necessarily sorted, but List folds per token regardless of order
// across tokens).
func (f *fakeWalkthroughStore) AllWalkthroughEvents(_ context.Context) ([]WalkthroughEvent, error) {
	tokens := make([]string, 0, len(f.events))
	for t := range f.events {
		tokens = append(tokens, t)
	}
	slices.Sort(tokens)
	var all []WalkthroughEvent
	for _, t := range tokens {
		all = append(all, f.events[t]...)
	}
	return all, nil
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

func TestDynamicWalkthroughChildrenFoldInAttachmentOrder(t *testing.T) {
	store := newFakeWalkthroughStore()
	now := time.Now()
	w := &Walkthroughs{store: store, now: func() time.Time { return now }, ttl: time.Hour}
	ctx := context.Background()

	st, err := w.StartWithDynamicChildren(ctx, "root", "snapshot-1", "github.com/a/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !st.AllowDynamicChildren || len(st.DynamicChildren) != 0 {
		t.Fatalf("initial dynamic state = %+v", st)
	}

	st, err = w.AttachChild(ctx, st.Token, "child.first")
	if err != nil {
		t.Fatal(err)
	}
	st, err = w.Advance(ctx, st.Token, &WalkthroughOutcome{Key: "child.first", Status: WalkthroughDone}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err = w.AttachChild(ctx, st.Token, "child.second")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"child.first", "child.second"}; !slices.Equal(st.DynamicChildren, want) {
		t.Fatalf("dynamic children = %v, want attachment order %v", st.DynamicChildren, want)
	}
	if st.Statuses["child.first"] != WalkthroughDone {
		t.Fatalf("late attachment lost the earlier child outcome: %v", st.Statuses)
	}
	if _, err := w.AttachChild(ctx, st.Token, "child.first"); !errors.Is(err, ErrDynamicChildExists) {
		t.Fatalf("duplicate child error = %v, want ErrDynamicChildExists", err)
	}
}

func TestStaticWalkthroughRejectsDynamicChild(t *testing.T) {
	w := NewWalkthroughs(newFakeWalkthroughStore())
	st, err := w.Start(context.Background(), "root", "snapshot-1", "github.com/a/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AttachChild(context.Background(), st.Token, "child"); !errors.Is(err, ErrDynamicChildrenDisabled) {
		t.Fatalf("attach to static walkthrough error = %v, want ErrDynamicChildrenDisabled", err)
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

// Walkthrough completion semantics and an explicit close (ADR-0055).

func TestWalkthroughsCloseEndsItImmediately(t *testing.T) {
	store := newFakeWalkthroughStore()
	w := &Walkthroughs{store: store, now: time.Now, ttl: time.Hour}
	ctx := context.Background()
	st, err := w.Start(ctx, "goal.key", "snap1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx, st.Token); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.events[st.Token]; ok {
		t.Fatalf("Close must leave no trace: events = %v", store.events[st.Token])
	}
	if len(store.deleted) != 1 || store.deleted[0] != st.Token {
		t.Fatalf("Close must delete the token's events: deleted = %v", store.deleted)
	}
	// A closed token behaves exactly like an expired one from then on.
	if _, err := w.State(ctx, st.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a closed token must be ErrNotFound on any later call, got %v", err)
	}
}

func TestWalkthroughsCloseAlreadyClosedOrExpiredIsNotFound(t *testing.T) {
	store := newFakeWalkthroughStore()
	now := time.Now()
	w := &Walkthroughs{store: store, now: func() time.Time { return now }, ttl: time.Minute}
	ctx := context.Background()

	// Already closed.
	closed, err := w.Start(ctx, "goal.key", "snap1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx, closed.Token); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx, closed.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closing an already-closed token must be ErrNotFound, got %v", err)
	}

	// Already expired.
	expired, err := w.Start(ctx, "goal.key", "snap1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute) // past the 1-minute TTL
	if err := w.Close(ctx, expired.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closing an expired token must be ErrNotFound, got %v", err)
	}

	// Never existed.
	if err := w.Close(ctx, "never-created"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closing an unknown token must be ErrNotFound, got %v", err)
	}
}

// blocked, reason, operator Status and List (ADR-0056, issue #163).

func TestWalkthroughsAdvanceBlockedWithReason(t *testing.T) {
	store := newFakeWalkthroughStore()
	w := &Walkthroughs{store: store, now: time.Now, ttl: time.Hour}
	ctx := context.Background()
	st, err := w.Start(ctx, "goal.key", "snap1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err = w.Advance(ctx, st.Token, &WalkthroughOutcome{Key: "step.a", Status: WalkthroughBlocked, Reason: "check.vet's required tool is not installed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Statuses["step.a"] != WalkthroughBlocked {
		t.Fatalf("statuses = %v", st.Statuses)
	}
	if st.Reasons["step.a"] != "check.vet's required tool is not installed" {
		t.Fatalf("reasons = %v", st.Reasons)
	}
	// A later done clears it, folded exactly like escalate already is.
	st, err = w.Advance(ctx, st.Token, &WalkthroughOutcome{Key: "step.a", Status: WalkthroughDone}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Statuses["step.a"] != WalkthroughDone {
		t.Fatalf("a later done must override an earlier blocked: %v", st.Statuses)
	}
}

func TestWalkthroughStateOperatorStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses map[string]WalkthroughOutcomeStatus
		want     string
	}{
		{"nothing reported", nil, WalkthroughOperatorActive},
		{"every leaf done", map[string]WalkthroughOutcomeStatus{"a": WalkthroughDone, "b": WalkthroughSkipped}, WalkthroughOperatorActive},
		{"one escalated", map[string]WalkthroughOutcomeStatus{"a": WalkthroughDone, "b": WalkthroughEscalate}, WalkthroughOperatorEscalate},
		{"one blocked", map[string]WalkthroughOutcomeStatus{"a": WalkthroughEscalate, "b": WalkthroughBlocked}, WalkthroughOperatorBlocked},
	} {
		st := WalkthroughState{Statuses: tc.statuses}
		if got := st.Status(); got != tc.want {
			t.Errorf("%s: Status() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWalkthroughsListFiltersAndPaginates(t *testing.T) {
	store := newFakeWalkthroughStore()
	now := time.Now()
	w := &Walkthroughs{store: store, now: func() time.Time { return now }, ttl: time.Hour}
	ctx := context.Background()

	a, err := w.Start(ctx, "goal.a", "snap1", "github.com/a/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	b, err := w.Start(ctx, "goal.b", "snap1", "github.com/other", nil)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := w.Advance(ctx, a.Token, &WalkthroughOutcome{Key: "step.a", Status: WalkthroughBlocked}, nil); err != nil {
		t.Fatal(err)
	}

	all, next, err := w.List(ctx, WalkthroughFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || next != "" || all[0].Token != a.Token || all[1].Token != b.Token {
		t.Fatalf("list = %+v (want a, then b, oldest first)", all)
	}

	byRepo, _, err := w.List(ctx, WalkthroughFilter{Repository: "github.com/other"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRepo) != 1 || byRepo[0].Token != b.Token {
		t.Fatalf("by repository = %+v", byRepo)
	}

	byGoal, _, err := w.List(ctx, WalkthroughFilter{Goal: "goal.a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byGoal) != 1 || byGoal[0].Token != a.Token {
		t.Fatalf("by goal = %+v", byGoal)
	}

	byStatus, _, err := w.List(ctx, WalkthroughFilter{Status: WalkthroughOperatorBlocked})
	if err != nil {
		t.Fatal(err)
	}
	if len(byStatus) != 1 || byStatus[0].Token != a.Token {
		t.Fatalf("by status = %+v", byStatus)
	}

	page, cursor, err := w.List(ctx, WalkthroughFilter{Page: Page{Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Token != a.Token || cursor == "" {
		t.Fatalf("first page = %+v, cursor %q", page, cursor)
	}
	page2, cursor2, err := w.List(ctx, WalkthroughFilter{Page: Page{Limit: 1, After: cursor}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].Token != b.Token || cursor2 != "" {
		t.Fatalf("second page = %+v, cursor %q", page2, cursor2)
	}
}

func TestWalkthroughsListSweepsExpiredOnes(t *testing.T) {
	store := newFakeWalkthroughStore()
	now := time.Now()
	w := &Walkthroughs{store: store, now: func() time.Time { return now }, ttl: time.Minute}
	ctx := context.Background()
	st, err := w.Start(ctx, "goal.key", "snap1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute) // past the 1-minute TTL

	list, _, err := w.List(ctx, WalkthroughFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("an expired walkthrough must not be listed: %+v", list)
	}
	if len(store.deleted) != 1 || store.deleted[0] != st.Token {
		t.Fatalf("List must sweep an expired walkthrough it finds: deleted = %v", store.deleted)
	}
}
