package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

const commitA = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"

func execution(id string, n int, repository string) memory.Execution {
	return memory.Execution{
		ID:        id,
		CreatedAt: created.Add(time.Duration(n) * time.Hour),
		ExecutionRecord: memory.ExecutionRecord{
			ProcedureID: "p1",
			Version:     2,
			Repository:  repository,
			Commit:      commitA,
			Environment: memory.Environment{Name: "ci.ubuntu-latest", Attributes: jsontext.Value(`{"os":"linux"}`)},
			Inputs:      jsontext.Value(`{"module":"modernc.org/sqlite"}`),
			Outcome:     memory.OutcomeSucceeded,
			Evidence:    jsontext.Value(fmt.Sprintf(`{"run":%d}`, n)),
		},
	}
}

func summary(e memory.Execution) memory.Execution {
	e.Inputs, e.Evidence = nil, nil
	return e
}

// seedExecutions stores e1 (with binding b1) and e2 (no binding) for p1@2 in
// the binding's repository, and e3 for p1@1 in "scratch".
func seedExecutions(t *testing.T, s *sqlite.Store) []memory.Execution {
	t.Helper()
	seed(t, s, 2)
	seedBinding(t, s, 1) // pins p1@2 in github.com/ashuangiras/polaroid
	e1 := execution("e1", 1, "github.com/ashuangiras/polaroid")
	e1.BindingID, e1.BindingRevision = "b1", 1
	e2 := execution("e2", 2, "github.com/ashuangiras/polaroid")
	e2.Outcome = memory.OutcomeFailed
	e3 := execution("e3", 3, "scratch")
	e3.Version = 1
	all := []memory.Execution{e1, e2, e3}
	for _, e := range all {
		if err := s.CreateExecution(context.Background(), e); err != nil {
			t.Fatalf("CreateExecution(%s): %v", e.ID, err)
		}
	}
	return all
}

func TestExecutionsRoundTripAndList(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	all := seedExecutions(t, s)

	for _, want := range all {
		got, err := s.Execution(ctx, want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Execution(%s) = %+v, %v\nwant %+v", want.ID, got, err, want)
		}
	}
	cases := []struct {
		filter memory.ExecutionFilter
		want   []memory.Execution
	}{
		{memory.ExecutionFilter{ProcedureID: "p1"}, all},
		{memory.ExecutionFilter{ProcedureID: "p1", Version: 2}, all[:2]},
		{memory.ExecutionFilter{ProcedureID: "p1", Repository: "scratch"}, all[2:]},
		{memory.ExecutionFilter{ProcedureID: "p1", Version: 2, Repository: "scratch"}, nil},
		{memory.ExecutionFilter{ProcedureID: "nope"}, nil},
	}
	for _, tc := range cases {
		got, err := s.ListExecutions(ctx, tc.filter)
		if err != nil {
			t.Fatal(err)
		}
		want := []memory.Execution{}
		for _, e := range tc.want {
			want = append(want, summary(e))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ListExecutions(%+v) =\n %+v\nwant\n %+v", tc.filter, got, want)
		}
	}
	if _, err := s.Execution(ctx, "missing"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("missing execution: err = %v", err)
	}
}

func TestReopenPreservesExecutions(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	all := seedExecutions(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openStore(t, path)
	for _, want := range all {
		if got, err := reopened.Execution(context.Background(), want.ID); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("after reopen, %s = %+v, %v", want.ID, got, err)
		}
	}
}

// The schema refuses inconsistent or changed executions, even from a client
// that bypasses Store.
func TestSchemaRejectsInvalidAndChangedExecutions(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seedExecutions(t, s)

	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	insert := func(edits map[string]string) string {
		cols := map[string]string{
			"id": "'x'", "procedure_id": "'p1'", "version": "2", "binding_id": "'b1'", "binding_revision": "1",
			"repository": "'github.com/ashuangiras/polaroid'", "commit_hash": "'" + commitA + "'",
			"environment_name": "'ci'", "environment_attributes": "'{}'", "inputs": "'{}'",
			"outcome": "'succeeded'", "evidence": `'{"ok":true}'`, "created_at": "'2026-10-09T12:00:00.000000000Z'",
		}
		for k, v := range edits {
			cols[k] = v
		}
		var names, values []string
		for _, k := range []string{"id", "procedure_id", "version", "binding_id", "binding_revision", "repository", "commit_hash",
			"environment_name", "environment_attributes", "inputs", "outcome", "evidence", "created_at"} {
			names, values = append(names, k), append(values, cols[k])
		}
		return "INSERT INTO executions (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(values, ", ") + ")"
	}
	cases := []struct {
		name         string
		stmt         string
		wantAccepted bool
	}{
		{"control: a consistent execution", insert(nil), true},
		{"control: no binding", insert(map[string]string{"binding_id": "NULL", "binding_revision": "NULL"}), true},
		{"update", `UPDATE executions SET outcome = 'failed' WHERE id = 'e1'`, false},
		{"delete", `DELETE FROM executions WHERE id = 'e2'`, false},
		{"binding in another repository", insert(map[string]string{"repository": "'scratch'"}), false},
		{"version other than the pin", insert(map[string]string{"version": "1"}), false},
		{"binding without revision", insert(map[string]string{"binding_revision": "NULL"}), false},
		{"unknown version", insert(map[string]string{"binding_id": "NULL", "binding_revision": "NULL", "version": "9"}), false},
		{"abbreviated commit", insert(map[string]string{"commit_hash": "'a1b2c3d'"}), false},
		{"uppercase commit", insert(map[string]string{"commit_hash": "'" + strings.ToUpper(commitA) + "'"}), false},
		{"empty evidence", insert(map[string]string{"evidence": "'{ }'"}), false},
		{"unknown outcome", insert(map[string]string{"outcome": "'passed'"}), false},
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
	if got, err := s.Execution(context.Background(), "e1"); err != nil || got.Outcome != memory.OutcomeSucceeded {
		t.Fatalf("e1 changed: %+v, %v", got, err)
	}
}
