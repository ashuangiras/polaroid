package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ErrManagerUnavailable is returned when the platform's service manager
// cannot be used from this session.
var ErrManagerUnavailable = errors.New("service manager unavailable")

// Result is a finished command. Code is its exit status.
type Result struct {
	Stdout, Stderr string
	Code           int
}

// Runner runs a program. It returns an error only if the program could not
// be run at all; a non-zero exit is a Result.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (Result, error)
}

// ExecRunner runs programs with os/exec.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // runs launchctl, systemctl, or the polaroid binaries the operator named with -from
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return Result{out.String(), errOut.String(), exit.ExitCode()}, nil
	}
	if err != nil {
		return Result{}, err
	}
	return Result{out.String(), errOut.String(), 0}, nil
}

// State is what the service manager reports about the service.
type State struct {
	Loaded bool   // the manager holds the definition for this session
	Active bool   // the manager runs it, or is starting or restarting it
	PID    int    // the managed process, if one runs
	Failed bool   // its last run ended unsuccessfully and it is not running
	Detail string // the manager's own words
}

// Manager is a service manager.
type Manager interface {
	Kind() string
	// Check reports whether the manager can be used from this session.
	Check(ctx context.Context) error
	// Register makes the written definition start at login.
	Register(ctx context.Context, l Layout) error
	// Unregister undoes Register; the definition is still on disk.
	Unregister(ctx context.Context, l Layout) error
	// Reload makes the manager forget a removed definition.
	Reload(ctx context.Context) error
	Start(ctx context.Context, l Layout) error
	// Stop stops the service for this session, without a restart.
	Stop(ctx context.Context, l Layout) error
	State(ctx context.Context, l Layout) (State, error)
}

// NewManager returns the manager for goos.
func NewManager(goos string, r Runner) (Manager, error) {
	switch goos {
	case "darwin":
		return &launchd{r: r, uid: os.Getuid()}, nil
	case "linux":
		return &systemd{r: r}, nil
	}
	return nil, fmt.Errorf("%w: service management supports macOS (launchd) and Linux (systemd user services), not %s; run polaroidd directly", ErrUnsupported, goos)
}

func failed(op string, res Result) error {
	msg := strings.TrimSpace(res.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(res.Stdout)
	}
	return fmt.Errorf("%s: exit status %d: %s", op, res.Code, msg)
}

// launchd manages a LaunchAgent in the user's GUI login domain.
type launchd struct {
	r   Runner
	uid int
}

const launchctl = "/bin/launchctl"

func (m *launchd) Kind() string   { return "launchd" }
func (m *launchd) domain() string { return "gui/" + strconv.Itoa(m.uid) }

func (m *launchd) run(ctx context.Context, args ...string) (Result, error) {
	res, err := m.r.Run(ctx, launchctl, args...)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s: %w", ErrManagerUnavailable, launchctl, err)
	}
	return res, nil
}

func (m *launchd) Check(ctx context.Context) error {
	res, err := m.run(ctx, "print", m.domain())
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%w: launchd has no GUI login session (%s) for this user; LaunchAgents run only in a logged-in session, so log in, or run polaroidd directly", ErrManagerUnavailable, m.domain())
	}
	return nil
}

func (m *launchd) Register(context.Context, Layout) error   { return nil }
func (m *launchd) Unregister(context.Context, Layout) error { return nil }
func (m *launchd) Reload(context.Context) error             { return nil }

// Start loads the agent afresh, so that its last exit status is this run's.
func (m *launchd) Start(ctx context.Context, l Layout) error {
	if err := m.Stop(ctx, l); err != nil {
		return err
	}
	res, err := m.run(ctx, "bootstrap", m.domain(), l.DefinitionPath())
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return failed("launchctl bootstrap", res)
	}
	return nil
}

// Stop unloads the agent: launchd sends SIGTERM, and the agent cannot
// respawn until it is loaded again, by start or at the next login.
func (m *launchd) Stop(ctx context.Context, l Layout) error {
	st, err := m.State(ctx, l)
	if err != nil || !st.Loaded {
		return err
	}
	res, err := m.run(ctx, "bootout", m.domain()+"/"+l.Label())
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return failed("launchctl bootout", res)
	}
	return nil
}

// State reads `launchctl list`, whose three columns (PID, last exit status,
// label) launchctl(1) documents; `launchctl print` output is not an
// interface. A loaded agent that exited with status 0 is idle: KeepAlive
// does not respawn it.
func (m *launchd) State(ctx context.Context, l Layout) (State, error) {
	res, err := m.run(ctx, "list")
	if err != nil {
		return State{}, err
	}
	if res.Code != 0 {
		return State{}, failed("launchctl list", res)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[2] != l.Label() {
			continue
		}
		st := State{Loaded: true, Detail: "loaded; last exit status " + f[1]}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			st.PID = pid
		}
		if code, err := strconv.Atoi(f[1]); err == nil && code != 0 && st.PID == 0 {
			st.Failed = true
			if code < 0 {
				st.Detail = "loaded; last run ended by signal " + strconv.Itoa(-code)
			}
		}
		st.Active = st.PID > 0 || st.Failed
		if !st.Active {
			st.Detail = "loaded, not running; last exit status 0, which launchd does not restart"
		}
		return st, nil
	}
	return State{Detail: "not loaded"}, nil
}

// systemd manages a user service of the user's systemd instance.
type systemd struct{ r Runner }

func (m *systemd) Kind() string { return "systemd" }

func (m *systemd) run(ctx context.Context, args ...string) (Result, error) {
	bin := "systemctl"
	for _, p := range []string{"/usr/bin/systemctl", "/bin/systemctl"} {
		if _, err := os.Stat(p); err == nil {
			bin = p
			break
		}
	}
	res, err := m.r.Run(ctx, bin, append([]string{"--user"}, args...)...)
	if err != nil {
		return Result{}, fmt.Errorf("%w: systemctl not found (%w): this Linux system does not run systemd; run polaroidd directly", ErrManagerUnavailable, err)
	}
	return res, nil
}

func (m *systemd) Check(ctx context.Context) error {
	res, err := m.run(ctx, "show-environment")
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%w: the systemd user manager is not reachable (%s); use a login session of this user, which starts it. Keeping it running without a login (loginctl enable-linger) is an administrator's decision that install does not make", ErrManagerUnavailable, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (m *systemd) do(ctx context.Context, args ...string) error {
	res, err := m.run(ctx, args...)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return failed("systemctl --user "+args[0], res)
	}
	return nil
}

func (m *systemd) Register(ctx context.Context, l Layout) error {
	if err := m.do(ctx, "daemon-reload"); err != nil {
		return err
	}
	return m.do(ctx, "enable", l.DefinitionPath())
}

func (m *systemd) Unregister(ctx context.Context, l Layout) error {
	return m.do(ctx, "disable", l.Unit())
}

func (m *systemd) Reload(ctx context.Context) error { return m.do(ctx, "daemon-reload") }

// Start clears an earlier failure, including a reached start limit, first.
func (m *systemd) Start(ctx context.Context, l Layout) error {
	_, _ = m.run(ctx, "reset-failed", l.Unit())
	return m.do(ctx, "start", l.Unit())
}

func (m *systemd) Stop(ctx context.Context, l Layout) error {
	return m.do(ctx, "stop", l.Unit())
}

func (m *systemd) State(ctx context.Context, l Layout) (State, error) {
	res, err := m.run(ctx, "show", l.Unit(), "--property=LoadState,ActiveState,SubState,MainPID,Result")
	if err != nil {
		return State{}, err
	}
	if res.Code != 0 {
		return State{}, failed("systemctl --user show", res)
	}
	p := map[string]string{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			p[k] = v
		}
	}
	st := State{Loaded: p["LoadState"] == "loaded", Detail: fmt.Sprintf("%s (%s), result %s", p["ActiveState"], p["SubState"], p["Result"])}
	st.PID, _ = strconv.Atoi(p["MainPID"])
	switch p["ActiveState"] {
	case "active", "activating", "reloading", "deactivating":
		st.Active = true
	}
	st.Failed = p["ActiveState"] == "failed" || p["SubState"] == "auto-restart"
	return st, nil
}
