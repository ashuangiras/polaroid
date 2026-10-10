package main

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/lifecycle"
)

// idleManager reports a manager with nothing registered; these tests check
// the CLI's contract, not service behavior (internal/lifecycle tests that).
type idleManager struct{}

func (idleManager) Kind() string                                       { return "test" }
func (idleManager) Check(context.Context) error                        { return nil }
func (idleManager) Register(context.Context, lifecycle.Layout) error   { return nil }
func (idleManager) Unregister(context.Context, lifecycle.Layout) error { return nil }
func (idleManager) Reload(context.Context) error                       { return nil }
func (idleManager) Start(context.Context, lifecycle.Layout) error      { return nil }
func (idleManager) Stop(context.Context, lifecycle.Layout) error       { return nil }
func (idleManager) State(context.Context, lifecycle.Layout) (lifecycle.State, error) {
	return lifecycle.State{}, nil
}

func withIsolatedService(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	old := openService
	openService = func() (*lifecycle.Service, error) {
		l, err := lifecycle.NewLayout("linux", home, lifecycle.DefaultServiceName)
		if err != nil {
			return nil, err
		}
		return &lifecycle.Service{Layout: l, Manager: idleManager{}, Runner: lifecycle.ExecRunner{},
			Listener: func(int) (lifecycle.Process, error) { return lifecycle.Process{}, nil },
			Probe:    func(context.Context, string) error { return nil }}, nil
	}
	t.Cleanup(func() { openService = old })
}

func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(""), &stdout, &stderr, func(string) string { return "" })
	return code, stdout.String(), stderr.String()
}

func TestLocalCommandsAreListedAndNeedNoDaemon(t *testing.T) {
	_, help, _ := runCLI("help")
	for _, name := range []string{"install -from DIR", "start ", "stop ", "restart ", "status ", "uninstall ", "version "} {
		if !strings.Contains(help, "\n  "+name) {
			t.Errorf("help does not list %q", name)
		}
	}
	// -server points nowhere: local commands never contact it.
	code, out, _ := runCLI("-server", "http://127.0.0.1:1", "version")
	var b struct {
		Name string `json:"name"`
		Go   string `json:"go"`
	}
	if code != exitOK || json.Unmarshal([]byte(out), &b) != nil || b.Name != "polaroid" || b.Go == "" {
		t.Fatalf("version: %d %q", code, out)
	}
}

func TestLocalCommandExitCodes(t *testing.T) {
	withIsolatedService(t)
	for _, tc := range []struct {
		args  []string
		code  int
		state string
	}{
		{[]string{"status"}, 4, lifecycle.NotInstalled},
		{[]string{"start"}, exitNotInstalled, lifecycle.NotInstalled},
		{[]string{"stop"}, exitNotInstalled, lifecycle.NotInstalled},
		{[]string{"restart", "-wait", "1s"}, exitNotInstalled, lifecycle.NotInstalled},
		{[]string{"uninstall"}, exitOK, lifecycle.NotInstalled},
		{[]string{"install"}, exitFailure, lifecycle.NotInstalled},
	} {
		code, out, errOut := runCLI(tc.args...)
		var st lifecycle.Status
		if code != tc.code || json.Unmarshal([]byte(out), &st) != nil || st.State != tc.state || errOut == "" {
			t.Errorf("%v: exit %d, %q, %q; want exit %d and state %s", tc.args, code, out, errOut, tc.code, tc.state)
		}
	}
	for _, args := range [][]string{{"status", "extra"}, {"start", "-bogus"}, {"version", "x"}, {"install", "-service-name", "Other", "-from", "x"}} {
		if code, _, _ := runCLI(args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	if code, _, errOut := runCLI("install", "-from", t.TempDir()); code != exitFailure || !strings.Contains(errOut, "run make build first") {
		t.Errorf("install from an empty directory: %d %s", code, errOut)
	}
}
