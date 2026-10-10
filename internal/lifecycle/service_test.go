package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const procedure = `{"canonical_key":"lifecycle.check","version":{"philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}}`

func call(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func sha(t *testing.T, path string) string {
	t.Helper()
	s, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestInstallUpgradeUninstallPreserveRecords(t *testing.T) {
	ctx := context.Background()
	s, mgr := newTestService(t)
	l := s.Layout
	addr := freeAddr(t)

	st := mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[0], Addr: addr})))
	if st.Action != "installed" || st.Build == nil || st.Build.Revision == "" || st.Database != l.DataDB || !mgr.registered {
		t.Fatalf("install: %+v", st)
	}
	for path, want := range map[string]os.FileMode{l.Polaroid(): 0o755, l.Polaroidd(): 0o755, l.StateDir: 0o700, l.manifestPath(): 0o600, l.DataDB: 0o600} {
		if got := modeOf(t, path); got != want {
			t.Errorf("%s: mode %o, want %o", path, got, want)
		}
	}
	if sha(t, l.Polaroidd()) != sha(t, filepath.Join(builds[0], "polaroidd")) {
		t.Fatal("the installed polaroidd is not the source one")
	}
	if code, body := call(t, http.MethodPost, "http://"+addr+"/v1/procedures", procedure); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	_, before := call(t, http.MethodGet, "http://"+addr+"/v1/procedures/by-key/lifecycle.check", "")

	// Repeating it changes nothing.
	pid := st.PID
	st = mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[0]})))
	if st.Action != "unchanged; already running" || st.PID != pid || mgr.stops != 0 {
		t.Fatalf("repeated install: %+v, %d stops", st, mgr.stops)
	}

	// An upgrade stops, replaces, keeps the previous build, and restarts.
	st = mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[1]})))
	if st.Action != "upgraded" || st.PID == pid || mgr.stops != 1 || st.Previous != l.previousDir() || st.Endpoint != "http://"+addr {
		t.Fatalf("upgrade: %+v, %d stops", st, mgr.stops)
	}
	if sha(t, l.Polaroidd()) != sha(t, filepath.Join(builds[1], "polaroidd")) || sha(t, filepath.Join(l.previousDir(), "polaroidd")) != sha(t, filepath.Join(builds[0], "polaroidd")) {
		t.Fatal("upgrade did not install build 1 and keep build 0 in previous/")
	}
	if _, after := call(t, http.MethodGet, "http://"+addr+"/v1/procedures/by-key/lifecycle.check", ""); after != before {
		t.Fatalf("after the upgrade: %s\nwant %s", after, before)
	}

	// Uninstall removes what install wrote, and nothing else.
	if err := os.WriteFile(filepath.Join(l.Home, ".polaroid", "backups-marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	st = mustState(t, outcome{}.of(s.Uninstall(ctx)))
	for _, p := range []string{l.Polaroid(), l.Polaroidd(), l.DefinitionPath(), l.StateDir} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("uninstall left %s", p)
		}
	}
	for _, p := range []string{l.DataDB, filepath.Join(l.Home, ".polaroid", "backups-marker"), l.BinDir} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("uninstall removed %s", p)
		}
	}
	if st.State != NotInstalled || mgr.registered {
		t.Fatalf("after uninstall: %+v", st)
	}
	if st, err := s.Uninstall(ctx); err != nil || st.Action != "nothing to uninstall" {
		t.Fatalf("repeated uninstall: %+v, %v", st, err)
	}

	// Reinstalling serves the same catalog.
	mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[0], Addr: addr})))
	if _, after := call(t, http.MethodGet, "http://"+addr+"/v1/procedures/by-key/lifecycle.check", ""); after != before {
		t.Fatalf("after reinstalling: %s\nwant %s", after, before)
	}
}

// firstOf adapts (Status, error) to mustState's want of running; see helpers_test.go.

func TestRepeatedStartStopRestart(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	if _, err := s.Start(ctx); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("start before install: %v", err)
	}
	if _, err := s.Stop(ctx); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("stop before install: %v", err)
	}
	if st := s.Status(ctx); st.State != NotInstalled || st.ExitCode() != 4 {
		t.Fatalf("status before install: %+v", st)
	}
	mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[0], Addr: freeAddr(t)})))
	for range 2 {
		st, err := s.Stop(ctx)
		mustBe(t, st, err, Stopped)
		if st.ExitCode() != 3 || st.PID != 0 {
			t.Fatalf("stopped: %+v", st)
		}
	}
	first := mustState(t, firstOf(s.Start(ctx)))
	if st, err := s.Start(ctx); err != nil || st.Action != "already running" || st.PID != first.PID {
		t.Fatalf("second start: %+v, %v", st, err)
	}
	st := mustState(t, firstOf(s.Restart(ctx)))
	if st.PID == first.PID || st.Action != "restarted" || st.ExitCode() != 0 {
		t.Fatalf("restart: %+v", st)
	}
}

func TestInstallRefusesFilesItDoesNotOwn(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	l := s.Layout
	if err := os.MkdirAll(l.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Polaroidd(), []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Install(ctx, InstallOptions{From: builds[0], Addr: freeAddr(t)}); !errors.Is(err, ErrNotOwned) || !strings.Contains(err.Error(), l.Polaroidd()) {
		t.Fatalf("install over an unrelated polaroidd: %v", err)
	}
	if b, _ := os.ReadFile(l.Polaroidd()); string(b) != "#!/bin/sh\necho mine\n" {
		t.Fatal("the unrelated executable was changed")
	}
	for _, p := range []string{l.Polaroid(), l.DefinitionPath(), l.manifestPath()} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a refused install wrote %s", p)
		}
	}
	if st := s.Status(ctx); st.State != NotInstalled || len(st.Problems) != 1 {
		t.Fatalf("status: %+v", st)
	}
	_ = os.Remove(l.Polaroidd())

	// A definition changed by hand is not overwritten or removed either.
	mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[0], Addr: freeAddr(t)})))
	edited := append(must(os.ReadFile(l.DefinitionPath())), []byte("# edited\n")...)
	if err := os.WriteFile(l.DefinitionPath(), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Install(ctx, InstallOptions{From: builds[1]}); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("upgrade over an edited definition: %v", err)
	}
	if sha(t, l.Polaroidd()) != sha(t, filepath.Join(builds[0], "polaroidd")) {
		t.Fatal("a refused upgrade replaced polaroidd")
	}
	st, err := s.Uninstall(ctx)
	if err != nil || !bytes.Equal(must(os.ReadFile(l.DefinitionPath())), edited) || len(st.Problems) != 1 {
		t.Fatalf("uninstall with an edited definition: %+v, %v", st, err)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestEndpointConflictsAreReportedNotClaimed(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	addr := freeAddr(t)
	other, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if _, err := s.Install(ctx, InstallOptions{From: builds[0], Addr: addr}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "process "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("install on an occupied endpoint: %v", err)
	}
	if _, err := os.Stat(s.Layout.manifestPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused install was recorded")
	}
	_ = other.Close()

	mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[0], Addr: addr})))
	if _, err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	other, err = (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	st := s.Status(ctx)
	if st.State != Stopped || st.Conflict == nil || st.Conflict.PID != os.Getpid() {
		t.Fatalf("status with an unmanaged listener: %+v", st)
	}
	if _, err := s.Start(ctx); !errors.Is(err, ErrConflict) {
		t.Fatalf("start on an occupied endpoint: %v", err)
	}
	if conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr); err != nil {
		t.Fatalf("the unmanaged listener was disturbed: %v", err)
	} else {
		_ = conn.Close()
	}
}

func TestReadinessTimeoutAndStartupFailure(t *testing.T) {
	ctx := context.Background()
	s, mgr := newTestService(t)
	mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[0], Addr: freeAddr(t)})))
	if _, err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// A manager that reports starting but never runs anything.
	s.Wait = time.Second
	mgr.startNothing = true
	began := time.Now()
	st, err := s.Start(ctx)
	if !errors.Is(err, ErrStartFailed) || st.State != Failed || !strings.Contains(st.Detail, "not healthy within 1s") || time.Since(began) > 10*time.Second {
		t.Fatalf("readiness timeout: %+v, %v after %s", st, err, time.Since(began))
	}
	if !strings.Contains(st.Detail, "was stopped so that it does not keep restarting") || mgr.startNothing {
		t.Fatalf("a failed start was not stopped: %+v", st)
	}
	if st := s.Status(ctx); st.State != Failed || st.ExitCode() != 1 || !strings.Contains(st.Detail, "the last start failed") {
		t.Fatalf("status after a failed start: %+v", st)
	}
	if st, err := s.Stop(ctx); err != nil || st.State != Stopped {
		t.Fatalf("an explicit stop clears the failure: %+v, %v", st, err)
	}

	// polaroidd itself fails: its database path is a directory.
	s.Wait = 20 * time.Second
	if err := os.MkdirAll(filepath.Join(s.Layout.Home, "not a file"), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err = s.Install(ctx, InstallOptions{From: builds[0], DB: filepath.Join(s.Layout.Home, "not a file")})
	if !errors.Is(err, ErrStartFailed) || st.State != Failed || !strings.Contains(st.Action, "start failed") || !strings.Contains(st.Detail, "exited with status 1") {
		t.Fatalf("a daemon that cannot start: %+v, %v", st, err)
	}
	if !strings.Contains(st.Detail, "refuses a database this build has migrated") {
		t.Fatalf("no recovery advice: %s", st.Detail)
	}
}

func TestInstallRollsBackAfterPartialFailure(t *testing.T) {
	ctx := context.Background()
	s, mgr := newTestService(t)
	l := s.Layout
	addr := freeAddr(t)

	// A fresh install whose registration fails leaves nothing behind.
	mgr.registerErr = errors.New("register failed")
	if _, err := s.Install(ctx, InstallOptions{From: builds[0], Addr: addr}); err == nil || !strings.Contains(err.Error(), "register failed") {
		t.Fatalf("install: %v", err)
	}
	for _, p := range []string{l.Polaroid(), l.Polaroidd(), l.DefinitionPath(), l.manifestPath()} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("rollback left %s", p)
		}
	}
	mgr.registerErr = nil

	// An upgrade that fails halfway restores the running installation.
	st := mustState(t, firstOf(s.Install(ctx, InstallOptions{From: builds[0], Addr: addr})))
	if code, _ := call(t, http.MethodPost, "http://"+addr+"/v1/procedures", procedure); code != http.StatusCreated {
		t.Fatal(code)
	}
	defDir := filepath.Dir(l.DefinitionPath())
	if err := os.Chmod(defDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(defDir, 0o755) })
	upgrade := InstallOptions{From: builds[1], DB: filepath.Join(l.Home, ".polaroid", "data", "other.db")} // the definition changes too
	after, err := s.Install(ctx, upgrade)
	if err == nil || !strings.Contains(err.Error(), "previous installation was restored") {
		t.Fatalf("upgrade with an unwritable definition directory: %v", err)
	}
	if after.State != Running || after.PID == st.PID || sha(t, l.Polaroidd()) != sha(t, filepath.Join(builds[0], "polaroidd")) || sha(t, l.Polaroid()) != sha(t, filepath.Join(builds[0], "polaroid")) {
		t.Fatalf("after rollback: %+v", after)
	}
	if m, _ := s.manifest(); m.DB != l.DataDB {
		t.Fatalf("the record was not restored: %+v", m)
	}
	if code, _ := call(t, http.MethodGet, "http://"+addr+"/v1/procedures/by-key/lifecycle.check", ""); code != http.StatusOK {
		t.Fatalf("the restored service lost the record: %d", code)
	}
	_ = os.Chmod(defDir, 0o755)
	if st := s.Status(ctx); len(st.Problems) != 0 {
		t.Fatalf("the restored installation differs from its record: %v", st.Problems)
	}
}

func TestInstallChecksItsInputs(t *testing.T) {
	ctx := context.Background()
	s, mgr := newTestService(t)
	fake := func(name, revision string) string {
		dir := t.TempDir()
		for _, n := range []string{"polaroid", "polaroidd"} {
			rev := "abc"
			if n == name {
				rev = revision
			}
			script := "#!/bin/sh\necho '{\"name\":\"" + n + "\",\"revision\":\"" + rev + "\",\"modified\":false,\"go\":\"go1\"}'\n"
			if err := os.WriteFile(filepath.Join(dir, n), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	for name, tc := range map[string]struct {
		o    InstallOptions
		want string
	}{
		"no source":           {InstallOptions{}, "-from is required"},
		"empty source":        {InstallOptions{From: t.TempDir()}, "no executable polaroidd there"},
		"mismatched pair":     {InstallOptions{From: fake("polaroid", "def")}, "install a matching pair"},
		"no revision":         {InstallOptions{From: fake("polaroid", "")}, "no embedded VCS revision"},
		"not loopback":        {InstallOptions{From: builds[0], Addr: "0.0.0.0:7417"}, "loopback only"},
		"no fixed port":       {InstallOptions{From: builds[0], Addr: "127.0.0.1:0"}, "fixed port"},
		"relative database":   {InstallOptions{From: builds[0], DB: "rel.db"}, "must be an absolute path"},
		"unavailable manager": {InstallOptions{From: builds[0]}, "service manager unavailable"},
	} {
		mgr.checkErr = nil
		if name == "unavailable manager" {
			mgr.checkErr = ErrManagerUnavailable
		}
		if _, err := s.Install(ctx, tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
		if _, err := os.Stat(s.Layout.BinDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: a refused install wrote %s", name, s.Layout.BinDir)
		}
	}
}
