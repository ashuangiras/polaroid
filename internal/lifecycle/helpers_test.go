package lifecycle

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// builds holds two builds of polaroid and polaroidd from this checkout: the
// same revision, different bytes (-trimpath on and off), so installing one
// over the other is an upgrade.
var builds [2]string

func TestMain(m *testing.M) {
	if err := buildBinaries(); err != nil {
		fmt.Fprintln(os.Stderr, "build polaroid binaries:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(filepath.Dir(builds[0]))
	os.Exit(code)
}

func buildBinaries() error {
	ctx := context.Background()
	root, err := exec.CommandContext(ctx, "go", "env", "GOMOD").Output()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "polaroid-lifecycle-builds")
	if err != nil {
		return err
	}
	for i, flags := range [][]string{{"-trimpath"}, nil} {
		builds[i] = filepath.Join(dir, fmt.Sprintf("build %d", i))
		args := append(append([]string{"build"}, flags...), "-o", builds[i]+"/", "./cmd/polaroidd", "./cmd/polaroid")
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = filepath.Dir(strings.TrimSpace(string(root)))
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%w: %s", err, out)
		}
	}
	return nil
}

// procManager stands in for launchd or systemd in these tests: it runs the
// command line and HOME of the written unit definition, with no other
// environment, and stops it with SIGTERM. It is no evidence of how a real
// manager behaves; scripts/lifecycle-check.sh is (ADR-0026).
type procManager struct {
	t        *testing.T
	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{}
	exitCode int
	exited   bool

	checkErr, registerErr error
	startNothing          bool // claims to start but runs nothing
	registered            bool
	starts, stops         int
}

func (m *procManager) Kind() string                 { return "test" }
func (m *procManager) Check(context.Context) error  { return m.checkErr }
func (m *procManager) Reload(context.Context) error { return nil }
func (m *procManager) Unregister(context.Context, Layout) error {
	m.registered = false
	return nil
}

func (m *procManager) Register(context.Context, Layout) error {
	if m.registerErr != nil {
		return m.registerErr
	}
	m.registered = true
	return nil
}

func (m *procManager) Start(_ context.Context, l Layout) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts++
	if m.startNothing {
		return nil
	}
	def, err := os.ReadFile(l.DefinitionPath())
	if err != nil {
		return err
	}
	var argv []string
	var env []string
	for _, line := range strings.Split(string(def), "\n") {
		if v, ok := strings.CutPrefix(line, "ExecStart="); ok {
			argv = unquoteSystemd(m.t, v)
		}
		if v, ok := strings.CutPrefix(line, "Environment="); ok {
			env = unquoteSystemd(m.t, v)
		}
	}
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...)
	cmd.Env, cmd.Dir = env, "/"
	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd, m.done, m.exited = cmd, make(chan struct{}), false
	go func(cmd *exec.Cmd, done chan struct{}) {
		err := cmd.Wait()
		m.mu.Lock()
		m.exited, m.exitCode = true, cmd.ProcessState.ExitCode()
		if err == nil {
			m.exitCode = 0
		}
		m.mu.Unlock()
		close(done)
	}(cmd, m.done)
	return nil
}

func (m *procManager) Stop(context.Context, Layout) error {
	m.mu.Lock()
	cmd, done, exited := m.cmd, m.done, m.exited
	m.stops++
	m.startNothing = false
	m.mu.Unlock()
	if cmd != nil && !exited {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-done
	}
	m.mu.Lock()
	m.cmd = nil
	m.mu.Unlock()
	return nil
}

func (m *procManager) State(context.Context, Layout) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.startNothing && m.starts > 0:
		return State{Active: true, Detail: "starting"}, nil
	case m.cmd != nil && !m.exited:
		return State{Active: true, PID: m.cmd.Process.Pid, Detail: "running"}, nil
	case m.cmd != nil && m.exitCode != 0:
		return State{Failed: true, Detail: fmt.Sprintf("exited with status %d", m.exitCode)}, nil
	}
	return State{Detail: "inactive"}, nil
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// newTestService is an isolated installation: a temporary home whose path
// has spaces, the Linux layout, and the stand-in manager.
func newTestService(t *testing.T) (*Service, *procManager) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home with spaces")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := NewLayout("linux", home, DefaultServiceName)
	if err != nil {
		t.Fatal(err)
	}
	mgr := &procManager{t: t}
	s := &Service{Layout: l, Manager: mgr, Runner: ExecRunner{}, Listener: Listener, Probe: Health, Wait: 20 * time.Second, Poll: 50 * time.Millisecond}
	t.Cleanup(func() { _ = mgr.Stop(context.Background(), l) })
	return s, mgr
}

// outcome is an operation's result and the state it must reach.
type outcome struct {
	st   Status
	err  error
	want string
}

// firstOf expects (Status, error) to have reached Running.
func firstOf(st Status, err error) outcome { return outcome{st, err, Running} }

// of expects (Status, error) to have reached NotInstalled.
func (outcome) of(st Status, err error) outcome { return outcome{st, err, NotInstalled} }

func mustState(t *testing.T, o outcome) Status {
	t.Helper()
	return mustBe(t, o.st, o.err, o.want)
}

func mustBe(t *testing.T, st Status, err error, want string) Status {
	t.Helper()
	if err != nil || st.State != want {
		t.Fatalf("state %s (%s), err %v; want %s", st.State, st.Detail, err, want)
	}
	return st
}
