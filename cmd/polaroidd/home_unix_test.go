//go:build unix

package main

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// withUmask022 makes the test independent of the caller's umask: under 022
// files SQLite creates on its own would be 0644.
func withUmask022(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func defaultConfig(t *testing.T, home string) config {
	t.Helper()
	cfg, err := parseConfig([]string{"-addr", "127.0.0.1:0"}, func(string) (string, bool) { return "", false },
		func() (string, error) { return home, nil }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestDefaultDatabaseIsPrivateAndPersists(t *testing.T) {
	withUmask022(t)
	home := t.TempDir()
	cfg := defaultConfig(t, home)
	var logs bytes.Buffer
	base, stop := startConfig(t, cfg, &logs)
	if status, body := request(t, http.MethodPost, base+"/v1/procedures", createBody); status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	_, before := request(t, http.MethodGet, base+"/v1/procedures/by-key/demo.restart", "")

	// While the daemon runs, SQLite holds its -wal and -shm files.
	root := filepath.Join(home, ".polaroid")
	db := filepath.Join(root, "data", "polaroid.db")
	for path, want := range map[string]fs.FileMode{root: 0o700, filepath.Dir(db): 0o700, db: 0o600, db + "-wal": 0o600, db + "-shm": 0o600} {
		if got := modeOf(t, path); got != want {
			t.Errorf("%s: mode %o, want %o", path, got, want)
		}
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Errorf(".polaroid holds %d entries, want only data/ (backups/ is created by a backup)", len(entries))
	}
	if !strings.Contains(logs.String(), "db="+db+" db_source=default") || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("startup diagnostics: %s", logs.String())
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	restarted, _ := startConfig(t, cfg, io.Discard)
	if status, after := request(t, http.MethodGet, restarted+"/v1/procedures/by-key/demo.restart", ""); status != http.StatusOK || !bytes.Equal(after, before) {
		t.Fatalf("after restart: %d %s\nwant %s", status, after, before)
	}
	if got := modeOf(t, db); got != 0o600 {
		t.Fatalf("after restart the database is %o", got)
	}
}

// Existing entries keep their modes; the daemon reports the ones others can
// read, and never changes explicit paths.
func TestExistingPermissionsAreNotChanged(t *testing.T) {
	withUmask022(t)
	home := t.TempDir()
	root := filepath.Join(home, ".polaroid")
	db := filepath.Join(root, "data", "polaroid.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	_, stop := startConfig(t, defaultConfig(t, home), &logs)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]fs.FileMode{root: 0o755, filepath.Dir(db): 0o755, db: 0o644} {
		if got := modeOf(t, path); got != want {
			t.Errorf("%s: mode changed to %o, want %o", path, got, want)
		}
		if !strings.Contains(logs.String(), "path="+path+"\n") {
			t.Errorf("no warning for %s: %s", path, logs.String())
		}
	}

	explicit := filepath.Join(t.TempDir(), "shared.db")
	if err := os.WriteFile(explicit, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	_, stop = startConfig(t, config{addr: "127.0.0.1:0", dbPath: explicit, dbSource: dbFromFlag}, &logs)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, explicit); got != 0o644 || strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("explicit path: mode %o, logs %s", got, logs.String())
	}
}
