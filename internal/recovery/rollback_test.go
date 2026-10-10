package recovery

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ashuangiras/polaroid/internal/lifecycle"
	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	httpapi "github.com/ashuangiras/polaroid/internal/transport/http"
)

// The test binary doubles as the managed daemon: with these variables set it
// serves the catalog over HTTP, like polaroidd, until SIGTERM.
const (
	holderDB   = "POLAROID_RECOVERY_TEST_DB"
	holderAddr = "POLAROID_RECOVERY_TEST_ADDR"
)

func TestMain(m *testing.M) {
	if db := os.Getenv(holderDB); db != "" {
		os.Exit(hold(db, os.Getenv(holderAddr)))
	}
	os.Exit(m.Run())
}

func hold(db, addr string) int {
	store, err := sqlite.Open(context.Background(), db)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	srv := &http.Server{Handler: httpapi.NewHandler(memory.NewService(store), slog.New(slog.DiscardHandler)), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	fmt.Println("ready")
	<-stop
	_ = srv.Close()
	if err := store.Close(); err != nil {
		return 1
	}
	return 0
}

// heldManager runs the daemon above as the "managed" process. Starts listed in
// unhealthy fail the health check; stops from failStopsFrom on fail and leave
// the process running, as a manager that lost control of it would.
type heldManager struct {
	db, addr      string
	unhealthy     map[int]bool
	failStopsFrom int

	mu     sync.Mutex
	cmd    *exec.Cmd
	done   chan struct{}
	starts int
	stops  int
	opened map[int]os.FileInfo // the catalog file each start opened
	all    []*exec.Cmd
}

func (m *heldManager) Kind() string                                     { return "test" }
func (m *heldManager) Check(context.Context) error                      { return nil }
func (m *heldManager) Register(context.Context, lifecycle.Layout) error { return nil }
func (m *heldManager) Unregister(context.Context, lifecycle.Layout) error {
	return nil
}
func (m *heldManager) Reload(context.Context) error { return nil }

func (m *heldManager) Start(ctx context.Context, _ lifecycle.Layout) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts++
	cmd := exec.CommandContext(context.WithoutCancel(ctx), os.Args[0], "-test.run=^$") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), holderDB+"="+m.db, holderAddr+"="+m.addr)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	m.all = append(m.all, cmd)
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		_ = cmd.Wait()
		return fmt.Errorf("the daemon did not start: %q %w", line, err)
	}
	st, err := os.Stat(m.db)
	if err != nil {
		return err
	}
	if m.opened == nil {
		m.opened = map[int]os.FileInfo{}
	}
	m.opened[m.starts] = st
	m.cmd, m.done = cmd, make(chan struct{})
	go func(cmd *exec.Cmd, done chan struct{}) { _ = cmd.Wait(); close(done) }(cmd, m.done)
	return nil
}

func (m *heldManager) Stop(context.Context, lifecycle.Layout) error {
	m.mu.Lock()
	m.stops++
	cmd, done, fail := m.cmd, m.done, m.failStopsFrom > 0 && m.stops >= m.failStopsFrom
	m.mu.Unlock()
	if fail {
		return errors.New("injected: the manager could not stop the process")
	}
	if cmd != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-done
	}
	return nil
}

func (m *heldManager) alive() (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd == nil {
		return 0, false
	}
	select {
	case <-m.done:
		return 0, false
	default:
		return m.cmd.Process.Pid, true
	}
}

func (m *heldManager) State(context.Context, lifecycle.Layout) (lifecycle.State, error) {
	if pid, ok := m.alive(); ok {
		return lifecycle.State{Loaded: true, Active: true, PID: pid, Detail: "running"}, nil
	}
	return lifecycle.State{Loaded: true, Detail: "inactive"}, nil
}

// managedService installs a lifecycle.Service for the catalog at db whose
// manager runs heldManager, and starts it.
func managedService(t *testing.T, db string) (*lifecycle.Service, *heldManager) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	l, err := lifecycle.NewLayout("linux", filepath.Join(t.TempDir(), "home"), "polaroid-recovery-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(lifecycle.Manifest{Format: 1, Manager: "test", ServiceName: l.ServiceName, Addr: addr, DB: db})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(l.StateDir, "install.json"), manifest)
	m := &heldManager{db: db, addr: addr, unhealthy: map[int]bool{}}
	t.Cleanup(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, c := range m.all {
			_ = c.Process.Kill()
		}
	})
	_, portText, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portText)
	svc := &lifecycle.Service{Layout: l, Manager: m, Wait: 5 * time.Second, Poll: 10 * time.Millisecond,
		Listener: func(p int) (lifecycle.Process, error) {
			if pid, ok := m.alive(); ok && p == port {
				return lifecycle.Process{PID: pid, Command: "polaroidd"}, nil
			}
			return lifecycle.Process{}, errors.New("no listener")
		},
		Probe: func(ctx context.Context, addr string) error {
			m.mu.Lock()
			bad := m.unhealthy[m.starts]
			m.mu.Unlock()
			if bad {
				return errors.New("injected: not healthy")
			}
			return lifecycle.Health(ctx, addr)
		}}
	if st, err := svc.Start(ctx); err != nil || st.State != lifecycle.Running {
		t.Fatalf("start the managed service: %+v %v", st, err)
	}
	return svc, m
}

func get(t *testing.T, addr, path string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestRollbackNeedsAConfirmedShutdownOfTheManagedProcess(t *testing.T) {
	setup := func(t *testing.T) (string, string, string, string) {
		path, _, _ := stoppedCatalog(t)
		dir := spaced(t, "backups")
		b := mustBackup(t, path, dir)
		a := serve(t, path)
		a.post("/v1/procedures", procedureBody("recovery.current", ""))
		a.close()
		return path, dir, b.Path, digest(t, path)
	}

	t.Run("the failed daemon cannot be stopped: its catalog is not replaced under it", func(t *testing.T) {
		path, dir, backup, before := setup(t)
		svc, m := managedService(t, path)
		m.unhealthy[2], m.failStopsFrom = true, 2
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		if err == nil || res.Outcome != RollbackBlocked || !strings.Contains(err.Error(), "could not stop the process") {
			t.Fatalf("restore: outcome %s, %v", res.Outcome, err)
		}
		pid, alive := m.alive()
		if !alive {
			t.Fatal("the failed daemon is gone; the scenario needs it running")
		}
		now, err := os.Stat(path)
		if err != nil || !os.SameFile(now, m.opened[2]) {
			t.Fatalf("the catalog the live daemon %d opened is no longer at %s", pid, path)
		}
		if len(sqlite.Sidecars(path)) == 0 || digest(t, res.Original) != before || !Inspect(ctx, res.RecoveryBackup).Valid {
			t.Fatalf("artifacts: sidecars %v, original intact %v, recovery backup %q", sqlite.Sidecars(path), digest(t, res.Original) == before, res.RecoveryBackup)
		}
		// The live daemon keeps writing; the write must land in the catalog at the destination.
		if code := get(t, m.addr, "/v1/procedures/by-key/recovery.current"); code != http.StatusNotFound {
			t.Fatalf("the live daemon does not serve the restored snapshot: %d", code)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+m.addr+"/v1/procedures", strings.NewReader(procedureBody("recovery.after-failure", "")))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusCreated {
			t.Fatalf("write through the live daemon: %v %v", resp, err)
		}
		_ = resp.Body.Close()
		m.mu.Lock()
		m.failStopsFrom = 0
		m.mu.Unlock()
		if st, err := svc.Stop(ctx); err != nil || st.State != lifecycle.Stopped {
			t.Fatalf("stop: %+v %v", st, err)
		}
		a := serve(t, path)
		if code, _ := a.call(http.MethodGet, "/v1/procedures/by-key/recovery.after-failure", ""); code != http.StatusOK {
			t.Errorf("the live daemon's write is not in %s: %d", path, code)
		}
		a.close()
	})
	t.Run("shutdown confirmed: the original is put back and served", func(t *testing.T) {
		path, dir, backup, before := setup(t)
		svc, m := managedService(t, path)
		m.unhealthy[2] = true
		res, err := Restore(ctx, RestoreRequest{Backup: backup, Destination: path, BackupDir: dir, Service: svc, Replace: true})
		if err == nil || res.Outcome != RolledBack || !strings.Contains(err.Error(), "the service runs on it") || res.Service == nil || res.Service.State != lifecycle.Running {
			t.Fatalf("restore: outcome %s, %v, %+v", res.Outcome, err, res.Service)
		}
		if code := get(t, m.addr, "/v1/procedures/by-key/recovery.current"); code != http.StatusOK {
			t.Errorf("the restarted daemon does not serve the original catalog: %d", code)
		}
		if now, err := os.Stat(path); err != nil || !os.SameFile(now, m.opened[3]) || os.SameFile(now, m.opened[2]) {
			t.Error("the restarted daemon does not use the catalog that was put back")
		}
		if len(res.Kept) != 2 || !strings.Contains(res.Kept[0], ".failed-restore-") {
			t.Errorf("kept %v", res.Kept)
		}
		if _, err := svc.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		if digest(t, path) != before {
			t.Error("the catalog put back differs from the original")
		}
	})
}
