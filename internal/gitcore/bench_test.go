package gitcore

import (
	"context"
	"fmt"
	"testing"
)

// go test -run '^$' -bench . -benchtime 20x ./internal/gitcore/

func benchRepo(b *testing.B, files int) (*Repository, Snapshot, string) {
	b.Helper()
	dir := b.TempDir()
	r, err := Open(dir, Options{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = r.Close() })
	changes := make([]Change, files)
	for i := range changes {
		changes[i] = put(fmt.Sprintf("procedures/t%d/g%d/p%d.json", i/100, i/10%10, i%10), fmt.Sprintf(`{"key":"p%d","steps":["build","vet","test"]}`, i))
	}
	s, err := r.Publish(context.Background(), Publication{Author: "bench", Subject: "initial", Changes: changes})
	if err != nil {
		b.Fatal(err)
	}
	return r, s, dir
}

func BenchmarkPublishOneFile(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprint("files=", n), func(b *testing.B) {
			r, s, _ := benchRepo(b, n)
			b.ResetTimer()
			for i := range b.N {
				var err error
				s, err = r.Publish(context.Background(), Publication{Expected: s, Author: "bench", Subject: "one",
					Changes: []Change{put("procedures/t0/g0/p1.json", fmt.Sprint(`{"rev":`, i, `}`))}})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkReadActive(b *testing.B) {
	r, _, _ := benchRepo(b, 1000)
	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		s, err := r.Active(ctx)
		if err == nil {
			var t *Tree
			if t, err = r.Tree(ctx, s); err == nil {
				_, err = t.Read("procedures/t5/g5/p5.json")
			}
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkColdStart opens the repository and reads one file: the snapshot
// index is rebuilt from the tree, as after a restart.
func BenchmarkColdStart(b *testing.B) {
	_, s, dir := benchRepo(b, 1000)
	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		r, err := Open(dir, Options{})
		if err != nil {
			b.Fatal(err)
		}
		t, err := r.Tree(ctx, s)
		if err == nil {
			_, err = t.Read("procedures/t5/g5/p5.json")
		}
		_ = r.Close()
		if err != nil {
			b.Fatal(err)
		}
	}
}
