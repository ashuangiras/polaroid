package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/ashuangiras/polaroid/internal/version"
)

// Service states reported by Status, with their exit codes (ADR-0026).
const (
	Running      = "running"
	Failed       = "failed"
	Stopped      = "stopped"
	NotInstalled = "not-installed"
	Starting     = "starting"
)

var (
	// ErrConflict means another process holds the endpoint.
	ErrConflict = errors.New("the endpoint is held by another process")
	// ErrNotOwned means an install path holds a file this installation did
	// not write.
	ErrNotOwned = errors.New("refusing to overwrite a file this installation does not own")
	// ErrStartFailed means the service did not become healthy in time.
	ErrStartFailed = errors.New("the service did not become healthy")
)

// File is an installed file and the SHA-256 it was written with.
type File struct {
	Role   string `json:"role"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Manifest is the installation record, ~/.local/state/polaroid/install.json.
type Manifest struct {
	Format      int           `json:"format"`
	Manager     string        `json:"manager"`
	ServiceName string        `json:"service_name"`
	Addr        string        `json:"addr"`
	DB          string        `json:"db"`
	Build       version.Build `json:"build"`
	Files       []File        `json:"files"`
	InstalledAt string        `json:"installed_at"`
}

// Status is what status and every other lifecycle command report.
type Status struct {
	State       string         `json:"state"`
	Action      string         `json:"action,omitempty"`
	Detail      string         `json:"detail,omitempty"`
	Manager     string         `json:"manager,omitempty"`
	Service     string         `json:"service,omitempty"`
	Definition  string         `json:"definition,omitempty"`
	PID         int            `json:"pid,omitzero"`
	Build       *version.Build `json:"build,omitempty"`
	Binaries    []string       `json:"binaries,omitempty"`
	Endpoint    string         `json:"endpoint"`
	Database    string         `json:"database,omitempty"`
	Diagnostics string         `json:"diagnostics,omitempty"`
	Conflict    *Process       `json:"conflict,omitempty"`
	Previous    string         `json:"previous,omitempty"`
	Problems    []string       `json:"problems,omitempty"`
}

// ExitCode is the status command's exit status for s.
func (s Status) ExitCode() int {
	switch s.State {
	case Running:
		return 0
	case Stopped:
		return 3
	case NotInstalled:
		return 4
	case Starting:
		return 5
	}
	return 1
}

// Service runs lifecycle operations for one user's installation.
type Service struct {
	Layout   Layout
	Manager  Manager
	Runner   Runner
	Listener ListenerFunc
	Probe    func(ctx context.Context, addr string) error
	Wait     time.Duration // bound for becoming healthy, and for stopping
	Poll     time.Duration
}

// Open returns the service for home on goos, named as recorded by an
// existing installation, else DefaultServiceName.
func Open(goos, home string, r Runner) (*Service, error) {
	l, err := NewLayout(goos, home, DefaultServiceName)
	if err != nil {
		return nil, err
	}
	mgr, err := NewManager(goos, r)
	if err != nil {
		return nil, err
	}
	s := &Service{Layout: l, Manager: mgr, Runner: r, Listener: Listener, Probe: Health, Wait: 30 * time.Second, Poll: 200 * time.Millisecond}
	m, err := s.manifest()
	if err != nil {
		return nil, err
	}
	if m != nil && m.ServiceName != l.ServiceName {
		if s.Layout, err = NewLayout(goos, home, m.ServiceName); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Health probes polaroidd's /healthz at addr.
func Health(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz returned %s", resp.Status)
	}
	return nil
}

func (s *Service) manifest() (*Manifest, error) {
	b, err := os.ReadFile(s.Layout.manifestPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read installation record: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("read installation record %s: %w", s.Layout.manifestPath(), err)
	}
	return &m, nil
}

func (m *Manifest) file(role string) (File, bool) {
	for _, f := range m.Files {
		if f.Role == role {
			return f, true
		}
	}
	return File{}, false
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // installation paths come from the layout
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// holder returns the process holding addr's port, nil if it is free, and a
// zero Process if something this user cannot identify holds it.
func (s *Service) holder(ctx context.Context, addr string) *Process {
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return &Process{}
	}
	port, _ := strconv.Atoi(portText)
	if p, err := s.Listener(port); err == nil && p.PID != 0 {
		return &p
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil
	}
	_ = conn.Close()
	// It may have started listening since the first look.
	if p, err := s.Listener(port); err == nil && p.PID != 0 {
		return &p
	}
	return &Process{}
}

func describe(p *Process) string {
	if p.PID == 0 {
		return "a process this user cannot identify"
	}
	return fmt.Sprintf("process %d (%s)", p.PID, p.Command)
}

// Status reports the installation and the service without changing either.
func (s *Service) Status(ctx context.Context) Status {
	l := s.Layout
	m, err := s.manifest()
	if err != nil {
		return Status{State: Failed, Detail: err.Error(), Endpoint: "http://" + DefaultAddr}
	}
	if m == nil {
		st := Status{State: NotInstalled, Endpoint: "http://" + DefaultAddr}
		for _, p := range []string{l.DefinitionPath(), l.Polaroid(), l.Polaroidd()} {
			if _, err := os.Lstat(p); err == nil {
				st.Problems = append(st.Problems, "not written by this installation, left alone: "+p)
			}
		}
		if h := s.holder(ctx, DefaultAddr); h != nil {
			st.Conflict = h
			st.Detail = "the default endpoint is held by " + describe(h) + ", which is not a managed installation"
		}
		return st
	}
	b := m.Build
	st := Status{Manager: m.Manager, Service: s.serviceID(), Definition: l.DefinitionPath(), Build: &b,
		Binaries: []string{l.Polaroid(), l.Polaroidd()}, Endpoint: "http://" + m.Addr, Database: m.DB, Diagnostics: l.Diagnostics()}
	if _, err := os.Stat(l.previousDir()); err == nil {
		st.Previous = l.previousDir()
	}
	for _, f := range m.Files {
		if sum, err := fileSHA256(f.Path); err != nil || sum != f.SHA256 {
			st.Problems = append(st.Problems, "differs from the installation record: "+f.Path)
		}
	}
	if err := s.Manager.Check(ctx); err != nil {
		st.State, st.Detail = Failed, err.Error()
		return st
	}
	ms, err := s.Manager.State(ctx, l)
	if err != nil {
		st.State, st.Detail = Failed, err.Error()
		return st
	}
	st.PID = ms.PID
	h := s.holder(ctx, m.Addr)
	switch {
	case ms.PID > 0 && h != nil && h.PID == ms.PID:
		if err := s.Probe(ctx, m.Addr); err != nil {
			st.State, st.Detail = Failed, "the managed process listens, but its health check failed: "+err.Error()
		} else {
			st.State, st.Detail = Running, "the managed process "+strconv.Itoa(ms.PID)+" serves the endpoint and is healthy"
		}
	case ms.PID > 0 && h != nil && h.PID == 0:
		// Transient while it binds; if it lasts, the readiness bound fails it.
		st.State, st.Detail = Starting, "the endpoint answers, but its process is not identified as the managed process "+strconv.Itoa(ms.PID)+" yet"
	case ms.PID > 0 && h != nil:
		st.State, st.Conflict = Failed, h
		st.Detail = "the endpoint is held by " + describe(h) + ", not by the managed process " + strconv.Itoa(ms.PID)
	case ms.PID > 0:
		st.State, st.Detail = Starting, "the managed process "+strconv.Itoa(ms.PID)+" is not listening yet"
	case ms.Failed:
		st.State, st.Detail = Failed, s.Manager.Kind()+": "+ms.Detail
	case ms.Active:
		st.State, st.Detail = Starting, s.Manager.Kind()+": "+ms.Detail
	default:
		st.State, st.Detail = Stopped, s.Manager.Kind()+": "+ms.Detail
		if f, err := os.ReadFile(l.lastFailurePath()); err == nil {
			st.State, st.Detail = Failed, "the last start failed: "+string(f)
		}
	}
	if st.Conflict == nil && st.State != Running && h != nil {
		st.Conflict = h
		st.Detail += "; the endpoint is held by " + describe(h)
	}
	return st
}

func (s *Service) serviceID() string {
	if s.Manager.Kind() == "launchd" {
		return s.Layout.Label()
	}
	return s.Layout.Unit()
}

// waitReady polls until the service is running, has failed, or s.Wait passes.
func (s *Service) waitReady(ctx context.Context) Status {
	deadline := time.Now().Add(s.Wait)
	for {
		st := s.Status(ctx)
		switch {
		case st.State == Running, st.State == Failed:
			return st
		case time.Now().After(deadline):
			st.State, st.Detail = Failed, fmt.Sprintf("not healthy within %s: %s", s.Wait, st.Detail)
			return st
		}
		time.Sleep(s.Poll)
	}
}

// stopService stops the service and waits, bounded, until no managed process
// remains.
func (s *Service) stopService(ctx context.Context) error {
	before, err := s.Manager.State(ctx, s.Layout)
	if err != nil {
		return err
	}
	if before.Active || before.PID > 0 || before.Loaded {
		if err := s.Manager.Stop(ctx, s.Layout); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(s.Wait)
	for {
		ms, err := s.Manager.State(ctx, s.Layout)
		if err != nil {
			return err
		}
		if !ms.Active && ms.PID == 0 && (before.PID == 0 || !processAlive(before.PID)) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the service did not stop within %s: %s", s.Wait, ms.Detail)
		}
		time.Sleep(s.Poll)
	}
}

// Start starts the installed service and waits until it is healthy. A start
// that fails is stopped again, so that the manager does not keep retrying it.
func (s *Service) Start(ctx context.Context) (Status, error) {
	m, err := s.manifest()
	if err != nil {
		return Status{State: Failed, Detail: err.Error()}, err
	}
	if m == nil {
		return s.Status(ctx), ErrNotInstalled
	}
	if err := s.Manager.Check(ctx); err != nil {
		return s.Status(ctx), err
	}
	st := s.Status(ctx)
	if st.State == Running {
		st.Action = "already running"
		return st, nil
	}
	if st.Conflict != nil {
		return st, fmt.Errorf("%w: %s; stop it, or install with another -addr", ErrConflict, describe(st.Conflict))
	}
	_ = os.Remove(s.Layout.lastFailurePath())
	if err := s.Manager.Start(ctx, s.Layout); err != nil {
		return s.failStart(ctx, err.Error())
	}
	st = s.waitReady(ctx)
	if st.State != Running {
		return s.failStart(ctx, st.Detail)
	}
	st.Action = "started"
	return st, nil
}

func (s *Service) failStart(ctx context.Context, detail string) (Status, error) {
	stopErr := s.stopService(ctx)
	_ = os.MkdirAll(s.Layout.StateDir, 0o700)
	_ = os.WriteFile(s.Layout.lastFailurePath(), []byte(time.Now().UTC().Format(time.RFC3339)+": "+detail), 0o600)
	st := s.Status(ctx)
	st.Action = "start failed"
	st.Detail = detail + "; the service was stopped so that it does not keep restarting; see " + s.Layout.Diagnostics()
	if stopErr != nil {
		st.Detail += "; stopping it also failed: " + stopErr.Error()
	}
	return st, ErrStartFailed
}

// Stop stops the service for this session; it starts again at the next login.
func (s *Service) Stop(ctx context.Context) (Status, error) {
	m, err := s.manifest()
	if err != nil {
		return Status{State: Failed, Detail: err.Error()}, err
	}
	if m == nil {
		return s.Status(ctx), ErrNotInstalled
	}
	if err := s.Manager.Check(ctx); err != nil {
		return s.Status(ctx), err
	}
	if err := s.stopService(ctx); err != nil {
		return s.Status(ctx), err
	}
	_ = os.Remove(s.Layout.lastFailurePath())
	st := s.Status(ctx)
	st.Action = "stopped"
	if st.State != Stopped {
		return st, fmt.Errorf("after stopping, the service is %s: %s", st.State, st.Detail)
	}
	return st, nil
}

// Restart stops and starts the service.
func (s *Service) Restart(ctx context.Context) (Status, error) {
	if st, err := s.Stop(ctx); err != nil {
		return st, err
	}
	st, err := s.Start(ctx)
	if err == nil {
		st.Action = "restarted"
	}
	return st, err
}
