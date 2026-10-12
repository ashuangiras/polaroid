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

// seedFeedback stores three reports, oldest first.
func seedFeedback(t *testing.T, s *sqlite.Store) []memory.Feedback {
	t.Helper()
	all := []memory.Feedback{
		feedback("f1", 1, memory.FeedbackProblem),
		feedback("f2", 2, memory.FeedbackSuggestion),
		feedback("f3", 3, memory.FeedbackProblem),
	}
	for i := range all {
		if err := s.CreateFeedback(context.Background(), &all[i]); err != nil {
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
		if got, err := s.ListFeedback(ctx, memory.FeedbackFilter{Kind: kind}); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("ListFeedback(%q) = %+v, %v\nwant %+v", kind, got, err, want)
		}
	}
	if _, err := s.Feedback(ctx, "missing"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing feedback: err = %v", err)
	}
}

func TestEmptyFeedbackListIsNotNil(t *testing.T) {
	got, err := openStore(t, dbPath(t)).ListFeedback(context.Background(), memory.FeedbackFilter{})
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
	got, err := openStore(t, path).ListFeedback(context.Background(), memory.FeedbackFilter{})
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

func resolution(id string, n int) memory.FeedbackResolution {
	return memory.FeedbackResolution{FeedbackID: id, Outcome: memory.OutcomeAddressed, Reason: "fixed in a later release",
		Actor: "copilot.vscode", References: []memory.ExternalReference{{URI: "https://github.com/ashuangiras/polaroid/pull/74"}},
		CreatedAt: created.Add(time.Duration(300+n) * time.Hour)}
}

func mustResolve(t *testing.T, s *sqlite.Store, id string, n int) {
	t.Helper()
	if err := s.ResolveFeedback(context.Background(), resolution(id, n)); err != nil {
		t.Fatalf("ResolveFeedback(%s): %v", id, err)
	}
}

// TestFeedbackResolutionRoundTripAndStatus: a resolution reads back on its
// report, status filters the list, and resolving twice or an unknown report
// fails without storing anything (ADR-0040).
func TestFeedbackResolutionRoundTripAndStatus(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	all := seedFeedback(t, s)
	mustResolve(t, s, "f2", 2)

	want := resolution("f2", 2)
	got, err := s.Feedback(ctx, "f2")
	if err != nil || got.Resolution == nil || !reflect.DeepEqual(*got.Resolution, want) || got.Status() != memory.FeedbackResolved {
		t.Fatalf("Feedback(f2) = %+v, %v\nwant resolution %+v", got, err, want)
	}
	if f1, err := s.Feedback(ctx, "f1"); err != nil || f1.Resolution != nil || f1.Status() != memory.FeedbackOpen {
		t.Fatalf("Feedback(f1) = %+v, %v; want open", f1, err)
	}

	for status, want := range map[memory.FeedbackStatus][]string{
		memory.FeedbackOpen:     {"f1", "f3"},
		memory.FeedbackResolved: {"f2"},
	} {
		list, err := s.ListFeedback(ctx, memory.FeedbackFilter{Status: status})
		if err != nil {
			t.Fatalf("ListFeedback(%s): %v", status, err)
		}
		var ids []string
		for _, f := range list {
			ids = append(ids, f.ID)
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("ListFeedback(%s) = %v, want %v", status, ids, want)
		}
	}
	unfiltered, err := s.ListFeedback(ctx, memory.FeedbackFilter{})
	if err != nil || len(unfiltered) != len(all) {
		t.Fatalf("ListFeedback() = %+v, %v; want every report", unfiltered, err)
	}

	if err := s.ResolveFeedback(ctx, resolution("f2", 9)); !errors.Is(err, memory.ErrFeedbackResolved) {
		t.Fatalf("second resolution: err = %v", err)
	}
	if err := s.ResolveFeedback(ctx, resolution("missing", 1)); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("resolving an unknown report: err = %v", err)
	}
}

// TestReopenPreservesFeedbackResolutions: resolutions survive a restart,
// byte for byte.
func TestReopenPreservesFeedbackResolutions(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seedFeedback(t, s)
	mustResolve(t, s, "f1", 1)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := openStore(t, path).Feedback(context.Background(), "f1")
	want := resolution("f1", 1)
	if err != nil || got.Resolution == nil || !reflect.DeepEqual(*got.Resolution, want) {
		t.Fatalf("after reopen: Feedback(f1) = %+v, %v\nwant resolution %+v", got, err, want)
	}
}

// The schema refuses an invalid, changed or repeated resolution, even from
// a client that bypasses Store (ADR-0040).
func TestSchemaGuardsFeedbackResolutions(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seedFeedback(t, s)
	mustResolve(t, s, "f1", 1)

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	insert := func(edits map[string]string) string {
		cols := map[string]string{
			"feedback_id": "'f2'", "outcome": "'addressed'", "reason": "'Fixed it.'",
			"actor": "'copilot.vscode'", "references_json": "'[]'", "created_at": "'2026-10-12T12:00:00.000000000Z'",
		}
		for k, v := range edits {
			cols[k] = v
		}
		names := []string{"feedback_id", "outcome", "reason", "actor", "references_json", "created_at"}
		values := make([]string, len(names))
		for i, k := range names {
			values[i] = cols[k]
		}
		return "INSERT INTO feedback_resolutions (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(values, ", ") + ")"
	}
	cases := []struct {
		name         string
		stmt         string
		wantAccepted bool
	}{
		{"control: a valid resolution", insert(nil), true},
		{"control: declared references", insert(map[string]string{"feedback_id": "'f3'", "references_json": `'[{"uri":"u"}]'`}), true},
		{"update", `UPDATE feedback_resolutions SET reason = 'changed' WHERE feedback_id = 'f1'`, false},
		{"delete", `DELETE FROM feedback_resolutions WHERE feedback_id = 'f1'`, false},
		{"second resolution", insert(map[string]string{"feedback_id": "'f1'"}), false},
		{"unknown outcome", insert(map[string]string{"feedback_id": "'f3'", "outcome": "'wontfix'"}), false},
		{"blank reason", insert(map[string]string{"feedback_id": "'f3'", "reason": "' ' || char(9)"}), false},
		{"invalid actor", insert(map[string]string{"feedback_id": "'f3'", "actor": "'Copilot Chat'"}), false},
		{"references not an array", insert(map[string]string{"feedback_id": "'f3'", "references_json": "'{}'"}), false},
		{"references not JSON", insert(map[string]string{"feedback_id": "'f3'", "references_json": "'['"}), false},
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
	if got, err := s.Feedback(context.Background(), "f1"); err != nil || got.Resolution == nil || got.Resolution.Reason != "fixed in a later release" {
		t.Fatalf("f1's resolution changed: %+v, %v", got, err)
	}
}
