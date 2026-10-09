package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

// runOf returns execution id of procedure@version in the polaroid repository
// at commitA, recorded n hours after `created`.
func runOf(id, procedure string, version, n int, children ...memory.ChildExecution) memory.Execution {
	e := execution(id, n, "github.com/ashuangiras/polaroid")
	e.ProcedureID, e.Version, e.Children = procedure, version, children
	return e
}

func mustRecord(t *testing.T, s *sqlite.Store, e memory.Execution) {
	t.Helper()
	if err := s.CreateExecution(context.Background(), &e); err != nil {
		t.Fatalf("CreateExecution(%s): %v", e.ID, err)
	}
}

// seedComposed stores child procedure "c" at versions 1 and 2, parent
// procedure "par" whose version 1 references c pinned at 2 ("pinned") and
// contextually ("latest"), and child executions ch1 (c@2) and ch2 (c@1).
func seedComposed(t *testing.T, s *sqlite.Store) {
	t.Helper()
	mustPut(t, s, "c", 1)
	mustPut(t, s, "c", 2)
	mustPut(t, s, "par", 1, pinTo("pinned", "c", 2), latestOf("latest", "c"))
	mustRecord(t, s, runOf("ch1", "c", 2, 1))
	mustRecord(t, s, runOf("ch2", "c", 1, 2))
}

func TestParentRecordsItsChildren(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	seedComposed(t, s)
	parent := runOf("p", "par", 1, 3,
		memory.ChildExecution{Reference: "pinned", ExecutionID: "ch1"},
		memory.ChildExecution{Reference: "latest", ExecutionID: "ch2"})
	mustRecord(t, s, parent)

	if got, err := s.Execution(ctx, "p"); err != nil || !reflect.DeepEqual(got, parent) {
		t.Fatalf("Execution(p) = %+v, %v\nwant %+v", got, err, parent)
	}
	if got, err := s.Execution(ctx, "ch1"); err != nil || got.Children != nil {
		t.Fatalf("a child without children reads back with %+v, %v", got.Children, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := openStore(t, path).Execution(ctx, "p"); err != nil || !reflect.DeepEqual(got, parent) {
		t.Fatalf("after reopen: %+v, %v", got, err)
	}
}

func TestAChildHasAtMostOneParent(t *testing.T) {
	s := openStore(t, dbPath(t))
	seedComposed(t, s)
	mustRecord(t, s, runOf("p1", "par", 1, 3, memory.ChildExecution{Reference: "pinned", ExecutionID: "ch1"}))

	second := runOf("p2", "par", 1, 4,
		memory.ChildExecution{Reference: "latest", ExecutionID: "ch2"},
		memory.ChildExecution{Reference: "pinned", ExecutionID: "ch1"})
	err := s.CreateExecution(context.Background(), &second)
	var linked *memory.LinkedChildError
	if !errors.As(err, &linked) || linked.Index != 1 || linked.ParentID != "p1" {
		t.Fatalf("second parent: err = %v, want child 1 linked to p1", err)
	}
	if _, err := s.Execution(context.Background(), "p2"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("the rejected parent was stored: %v", err)
	}
	// ch2 was not claimed by the rejected parent.
	mustRecord(t, s, runOf("p3", "par", 1, 5, memory.ChildExecution{Reference: "latest", ExecutionID: "ch2"}))
}

func TestConcurrentParentsClaimAChildOnce(t *testing.T) {
	s := openStore(t, dbPath(t))
	seedComposed(t, s)

	const parents = 8
	start := make(chan struct{})
	errs := make([]error, parents)
	var wg sync.WaitGroup
	for i := range parents {
		wg.Go(func() {
			<-start
			parent := runOf(fmt.Sprint("p", i), "par", 1, 3+i, memory.ChildExecution{Reference: "pinned", ExecutionID: "ch1"})
			errs[i] = s.CreateExecution(context.Background(), &parent)
		})
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for i, err := range errs {
		var linked *memory.LinkedChildError
		switch {
		case err == nil:
			succeeded++
		case !errors.As(err, &linked):
			t.Fatalf("parent %d: unexpected error %v", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d parents claimed the same child, want exactly 1", succeeded)
	}
}

func TestNestedExecutionTrees(t *testing.T) {
	s := openStore(t, dbPath(t))
	mustPut(t, s, "leaf", 1)
	mustPut(t, s, "mid", 1, pinTo("leaf", "leaf", 1))
	mustPut(t, s, "top", 1, latestOf("mid", "mid"))
	mustRecord(t, s, runOf("leaf-run", "leaf", 1, 1))
	mustRecord(t, s, runOf("mid-run", "mid", 1, 2, memory.ChildExecution{Reference: "leaf", ExecutionID: "leaf-run"}))
	top := runOf("top-run", "top", 1, 3, memory.ChildExecution{Reference: "mid", ExecutionID: "mid-run"})
	mustRecord(t, s, top)

	got, err := s.Execution(context.Background(), "mid-run")
	if err != nil || len(got.Children) != 1 || got.Children[0].ExecutionID != "leaf-run" {
		t.Fatalf("mid-run = %+v, %v", got, err)
	}
	if got, err := s.Execution(context.Background(), "top-run"); err != nil || !reflect.DeepEqual(got, top) {
		t.Fatalf("top-run = %+v, %v", got, err)
	}
}

// The schema accepts a link only in the transaction that writes its parent,
// and only if it fulfils the reference; it never accepts changes.
func TestSchemaRejectsInvalidAndChangedChildLinks(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seedComposed(t, s)
	// One unlinked child per case, so no case can fail because an earlier
	// one committed a link to the same child.
	for i, version := range []int{2, 1, 1, 1, 1, 1} {
		mustRecord(t, s, runOf(fmt.Sprint("free", i), "c", version, 3+i))
	}
	mustRecord(t, s, runOf("p", "par", 1, 10, memory.ChildExecution{Reference: "pinned", ExecutionID: "ch1"}))

	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	const link = `INSERT INTO execution_children VALUES ('%s', 'par', 1, 'github.com/ashuangiras/polaroid', '%s', 0, '%s', '%s')`
	const parentRow = `INSERT INTO executions (id, procedure_id, version, repository, commit_hash, environment_name,
		environment_attributes, inputs, outcome, evidence, created_at)
		VALUES ('new', 'par', 1, 'github.com/ashuangiras/polaroid', '%s', 'ci', '{}', '{}', 'succeeded', '{"ok":1}', '2026-10-09T20:00:00.000000000Z')`
	otherCommit := strings.Repeat("b2", 20)
	cases := []struct {
		name, link, parent string
		accepted           bool
	}{
		{"control: a valid link with its parent", fmt.Sprintf(link, "new", commitA, "pinned", "free0"), fmt.Sprintf(parentRow, commitA), true},
		{"update a link", `UPDATE execution_children SET reference = 'latest'`, "", false},
		{"delete a link", `DELETE FROM execution_children`, "", false},
		{"add a link to an existing parent", fmt.Sprintf(link, "p", commitA, "latest", "free1"), "", false},
		{"a link whose parent never arrives", fmt.Sprintf(link, "new", commitA, "latest", "free1"), "", false},
		{"a link that disagrees with its parent", fmt.Sprintf(link, "new", commitA, "latest", "free2"), fmt.Sprintf(parentRow, otherCommit), false},
		{"child at a version other than the pin", fmt.Sprintf(link, "new", commitA, "pinned", "free3"), fmt.Sprintf(parentRow, commitA), false},
		{"child of another procedure", fmt.Sprintf(link, "new", commitA, "latest", "p"), fmt.Sprintf(parentRow, commitA), false},
		{"child at another commit", fmt.Sprintf(link, "new", otherCommit, "latest", "free4"), fmt.Sprintf(parentRow, otherCommit), false},
		{"unknown reference", fmt.Sprintf(link, "new", commitA, "missing", "free5"), fmt.Sprintf(parentRow, commitA), false},
		{"a child that already has a parent", fmt.Sprintf(link, "new", commitA, "pinned", "ch1"), fmt.Sprintf(parentRow, commitA), false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			// A fresh parent ID per case, so a committed control cannot make a
			// later case fail for the wrong reason.
			fresh := func(stmt string) string { return strings.ReplaceAll(stmt, "'new'", fmt.Sprintf("'new-%d'", i)) }
			tx, err := raw.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			_, err = tx.ExecContext(ctx, fresh(tc.link))
			if err == nil && tc.parent != "" {
				_, err = tx.ExecContext(ctx, fresh(tc.parent))
			}
			if err == nil {
				err = tx.Commit()
			}
			if accepted := err == nil; accepted != tc.accepted {
				t.Fatalf("accepted = %v, want %v (err %v)", accepted, tc.accepted, err)
			}
		})
	}
}
