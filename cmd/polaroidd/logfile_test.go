package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogFileIsRotatedAtItsBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polaroidd.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("o", 90)), 0o600); err != nil {
		t.Fatal(err)
	}
	var redirected []string
	f, err := openLogFile(path, 100, func(f *os.File) error { redirected = append(redirected, f.Name()); return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"first line\n", "second line\n"} {
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(path + ".1")
	cur, _ := os.ReadFile(path)
	if string(old) != strings.Repeat("o", 90) || string(cur) != "first line\nsecond line\n" {
		t.Fatalf("after rotation: %s.1 = %q, %s = %q", path, old, path, cur)
	}
	if len(redirected) != 2 {
		t.Fatalf("standard output followed %d files, want the original and the rotated one", len(redirected))
	}

	// A file already over the bound is rotated on open, so restarts in a loop
	// cannot grow it.
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 150)), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err = openLogFile(path, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if st, _ := os.Stat(path); st.Size() != 0 {
		t.Fatalf("an oversized log file was not rotated on open: %d bytes", st.Size())
	}
	if st, _ := os.Stat(path + ".1"); st.Size() != 150 || st.Mode().Perm() != 0o600 {
		t.Fatalf("rotated file: %d bytes, mode %o", st.Size(), st.Mode().Perm())
	}
}
