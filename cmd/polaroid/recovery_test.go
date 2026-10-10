package main

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/lifecycle"
	"github.com/ashuangiras/polaroid/internal/recovery"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
)

// withRecoveryHome isolates the lifecycle service and the home directory
// that backup and restore default to.
func withRecoveryHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home with spaces")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	oldService, oldHome := openService, userHome
	openService = func() (*lifecycle.Service, error) {
		l, err := lifecycle.NewLayout("linux", home, lifecycle.DefaultServiceName)
		if err != nil {
			return nil, err
		}
		return &lifecycle.Service{Layout: l, Manager: idleManager{}, Runner: lifecycle.ExecRunner{},
			Listener: func(int) (lifecycle.Process, error) { return lifecycle.Process{}, nil },
			Probe:    func(context.Context, string) error { return nil }}, nil
	}
	userHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { openService, userHome = oldService, oldHome })
	return home
}

func runCLIEnv(env map[string]string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(""), &stdout, &stderr, func(k string) string { return env[k] })
	return code, stdout.String(), stderr.String()
}

func newCatalog(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func decode[T any](t *testing.T, out string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not the expected JSON: %v\n%s", err, out)
	}
	return v
}

func TestBackupInspectRestoreCommands(t *testing.T) {
	home := withRecoveryHome(t)
	_, help, _ := runCLI("help")
	for _, name := range []string{"backup [-db FILE] [-dir DIR]", "inspect-backup BACKUP", "restore [-db FILE] [-dir DIR] [-plan] [-replace]"} {
		if !strings.Contains(help, "\n  "+name) {
			t.Errorf("help does not list %q", name)
		}
	}
	catalog := filepath.Join(home, "a catalog", "polaroid.db")
	newCatalog(t, catalog)

	code, out, errOut := runCLI("backup", "-db", catalog)
	b := decode[recovery.BackupResult](t, out)
	if code != exitOK || filepath.Dir(b.Path) != filepath.Join(home, ".polaroid", "backups") || b.Metadata.Source.Path != catalog || !strings.Contains(errOut, "backed up") {
		t.Fatalf("backup: %d %s %s", code, out, errOut)
	}
	if code, out, _ := runCLI("inspect-backup", b.Path); code != exitOK || !decode[recovery.Report](t, out).Valid {
		t.Fatalf("inspect-backup: %d %s", code, out)
	}

	code, out, errOut = runCLI("restore", "-db", catalog, b.Path)
	if r := decode[recovery.Result](t, out); code != exitRefused || r.Outcome != recovery.Unchanged || !strings.Contains(errOut, "-replace") {
		t.Fatalf("restore over an existing catalog without -replace: %d %s %s", code, out, errOut)
	}
	code, out, errOut = runCLI("restore", "-db", catalog, "-plan", b.Path)
	if p := decode[recovery.Plan](t, out); code != exitOK || !p.Ready || !p.RequiresReplace || p.Destination.Managed || !strings.Contains(errOut, "needs -replace") {
		t.Fatalf("restore -plan: %d %s %s", code, out, errOut)
	}
	code, out, errOut = runCLI("restore", "-db", catalog, "-replace", b.Path)
	if r := decode[recovery.Result](t, out); code != exitOK || r.Outcome != recovery.Restored || r.RecoveryBackup == "" || !strings.Contains(errOut, "restored") {
		t.Fatalf("restore -replace: %d %s %s", code, out, errOut)
	}
	fresh := filepath.Join(home, "new place", "polaroid.db")
	if code, out, _ := runCLI("restore", "-db", fresh, "-dir", filepath.Join(home, "elsewhere"), b.Path); code != exitOK || decode[recovery.Result](t, out).Outcome != recovery.Restored {
		t.Fatalf("restore into a new catalog: %d %s", code, out)
	}

	broken := filepath.Join(home, "broken")
	if err := os.Mkdir(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := runCLI("inspect-backup", broken); code != exitFailure || decode[recovery.Report](t, out).Valid || !strings.Contains(errOut, "not a valid backup") {
		t.Errorf("inspect-backup of an incomplete backup: %d %s %s", code, out, errOut)
	}
	if code, out, _ := runCLI("restore", "-db", catalog, "-plan", broken); code != exitRefused || decode[recovery.Plan](t, out).Ready {
		t.Errorf("restore -plan of an incomplete backup: %d %s", code, out)
	}
	if code, _, _ := runCLI("restore", "-db", catalog, "-replace", broken); code != exitRefused {
		t.Errorf("restore of an incomplete backup: %d", code)
	}
	if code, _, errOut := runCLI("backup", "-db", filepath.Join(home, "missing.db")); code != exitFailure || !strings.Contains(errOut, "no catalog to back up") {
		t.Errorf("backup of a missing catalog: %d %s", code, errOut)
	}

	for _, args := range [][]string{{"backup", "extra"}, {"inspect-backup"}, {"inspect-backup", "a", "b"}, {"restore"}, {"restore", "-bogus", "x"}, {"inspect-backup", "-db", "x", "y"}} {
		if code, _, _ := runCLI(args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestBackupAndRestoreSelectTheCatalog(t *testing.T) {
	home := withRecoveryHome(t)
	flagged := filepath.Join(home, "flag", "polaroid.db")
	env := filepath.Join(home, "env", "polaroid.db")
	installed := filepath.Join(home, "installed", "polaroid.db")
	for _, p := range []string{flagged, env, installed} {
		newCatalog(t, p)
	}
	source := func(env map[string]string, args ...string) (string, bool) {
		t.Helper()
		code, out, errOut := runCLIEnv(env, append([]string{"backup"}, args...)...)
		if code != exitOK {
			t.Fatalf("backup %v: %d %s", args, code, errOut)
		}
		s := decode[recovery.BackupResult](t, out).Metadata.Source
		return s.Path, s.Managed
	}
	// Nothing installed and nothing named: the per-user catalog, which is missing.
	if code, _, errOut := runCLI("backup"); code != exitFailure || !strings.Contains(errOut, filepath.Join(home, ".polaroid", "data", "polaroid.db")) {
		t.Errorf("backup with no catalog named: %d %s", code, errOut)
	}
	if p, _ := source(map[string]string{"POLAROID_DB": env}); p != env {
		t.Errorf("POLAROID_DB: backed up %s", p)
	}
	if p, _ := source(map[string]string{"POLAROID_DB": env}, "-db", flagged); p != flagged {
		t.Errorf("-db over POLAROID_DB: backed up %s", p)
	}

	// An installation's catalog is the default, and it is managed.
	state := filepath.Join(home, ".local", "state", "polaroid")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	m, _ := json.Marshal(lifecycle.Manifest{Format: 1, Manager: "test", ServiceName: "polaroid", Addr: "127.0.0.1:1", DB: installed})
	if err := os.WriteFile(filepath.Join(state, "install.json"), m, 0o600); err != nil {
		t.Fatal(err)
	}
	if p, managed := source(nil); p != installed || !managed {
		t.Errorf("installed: backed up %s, managed %v", p, managed)
	}
	if p, managed := source(nil, "-db", flagged); p != flagged || managed {
		t.Errorf("-db with an installation: backed up %s, managed %v", p, managed)
	}
	_, out, _ := runCLI("backup")
	b := decode[recovery.BackupResult](t, out)
	code, out, _ := runCLI("restore", "-plan", b.Path)
	p := decode[recovery.Plan](t, out)
	if code != exitOK || !p.Destination.Managed || p.Service == nil || p.Service.State != lifecycle.Stopped || !strings.Contains(strings.Join(p.Actions, "\n"), "leave the managed service stopped") {
		t.Errorf("restore -plan of the managed catalog: %d %s", code, out)
	}
}
