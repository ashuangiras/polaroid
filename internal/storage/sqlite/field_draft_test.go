package sqlite_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
)

func TestFieldDraftEventsRoundTripAndDelete(t *testing.T) {
	store := openStore(t, dbPath(t))
	ctx := context.Background()
	token := "field-draft-1"
	created := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	start := memory.FieldDraftEvent{
		Token: token, Key: "concept.example", Snapshot: "snapshot-a", RecordType: "Concept",
		New: true, Proposer: "test.agent", Rationale: "refresh concept", CreatedAt: created,
	}
	if err := store.AppendFieldDraftEvent(ctx, start, 0); err != nil {
		t.Fatal(err)
	}
	answer := memory.FieldDraftEvent{
		Token: token, Answer: &memory.FieldDraftAnswer{
			Entity: "record", Field: "definition", Operation: "replace",
			Value:     []byte(`"A grounded definition."`),
			Citations: []memory.FieldDraftCitation{{Source: "docs/concepts.md", Quote: "A grounded definition."}},
		}, CreatedAt: created.Add(time.Minute),
	}
	if err := store.AppendFieldDraftEvent(ctx, answer, 1); err != nil {
		t.Fatal(err)
	}
	events, err := store.FieldDraftEvents(ctx, token)
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %+v, %v", events, err)
	}
	if events[0].Key != start.Key || events[0].RecordType != start.RecordType || !events[0].New || events[0].Answer != nil {
		t.Fatalf("start event = %+v", events[0])
	}
	if got := events[1].Answer; got == nil || got.Entity != "record" || got.Field != "definition" ||
		got.Operation != "replace" || string(got.Value) != `"A grounded definition."` ||
		len(got.Citations) != 1 || got.Citations[0].Quote != "A grounded definition." {
		t.Fatalf("answer event = %+v", got)
	}
	if !events[0].CreatedAt.Equal(start.CreatedAt) || !events[1].CreatedAt.Equal(answer.CreatedAt) {
		t.Fatalf("event timestamps = %v, %v", events[0].CreatedAt, events[1].CreatedAt)
	}
	if err := store.DeleteFieldDraft(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FieldDraftEvents(ctx, token); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("deleted draft read = %v, want ErrNotFound", err)
	}
}

func TestFieldDraftEventsUnknownTokenIsNotFound(t *testing.T) {
	store := openStore(t, dbPath(t))
	if _, err := store.FieldDraftEvents(context.Background(), "never-created"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFieldDraftConcurrentAdvanceRejectsStaleAppend(t *testing.T) {
	store := openStore(t, dbPath(t))
	ctx := context.Background()
	token := "field-draft-concurrent"
	start := memory.FieldDraftEvent{Token: token, Key: "concept.example", RecordType: "Concept", Proposer: "test.agent", Rationale: "r", CreatedAt: time.Now()}
	if err := store.AppendFieldDraftEvent(ctx, start, 0); err != nil {
		t.Fatal(err)
	}
	answers := []memory.FieldDraftEvent{
		{Token: token, Answer: &memory.FieldDraftAnswer{Entity: "record", Field: "label", Operation: "replace"}, CreatedAt: time.Now()},
		{Token: token, Answer: &memory.FieldDraftAnswer{Entity: "record", Field: "definition", Operation: "replace"}, CreatedAt: time.Now()},
	}
	ready := make(chan struct{}, len(answers))
	release := make(chan struct{})
	errs := make([]error, len(answers))
	var wg sync.WaitGroup
	for i, answer := range answers {
		i, answer := i, answer
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready <- struct{}{}
			<-release
			errs[i] = store.AppendFieldDraftEvent(ctx, answer, 1)
		}()
	}
	for range answers {
		<-ready
	}
	close(release)
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, memory.ErrFieldDraftConflict):
			conflicts++
		default:
			t.Fatalf("concurrent advance error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent advances: successes=%d conflicts=%d, want one of each", successes, conflicts)
	}
	events, err := store.FieldDraftEvents(ctx, token)
	if err != nil || len(events) != 2 {
		t.Fatalf("stored events = %d, %v; want only one competing answer", len(events), err)
	}
}
