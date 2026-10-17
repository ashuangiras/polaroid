package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

func draftCreate(token, key, snapshot string, n int) memory.DraftEvent {
	return memory.DraftEvent{Token: token, Key: key, Snapshot: snapshot, Proposer: "test.agent", Rationale: "r", CreatedAt: created.Add(time.Duration(n) * time.Minute)}
}

func TestDraftEventsRoundTrip(t *testing.T) {
	s := openStore(t, dbPath(t))
	ctx := context.Background()
	token := "draft-1"

	create := draftCreate(token, "proc.key", "snap1", 0)
	if err := s.AppendDraftEvent(ctx, create); err != nil {
		t.Fatal(err)
	}
	answer := memory.DraftEvent{
		Token: token,
		Answer: &memory.DraftFieldAnswer{Field: memory.DraftFieldPostconditions, Values: []string{"concept.a"},
			Citations: []memory.DraftCitation{{Source: "proc.other", Quote: "concept.a"}}},
		CreatedAt: created.Add(time.Minute),
	}
	if err := s.AppendDraftEvent(ctx, answer); err != nil {
		t.Fatal(err)
	}
	decline := memory.DraftEvent{
		Token:     token,
		Answer:    &memory.DraftFieldAnswer{Field: memory.DraftFieldInvariants, ConfirmedAbsent: true, Reason: "genuinely none"},
		CreatedAt: created.Add(2 * time.Minute),
	}
	if err := s.AppendDraftEvent(ctx, decline); err != nil {
		t.Fatal(err)
	}

	got, err := s.DraftEvents(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(got))
	}
	if got[0].Key != "proc.key" || got[0].Snapshot != "snap1" || got[0].Proposer != "test.agent" || got[0].Rationale != "r" {
		t.Fatalf("creating event = %+v", got[0])
	}
	if got[0].Answer != nil {
		t.Fatalf("creating event has an answer: %+v", got[0].Answer)
	}
	if got[1].Key != "" || got[1].Snapshot != "" {
		t.Fatalf("later event carries identity it should not: %+v", got[1])
	}
	if got[1].Answer == nil || got[1].Answer.Field != memory.DraftFieldPostconditions || len(got[1].Answer.Values) != 1 ||
		got[1].Answer.Values[0] != "concept.a" || got[1].Answer.ConfirmedAbsent {
		t.Fatalf("answer = %+v", got[1].Answer)
	}
	if len(got[1].Answer.Citations) != 1 || got[1].Answer.Citations[0].Source != "proc.other" || got[1].Answer.Citations[0].Quote != "concept.a" {
		t.Fatalf("citations = %+v", got[1].Answer.Citations)
	}
	if got[2].Answer == nil || got[2].Answer.Field != memory.DraftFieldInvariants || !got[2].Answer.ConfirmedAbsent || got[2].Answer.Reason != "genuinely none" {
		t.Fatalf("decline answer = %+v", got[2].Answer)
	}
	if !got[0].CreatedAt.Equal(create.CreatedAt) || !got[1].CreatedAt.Equal(answer.CreatedAt) || !got[2].CreatedAt.Equal(decline.CreatedAt) {
		t.Fatalf("created_at round-trip: %v, %v, %v", got[0].CreatedAt, got[1].CreatedAt, got[2].CreatedAt)
	}
}

func TestDraftEventsUnknownTokenIsNotFound(t *testing.T) {
	s := openStore(t, dbPath(t))
	if _, err := s.DraftEvents(context.Background(), "never-created"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDraftEventsSeparatesTokens(t *testing.T) {
	s := openStore(t, dbPath(t))
	ctx := context.Background()
	if err := s.AppendDraftEvent(ctx, draftCreate("draft-a", "proc.a", "snap1", 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendDraftEvent(ctx, draftCreate("draft-b", "proc.b", "snap1", 0)); err != nil {
		t.Fatal(err)
	}
	a, err := s.DraftEvents(ctx, "draft-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].Key != "proc.a" {
		t.Fatalf("draft-a events = %+v", a)
	}
}

func TestDeleteDraftRemovesEveryTrace(t *testing.T) {
	s := openStore(t, dbPath(t))
	ctx := context.Background()
	token := "draft-delete"
	if err := s.AppendDraftEvent(ctx, draftCreate(token, "proc.key", "snap1", 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDraft(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DraftEvents(ctx, token); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound after delete", err)
	}
}
