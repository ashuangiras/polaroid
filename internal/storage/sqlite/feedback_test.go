package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

func feedback(id string, n int, kind memory.FeedbackKind) memory.Feedback {
	return memory.Feedback{
		ID:        id,
		CreatedAt: created.Add(time.Duration(n) * time.Hour),
		FeedbackRecord: memory.FeedbackRecord{
			Kind:     kind,
			Summary:  "report " + id,
			Details:  "Line one.\nLine two.",
			Reporter: "copilot.vscode",
			Context:  jsontext.Value(`{"tool":"record_execution","n":` + strconv.Itoa(n) + `}`),
		},
	}
}

// seedFeedback stores f2 before f1, so that order must come from created_at.
func seedFeedback(t *testing.T, s *sqlite.Store) []memory.Feedback {
	t.Helper()
	all := []memory.Feedback{
		feedback("f1", 1, memory.FeedbackProblem),
		feedback("f2", 2, memory.FeedbackSuggestion),
		feedback("f3", 3, memory.FeedbackProblem),
	}
	for _, i := range []int{1, 0, 2} {
		if err := s.CreateFeedback(context.Background(), all[i]); err != nil {
			t.Fatalf("CreateFeedback(%s): %v", all[i].ID, err)
		}
	}
	return all
}

func TestFeedbackRoundTripAndList(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	all := seedFeedback(t, s)

	for _, want := range all {
		if got, err := s.Feedback(ctx, want.ID); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Feedback(%s) = %+v, %v\nwant %+v", want.ID, got, err, want)
		}
	}
	for kind, want := range map[memory.FeedbackKind][]memory.Feedback{
		"":                        all,
		memory.FeedbackProblem:    {all[0], all[2]},
		memory.FeedbackSuggestion: {all[1]},
	} {
		if got, err := s.ListFeedback(ctx, kind); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("ListFeedback(%q) = %+v, %v\nwant %+v", kind, got, err, want)
		}
	}
	if _, err := s.Feedback(ctx, "missing"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing feedback: err = %v", err)
	}
}

func TestEmptyFeedbackListIsNotNil(t *testing.T) {
	got, err := openStore(t, dbPath(t)).ListFeedback(context.Background(), "")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("ListFeedback = %#v, %v; want an empty list", got, err)
	}
}

func TestReopenPreservesFeedback(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	all := seedFeedback(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := openStore(t, path).ListFeedback(context.Background(), "")
	if err != nil || !reflect.DeepEqual(got, all) {
		t.Fatalf("after reopen = %+v, %v", got, err)
	}
}

// The schema refuses invalid or changed reports, even from a client that
// bypasses Store.
func TestSchemaRejectsInvalidAndChangedFeedback(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seedFeedback(t, s)

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	insert := func(edits map[string]string) string {
		cols := map[string]string{
			"id": "'x'", "kind": "'problem'", "summary": "'One line.'", "details": "'Some details.'",
			"reporter": "'copilot.vscode'", "context": "'{}'", "created_at": "'2026-10-09T12:00:00.000000000Z'",
		}
		for k, v := range edits {
			cols[k] = v
		}
		names := []string{"id", "kind", "summary", "details", "reporter", "context", "created_at"}
		values := make([]string, len(names))
		for i, k := range names {
			values[i] = cols[k]
		}
		return "INSERT INTO feedback (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(values, ", ") + ")"
	}
	cases := []struct {
		name         string
		stmt         string
		wantAccepted bool
	}{
		{"control: a valid report", insert(nil), true},
		{"control: multi-line details", insert(map[string]string{"details": "'one' || char(10) || 'two'"}), true},
		{"update", `UPDATE feedback SET summary = 'changed' WHERE id = 'f1'`, false},
		{"delete", `DELETE FROM feedback WHERE id = 'f2'`, false},
		{"unknown kind", insert(map[string]string{"kind": "'bug'"}), false},
		{"blank summary", insert(map[string]string{"summary": "' ' || char(9)"}), false},
		{"multi-line summary", insert(map[string]string{"summary": "'one' || char(10) || 'two'"}), false},
		{"carriage return in summary", insert(map[string]string{"summary": "'one' || char(13) || 'two'"}), false},
		{"line separator in summary", insert(map[string]string{"summary": "'one' || char(8232) || 'two'"}), false},
		{"blank details", insert(map[string]string{"details": "char(10)"}), false},
		{"invalid reporter", insert(map[string]string{"reporter": "'Copilot Chat'"}), false},
		{"context not an object", insert(map[string]string{"context": "'[]'"}), false},
		{"context not JSON", insert(map[string]string{"context": "'{'"}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx, err := raw.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			_, err = tx.ExecContext(ctx, tc.stmt)
			if accepted := err == nil; accepted != tc.wantAccepted {
				t.Fatalf("accepted = %v, want %v (err %v): %s", accepted, tc.wantAccepted, err, tc.stmt)
			}
		})
	}
	if got, err := s.Feedback(context.Background(), "f1"); err != nil || got.Summary != "report f1" {
		t.Fatalf("f1 changed: %+v, %v", got, err)
	}
}
