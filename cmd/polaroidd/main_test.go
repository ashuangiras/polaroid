package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const createBody = `{"canonical_key":"demo.restart","version":{"philosophy":"p","method":"m",` +
	`"contract":{},"instructions":{"steps":["one"]},"revision_reason":"Initial version."}}`

// TestMain points every home lookup at a temporary directory, so no test can
// reach the real per-user catalog.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "polaroidd-test-home")
	if err != nil {
		panic(err)
	}
	for k, v := range map[string]string{"HOME": home, "USERPROFILE": home} {
		if err := os.Setenv(k, v); err != nil {
			panic(err)
		}
	}
	if err := os.Unsetenv("POLAROID_DB"); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// startDaemon runs the daemon on an ephemeral loopback port and returns its
// base URL and a function that stops it and reports run's result.
func startDaemon(t *testing.T, dbPath string) (string, func() error) {
	t.Helper()
	return startConfig(t, config{addr: "127.0.0.1:0", dbPath: dbPath, dbSource: dbFromFlag}, io.Discard)
}

// startConfig runs the daemon with cfg, logging to logs.
func startConfig(t *testing.T, cfg config, logs io.Writer) (string, func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(chan net.Addr, 1)
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(logs, nil))
	go func() {
		done <- run(ctx, cfg, logger, func(a net.Addr) { addrs <- a })
	}()
	select {
	case a := <-addrs:
		stopped := false
		stop := func() error {
			if stopped {
				return nil
			}
			stopped = true
			cancel()
			return <-done
		}
		t.Cleanup(func() { _ = stop() })
		return "http://" + a.String(), stop
	case err := <-done:
		cancel()
		t.Fatalf("daemon exited before listening: %v", err)
		return "", nil
	}
}

func request(t *testing.T, method, url, body string) (int, []byte) {
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
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func TestDaemonServesStopsGracefullyAndKeepsHistoryAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "polaroid.db")

	base, stop := startDaemon(t, dbPath)
	if status, body := request(t, http.MethodGet, base+"/healthz", ""); status != http.StatusOK {
		t.Fatalf("health: %d %s", status, body)
	}
	if status, body := request(t, http.MethodPost, base+"/v1/procedures", createBody); status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	_, before := request(t, http.MethodGet, base+"/v1/procedures/by-key/demo.restart", "")
	if err := stop(); err != nil {
		t.Fatalf("graceful shutdown returned %v", err)
	}
	probe, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := http.DefaultClient.Do(probe); err == nil {
		_ = resp.Body.Close()
		t.Fatal("daemon still accepts connections after shutdown")
	}

	restarted, _ := startDaemon(t, dbPath)
	status, after := request(t, http.MethodGet, restarted+"/v1/procedures/by-key/demo.restart", "")
	if status != http.StatusOK || !bytes.Equal(after, before) {
		t.Fatalf("history after restart = %d %s\nwant %s", status, after, before)
	}
}

func TestDaemonServesMCPBesideTheAPI(t *testing.T) {
	base, _ := startDaemon(t, filepath.Join(t.TempDir(), "polaroid.db"))
	if status, body := request(t, http.MethodPost, base+"/v1/procedures", createBody); status != http.StatusCreated {
		t.Fatalf("create over HTTP: %d %s", status, body)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "daemon-test", Version: "v0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: base + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect to /mcp: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_procedure", Arguments: map[string]any{"canonical_key": "demo.restart"}})
	if err != nil || res.IsError {
		t.Fatalf("get_procedure over MCP: %+v, %v", res, err)
	}
	_, viaHTTP := request(t, http.MethodGet, base+"/v1/procedures/by-key/demo.restart", "")
	if text := res.Content[0].(*mcp.TextContent).Text; text+"\n" != string(viaHTTP) {
		t.Fatalf("MCP and HTTP disagree:\n%s\n%s", text, viaHTTP)
	}

	// /mcp sits behind the same loopback-host check as the API.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example"
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host on /mcp: %d, want 403", resp.StatusCode)
	}
}

func TestDaemonFailsWhenAddressIsTaken(t *testing.T) {
	taken, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()

	cfg := config{addr: taken.Addr().String(), dbPath: filepath.Join(t.TempDir(), "polaroid.db")}
	err = run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("run = %v, want a listen error", err)
	}
}

func TestParseConfig(t *testing.T) {
	env := func(vars map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	}
	home := filepath.Join(string(filepath.Separator)+"home", "u")
	atHome := func() (string, error) { return home, nil }
	defaultDB := filepath.Join(home, ".polaroid", "data", "polaroid.db")
	noHome := func() (string, error) { return "", errors.New("$HOME is not defined") }
	cases := []struct {
		name string
		args []string
		env  map[string]string
		home func() (string, error)
		want config
	}{
		{"defaults", nil, nil, atHome, config{addr: defaultAddr, dbPath: defaultDB, dbSource: dbFromDefault}},
		{"environment", nil, map[string]string{"POLAROID_ADDR": "127.0.0.1:9000", "POLAROID_DB": "/tmp/p.db"}, noHome, config{addr: "127.0.0.1:9000", dbPath: "/tmp/p.db", dbSource: dbFromEnv}},
		{"relative environment path kept as given", nil, map[string]string{"POLAROID_DB": "rel/p.db"}, noHome, config{addr: defaultAddr, dbPath: "rel/p.db", dbSource: dbFromEnv}},
		{"flags override environment", []string{"-addr", "localhost:9001", "-db", "x.db"}, map[string]string{"POLAROID_ADDR": "127.0.0.1:9000", "POLAROID_DB": "/tmp/p.db"}, noHome, config{addr: "localhost:9001", dbPath: "x.db", dbSource: dbFromFlag}},
		{"a flag overrides an empty environment value", []string{"-db", "x.db"}, map[string]string{"POLAROID_DB": ""}, noHome, config{addr: defaultAddr, dbPath: "x.db", dbSource: dbFromFlag}},
		{"empty POLAROID_ADDR is unset", nil, map[string]string{"POLAROID_ADDR": ""}, atHome, config{addr: defaultAddr, dbPath: defaultDB, dbSource: dbFromDefault}},
	}
	for _, tc := range cases {
		got, err := parseConfig(tc.args, env(tc.env), tc.home, io.Discard)
		if err != nil || got != tc.want {
			t.Errorf("%s: parseConfig = %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}

	for _, args := range [][]string{{"-addr", "no-port"}, {"extra"}, {"-unknown"}} {
		if _, err := parseConfig(args, env(nil), atHome, io.Discard); err == nil {
			t.Errorf("parseConfig(%v) succeeded", args)
		}
	}
	if _, err := parseConfig([]string{"-h"}, env(nil), noHome, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: err = %v, want flag.ErrHelp", err)
	}

	// Never a silent fallback: empty values and an unusable home are errors.
	for name, tc := range map[string]struct {
		args []string
		env  map[string]string
		home func() (string, error)
		want string
	}{
		"empty -db":            {[]string{"-db", ""}, nil, atHome, "-db must not be empty"},
		"empty POLAROID_DB":    {nil, map[string]string{"POLAROID_DB": ""}, atHome, "POLAROID_DB is set but empty"},
		"no home directory":    {nil, nil, noHome, "$HOME is not defined; pass -db or set POLAROID_DB"},
		"relative home":        {nil, nil, func() (string, error) { return "relative", nil }, `home directory "relative" is not absolute; pass -db or set POLAROID_DB`},
		"empty home directory": {nil, nil, func() (string, error) { return "", nil }, `home directory "" is not absolute`},
	} {
		if _, err := parseConfig(tc.args, env(tc.env), tc.home, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

// The daemon's own wiring resolves the home directory with os.UserHomeDir,
// which TestMain points at a temporary directory.
func TestDefaultUsesTheUserHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseConfig(nil, os.LookupEnv, os.UserHomeDir, io.Discard)
	if want := filepath.Join(home, ".polaroid", "data", "polaroid.db"); err != nil || cfg.dbPath != want || cfg.dbSource != dbFromDefault {
		t.Fatalf("parseConfig = %+v, %v; want %s from the default", cfg, err, want)
	}
	if !strings.Contains(home, "polaroidd-test-home") {
		t.Fatalf("tests resolve the real home directory %s", home)
	}
}

// An explicit relative path keeps its meaning: relative to the working
// directory, and no home directory is needed or created.
func TestExplicitRelativePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	cfg, err := parseConfig([]string{"-addr", "127.0.0.1:0", "-db", "rel.db"}, func(string) (string, bool) { return "", false },
		func() (string, error) { return "", errors.New("no home") }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	base, stop := startConfig(t, cfg, &logs)
	if status, body := request(t, http.MethodPost, base+"/v1/procedures", createBody); status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rel.db")); err != nil {
		t.Fatalf("rel.db is not in the working directory: %v", err)
	}
	if !strings.Contains(logs.String(), "rel.db db_source=flag") || !strings.Contains(logs.String(), "db=/") {
		t.Fatalf("startup diagnostics do not name the absolute path and its source: %s", logs.String())
	}
}

func TestUnusableHomeFailsAtStartup(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ home, want string }{
		"missing home":           {filepath.Join(home, "missing"), "is not usable for the per-user database"},
		"home is a file":         {file, "is not usable for the per-user database"},
		".polaroid is not a dir": {home, "create "},
	} {
		if name == ".polaroid is not a dir" {
			if err := os.WriteFile(filepath.Join(home, ".polaroid"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		dbPath, err := defaultDBPath(func() (string, error) { return tc.home, nil })
		if err != nil {
			t.Fatal(err)
		}
		// Canceled, so a start that wrongly succeeds returns instead of serving.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err = run(ctx, config{addr: "127.0.0.1:0", dbPath: dbPath, dbSource: dbFromDefault}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: run = %v, want an error containing %q", name, err, tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing home directory was created: %v", err)
	}
}
