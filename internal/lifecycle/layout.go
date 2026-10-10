// Package lifecycle installs Polaroid for one user and runs polaroidd as a
// managed service: a launchd agent on macOS, a systemd user service on Linux
// (ADR-0026). It knows nothing of procedures or storage; it manages files,
// a service manager and processes.
package lifecycle

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
)

// DefaultServiceName names the service; validation installs use another.
const DefaultServiceName = "polaroid"

// DefaultAddr is polaroidd's default loopback endpoint.
const DefaultAddr = "127.0.0.1:7417"

const labelPrefix = "io.github.ashuangiras."

// ErrUnsupported is returned on platforms without a supported service manager.
var ErrUnsupported = errors.New("unsupported platform")

// ErrNotInstalled is returned by operations that need an installation.
var ErrNotInstalled = errors.New("polaroid is not installed for this user")

var serviceNamePattern = regexp.MustCompile(`^polaroid(-[a-z0-9]+)*$`)

// Layout is where an installation's files go, all under one home directory.
type Layout struct {
	GOOS        string
	Home        string
	ServiceName string
	BinDir      string // polaroid and polaroidd
	StateDir    string // install.json, previous/, last-failure.json
	LogDir      string // macOS diagnostics; empty on Linux (journald)
	DataDB      string // the default catalog
}

// NewLayout returns the layout for home, which must be absolute.
func NewLayout(goos, home, serviceName string) (Layout, error) {
	if goos != "darwin" && goos != "linux" {
		return Layout{}, fmt.Errorf("%w: service management supports macOS (launchd) and Linux (systemd user services), not %s; run polaroidd directly", ErrUnsupported, goos)
	}
	if !filepath.IsAbs(home) {
		return Layout{}, fmt.Errorf("home directory %q is not absolute", home)
	}
	if !serviceNamePattern.MatchString(serviceName) {
		return Layout{}, fmt.Errorf("service name %q: want polaroid, optionally followed by -suffix parts of lowercase letters and digits", serviceName)
	}
	l := Layout{
		GOOS:        goos,
		Home:        home,
		ServiceName: serviceName,
		BinDir:      filepath.Join(home, ".local", "bin"),
		StateDir:    filepath.Join(home, ".local", "state", "polaroid"),
		DataDB:      filepath.Join(home, ".polaroid", "data", "polaroid.db"),
	}
	if goos == "darwin" {
		l.LogDir = filepath.Join(home, "Library", "Logs", "Polaroid")
	}
	return l, nil
}

// CurrentLayout is NewLayout for this platform.
func CurrentLayout(home, serviceName string) (Layout, error) {
	return NewLayout(runtime.GOOS, home, serviceName)
}

// Polaroid and Polaroidd are the installed executables.
func (l Layout) Polaroid() string  { return filepath.Join(l.BinDir, "polaroid") }
func (l Layout) Polaroidd() string { return filepath.Join(l.BinDir, "polaroidd") }

// Label is the launchd label; Unit the systemd unit name.
func (l Layout) Label() string { return labelPrefix + l.ServiceName }
func (l Layout) Unit() string  { return l.ServiceName + ".service" }

// DefinitionPath is where the service definition is written.
func (l Layout) DefinitionPath() string {
	if l.GOOS == "darwin" {
		return filepath.Join(l.Home, "Library", "LaunchAgents", l.Label()+".plist")
	}
	return filepath.Join(l.Home, ".config", "systemd", "user", l.Unit())
}

// LogFile is polaroidd's -log-file on macOS, and empty on Linux.
func (l Layout) LogFile() string {
	if l.LogDir == "" {
		return ""
	}
	return filepath.Join(l.LogDir, "polaroidd.log")
}

// Diagnostics tells an operator where the service's output is.
func (l Layout) Diagnostics() string {
	if l.GOOS == "darwin" {
		return l.LogFile()
	}
	return "journalctl --user -u " + l.Unit()
}

func (l Layout) manifestPath() string    { return filepath.Join(l.StateDir, "install.json") }
func (l Layout) previousDir() string     { return filepath.Join(l.StateDir, "previous") }
func (l Layout) lastFailurePath() string { return filepath.Join(l.StateDir, "last-failure.json") }

// Args is the polaroidd command line the service runs.
func (l Layout) Args(addr, db string) []string {
	args := []string{l.Polaroidd(), "-addr", addr, "-db", db}
	if f := l.LogFile(); f != "" {
		args = append(args, "-log-file", f)
	}
	return args
}
