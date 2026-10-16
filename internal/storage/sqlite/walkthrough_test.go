package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

func walkthroughEvent(token, goal, snapshot string, n int) memory.WalkthroughEvent {
	return memory.WalkthroughEvent{Token: token, Goal: goal, Snapshot: snapshot, Repository: "github.com/a/b", CreatedAt: created.Add(time.Duration(n) * time.Minute)}
}

func TestWalkthroughEventsRoundTrip(t *testing.T) {
	s := openStore(t, dbPath(t))
	ctx := context.Background()
	token := "wt-1"

	create := walkthroughEvent(token, "goal.key", "snap1", 0)
	if err := s.AppendWalkthroughEvent(ctx, create); err != nil {
		t.Fatal(err)
	}
	outcome := memory.WalkthroughEvent{
		Token:     token,
		Outcome:   &memory.WalkthroughOutcome{Key: "step.a", Status: memory.WalkthroughEscalate, Note: "need a human"},
		Context:   map[string]string{"concurrency": "true"},
		CreatedAt: created.Add(time.Minute),
	}
	if err := s.AppendWalkthroughEvent(ctx, outcome); err != nil {
		t.Fatal(err)
	}

	got, err := s.WalkthroughEvents(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(got))
	}
	if got[0].Goal != "goal.key" || got[0].Snapshot != "snap1" || got[0].Repository != "github.com/a/b" {
		t.Fatalf("creating event = %+v", got[0])
	}
	if got[0].Outcome != nil {
		t.Fatalf("creating event has an outcome: %+v", got[0].Outcome)
	}
	if got[1].Goal != "" || got[1].Snapshot != "" {
		t.Fatalf("later event carries identity it should not: %+v", got[1])
	}
	if got[1].Outcome == nil || got[1].Outcome.Key != "step.a" || got[1].Outcome.Status != memory.WalkthroughEscalate || got[1].Outcome.Note != "need a human" {
		t.Fatalf("outcome = %+v", got[1].Outcome)
	}
	if got[1].Context["concurrency"] != "true" {
		t.Fatalf("context = %v", got[1].Context)
	}
	if !got[0].CreatedAt.Equal(create.CreatedAt) || !got[1].CreatedAt.Equal(outcome.CreatedAt) {
		t.Fatalf("created_at round-trip: %v, %v", got[0].CreatedAt, got[1].CreatedAt)
	}
}

func TestWalkthroughEventsUnknownTokenIsNotFound(t *testing.T) {
	s := openStore(t, dbPath(t))
	if _, err := s.WalkthroughEvents(context.Background(), "never-created"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestWalkthroughEventsSeparatesTokens(t *testing.T) {
	s := openStore(t, dbPath(t))
	ctx := context.Background()
	if err := s.AppendWalkthroughEvent(ctx, walkthroughEvent("wt-1", "goal.a", "snap1", 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendWalkthroughEvent(ctx, walkthroughEvent("wt-2", "goal.b", "snap1", 1)); err != nil {
		t.Fatal(err)
	}
	got, err := s.WalkthroughEvents(ctx, "wt-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Goal != "goal.a" {
		t.Fatalf("wt-1 events = %+v", got)
	}
}

func TestDeleteWalkthroughRemovesAllTrace(t *testing.T) {
	s := openStore(t, dbPath(t))
	ctx := context.Background()
	token := "wt-1"
	if err := s.AppendWalkthroughEvent(ctx, walkthroughEvent(token, "goal.key", "snap1", 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendWalkthroughEvent(ctx, memory.WalkthroughEvent{Token: token, Outcome: &memory.WalkthroughOutcome{Key: "step.a", Status: memory.WalkthroughDone}, CreatedAt: created.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteWalkthrough(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WalkthroughEvents(ctx, token); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("after delete, err = %v, want ErrNotFound", err)
	}
}

func TestDeleteWalkthroughUnknownTokenIsNoOp(t *testing.T) {
	s := openStore(t, dbPath(t))
	if err := s.DeleteWalkthrough(context.Background(), "never-created"); err != nil {
		t.Fatalf("deleting an unknown token must not error: %v", err)
	}
}

// Walkthroughs' own fold/TTL behavior, exercised against a real store,
// confirms memory.Walkthroughs and *sqlite.Store work together end to
// end; internal/memory's own tests cover the fold and TTL logic itself
// with a controllable clock.
func TestWalkthroughsAgainstRealStore(t *testing.T) {
	s := openStore(t, dbPath(t))
	w := memory.NewWalkthroughs(s)
	ctx := context.Background()

	st, err := w.Start(ctx, "goal.key", "snap1", "github.com/a/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err = w.Advance(ctx, st.Token, &memory.WalkthroughOutcome{Key: "step.a", Status: memory.WalkthroughDone}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Statuses["step.a"] != memory.WalkthroughDone {
		t.Fatalf("statuses = %v", st.Statuses)
	}

	// Close (ADR-0055): leaves no trace in the real table.
	if err := w.Close(ctx, st.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WalkthroughEvents(ctx, st.Token); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("Close must leave no trace in storage, got %v", err)
	}
	if _, err := w.State(ctx, st.Token); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("a closed token must be ErrNotFound on any later call, got %v", err)
	}
}
