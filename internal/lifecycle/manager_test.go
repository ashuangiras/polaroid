package lifecycle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// scripted answers commands from a table; it checks the commands an adapter
// issues and how it reads their output, not a manager's behavior.
type scripted struct {
	answers map[string]Result
	missing bool
	calls   []string
}

func (s *scripted) Run(_ context.Context, name string, args ...string) (Result, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	s.calls = append(s.calls, call)
	if s.missing {
		return Result{}, errors.New("exec: not found")
	}
	for prefix, res := range s.answers {
		if strings.HasPrefix(call, prefix) {
			return res, nil
		}
	}
	return Result{}, nil
}

func TestLaunchdState(t *testing.T) {
	l, _ := NewLayout("darwin", "/Users/u", DefaultServiceName)
	list := "PID\tStatus\tLabel\n-\t0\tcom.apple.x\n%s\n"
	for name, tc := range map[string]struct {
		line string
		want State
	}{
		"running":         {"4242\t0\tio.github.ashuangiras.polaroid", State{Loaded: true, Active: true, PID: 4242, Detail: "loaded; last exit status 0"}},
		"loaded, idle":    {"-\t0\tio.github.ashuangiras.polaroid", State{Loaded: true, Detail: "loaded, not running; last exit status 0, which launchd does not restart"}},
		"crashed":         {"-\t1\tio.github.ashuangiras.polaroid", State{Loaded: true, Active: true, Failed: true, Detail: "loaded; last exit status 1"}},
		"killed":          {"-\t-9\tio.github.ashuangiras.polaroid", State{Loaded: true, Active: true, Failed: true, Detail: "loaded; last run ended by signal 9"}},
		"running again":   {"77\t-9\tio.github.ashuangiras.polaroid", State{Loaded: true, Active: true, PID: 77, Detail: "loaded; last exit status -9"}},
		"not loaded":      {"-\t0\tio.github.ashuangiras.polaroid-other", State{Detail: "not loaded"}},
		"prefix is not a": {"5\t0\tio.github.ashuangiras.polaroid.x", State{Detail: "not loaded"}},
	} {
		r := &scripted{answers: map[string]Result{"/bin/launchctl list": {Stdout: strings.Replace(list, "%s", tc.line, 1)}}}
		m, _ := NewManager("darwin", r)
		got, err := m.State(context.Background(), l)
		if err != nil || got != tc.want {
			t.Errorf("%s: %+v, %v; want %+v", name, got, err, tc.want)
		}
	}
}

func TestLaunchdCommands(t *testing.T) {
	l, _ := NewLayout("darwin", "/Users/a b", DefaultServiceName)
	r := &scripted{answers: map[string]Result{"/bin/launchctl list": {Stdout: "77\t0\tio.github.ashuangiras.polaroid\n"}}}
	m, _ := NewManager("darwin", r)
	ld := m.(*launchd)
	if err := m.Start(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/launchctl list", "/bin/launchctl bootout " + ld.domain() + "/io.github.ashuangiras.polaroid",
		"/bin/launchctl bootstrap " + ld.domain() + " /Users/a b/Library/LaunchAgents/io.github.ashuangiras.polaroid.plist"}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("start of a loaded agent ran\n%q\nwant\n%q", r.calls, want)
	}
	r = &scripted{answers: map[string]Result{"/bin/launchctl bootstrap": {Code: 5, Stderr: "Bootstrap failed: 5: Input/output error"}}}
	m, _ = NewManager("darwin", r)
	if err := m.Start(context.Background(), l); err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("a failed bootstrap: %v", err)
	}
}

func TestSystemdState(t *testing.T) {
	l, _ := NewLayout("linux", "/home/u", DefaultServiceName)
	for name, tc := range map[string]struct {
		show string
		want State
	}{
		"running":      {"LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=99\nResult=success\n", State{Loaded: true, Active: true, PID: 99, Detail: "active (running), result success"}},
		"stopped":      {"LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\nResult=success\n", State{Loaded: true, Detail: "inactive (dead), result success"}},
		"restarting":   {"LoadState=loaded\nActiveState=activating\nSubState=auto-restart\nMainPID=0\nResult=exit-code\n", State{Loaded: true, Active: true, Failed: true, Detail: "activating (auto-restart), result exit-code"}},
		"failed":       {"LoadState=loaded\nActiveState=failed\nSubState=failed\nMainPID=0\nResult=start-limit-hit\n", State{Loaded: true, Failed: true, Detail: "failed (failed), result start-limit-hit"}},
		"not found":    {"LoadState=not-found\nActiveState=inactive\nSubState=dead\nMainPID=0\nResult=success\n", State{Detail: "inactive (dead), result success"}},
		"starting now": {"LoadState=loaded\nActiveState=activating\nSubState=start\nMainPID=12\nResult=success\n", State{Loaded: true, Active: true, PID: 12, Detail: "activating (start), result success"}},
	} {
		r := &scripted{answers: map[string]Result{"": {Stdout: tc.show}}}
		m, _ := NewManager("linux", r)
		got, err := m.State(context.Background(), l)
		if err != nil || got != tc.want {
			t.Errorf("%s: %+v, %v; want %+v", name, got, err, tc.want)
		}
		if !strings.HasSuffix(r.calls[0], "--user show polaroid.service --property=LoadState,ActiveState,SubState,MainPID,Result") {
			t.Errorf("%s: ran %q", name, r.calls[0])
		}
	}
}

func TestSystemdCommands(t *testing.T) {
	l, _ := NewLayout("linux", "/home/a b", DefaultServiceName)
	r := &scripted{}
	m, _ := NewManager("linux", r)
	ctx := context.Background()
	for _, op := range []func() error{
		func() error { return m.Register(ctx, l) }, func() error { return m.Start(ctx, l) },
		func() error { return m.Stop(ctx, l) }, func() error { return m.Unregister(ctx, l) }, func() error { return m.Reload(ctx) },
	} {
		if err := op(); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, c := range r.calls {
		_, args, _ := strings.Cut(c, " ")
		got = append(got, args)
	}
	want := []string{"--user daemon-reload", "--user enable /home/a b/.config/systemd/user/polaroid.service",
		"--user reset-failed polaroid.service", "--user start polaroid.service", "--user stop polaroid.service",
		"--user disable polaroid.service", "--user daemon-reload"}
	if !slices.Equal(got, want) {
		t.Fatalf("ran\n%q\nwant\n%q", got, want)
	}
}

func TestMissingOrUnreachableManagers(t *testing.T) {
	ctx := context.Background()
	m, _ := NewManager("linux", &scripted{missing: true})
	if err := m.Check(ctx); !errors.Is(err, ErrManagerUnavailable) || !strings.Contains(err.Error(), "systemctl not found") || !strings.Contains(err.Error(), "run polaroidd directly") {
		t.Errorf("no systemctl: %v", err)
	}
	m, _ = NewManager("linux", &scripted{answers: map[string]Result{"": {Code: 1, Stderr: "Failed to connect to bus: No medium found"}}})
	if err := m.Check(ctx); !errors.Is(err, ErrManagerUnavailable) || !strings.Contains(err.Error(), "No medium found") || !strings.Contains(err.Error(), "install does not make") {
		t.Errorf("no user manager: %v", err)
	}
	m, _ = NewManager("darwin", &scripted{answers: map[string]Result{"/bin/launchctl print": {Code: 113}}})
	if err := m.Check(ctx); !errors.Is(err, ErrManagerUnavailable) || !strings.Contains(err.Error(), "no GUI login session") {
		t.Errorf("no GUI session: %v", err)
	}
	m, _ = NewManager("darwin", &scripted{missing: true})
	if err := m.Check(ctx); !errors.Is(err, ErrManagerUnavailable) {
		t.Errorf("no launchctl: %v", err)
	}
}

func TestStatusExitCodes(t *testing.T) {
	for state, code := range map[string]int{Running: 0, Failed: 1, Stopped: 3, NotInstalled: 4, Starting: 5, "": 1} {
		if got := (Status{State: state}).ExitCode(); got != code {
			t.Errorf("%q: exit %d, want %d", state, got, code)
		}
	}
}
