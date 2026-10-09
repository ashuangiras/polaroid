package sqlite_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
)

func TestResolveUsesStoredEvidence(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s := openStore(t, path)
	seedComposed(t, s) // c@1, c@2; par@1 pins c@2 ("pinned") and follows c ("latest"); ch1 = c@2, ch2 = c@1
	mustPut(t, s, "c", 3)
	failed := runOf("c3-failed", "c", 3, 3)
	failed.Outcome = memory.OutcomeFailed
	mustRecord(t, s, failed)

	ubuntu := memory.ResolutionContext{Repository: "github.com/ashuangiras/polaroid", Environment: "ci.ubuntu-latest"}
	resolve := func(c memory.ResolutionContext) []string {
		t.Helper()
		res, err := s.Resolve(ctx, "par", memory.VersionPolicy{Kind: memory.PolicyPin, Pin: 1}, c)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range res.Graph.Edges {
			out = append(out, fmt.Sprintf("%s=%s@%d/%s/%s", e.Reference.Name, e.Node.ProcedureID, e.Node.Version, e.SelectedBy, e.Node.VerifiedBy))
		}
		return out
	}

	// c@3 failed here and c@2's latest run (ch1) succeeded, so "latest" selects c@2.
	want := []string{"pinned=c@2/pin/ch1", "latest=c@2/evidence/ch1"}
	if got := resolve(ubuntu); !reflect.DeepEqual(got, want) {
		t.Fatalf("in context: %q, want %q", got, want)
	}
	if got, want := resolve(memory.ResolutionContext{}), []string{"pinned=c@2/pin/", "latest=c@3/latest/"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("without context: %q, want %q", got, want)
	}

	// A newer failed run of c@2 withdraws it; c@1 (ch2) is then the highest verified.
	regressed := runOf("c2-failed", "c", 2, 4)
	regressed.Outcome = memory.OutcomeFailed
	mustRecord(t, s, regressed)
	if got, want := resolve(ubuntu), []string{"pinned=c@2/pin/", "latest=c@1/evidence/ch2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after c@2 regressed: %q, want %q", got, want)
	}

	// A verified parent fixes its children: its run linked ch1 (c@2) and ch2 (c@1).
	mustRecord(t, s, runOf("p", "par", 1, 5,
		memory.ChildExecution{Reference: "pinned", ExecutionID: "ch1"},
		memory.ChildExecution{Reference: "latest", ExecutionID: "ch2"}))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, path)
	want = []string{"pinned=c@2/pin/ch1", "latest=c@1/evidence/ch2"}
	if got := resolve(ubuntu); !reflect.DeepEqual(got, want) {
		t.Fatalf("under the parent's evidence (after reopen): %q, want %q", got, want)
	}
}
