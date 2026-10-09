package sqlite_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
)

func TestVerificationReadsStoredExecutions(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	seedComposed(t, s)
	mustRecord(t, s, runOf("p", "par", 1, 3,
		memory.ChildExecution{Reference: "latest", ExecutionID: "ch2"},
		memory.ChildExecution{Reference: "pinned", ExecutionID: "ch1"}))
	mustRecord(t, s, runOf("ch1-again", "c", 2, 4))
	mustRecord(t, s, runOf("p-partial", "par", 1, 4, memory.ChildExecution{Reference: "pinned", ExecutionID: "ch1-again"}))
	elsewhere := runOf("c-mac", "c", 1, 5)
	elsewhere.Commit, elsewhere.Environment.Name, elsewhere.Outcome = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2", "ci.macos", memory.OutcomeFailed
	mustRecord(t, s, elsewhere)

	v, err := s.ExecutionVerification(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	want := memory.Verification{
		ExecutionID: "p", ProcedureID: "par", Version: 1, Verified: true,
		Combination: memory.Combination{
			Repository: "github.com/ashuangiras/polaroid", Commit: commitA, Environment: "ci.ubuntu-latest",
			Inputs:   []byte(`{"module":"modernc.org/sqlite"}`),
			Children: []memory.ChildVersion{{Reference: "pinned", Version: 2}, {Reference: "latest", Version: 1}},
		},
	}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("ExecutionVerification(p) =\n%+v\nwant\n%+v", v, want)
	}

	lists := func() [][]memory.CombinationStatus {
		var out [][]memory.CombinationStatus
		for _, f := range []memory.VerificationFilter{
			{ProcedureID: "par", Version: 1},
			{ProcedureID: "c", Version: 1},
			{ProcedureID: "c", Version: 1, Environment: "ci.macos"},
			{ProcedureID: "c", Version: 1, Commit: commitA},
			{ProcedureID: "c", Version: 1, Repository: "scratch"},
		} {
			got, err := s.Verifications(ctx, f)
			if err != nil {
				t.Fatalf("Verifications(%+v): %v", f, err)
			}
			out = append(out, got)
		}
		return out
	}
	before := lists()
	ids := func(statuses []memory.CombinationStatus) [][]string {
		var out [][]string
		for _, st := range statuses {
			out = append(out, st.ExecutionIDs)
		}
		return out
	}
	for i, want := range [][][]string{
		{{"p"}, {"p-partial"}},
		{{"ch2"}, {"c-mac"}},
		{{"c-mac"}},
		{{"ch2"}},
		nil,
	} {
		if got := ids(before[i]); !reflect.DeepEqual(got, want) {
			t.Errorf("list %d = %v, want %v", i, got, want)
		}
	}
	if before[0][1].Verified || !before[0][0].Verified || before[1][1].Verified {
		t.Fatalf("statuses: %+v / %+v", before[0], before[1])
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, path)
	if after := lists(); !reflect.DeepEqual(after, before) {
		t.Fatalf("after reopen:\n%+v\nwant\n%+v", after, before)
	}
	if _, err := s.ExecutionVerification(ctx, "missing"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("unknown execution: %v", err)
	}
}
