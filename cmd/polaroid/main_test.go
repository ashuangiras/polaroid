package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	httptransport "github.com/ashuangiras/polaroid/internal/transport/http"
)

const createJSON = `{"canonical_key":"go.dependency.add","version":{"philosophy":"p","method":"m",` +
	`"contract":{},"instructions":{"steps":["one"]},"revision_reason":"Initial version."}}`

const reviseJSON = `{"base_version":1,"version":{"philosophy":"p","method":"m",` +
	`"contract":{},"instructions":{"steps":["corrected"]},"revision_reason":"Corrected step one."}}`

// newServer runs the real API over a real SQLite file and returns its URL.
func newServer(t *testing.T) string {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "polaroid.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := httptest.NewServer(httptransport.NewHandler(memory.NewService(store), slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv.URL
}

type result struct {
	code           int
	stdout, stderr string
}

func cli(stdin string, env map[string]string, args ...string) result {
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr, func(k string) string { return env[k] })
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustSucceed(t *testing.T, r result) {
	t.Helper()
	if r.code != exitOK {
		t.Fatalf("exit %d; stdout: %s; stderr: %s", r.code, r.stdout, r.stderr)
	}
}

func TestCommandsReachTheAPI(t *testing.T) {
	server := newServer(t)

	created := cli("", nil, "-server", server, "create", writeFile(t, createJSON))
	mustSucceed(t, created)
	var h struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created.stdout), &h); err != nil || h.ID == "" {
		t.Fatalf("create output is not a procedure: %q (%v)", created.stdout, err)
	}

	revised := cli(reviseJSON, nil, "-server", server, "revise", h.ID)
	mustSucceed(t, revised)
	if !strings.Contains(revised.stdout, `"version":2`) {
		t.Fatalf("revise output = %s", revised.stdout)
	}

	for _, args := range [][]string{
		{"health"},
		{"list"},
		{"get", h.ID},
		{"get-by-key", "go.dependency.add"},
		{"get-version", h.ID, "2"},
	} {
		r := cli("", nil, append([]string{"-server", server}, args...)...)
		mustSucceed(t, r)
		if !jsontext.Value(r.stdout).IsValid() || r.stderr != "" {
			t.Fatalf("%v: stdout %q, stderr %q", args, r.stdout, r.stderr)
		}
	}

	// The server can also come from the environment.
	mustSucceed(t, cli("", map[string]string{"POLAROID_URL": server}, "get", h.ID))
}

func TestFailedRequestsExitOne(t *testing.T) {
	server := newServer(t)
	created := cli("", nil, "-server", server, "create", writeFile(t, createJSON))
	mustSucceed(t, created)
	var h struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created.stdout), &h); err != nil {
		t.Fatal(err)
	}
	mustSucceed(t, cli(reviseJSON, nil, "-server", server, "revise", h.ID, "-"))

	unreachable := func() string {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		return "http://" + addr
	}()

	cases := []struct {
		name      string
		stdin     string
		args      []string
		errorCode string // expected API error code on stdout, if any
	}{
		{"stale revision", reviseJSON, []string{"-server", server, "revise", h.ID}, "version_conflict"},
		{"duplicate canonical key", createJSON, []string{"-server", server, "create"}, "canonical_key_exists"},
		{"unknown procedure", "", []string{"-server", server, "get", "no-such-id"}, "not_found"},
		{"unknown version", "", []string{"-server", server, "get-version", h.ID, "9"}, "not_found"},
		{"invalid JSON", `{"canonical_key":`, []string{"-server", server, "create"}, "invalid_request"},
		{"missing input file", "", []string{"-server", server, "create", filepath.Join(t.TempDir(), "absent.json")}, ""},
		{"server unreachable", "", []string{"-server", unreachable, "list"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := cli(tc.stdin, nil, tc.args...)
			if r.code != exitFailure {
				t.Fatalf("exit %d, want %d; stdout: %s; stderr: %s", r.code, exitFailure, r.stdout, r.stderr)
			}
			if !strings.HasPrefix(r.stderr, "polaroid: ") {
				t.Fatalf("stderr = %q, want a diagnostic", r.stderr)
			}
			if tc.errorCode == "" {
				return
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(r.stdout), &body); err != nil || body.Error.Code != tc.errorCode {
				t.Fatalf("stdout = %s, want API error %s", r.stdout, tc.errorCode)
			}
		})
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"get"},
		{"get", "a", "b"},
		{"get-version", "a"},
		{"revise"},
		{"-server", "ftp://example.com", "list"},
		{"-server", "127.0.0.1:7417", "list"},
		{"-no-such-flag", "list"},
	} {
		r := cli("", nil, args...)
		if r.code != exitUsage || r.stderr == "" {
			t.Errorf("%v: exit %d, stderr %q; want exit %d with a message", args, r.code, r.stderr, exitUsage)
		}
	}
}

func TestHelp(t *testing.T) {
	r := cli("", nil, "help")
	if r.code != exitOK || !strings.Contains(r.stdout, "get-by-key KEY") {
		t.Fatalf("help: exit %d, stdout %q", r.code, r.stdout)
	}
	if r := cli("", nil, "-h"); r.code != exitOK || !strings.Contains(r.stderr, "Commands:") {
		t.Fatalf("-h: exit %d, stderr %q", r.code, r.stderr)
	}
}
