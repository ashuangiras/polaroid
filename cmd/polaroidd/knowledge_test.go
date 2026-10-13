package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Daemons starting together on one knowledge directory must agree on a
// single operator token, the one in the file, readable only by its owner.
func TestOperatorTokenConcurrentFirstStartAgree(t *testing.T) {
	for round := range 50 {
		path := filepath.Join(t.TempDir(), "knowledge-operator-token")
		const n = 8
		tokens := make([]string, n)
		errs := make([]error, n)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				<-start
				tokens[i], errs[i] = loadOrCreateOperatorToken(path)
			})
		}
		close(start)
		wg.Wait()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.TrimSpace(string(b))
		for i := range n {
			if errs[i] != nil {
				t.Fatalf("round %d, start %d: %v", round, i, errs[i])
			}
			if tokens[i] != want {
				t.Fatalf("round %d, start %d: token %q, file holds %q", round, i, tokens[i], want)
			}
		}
		if len(want) != 64 {
			t.Fatalf("token %q: want 64 hex characters", want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("token file mode %o, want 600", mode)
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("round %d: leftover files beside the token: %v", round, entries)
		}
	}
}

func TestOperatorTokenReusedAndEmptyRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge-operator-token")
	if err := os.WriteFile(path, []byte("existing-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := loadOrCreateOperatorToken(path); err != nil || tok != "existing-token" {
		t.Fatalf("got %q, %v; want the existing token", tok, err)
	}
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateOperatorToken(path); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("empty token file: got %v, want an error naming it empty", err)
	}
}
