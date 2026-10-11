package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

// inContext is the context runOf's executions ran in.
var inContext = memory.ResolutionContext{Repository: "github.com/ashuangiras/polaroid", Environment: "ci.ubuntu-latest"}

func invalidation(id string) memory.Invalidation {
	return memory.Invalidation{ExecutionID: id, Reason: "recorded against the wrong version", Actor: "copilot.vscode",
		CreatedAt: created.Add(100 * time.Hour)}
}

func mustInvalidate(t *testing.T, s *sqlite.Store, id string) {
	t.Helper()
	if err := s.InvalidateExecution(context.Background(), invalidation(id)); err != nil {
		t.Fatalf("InvalidateExecution(%s): %v", id, err)
	}
}

func lifecycleEvent(id string, n int, status memory.LifecycleStatus) memory.LifecycleEvent {
	return memory.LifecycleEvent{ProcedureID: id, Event: n, Status: status, Reason: "superseded", Actor: "copilot.vscode",
		CreatedAt: created.Add(time.Duration(200+n) * time.Hour)}
}

func mustChangeLifecycle(t *testing.T, s *sqlite.Store, id string, base int, status memory.LifecycleStatus) memory.Lifecycle {
	t.Helper()
	l, err := s.AppendLifecycleEvent(context.Background(), base, lifecycleEvent(id, base+1, status))
	if err != nil {
		t.Fatalf("AppendLifecycleEvent(%s, %d, %s): %v", id, base, status, err)
	}
	return l
}

func resolve(t *testing.T, s *sqlite.Store, id string, n int, c memory.ResolutionContext) memory.GraphNode {
	t.Helper()
	res, err := s.Resolve(context.Background(), id, memory.VersionPolicy{Kind: memory.PolicyPin, Pin: n}, c)
	if err != nil {
		t.Fatalf("Resolve(%s@%d): %v", id, n, err)
	}
	return res.Graph
}

func verification(t *testing.T, s *sqlite.Store, id string) memory.Verification {
	t.Helper()
	v, err := s.ExecutionVerification(context.Background(), id)
	if err != nil {
		t.Fatalf("ExecutionVerification(%s): %v", id, err)
	}
	return v
}

func TestInvalidationIsStoredApartOnceAndForAll(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	seedComposed(t, s)
	before, err := s.Execution(ctx, "ch1")
	if err != nil || before.Invalidation != nil {
		t.Fatalf("Execution(ch1) = %+v, %v", before, err)
	}
	mustInvalidate(t, s, "ch1")

	check := func(s *sqlite.Store) {
		t.Helper()
		after, err := s.Execution(ctx, "ch1")
		if err != nil || after.Invalidation == nil || *after.Invalidation != invalidation("ch1") {
			t.Fatalf("Execution(ch1).Invalidation = %+v, %v", after.Invalidation, err)
		}
		after.Invalidation = nil
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("invalidation changed the execution:\n got %+v\nwant %+v", after, before)
		}
		if other, err := s.Execution(ctx, "ch2"); err != nil || other.Invalidation != nil {
			t.Fatalf("an unrelated execution reads %+v, %v", other.Invalidation, err)
		}
		for status, want := range map[memory.EvidenceStatus][]string{
			"": {"ch1", "ch2"}, memory.EvidenceInvalidated: {"ch1"}, memory.EvidenceValid: {"ch2"},
		} {
			list, err := s.ListExecutions(ctx, memory.ExecutionFilter{EvidenceStatus: status})
			var ids []string
			for _, e := range list {
				ids = append(ids, e.ID)
			}
			if err != nil || !reflect.DeepEqual(ids, want) {
				t.Fatalf("ListExecutions(%q) = %v, %v; want %v", status, ids, err, want)
			}
		}
	}
	check(s)
	if err := s.InvalidateExecution(ctx, invalidation("ch1")); !errors.Is(err, memory.ErrExecutionInvalidated) {
		t.Fatalf("second invalidation: %v", err)
	}
	if err := s.InvalidateExecution(ctx, invalidation("nope")); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("unknown execution: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	check(openStore(t, path))
}

func TestConcurrentInvalidationsStoreExactlyOne(t *testing.T) {
	s := openStore(t, dbPath(t))
	seedComposed(t, s)
	const n = 8
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			inv := invalidation("ch1")
			inv.Reason = strings.Repeat("r", i+1)
			errs[i] = s.InvalidateExecution(context.Background(), inv)
		})
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, memory.ErrExecutionInvalidated):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d invalidations stored, want 1", ok)
	}
}

// seedTree adds "top", whose version 1 pins par@1, and runs: p (par@1
// linking ch1 and ch2) and t (top@1 linking p), all verified.
func seedTree(t *testing.T, s *sqlite.Store) {
	t.Helper()
	seedComposed(t, s)
	mustPut(t, s, "top", 1, pinTo("par", "par", 1))
	mustRecord(t, s, runOf("p", "par", 1, 3,
		memory.ChildExecution{Reference: "pinned", ExecutionID: "ch1"}, memory.ChildExecution{Reference: "latest", ExecutionID: "ch2"}))
	mustRecord(t, s, runOf("t", "top", 1, 4, memory.ChildExecution{Reference: "par", ExecutionID: "p"}))
}

func TestInvalidationWithdrawsVerificationUpTheTree(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seedTree(t, s)
	mustRecord(t, s, runOf("ch1-again", "c", 2, 5))
	for _, id := range []string{"ch1", "p", "t"} {
		if v := verification(t, s, id); !v.Verified || v.EvidenceStatus != memory.EvidenceValid {
			t.Fatalf("before: %s = %+v", id, v)
		}
	}
	mustInvalidate(t, s, "ch1")

	cases := map[string][]memory.VerificationProblem{
		"ch1": {{Code: memory.ProblemInvalidated}},
		"p":   {{Code: memory.ProblemChildInvalidated, Reference: "pinned", ExecutionID: "ch1"}},
		"t":   {{Code: memory.ProblemChildNotVerified, Reference: "par", ExecutionID: "p"}},
	}
	for id, want := range cases {
		v := verification(t, s, id)
		status := memory.EvidenceValid
		if id == "ch1" {
			status = memory.EvidenceInvalidated
		}
		if v.Verified || v.EvidenceStatus != status || !reflect.DeepEqual(v.Problems, want) {
			t.Fatalf("%s = %+v; want unverified, %s, %+v", id, v, status, want)
		}
	}
	for _, id := range []string{"ch2", "ch1-again"} {
		if v := verification(t, s, id); !v.Verified {
			t.Fatalf("unrelated %s = %+v", id, v)
		}
	}
	// The parent keeps its recorded child: nothing is substituted.
	if p, err := s.Execution(ctx, "p"); err != nil || p.Children[0].ExecutionID != "ch1" {
		t.Fatalf("Execution(p) = %+v, %v", p, err)
	}
	// p is valid evidence that is no longer verified, so it still decides its
	// combination, which is now unverified.
	statuses, err := s.Verifications(ctx, memory.VerificationFilter{ProcedureID: "par", Version: 1})
	if err != nil || len(statuses) != 1 || statuses[0].Verified || statuses[0].LatestExecutionID != "p" || statuses[0].InvalidatedExecutionIDs != nil {
		t.Fatalf("Verifications(par@1) = %+v, %v", statuses, err)
	}
	statuses, err = s.Verifications(ctx, memory.VerificationFilter{ProcedureID: "c", Version: 2})
	if err != nil || len(statuses) != 1 || !statuses[0].Verified || statuses[0].LatestExecutionID != "ch1-again" ||
		!reflect.DeepEqual(statuses[0].ExecutionIDs, []string{"ch1", "ch1-again"}) || !reflect.DeepEqual(statuses[0].InvalidatedExecutionIDs, []string{"ch1"}) {
		t.Fatalf("Verifications(c@2) = %+v, %v", statuses, err)
	}
}

func TestContextualSelectionSkipsInvalidatedEvidence(t *testing.T) {
	s := openStore(t, dbPath(t))
	mustPut(t, s, "c", 1)
	mustPut(t, s, "c", 2)
	mustPut(t, s, "par", 1, latestOf("latest", "c"))
	mustRecord(t, s, runOf("c1", "c", 1, 1))
	mustRecord(t, s, runOf("c2-old", "c", 2, 2))
	mustRecord(t, s, runOf("c2", "c", 2, 3))

	selected := func() (int, memory.Selection, string) {
		t.Helper()
		e := resolve(t, s, "par", 1, inContext).Edges[0]
		return e.Node.Version, e.SelectedBy, e.Node.VerifiedBy
	}
	type sel struct {
		version int
		by      memory.Selection
		ev      string
	}
	for _, step := range []struct {
		invalidate string
		want       sel
	}{
		{"", sel{2, memory.SelectedByEvidence, "c2"}},
		{"c2", sel{2, memory.SelectedByEvidence, "c2-old"}}, // the version's previous valid run
		{"c2-old", sel{1, memory.SelectedByEvidence, "c1"}}, // another version
		{"c1", sel{2, memory.SelectedByLatest, ""}},         // no usable evidence
	} {
		if step.invalidate != "" {
			mustInvalidate(t, s, step.invalidate)
		}
		if v, by, ev := selected(); (sel{v, by, ev}) != step.want {
			t.Fatalf("after invalidating %q: selected %d by %s (%q), want %+v", step.invalidate, v, by, ev, step.want)
		}
	}
}

func TestInvalidatedChildWithdrawsComposedEvidence(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	mustPut(t, s, "c", 1)
	mustPut(t, s, "par", 1, latestOf("latest", "c"))
	mustPut(t, s, "par", 2, latestOf("latest", "c"))
	mustPut(t, s, "root", 1, latestOf("par", "par"))
	mustRecord(t, s, runOf("y", "c", 1, 1))
	mustRecord(t, s, runOf("p1", "par", 1, 2, memory.ChildExecution{Reference: "latest", ExecutionID: "y"}))
	mustRecord(t, s, runOf("x", "c", 1, 3))
	mustRecord(t, s, runOf("p2", "par", 2, 4, memory.ChildExecution{Reference: "latest", ExecutionID: "x"}))
	mustRecord(t, s, runOf("z", "c", 1, 5))

	follow := func() (int, string, string) {
		t.Helper()
		par := resolve(t, s, "root", 1, inContext).Edges[0].Node
		return par.Version, par.VerifiedBy, par.Edges[0].Node.VerifiedBy
	}
	if v, ev, child := follow(); v != 2 || ev != "p2" || child != "x" {
		t.Fatalf("before: par@%d by %q, child %q; want par@2 by p2 following x", v, ev, child)
	}
	mustInvalidate(t, s, "x")
	// p2 is no longer verified, so the walk falls back to p1's whole tree; it
	// never pairs p2 with the newer valid child z.
	if v, ev, child := follow(); v != 1 || ev != "p1" || child != "y" {
		t.Fatalf("after: par@%d by %q, child %q; want par@1 by p1 following y", v, ev, child)
	}
	if p2, err := s.Execution(ctx, "p2"); err != nil || p2.Invalidation != nil || p2.Children[0].ExecutionID != "x" {
		t.Fatalf("Execution(p2) = %+v, %v", p2, err)
	}
}

func TestTargetVerificationLosesInvalidatedSupport(t *testing.T) {
	s := openStore(t, dbPath(t))
	mustPut(t, s, "c", 1)
	mustRecord(t, s, runOf("c1", "c", 1, 1))
	mustRecord(t, s, runOf("c1b", "c", 1, 2))
	target := inContext
	target.Target = &memory.Target{Commit: commitA, Inputs: []byte(`{"module":"modernc.org/sqlite"}`)}

	status := func() memory.CombinationStatus {
		t.Helper()
		return *resolve(t, s, "c", 1, target).Target
	}
	if st := status(); !st.Verified || st.LatestExecutionID != "c1b" || st.InvalidatedExecutionIDs != nil {
		t.Fatalf("before: %+v", st)
	}
	mustInvalidate(t, s, "c1b")
	if st := status(); !st.Verified || st.LatestExecutionID != "c1" || !reflect.DeepEqual(st.ExecutionIDs, []string{"c1", "c1b"}) ||
		!reflect.DeepEqual(st.InvalidatedExecutionIDs, []string{"c1b"}) {
		t.Fatalf("after invalidating the latest: %+v", st)
	}
	mustInvalidate(t, s, "c1")
	if st := status(); st.Verified || st.LatestExecutionID != "" || !reflect.DeepEqual(st.InvalidatedExecutionIDs, []string{"c1", "c1b"}) {
		t.Fatalf("after invalidating both: %+v", st)
	}
	if g := resolve(t, s, "c", 1, inContext); g.VerifiedBy != "" || g.Evidence != nil {
		t.Fatalf("root evidence after invalidating both: %+v", g)
	}
}

func TestProcedureLifecycleEvents(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	seed(t, s, 1)
	if l, err := s.ProcedureLifecycle(ctx, "p1"); err != nil || l.Status() != memory.LifecycleActive || len(l.Events) != 0 {
		t.Fatalf("ProcedureLifecycle(p1) = %+v, %v", l, err)
	}
	retired := mustChangeLifecycle(t, s, "p1", 0, memory.LifecycleRetired)
	if retired.Status() != memory.LifecycleRetired || retired.LatestEvent() != 1 || retired.Events[0] != lifecycleEvent("p1", 1, memory.LifecycleRetired) {
		t.Fatalf("after retiring: %+v", retired)
	}
	var conflict *memory.LifecycleConflictError
	if _, err := s.AppendLifecycleEvent(ctx, 0, lifecycleEvent("p1", 1, memory.LifecycleRetired)); !errors.As(err, &conflict) || conflict.LatestEvent != 1 {
		t.Fatalf("stale base: %v", err)
	}
	if _, err := s.AppendLifecycleEvent(ctx, 1, lifecycleEvent("p1", 2, memory.LifecycleRetired)); !errors.Is(err, memory.ErrLifecycleUnchanged) {
		t.Fatalf("retiring a retired procedure: %v", err)
	}
	if _, err := s.AppendLifecycleEvent(ctx, 0, lifecycleEvent("nope", 1, memory.LifecycleRetired)); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("unknown procedure: %v", err)
	}
	mustChangeLifecycle(t, s, "p1", 1, memory.LifecycleActive)
	want := memory.Lifecycle{ProcedureID: "p1", Events: []memory.LifecycleEvent{
		lifecycleEvent("p1", 1, memory.LifecycleRetired), lifecycleEvent("p1", 2, memory.LifecycleActive)}}
	check := func(s *sqlite.Store) {
		t.Helper()
		if l, err := s.ProcedureLifecycle(ctx, "p1"); err != nil || !reflect.DeepEqual(l, want) {
			t.Fatalf("ProcedureLifecycle(p1) = %+v, %v\nwant %+v", l, err, want)
		}
		if h := history(t, s, "p1"); h.Procedure.Lifecycle != memory.LifecycleActive || h.Procedure.LifecycleEvent != 2 {
			t.Fatalf("History(p1).Procedure = %+v", h.Procedure)
		}
	}
	check(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	check(openStore(t, path))
}

func TestConcurrentRetirementsFromOneBaseStoreOne(t *testing.T) {
	s := openStore(t, dbPath(t))
	seed(t, s, 1)
	const n = 8
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			_, errs[i] = s.AppendLifecycleEvent(context.Background(), 0, lifecycleEvent("p1", 1, memory.LifecycleRetired))
		})
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		var conflict *memory.LifecycleConflictError
		switch {
		case err == nil:
			ok++
		case !errors.As(err, &conflict) || conflict.LatestEvent != 1:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if l, err := s.ProcedureLifecycle(context.Background(), "p1"); ok != 1 || err != nil || len(l.Events) != 1 {
		t.Fatalf("%d retirements stored; lifecycle %+v, %v", ok, l, err)
	}
}

func TestRetiredProceduresRefuseNewWritesAndKeepTheirGraphs(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	seed(t, s, 2)
	seedParent(t, s)
	bindings := seedBinding(t, s, 1)
	mustRecord(t, s, runOf("e1", "p1", 2, 1))
	before := resolve(t, s, "parent", 1, inContext)
	parentBefore, p1Before := history(t, s, "parent"), history(t, s, "p1")
	mustChangeLifecycle(t, s, "p1", 0, memory.LifecycleRetired)

	wantRetired := func(err error, procedure string, refs []int) {
		t.Helper()
		var retired *memory.RetiredProcedureError
		if !errors.As(err, &retired) || retired.ProcedureID != procedure || !reflect.DeepEqual(retired.References, refs) {
			t.Fatalf("err = %v, want RetiredProcedureError{%q, %v}", err, procedure, refs)
		}
	}
	wantRetired(s.AppendVersion(ctx, 2, version("p1", 3, `{}`)), "p1", nil)
	v := version("q", 1, `{}`)
	v.References = []memory.Reference{pinTo("pinned", "p1", 1), latestOf("latest", "p1")}
	wantRetired(s.CreateProcedure(ctx, procedure("q", "q.new"), v), "", []int{0, 1})
	if _, err := s.History(ctx, "q"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("a refused procedure was stored: %v", err)
	}
	v = version("parent", 2, `{}`)
	v.References = []memory.Reference{latestOf("again", "p1")}
	wantRetired(s.AppendVersion(ctx, 1, v), "", []int{0})
	b := binding("b9", "github.com/o/r", "other")
	wantRetired(s.CreateBinding(ctx, b, revision("b9", 1, contextual, `{}`)), "p1", nil)
	wantRetired(s.AppendBindingRevision(ctx, 1, revision("b1", 2, contextual, `{}`)), "p1", nil)

	// Nothing that existed changed, and resolution selects as before, now
	// marking the retired nodes.
	p1After := history(t, s, "p1")
	p1After.Procedure.Lifecycle, p1After.Procedure.LifecycleEvent = memory.LifecycleActive, 0
	if !reflect.DeepEqual(history(t, s, "parent"), parentBefore) || !reflect.DeepEqual(p1After, p1Before) ||
		!reflect.DeepEqual(bindingHistory(t, s, "b1"), bindings) {
		t.Fatal("a refused write or the retirement changed stored history")
	}
	after := resolve(t, s, "parent", 1, inContext)
	for i, e := range after.Edges {
		if r := e.Node.Retirement; r == nil || r.Event != 1 || r.Status != memory.LifecycleRetired || r.Reason != "superseded" {
			t.Fatalf("edge %s node retirement = %+v", e.Reference.Name, r)
		}
		e.Node.Retirement = nil
		if !reflect.DeepEqual(e, before.Edges[i]) {
			t.Fatalf("edge %s changed:\n got %+v\nwant %+v", e.Reference.Name, e, before.Edges[i])
		}
	}
	if after.Retirement != nil || after.Edges[0].Node.VerifiedBy != "e1" {
		t.Fatalf("parent node %+v", after)
	}

	mustChangeLifecycle(t, s, "p1", 1, memory.LifecycleActive)
	if err := s.AppendVersion(ctx, 2, version("p1", 3, `{}`)); err != nil {
		t.Fatalf("revising a reactivated procedure: %v", err)
	}
	if err := s.AppendBindingRevision(ctx, 1, revision("b1", 2, contextual, `{}`)); err != nil {
		t.Fatalf("revising a binding of a reactivated procedure: %v", err)
	}
	if g := resolve(t, s, "parent", 1, inContext); g.Edges[0].Node.Retirement != nil {
		t.Fatalf("a reactivated node still reports retirement: %+v", g.Edges[0].Node)
	}
}

func TestProcedureListsFilterByLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbPath(t))
	for _, id := range []string{"a", "b", "c"} {
		mustPut(t, s, id, 1)
	}
	mustChangeLifecycle(t, s, "b", 0, memory.LifecycleRetired)
	snapshot, err := s.ProcedureSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mustChangeLifecycle(t, s, "c", 0, memory.LifecycleRetired)

	list := func(lifecycle string, snap memory.Snapshot) string {
		t.Helper()
		ps, err := s.ListProcedures(ctx, memory.ProcedureFilter{Lifecycle: lifecycle, Snapshot: snap})
		if err != nil {
			t.Fatalf("ListProcedures(%q): %v", lifecycle, err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.ID+":"+string(p.Lifecycle))
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		lifecycle string
		snapshot  memory.Snapshot
		want      string
	}{
		{"active", nil, "a:active"},
		{"retired", nil, "b:retired,c:retired"},
		{"all", nil, "a:active,b:retired,c:retired"},
		{"active", snapshot, "a:active,c:active"},
		{"all", snapshot, "a:active,b:retired,c:active"},
		// A snapshot issued before migration 10 sees no lifecycle events.
		{"all", snapshot[:4], "a:active,b:active,c:active"},
	} {
		if got := list(tc.lifecycle, tc.snapshot); got != tc.want {
			t.Errorf("ListProcedures(%q, snapshot %v) = %s, want %s", tc.lifecycle, tc.snapshot, got, tc.want)
		}
	}
	latest, err := s.LatestVersions(ctx, "")
	if err != nil || len(latest) != 3 || latest[0].Lifecycle != memory.LifecycleActive || latest[1].Lifecycle != memory.LifecycleRetired {
		t.Fatalf("LatestVersions = %+v, %v", latest, err)
	}
}

// TestSchemaGuardsLifecycleHistory writes around the store. Each case
// checks the trigger's own message, so it fails for no other reason.
func TestSchemaGuardsLifecycleHistory(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	seedComposed(t, s)
	mustPut(t, s, "r", 1)
	mustPut(t, s, "fresh", 1)
	if err := s.CreateBinding(ctx, memory.Binding{ID: "rb", Repository: "github.com/o/r", Name: "r", ProcedureID: "r", CreatedAt: created, LatestRevision: 1},
		revision("rb", 1, contextual, `{}`)); err != nil {
		t.Fatal(err)
	}
	mustInvalidate(t, s, "ch1")
	mustChangeLifecycle(t, s, "r", 0, memory.LifecycleRetired)
	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	const at = "'2026-10-11T00:00:00.000000000Z'"
	for _, tc := range []struct{ name, stmt, message string }{
		{"update invalidation", `UPDATE execution_invalidations SET reason = 'x'`, "immutable"},
		{"delete invalidation", `DELETE FROM execution_invalidations`, "cannot be deleted"},
		{"second invalidation", `INSERT INTO execution_invalidations VALUES ('ch1', 'again', 'a', ` + at + `)`, "UNIQUE"},
		{"update event", `UPDATE procedure_lifecycle_events SET reason = 'x'`, "immutable"},
		{"delete event", `DELETE FROM procedure_lifecycle_events`, "cannot be deleted"},
		{"event gap", `INSERT INTO procedure_lifecycle_events VALUES ('fresh', 2, 'retired', 'r', 'a', ` + at + `)`, "contiguous"},
		{"first event active", `INSERT INTO procedure_lifecycle_events VALUES ('fresh', 1, 'active', 'r', 'a', ` + at + `)`, "change the status"},
		{"repeated status", `INSERT INTO procedure_lifecycle_events VALUES ('r', 2, 'retired', 'r', 'a', ` + at + `)`, "change the status"},
		{"blank reason", `INSERT INTO execution_invalidations VALUES ('ch2', ' ', 'a', ` + at + `)`, "CHECK"},
		{"bad actor", `INSERT INTO execution_invalidations VALUES ('ch2', 'r', 'A B', ` + at + `)`, "CHECK"},
		{"version of retired", `INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
			VALUES ('r', 2, 'p', 'm', '{}', '{}', 'r', ` + at + `)`, "the procedure is retired"},
		{"reference to retired", `INSERT INTO procedure_version_references (procedure_id, version, position, name, target_procedure_id, policy, pinned_version, inputs)
			VALUES ('fresh', 2, 0, 'x', 'r', 'pin', 1, '{}')`, "the referenced procedure is retired"},
		{"binding of retired", `INSERT INTO bindings (id, repository, name, procedure_id, created_at) VALUES ('rb2', 'github.com/o/r', 'r2', 'r', ` + at + `)`, "the bound procedure is retired"},
		{"binding revision of retired", `INSERT INTO binding_revisions (binding_id, revision, inputs, policy, pinned_version, revision_reason, created_at)
			VALUES ('rb', 2, '{}', 'contextual', NULL, 'r', ` + at + `)`, "the bound procedure is retired"},
	} {
		if _, err := raw.ExecContext(ctx, tc.stmt); err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.message)
		}
	}
}

func TestOpenMigratesSchemaVersion9(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	raw, err := sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	for i, file := range []string{"0001_procedures", "0002_bindings", "0003_procedure_references", "0004_executions", "0005_execution_children",
		"0006_feedback", "0007_repositories_scope_feedback", "0008_conditional_references", "0009_toil_items"} {
		script, err := os.ReadFile("migrations/" + file + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
	}
	const at = "2026-10-11T12:00:00.000000000Z"
	commit := strings.Repeat("ef", 20)
	for _, stmt := range []string{
		`PRAGMA user_version = 9`,
		`INSERT INTO procedures (id, canonical_key, created_at) VALUES ('leaf', 'go.module.build', '` + at + `')`,
		`INSERT INTO procedure_versions (procedure_id, version, philosophy, method, contract, instructions, revision_reason, created_at)
		 VALUES ('leaf', 1, 'p', 'm', '{}', '{}', 'r', '` + at + `')`,
		`INSERT INTO executions (id, procedure_id, version, repository, commit_hash, environment_name, environment_attributes, inputs, outcome, evidence, created_at)
		 VALUES ('run', 'leaf', 1, 'github.com/o/a', '` + commit + `', 'ci', '{}', '{}', 'succeeded', '{"exit":0}', '` + at + `')`,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = raw.Close()

	check := func(s *sqlite.Store) {
		t.Helper()
		h := history(t, s, "leaf")
		if h.Procedure.Lifecycle != memory.LifecycleActive || h.Procedure.LifecycleEvent != 0 || len(h.Versions) != 1 {
			t.Fatalf("History(leaf) = %+v", h)
		}
		e, err := s.Execution(ctx, "run")
		if err != nil || e.Invalidation != nil || string(e.Evidence) != `{"exit":0}` {
			t.Fatalf("Execution(run) = %+v, %v", e, err)
		}
		if v := verification(t, s, "run"); !v.Verified || v.EvidenceStatus != memory.EvidenceValid {
			t.Fatalf("verification = %+v", v)
		}
		if l, err := s.ProcedureLifecycle(ctx, "leaf"); err != nil || len(l.Events) != 0 {
			t.Fatalf("lifecycle = %+v, %v", l, err)
		}
	}
	s := openStore(t, path)
	check(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	check(openStore(t, path))
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var schema int
	if err := raw.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil || schema != latestSchema {
		t.Fatalf("schema version = %d, %v; want %d", schema, err, latestSchema)
	}
}
