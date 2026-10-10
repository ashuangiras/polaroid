package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

// builds holds two builds of polaroid and polaroidd, A and B, from two commits
// of a snapshot of this working tree, so they differ in bytes and revision
// and installing one over the other is an upgrade. broken is a third commit
// whose polaroidd reports its build but exits 3 when started as a daemon.
var (
	builds [2]string
	broken string
)

func TestMain(m *testing.M) {
	dir, err := buildBinaries()
	if err != nil {
		fmt.Fprintln(os.Stderr, "build polaroid binaries:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

const brokenStart = `package main

import "os"

func init() {
	if len(os.Args) < 2 || os.Args[1] != "-version" {
		os.Exit(3)
	}
}
`

func buildBinaries() (string, error) {
	ctx := context.Background()
	gomod, err := exec.CommandContext(ctx, "go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	root := filepath.Dir(strings.TrimSpace(string(gomod)))
	dir, err := os.MkdirTemp("", "polaroid-lifecycle-builds")
	if err != nil {
		return "", err
	}
	src := filepath.Join(dir, "src")
	run := func(name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s %v: %w: %s", name, args, err, out)
		}
		return nil
	}
	files, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "-co", "--exclude-standard").Output()
	if err != nil {
		return dir, err
	}
	for _, f := range strings.Split(strings.TrimRight(string(files), "\x00"), "\x00") {
		b, err := os.ReadFile(filepath.Join(root, f))
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the working tree
		}
		if err == nil {
			err = os.MkdirAll(filepath.Dir(filepath.Join(src, f)), 0o755)
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(src, f), b, 0o644)
		}
		if err != nil {
			return dir, err
		}
	}
	build := func(name string) (string, error) {
		out := filepath.Join(dir, name)
		return out, run("go", "build", "-o", out+"/", "./cmd/polaroidd", "./cmd/polaroid")
	}
	steps := []func() error{
		func() error { return run("git", "init", "-q") },
		func() error { return run("git", "add", "-A") },
		func() error { return run("git", "-c", "commit.gpgsign=false", "commit", "-qm", "A") },
		func() (err error) { builds[0], err = build("build A"); return err },
		func() error {
			return run("git", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "B")
		},
		func() (err error) { builds[1], err = build("build B"); return err },
		func() error {
			return os.WriteFile(filepath.Join(src, "cmd", "polaroidd", "broken.go"), []byte(brokenStart), 0o644)
		},
		func() error { return run("git", "add", "-A") },
		func() error { return run("git", "-c", "commit.gpgsign=false", "commit", "-qm", "broken B") },
		func() (err error) { broken, err = build("build broken"); return err },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return dir, err
		}
	}
	return dir, nil
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
	executed              []string // SHA-256 of each polaroidd started, read when it was started
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
	if sum, err := fileSHA256(argv[0]); err == nil {
		m.executed = append(m.executed, sum)
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
