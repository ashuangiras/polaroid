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
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const createBody = `{"canonical_key":"demo.restart","version":{"philosophy":"p","method":"m",` +
	`"contract":{},"instructions":{"steps":["one"]},"revision_reason":"Initial version."}}`

// startDaemon runs the daemon on an ephemeral loopback port and returns its
// base URL and a function that stops it and reports run's result.
func startDaemon(t *testing.T, dbPath string) (string, func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(chan net.Addr, 1)
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() {
		done <- run(ctx, config{addr: "127.0.0.1:0", dbPath: dbPath}, logger, func(a net.Addr) { addrs <- a })
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
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want config
	}{
		{"defaults", nil, nil, config{addr: defaultAddr, dbPath: defaultDBPath}},
		{"environment", nil, map[string]string{"POLAROID_ADDR": "127.0.0.1:9000", "POLAROID_DB": "/tmp/p.db"}, config{addr: "127.0.0.1:9000", dbPath: "/tmp/p.db"}},
		{"flags override environment", []string{"-addr", "localhost:9001", "-db", "x.db"}, map[string]string{"POLAROID_ADDR": "127.0.0.1:9000", "POLAROID_DB": "/tmp/p.db"}, config{addr: "localhost:9001", dbPath: "x.db"}},
	}
	for _, tc := range cases {
		got, err := parseConfig(tc.args, env(tc.env), io.Discard)
		if err != nil || got != tc.want {
			t.Errorf("%s: parseConfig = %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}

	for _, args := range [][]string{{"-addr", "no-port"}, {"extra"}, {"-unknown"}} {
		if _, err := parseConfig(args, env(nil), io.Discard); err == nil {
			t.Errorf("parseConfig(%v) succeeded", args)
		}
	}
	if _, err := parseConfig([]string{"-h"}, env(nil), io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: err = %v, want flag.ErrHelp", err)
	}
}
