package memory

import (
	"context"
	"reflect"
	"testing"
)

func invalidate(f *fakeRuns, id string) {
	e := f.runs[id]
	e.Invalidation = &Invalidation{ExecutionID: id, Reason: "wrong version", Actor: "copilot.vscode"}
	f.runs[id] = e
}

func TestInvalidationWithdrawsVerification(t *testing.T) {
	f := coverageRuns()
	invalidate(f, "c-ok")
	invalidate(f, "c-failed")
	cases := []struct {
		id       string
		status   EvidenceStatus
		problems []VerificationProblem
	}{
		{"c-ok", EvidenceInvalidated, []VerificationProblem{{Code: ProblemInvalidated}}},
		// Structural problems are still reported next to the invalidation.
		{"c-failed", EvidenceInvalidated, []VerificationProblem{{Code: ProblemInvalidated}, {Code: ProblemOutcomeFailed}}},
		{"b-ok", EvidenceValid, []VerificationProblem{{Code: ProblemChildInvalidated, Reference: "c", ExecutionID: "c-ok"}}},
		{"a-ok", EvidenceValid, []VerificationProblem{{Code: ProblemChildNotVerified, Reference: "b", ExecutionID: "b-ok"}}},
		{"c-ok-2", EvidenceValid, nil},
	}
	for _, tc := range cases {
		v, err := VerifyExecution(context.Background(), f, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Verified != (tc.problems == nil) || v.EvidenceStatus != tc.status || !reflect.DeepEqual(v.Problems, tc.problems) {
			t.Errorf("%s: verified %v, %s, %+v; want %s, %+v", tc.id, v.Verified, v.EvidenceStatus, v.Problems, tc.status, tc.problems)
		}
	}
}

func TestCombinationsAreDecidedByTheLatestValidExecution(t *testing.T) {
	f := newFakeRuns(nil)
	f.add("ok", "p", 1, OutcomeSucceeded, nil)
	f.add("failed", "p", 1, OutcomeFailed, nil)
	status := func() CombinationStatus {
		t.Helper()
		s, err := ListCombinations(context.Background(), f, VerificationFilter{ProcedureID: "p", Version: 1})
		if err != nil || len(s) != 1 {
			t.Fatalf("ListCombinations = %+v, %v", s, err)
		}
		return s[0]
	}
	if s := status(); s.Verified || s.LatestExecutionID != "failed" {
		t.Fatalf("a regression decides: %+v", s)
	}
	invalidate(f, "failed")
	if s := status(); !s.Verified || s.LatestExecutionID != "ok" || !reflect.DeepEqual(s.ExecutionIDs, []string{"ok", "failed"}) ||
		!reflect.DeepEqual(s.InvalidatedExecutionIDs, []string{"failed"}) {
		t.Fatalf("an invalidated regression decided: %+v", s)
	}
	invalidate(f, "ok")
	if s := status(); s.Verified || s.LatestExecutionID != "" || len(s.InvalidatedExecutionIDs) != 2 {
		t.Fatalf("only invalidated executions: %+v", s)
	}
}

func TestLifecycleFilterValues(t *testing.T) {
	for in, want := range map[string]string{"": "active", "active": "active", "retired": "retired", "all": "all"} {
		var p problems
		if got := lifecycleFilter(&p, "lifecycle", in); got != want || len(p) != 0 {
			t.Errorf("lifecycleFilter(%q) = %q, %v", in, got, p)
		}
	}
	var p problems
	if lifecycleFilter(&p, "lifecycle", "Retired"); len(p) != 1 || p[0].Field != "lifecycle" {
		t.Fatalf("a wrong value is accepted: %v", p)
	}
	if !matchesLifecycle("all", LifecycleRetired) || matchesLifecycle("active", LifecycleRetired) || !matchesLifecycle("retired", LifecycleRetired) {
		t.Fatal("matchesLifecycle")
	}
	l := Lifecycle{Events: []LifecycleEvent{{Event: 1, Status: LifecycleRetired}, {Event: 2, Status: LifecycleActive}}}
	if l.Status() != LifecycleActive || l.LatestEvent() != 2 || (Lifecycle{}).Status() != LifecycleActive {
		t.Fatalf("Lifecycle %+v", l)
	}
}
