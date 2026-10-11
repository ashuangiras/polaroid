package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ashuangiras/polaroid/internal/memory"
	"github.com/ashuangiras/polaroid/internal/storage/sqlite"
	httptransport "github.com/ashuangiras/polaroid/internal/transport/http"
)

func TestDiscoveryCommands(t *testing.T) {
	server := newServer(t)
	mustSucceed(t, cli(createJSON, nil, "-server", server, "create"))

	found := cli("", nil, "-server", server, "discover", "Adding a dependency", "repository=github.com/o/a", "limit=1")
	mustSucceed(t, found)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		server+"/v1/procedures/discovery?"+url.Values{"task": {"Adding a dependency"}, "repository": {"github.com/o/a"}, "limit": {"1"}}.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	direct, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if found.stdout != string(direct) || !strings.Contains(found.stdout, `"canonical_key":"go.dependency.add"`) {
		t.Fatalf("discover = %s, GET = %s", found.stdout, direct)
	}

	dup := cli(`{"canonical_key":"go.dependency.add","method":"Justify and pin.","philosophy":"p"}`, nil, "-server", server, "duplicates")
	mustSucceed(t, dup)
	if !strings.Contains(dup.stdout, `"key_collision":{"procedure_id":`) {
		t.Fatalf("duplicates = %s", dup.stdout)
	}

	if r := cli("", nil, "-server", server, "discover", "the and"); r.code != exitFailure || !strings.Contains(r.stdout, `"field":"task"`) {
		t.Fatalf("a task without searchable words: exit %d, %s", r.code, r.stdout)
	}
	if r := cli("", nil, "-server", server, "discover"); r.code != exitUsage {
		t.Fatalf("discover without a task: exit %d", r.code)
	}
}

// recordingServer runs the real API over a real SQLite file and records the
// query of every request it receives.
func recordingServer(t *testing.T) (string, func() []url.Values) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "polaroid.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	api := httptransport.NewHandler(memory.NewService(store), slog.New(slog.NewTextHandler(io.Discard, nil)))
	var mu sync.Mutex
	var seen []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Query())
		mu.Unlock()
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []url.Values {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

// TestRequiredArgumentsAreOpaque: a required positional argument is sent as
// written, even when it contains "=", and only the trailing arguments are
// NAME=VALUE parameters (#102).
func TestRequiredArgumentsAreOpaque(t *testing.T) {
	server, seen := recordingServer(t)
	mustSucceed(t, cli(createJSON, nil, "-server", server, "create"))
	for _, task := range []string{
		"Add max_age=0 timestamp expiry boundary tests",
		"a=b=c and key=value",
		"  leading spaces, = alone, trailing  ",
		"Prüfe Ablauf für max_age=0 – 期限テスト",
		"repository=github.com/o/b",
		"=starts with an equals sign",
	} {
		before := len(seen())
		r := cli("", nil, "-server", server, "discover", task, "repository=github.com/o/a", "limit=2")
		if r.code != exitOK {
			t.Fatalf("discover %q: exit %d, stdout %s, stderr %s", task, r.code, r.stdout, r.stderr)
		}
		got := seen()[before:]
		want := url.Values{"task": {task}, "repository": {"github.com/o/a"}, "limit": {"2"}}
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Fatalf("discover %q sent %v, want %v", task, got, want)
		}
	}

	before := len(seen())
	r := cli("", nil, "-server", server, "bindings", "github.com/o/a=b", "limit=1")
	if got := seen()[before:]; r.code == exitUsage || len(got) != 1 ||
		!reflect.DeepEqual(got[0], url.Values{"repository": {"github.com/o/a=b"}, "limit": {"1"}}) {
		t.Fatalf("bindings with '=' in REPOSITORY: exit %d, sent %v", r.code, got)
	}

	before = len(seen())
	if r := cli("", nil, "-server", server, "discover", "max_age=0 expiry", "no_such_filter=1"); r.code != exitFailure ||
		!strings.Contains(r.stdout, `"code":"invalid_request"`) {
		t.Fatalf("an unknown trailing filter: exit %d, %s; want the server's 400", r.code, r.stdout)
	}
	for _, args := range [][]string{
		{"discover", "max_age=0", "=x"},
		{"discover", "max_age=0", "limit=1", "more words"},
		{"discover", "max_age=0", "second task"},
		{"bindings", "github.com/o/a", "=1"},
	} {
		if r := cli("", nil, append([]string{"-server", server}, args...)...); r.code != exitUsage || r.stderr == "" {
			t.Errorf("%q: exit %d, stderr %q; want a usage error", args, r.code, r.stderr)
		}
	}
	if got := seen()[before:]; len(got) != 1 {
		t.Fatalf("requests sent for the malformed calls: %v; want only the unknown-filter request", got)
	}
}
