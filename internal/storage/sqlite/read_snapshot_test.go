package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

// seedVerified is seedComposed plus parent execution p, which links ch1
// (c@2) and ch2 (c@1) and is verified.
func seedVerified(t *testing.T, s *sqlite.Store) {
	t.Helper()
	seedComposed(t, s)
	mustRecord(t, s, runOf("p", "par", 1, 3,
		memory.ChildExecution{Reference: "pinned", ExecutionID: "ch1"},
		memory.ChildExecution{Reference: "latest", ExecutionID: "ch2"}))
}

// observed is what the three multi-statement reads return for the seeded
// catalog, reduced to comparable strings.
type observed struct {
	Graph        []string
	Verification string
	Combinations []string
}

func observe(ctx context.Context, s *sqlite.Store) (observed, error) {
	var o observed
	res, err := s.Resolve(ctx, "par", memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 1}, memory.ResolutionContext{})
	if err != nil {
		return o, fmt.Errorf("resolve: %w", err)
	}
	for _, e := range res.Graph.Edges {
		o.Graph = append(o.Graph, fmt.Sprintf("%s=%s@%d", e.Reference.Name, e.Node.ProcedureID, e.Node.Version))
	}
	v, err := s.ExecutionVerification(ctx, "p")
	if err != nil {
		return o, fmt.Errorf("verification: %w", err)
	}
	o.Verification = fmt.Sprintf("verified=%t status=%s problems=%d", v.Verified, v.EvidenceStatus, len(v.Problems))
	statuses, err := s.Verifications(ctx, memory.VerificationFilter{ProcedureID: "c", Version: 2})
	if err != nil {
		return o, fmt.Errorf("verifications: %w", err)
	}
	for _, c := range statuses {
		o.Combinations = append(o.Combinations, fmt.Sprintf("latest=%s invalidated=%v", c.LatestExecutionID, c.InvalidatedExecutionIDs))
	}
	return o, nil
}

func mustObserve(t *testing.T, s *sqlite.Store) observed {
	t.Helper()
	o, err := observe(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// changeCatalog writes, through q, a version c@3 (which the contextual
// reference "latest" then selects) and an invalidation of ch1 (which
// withdraws p's verification and c@2's only run).
func changeCatalog(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	if _, err := q.ExecContext(ctx, `INSERT INTO procedure_versions
		(procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		VALUES ('c', 3, 'p', 'm', '{}', '{}', 'r', '2026-10-09T13:00:00.000000000Z')`); err != nil {
		return fmt.Errorf("insert c@3: %w", err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO execution_invalidations (execution_id, reason, actor, created_at)
		VALUES ('ch1', 'wrong evidence', 'test.writer', '2026-10-09T13:00:00.000000000Z')`); err != nil {
		return fmt.Errorf("invalidate ch1: %w", err)
	}
	return nil
}

// otherWriter opens a second connection pool on path, as another process
// would, whose transactions take the write lock at BEGIN.
func otherWriter(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_txlock=immediate&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMultiStatementReadsProceedBesideAnUncommittedWrite(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	seedVerified(t, s)
	before := mustObserve(t, s)

	held, err := otherWriter(t, path).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback() }()
	if err := changeCatalog(ctx, held); err != nil {
		t.Fatal(err)
	}

	// The other connection holds the write lock with uncommitted changes. A
	// read that needed the lock would wait out busy_timeout and fail.
	during, err := observe(ctx, s)
	if err != nil {
		t.Fatalf("a read failed beside an uncommitted write: %v", err)
	}
	if !reflect.DeepEqual(during, before) {
		t.Fatalf("a read saw uncommitted changes:\n got %+v\nwant %+v", during, before)
	}

	// Committed, the same changes alter every result, so the comparison
	// above could tell them apart.
	if err := held.Commit(); err != nil {
		t.Fatal(err)
	}
	after := mustObserve(t, s)
	want := observed{
		Graph:        []string{"pinned=c@2", "latest=c@3"},
		Verification: "verified=false status=valid problems=1",
		Combinations: []string{"latest= invalidated=[ch1]"},
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("after the commit:\n got %+v\nwant %+v (before: %+v)", after, want, before)
	}
}

func TestMultiStatementReadsKeepOneSnapshotWhileWritesCommit(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	seedVerified(t, s)
	before := mustObserve(t, s)
	writer := otherWriter(t, path)

	// Once each read has opened its snapshot and before it reads anything
	// else, another connection commits changes to every record it reads.
	// The read must neither block that write nor see any part of it.
	commits := 0
	sqlite.SetReadStarted(s, func() {
		tx, err := writer.BeginTx(ctx, nil)
		if err != nil {
			t.Errorf("begin the concurrent write: %v", err)
			return
		}
		if err := changeCatalog(ctx, tx); err != nil {
			_ = tx.Rollback()
			t.Errorf("concurrent write: %v", err)
			return
		}
		if err := tx.Commit(); err != nil {
			t.Errorf("commit the concurrent write: %v", err)
			return
		}
		commits++
		sqlite.SetReadStarted(s, nil)
	})
	res, err := s.Resolve(ctx, "par", memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 1}, memory.ResolutionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if commits != 1 {
		t.Fatalf("the concurrent write did not commit during the read")
	}
	var graph []string
	for _, e := range res.Graph.Edges {
		graph = append(graph, fmt.Sprintf("%s=%s@%d", e.Reference.Name, e.Node.ProcedureID, e.Node.Version))
	}
	if !reflect.DeepEqual(graph, before.Graph) {
		t.Fatalf("the graph mixed snapshots: %q, want %q", graph, before.Graph)
	}
	if after := mustObserve(t, s); reflect.DeepEqual(after.Graph, before.Graph) {
		t.Fatalf("the committed version is not visible to a later read: %q", after.Graph)
	}
}

func TestVerificationKeepsOneSnapshotWhileWritesCommit(t *testing.T) {
	for _, read := range []struct {
		name string
		run  func(context.Context, *sqlite.Store) (string, error)
	}{
		{"execution verification", func(ctx context.Context, s *sqlite.Store) (string, error) {
			v, err := s.ExecutionVerification(ctx, "p")
			return fmt.Sprintf("verified=%t status=%s problems=%d", v.Verified, v.EvidenceStatus, len(v.Problems)), err
		}},
		{"combinations", func(ctx context.Context, s *sqlite.Store) (string, error) {
			statuses, err := s.Verifications(ctx, memory.VerificationFilter{ProcedureID: "c", Version: 2})
			var out []string
			for _, c := range statuses {
				out = append(out, fmt.Sprintf("latest=%s invalidated=%v", c.LatestExecutionID, c.InvalidatedExecutionIDs))
			}
			return fmt.Sprint(out), err
		}},
	} {
		t.Run(read.name, func(t *testing.T) {
			ctx := context.Background()
			path := dbPath(t)
			s := openStore(t, path)
			seedVerified(t, s)
			writer := otherWriter(t, path)
			before, err := read.run(ctx, s)
			if err != nil {
				t.Fatal(err)
			}

			committed := false
			sqlite.SetReadStarted(s, func() {
				sqlite.SetReadStarted(s, nil)
				if _, err := writer.ExecContext(ctx, `INSERT INTO execution_invalidations (execution_id, reason, actor, created_at)
					VALUES ('ch1', 'wrong evidence', 'test.writer', '2026-10-09T13:00:00.000000000Z')`); err != nil {
					t.Errorf("concurrent invalidation: %v", err)
					return
				}
				committed = true
			})
			during, err := read.run(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			if !committed {
				t.Fatal("the concurrent invalidation did not commit during the read")
			}
			if during != before {
				t.Fatalf("read %s, want the snapshot from before the commit: %s", during, before)
			}
			if after, err := read.run(ctx, s); err != nil || after == before {
				t.Fatalf("the committed invalidation is not visible to a later read: %s, %v", after, err)
			}
		})
	}
}

func TestFailedAndCancelledReadsReleaseTheirSnapshot(t *testing.T) {
	path := dbPath(t)
	s := openStore(t, path)
	seedVerified(t, s)
	writer := otherWriter(t, path)

	ctx, cancel := context.WithCancel(context.Background())
	sqlite.SetReadStarted(s, func() {
		sqlite.SetReadStarted(s, nil)
		cancel()
	})
	if _, err := s.Resolve(ctx, "par", memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 1}, memory.ResolutionContext{}); err == nil {
		t.Fatal("a read cancelled inside its snapshot succeeded")
	}
	if _, err := s.ExecutionVerification(context.Background(), "missing"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("verification of a missing execution: %v, want not found", err)
	}
	if _, err := s.Resolve(context.Background(), "missing", memory.VersionPolicy{Kind: memory.PolicyContextual}, memory.ResolutionContext{}); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("resolution of a missing procedure: %v, want not found", err)
	}
	if n := sqlite.ConnectionsInUse(s); n != 0 {
		t.Fatalf("%d connections still in use after failed reads", n)
	}

	// A snapshot left open would pin the write-ahead log: a write followed by
	// a truncating checkpoint could not complete.
	if err := changeCatalog(context.Background(), writer); err != nil {
		t.Fatal(err)
	}
	var busy, logFrames, checkpointed int
	if err := writer.QueryRowContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		t.Fatal(err)
	}
	if busy != 0 || logFrames != 0 {
		t.Fatalf("checkpoint busy=%d log=%d: a read still holds a snapshot", busy, logFrames)
	}
	if got := mustObserve(t, s); got.Graph[1] != "latest=c@3" {
		t.Fatalf("after the failed reads: %+v", got)
	}
}

func TestConcurrentReadsKeepWritesAndConflicts(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seedVerified(t, s)

	const readers, recorders, revisers = 8, 8, 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	readErrs := make([]error, readers)
	recordErrs := make([]error, recorders)
	reviseErrs := make([]error, revisers)
	for i := range readers {
		wg.Go(func() {
			<-start
			for range 20 {
				if _, err := observe(ctx, s); err != nil {
					readErrs[i] = err
					return
				}
			}
		})
	}
	for i := range recorders {
		wg.Go(func() {
			<-start
			e := runOf(fmt.Sprintf("r%d", i), "c", 2, 10+i)
			recordErrs[i] = s.CreateExecution(ctx, &e)
		})
	}
	for i := range revisers {
		wg.Go(func() {
			<-start
			reviseErrs[i] = put(s, "c", 3)
		})
	}
	close(start)
	wg.Wait()

	for i, err := range readErrs {
		if err != nil {
			t.Errorf("reader %d: %v", i, err)
		}
	}
	for i, err := range recordErrs {
		if err != nil {
			t.Errorf("recorder %d: %v", i, err)
		} else if _, err := s.Execution(ctx, fmt.Sprintf("r%d", i)); err != nil {
			t.Errorf("recorded r%d does not read back: %v", i, err)
		}
	}
	succeeded := 0
	for i, err := range reviseErrs {
		var conflict *memory.VersionConflictError
		switch {
		case err == nil:
			succeeded++
		case errors.As(err, &conflict) && conflict.LatestVersion == 3:
		default:
			t.Errorf("reviser %d: unexpected error %v", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d revisions from one base succeeded, want exactly 1", succeeded)
	}
}
