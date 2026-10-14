package reasoning_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ashuangiras/polaroid/internal/knowledge"
	"github.com/ashuangiras/polaroid/internal/reasoning"
)

// BenchmarkResolveMissingContext measures what completeness costs: every
// context name a rule reads and the caller omits multiplies the
// hypothetical evaluations, up to the 64 combinations Resolve examines; past
// that the result is reported incomplete instead. Each name here has two
// values a rule compares it with, so m names need 2^m evaluations; extra
// concepts grow the facts each evaluation reads.
func BenchmarkResolveMissingContext(b *testing.B) {
	for _, size := range []int{0, 500} {
		for _, missing := range []int{0, 1, 3, 6, 7} {
			b.Run(fmt.Sprintf("records=+%d/missing=%d", size, missing), func(b *testing.B) {
				w := newWorld(b)
				var extra []*knowledge.Record
				for i := range size {
					c := &knowledge.Record{Type: knowledge.TypeConcept, Key: fmt.Sprintf("bench.concept.%d", i), Label: "bench"}
					c.ClassifiedAs = []string{"domain.software.go"}
					extra = append(extra, c)
				}
				for i := range missing {
					for _, v := range []string{"a", "b"} {
						r := &knowledge.Record{Type: knowledge.TypeRule, Key: fmt.Sprintf("bench.rule.%d.%s", i, v), Label: "bench",
							Statement: "Bench rule.", Revision: 1, Waivable: true, ClassifiedAs: []string{"domain.software.go"},
							Program: fmt.Sprintf(`obligation(P, "check.vet") :- record(P, "Procedure"), in_domain(P, "domain.software.go"), context("bench%d", %q).`, i, v)}
						extra = append(extra, r)
					}
				}
				if len(extra) > 0 {
					w.publish(extra...)
				}
				cat, err := w.kernel.At(context.Background(), w.snap)
				if err != nil {
					b.Fatal(err)
				}
				// concurrency is given, so only the bench names are missing.
				req := reasoning.Request{Goal: "go.verify", Context: map[string]string{"concurrency": "false"}, At: at}
				r, err := reasoning.Resolve(context.Background(), cat, ev, w.trust, req)
				if err != nil {
					b.Fatal(err)
				}
				if r.Complete != (missing < 7) {
					b.Fatalf("missing=%d complete=%v incomplete=%v", missing, r.Complete, r.Incomplete)
				}
				b.ResetTimer()
				for b.Loop() {
					if _, err := reasoning.Resolve(context.Background(), cat, ev, w.trust, req); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
